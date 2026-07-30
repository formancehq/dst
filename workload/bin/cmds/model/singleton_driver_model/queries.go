package main

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

// Query reads exercise the ListTransactions filtered/ordered/paginated surface.
// A first page is a deterministic ordered window: given the filter, the order,
// and the page size, exactly one prefix of the matching transactions is correct.
//
// The model can't predict an in-flight transaction's id (v2 assigns ids per
// ledger and the model only learns them from responses), so it can't place one in
// an ordered-by-id window. Every query is therefore bounded with id <= K, where K
// is the drained frontier (the largest committed id the model has resolved). The
// drain gate guarantees no in-flight op has an id <= K, so the transactions with
// id <= K are exactly modelState's committed set — the window is a deterministic
// function of modelState, needing no candidate search. The page is checked
// element-for-element (id, postings, reverted, reference, timestamp); each row's
// metadata is checked per-row on the register track (metaStore), which is where v2
// metadata lives and is not filtered on.
//
// Ordering: transactions default to id DESC, reverse flips to ASC. No pit
// (current state), no cursor (first page only).

// queryPageSize is the page size a query requests. Kept at or below the server's
// default (15) so the model sizes its window identically.
func queryPageSize() int {
	return int(random.RandomChoice([]uint8{1, 2, 3, 5, 10, 15}))
}

// tri is a SQL three-valued truth value. The server evaluates filters in SQL, so
// a condition over a NULL column (only reference is nullable — reverts carry no
// reference) is NULL, not false, and NULL propagates through NOT/AND/OR. A row is
// returned only when the whole filter evaluates to triTrue.
type tri int8

const (
	triFalse tri = iota
	triTrue
	triNull
)

func triNot(a tri) tri {
	switch a {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	default:
		return triNull
	}
}

func triAnd(a, b tri) tri {
	if a == triFalse || b == triFalse {
		return triFalse
	}
	if a == triTrue && b == triTrue {
		return triTrue
	}
	return triNull
}

func triOr(a, b tri) tri {
	if a == triTrue || b == triTrue {
		return triTrue
	}
	if a == triFalse && b == triFalse {
		return triFalse
	}
	return triNull
}

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// txFilter is a generated transaction query filter: it both renders to the v2
// JSON DSL (toQuery) and evaluates against a model transaction (match), so
// generation and prediction cannot drift. match returns a SQL truth value.
type txFilter interface {
	match(id uint64, rec *txRecord) tri
	toQuery() map[string]any
}

// runTransactionQuery issues a ListTransactions first page and checks it against
// the model's ordered window (validateTransactionQuery).
func runTransactionQuery(ctx context.Context, cl *client.Formance, c *Checker) {
	reverse := random.RandomChoice([]uint8{0, 1}) == 1
	pageSize := queryPageSize()

	c.mu.Lock()
	readID := c.registerRead()
	frontier := drainedFrontier(c.modelState)
	sampleRef := pickCommittedRef(c.modelState)
	c.mu.Unlock()
	defer c.finishRead(readID)

	filter := genTxFilter(0, sampleRef)

	req := operations.V2ListTransactionsRequest{
		Ledger:   c.ledger,
		PageSize: pointer.For(int64(pageSize)),
		Reverse:  pointer.For(reverse),
		Query:    boundedTxQuery(filter, frontier),
	}

	resp, err := cl.Ledger.V2.ListTransactions(ctx, req)

	// High-water at the read's completion, for the metadata register window.
	oR := c.ticketSeq.Load()

	if err != nil {
		if isTransient(err) {
			return
		}
		assert.Unreachable("singleton_driver_model: ListTransactions returned unexpected error", internal.Details{
			"ledger": c.ledger,
			"filter": describeTxFilter(filter),
			"error":  err.Error(),
		})
		return
	}

	serverTxs := resp.V2TransactionsCursorResponse.Cursor.Data
	c.validateTransactionQuery(oR, readID, filter, frontier, reverse, pageSize, serverTxs)
}

// boundedTxQuery renders the generated filter conjoined with id <= frontier — the
// bound that makes the window deterministic. With no generated filter it is the
// bound alone.
func boundedTxQuery(filter txFilter, frontier uint64) map[string]any {
	bound := map[string]any{"$lte": map[string]any{"id": frontier}}
	if filter == nil {
		return bound
	}

	return map[string]any{"$and": []map[string]any{filter.toQuery(), bound}}
}

// validateTransactionQuery checks a ListTransactions page. The window's tx set is
// fixed (committed txs with id <= frontier), but a tx's reverted status is mutable
// by an in-flight revert, so the page is legal iff some candidate base's ordered
// window matches it element-for-element — candidateBases folds in-flight reverts,
// flipping targets' reverted flags, while folded creates (unknown id > frontier)
// never enter the bounded window. Each row's metadata is then validated per-row on
// the register track over [dR, maxTicket].
func (c *Checker) validateTransactionQuery(maxTicket, dR uint64, filter txFilter, frontier uint64, reverse bool, pageSize int, serverTxs []shared.V2Transaction) {
	c.mu.Lock()
	matched := false
	c.candidateBases(maxTicket, func(base State) bool {
		want := transactionWindow(base, filter, frontier, reverse, pageSize)
		if len(want) != len(serverTxs) {
			return false
		}
		for i, id := range want {
			st := serverTxs[i]
			if st.ID == nil || st.ID.Uint64() != id || !txRecordMatchesServer(base.txs[strconv.FormatUint(id, 10)], st) {
				return false
			}
		}
		matched = true
		return true
	})
	c.mu.Unlock()

	if !matched {
		assert.Unreachable("singleton_driver_model: transaction query outside model", internal.Details{
			"ledger":    c.ledger,
			"filter":    describeTxFilter(filter),
			"frontier":  frontier,
			"reverse":   reverse,
			"pageSize":  pageSize,
			"rows":      len(serverTxs),
			"serverIds": serverTxIDs(serverTxs),
			"modelIds":  joinUint64(c.modelTransactionWindow(filter, frontier, reverse, pageSize)),
		})
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, st := range serverTxs {
		id := st.ID.String()
		key, serverVal, metaOK := c.metaStore.validateRead(metaTransaction, id, dR, maxTicket, st.Metadata)
		if !metaOK {
			assert.Unreachable("singleton_driver_model: transaction query metadata outside model", internal.Details{
				"ledger":    c.ledger,
				"id":        id,
				"key":       key,
				"serverVal": serverVal,
			})
			return
		}
	}
}

// modelTransactionWindow returns the window on committed modelState, for a
// finding's diagnostics. Acquires c.mu.
func (c *Checker) modelTransactionWindow(filter txFilter, frontier uint64, reverse bool, pageSize int) []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return transactionWindow(c.modelState, filter, frontier, reverse, pageSize)
}

// drainedFrontier returns the largest committed transaction id the model has
// resolved. The drain gate guarantees no in-flight op has an id at or below it, so
// the transactions with id <= frontier are exactly modelState's committed set.
// Caller holds c.mu.
func drainedFrontier(s State) uint64 {
	var max uint64
	for idStr := range s.txs {
		if id, err := strconv.ParseUint(idStr, 10, 64); err == nil && id > max {
			max = id
		}
	}

	return max
}

// transactionWindow is the model's prediction of a ListTransactions first page:
// the committed transactions with id <= frontier matching filter, ordered by id
// (DESC by default, ASC when reverse), capped at pageSize. Caller holds c.mu.
func transactionWindow(s State, filter txFilter, frontier uint64, reverse bool, pageSize int) []uint64 {
	var ids []uint64
	for idStr, rec := range s.txs {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || id > frontier {
			continue
		}
		if filter == nil || filter.match(id, rec) == triTrue {
			ids = append(ids, id)
		}
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// Default order is id DESC; reverse yields ASC.
	if !reverse {
		reverseUint64(ids)
	}

	if len(ids) > pageSize {
		ids = ids[:pageSize]
	}

	return ids
}

// txRecordMatchesServer reports whether the model record is consistent with the
// server transaction on its immutable/predictable fields: postings, reverted,
// reference, and (when the model predicts one) timestamp. Metadata is validated
// separately on the register track.
func txRecordMatchesServer(rec *txRecord, st shared.V2Transaction) bool {
	if rec == nil {
		return false
	}
	if rec.reverted != st.Reverted {
		return false
	}
	ref := ""
	if st.Reference != nil {
		ref = *st.Reference
	}
	if rec.reference != ref {
		return false
	}
	if rec.timestamp != nil && !rec.timestamp.Equal(st.Timestamp) {
		return false
	}

	return postingsEqual(rec.postings, fromSDKPostings(st.Postings))
}

// --- Filter generation ---------------------------------------------------

// maxQueryGenDepth bounds generated boolean nesting.
const maxQueryGenDepth = 3

// genTxFilter rolls a transaction query filter: ~1/4 the universe (nil), else an
// index-free leaf (reverted / id / reference / address) or a boolean composition.
// sampleRef, when non-empty, is a committed reference the reference leaf targets
// so the filter sometimes matches rows rather than always missing.
func genTxFilter(depth int, sampleRef string) txFilter {
	if depth == 0 && random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
		return nil // universe (no filter)
	}

	if depth >= maxQueryGenDepth || random.RandomChoice([]uint8{0, 1}) == 0 {
		return genTxLeaf(sampleRef)
	}

	switch random.RandomChoice([]uint8{0, 1, 2}) {
	case 0:
		return txAnd{genTxFilter(depth+1, sampleRef), genTxFilter(depth+1, sampleRef)}
	case 1:
		return txOr{genTxFilter(depth+1, sampleRef), genTxFilter(depth+1, sampleRef)}
	default:
		return txNot{genTxFilter(depth+1, sampleRef)}
	}
}

func genTxLeaf(sampleRef string) txFilter {
	switch random.RandomChoice([]uint8{0, 1, 2, 3}) {
	case 0:
		return txReverted(random.RandomChoice([]uint8{0, 1}) == 0)
	case 1:
		op := random.RandomChoice([]string{"$match", "$gte", "$lte", "$gt", "$lt"})
		return txID{op: op, val: 1 + random.GetRandom()%16}
	case 2:
		// A committed reference (matches rows) ~half the time, else a fresh one
		// (an empty window — still a valid page to validate).
		if sampleRef != "" && random.RandomChoice([]uint8{0, 1}) == 0 {
			return txReference(sampleRef)
		}
		return txReference(reference())
	default:
		field := random.RandomChoice([]string{"source", "destination", "account"})
		addr := random.RandomChoice([]string{"world", poolAddress()})
		return txAddr{field: field, addr: addr}
	}
}

// --- Filter leaves -------------------------------------------------------

type txReverted bool

func (f txReverted) match(_ uint64, rec *txRecord) tri { return triOf(rec.reverted == bool(f)) }
func (f txReverted) toQuery() map[string]any           { return match1("reverted", bool(f)) }

type txID struct {
	op  string
	val uint64
}

func (f txID) match(id uint64, _ *txRecord) tri {
	switch f.op {
	case "$match":
		return triOf(id == f.val)
	case "$gte":
		return triOf(id >= f.val)
	case "$lte":
		return triOf(id <= f.val)
	case "$gt":
		return triOf(id > f.val)
	case "$lt":
		return triOf(id < f.val)
	}
	return triFalse
}
func (f txID) toQuery() map[string]any { return map[string]any{f.op: map[string]any{"id": f.val}} }

type txReference string

// match is NULL when the transaction carries no reference (a revert): SQL
// reference = X over a NULL column is NULL, not false.
func (f txReference) match(_ uint64, rec *txRecord) tri {
	if rec.reference == "" {
		return triNull
	}
	return triOf(rec.reference == string(f))
}
func (f txReference) toQuery() map[string]any { return match1("reference", string(f)) }

// txAddr matches transactions whose postings reference addr as source,
// destination, or either (account). Addresses generated are full (no wildcards),
// so the server's partial-address match reduces to membership in the
// source/destination set — exists a posting touching addr on the chosen side.
type txAddr struct {
	field string
	addr  string
}

func (f txAddr) match(_ uint64, rec *txRecord) tri {
	for _, p := range rec.postings {
		switch f.field {
		case "source":
			if p.Source == f.addr {
				return triTrue
			}
		case "destination":
			if p.Destination == f.addr {
				return triTrue
			}
		default: // account
			if p.Source == f.addr || p.Destination == f.addr {
				return triTrue
			}
		}
	}
	return triFalse
}
func (f txAddr) toQuery() map[string]any { return match1(f.field, f.addr) }

// --- Filter combinators --------------------------------------------------

type txAnd [2]txFilter

func (f txAnd) match(id uint64, rec *txRecord) tri {
	return triAnd(f[0].match(id, rec), f[1].match(id, rec))
}
func (f txAnd) toQuery() map[string]any {
	return map[string]any{"$and": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type txOr [2]txFilter

func (f txOr) match(id uint64, rec *txRecord) tri {
	return triOr(f[0].match(id, rec), f[1].match(id, rec))
}
func (f txOr) toQuery() map[string]any {
	return map[string]any{"$or": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type txNot [1]txFilter

func (f txNot) match(id uint64, rec *txRecord) tri { return triNot(f[0].match(id, rec)) }
func (f txNot) toQuery() map[string]any            { return map[string]any{"$not": f[0].toQuery()} }

// --- helpers -------------------------------------------------------------

// match1 builds a single-field $match condition, the v2 DSL's equality leaf.
func match1(field string, value any) map[string]any {
	return map[string]any{"$match": map[string]any{field: value}}
}

func reverseUint64(s []uint64) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func joinUint64(s []uint64) string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = strconv.FormatUint(v, 10)
	}
	return strings.Join(parts, ",")
}

func serverTxIDs(txs []shared.V2Transaction) string {
	parts := make([]string, len(txs))
	for i, t := range txs {
		if t.ID != nil {
			parts[i] = t.ID.String()
		}
	}
	return strings.Join(parts, ",")
}

func describeTxFilter(f txFilter) string {
	if f == nil {
		return "*"
	}
	switch x := f.(type) {
	case txReverted:
		return "reverted=" + strconv.FormatBool(bool(x))
	case txID:
		return "id" + x.op + strconv.FormatUint(x.val, 10)
	case txReference:
		return "ref=" + string(x)
	case txAddr:
		return x.field + "=" + x.addr
	case txAnd:
		return "and(" + describeTxFilter(x[0]) + "," + describeTxFilter(x[1]) + ")"
	case txOr:
		return "or(" + describeTxFilter(x[0]) + "," + describeTxFilter(x[1]) + ")"
	case txNot:
		return "not(" + describeTxFilter(x[0]) + ")"
	}
	return "?"
}
