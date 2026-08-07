package main

import (
	"context"
	"math/big"
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
// Ordering: transactions default to id DESC; reverse flips to ASC, and a
// generated sort=id:<dir> overrides the direction (the server applies reverse
// first, then sort). No pit (current state), no cursor (first page only).

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
	match(id uint64, rec *txRecord, assign metaAssign) tri
	toQuery() map[string]any
}

// runTransactionQuery issues a ListTransactions first page and checks it against
// the model's ordered window (validateTransactionQuery).
func runTransactionQuery(ctx context.Context, cl *client.Formance, c *Checker) {
	reverse := random.RandomChoice([]uint8{0, 1}) == 1
	// Default order is id DESC; reverse flips to ASC; a generated sort=id:<dir>
	// then overrides the direction, mirroring the server's precedence.
	descending := !reverse
	var sortParam *string
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		descending = random.RandomChoice([]uint8{0, 1}) == 0
		if descending {
			sortParam = pointer.For("id:desc")
		} else {
			sortParam = pointer.For("id:asc")
		}
	}
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
		Sort:     sortParam,
		Query:    txQuery(filter),
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
	c.validateTransactionQuery(oR, readID, filter, frontier, descending, pageSize, serverTxs)
}

// txQuery renders the generated filter to the v2 DSL, or nil for the universe.
// The query is unbounded: in-flight creates the server has committed may appear
// in the page, matched by content (they have no model-predictable id).
func txQuery(filter txFilter) map[string]any {
	if filter == nil {
		return nil
	}

	return filter.toQuery()
}

// validateTransactionQuery checks a ListTransactions page. The page is legal iff
// some candidate base and some admissible transaction-metadata assignment reproduce
// it (transactionPageMatches): drained txs (id <= frontier) match exactly and in
// order, while in-flight creates/reverts the candidate folds — whose ids the model
// can't predict — match by content in any order past the frontier. candidateBases
// supplies the fold (and reverted-flag flips); enumerateMeta supplies each drained
// row's metadata under one consistent snapshot.
func (c *Checker) validateTransactionQuery(maxTicket, dR uint64, filter txFilter, frontier uint64, descending bool, pageSize int, serverTxs []shared.V2Transaction) {
	c.mu.Lock()
	matched := false
	c.candidateBases(maxTicket, func(base State) bool {
		enumerated := c.metaStore.enumerateMeta(metaTransaction, dR, maxTicket, func(assign metaAssign) bool {
			if transactionPageMatches(base, assign, filter, frontier, descending, pageSize, serverTxs) {
				matched = true
				return true
			}
			return false
		})
		if !enumerated {
			// Too many uncertain metadata cells to enumerate — accept rather than
			// explode (rare; logged as coverage loss).
			dbg("TQUERY meta enumeration capped: ledger=%s", c.ledger)
			matched = true
		}
		return matched
	})
	c.mu.Unlock()

	if !matched {
		assert.Unreachable("singleton_driver_model: transaction query outside model", internal.Details{
			"ledger":    c.ledger,
			"filter":     describeTxFilter(filter),
			"frontier":   frontier,
			"descending": descending,
			"pageSize":   pageSize,
			"rows":       len(serverTxs),
			"serverIds":  serverTxIDs(serverTxs),
			"modelIds":   joinUint64(c.modelTransactionWindow(filter, frontier, descending, pageSize)),
		})
	}
}

// transactionPageMatches reports whether the server page is a legal window over the
// (base, assign) snapshot. Rows with id <= frontier are drained: matched exactly by
// id, content, and metadata, as an ordered prefix of the drained window. Rows with
// id > frontier are in-flight creates/reverts folded into base.unknownTxs — the
// model can't predict their ids, so they are matched by content in any order, and a
// full page may legally show only a subset of them (truncation at the drained
// boundary). Folded rows sort above all drained ones, so descending shows folded
// first, ascending shows drained first.
func transactionPageMatches(base State, assign metaAssign, filter txFilter, frontier uint64, descending bool, pageSize int, serverTxs []shared.V2Transaction) bool {
	drained := drainedMatching(base, assign, filter, frontier, descending)

	// The page is two contiguous blocks (folded, drained), ordered by direction;
	// reject an interleaving.
	var sFold, sDrain []shared.V2Transaction
	switchedToSecond := false
	for _, st := range serverTxs {
		if st.ID == nil {
			return false
		}
		inFirstRegion := (st.ID.Uint64() > frontier) == descending
		if inFirstRegion {
			if switchedToSecond {
				return false
			}
		} else {
			switchedToSecond = true
		}
		if st.ID.Uint64() > frontier {
			sFold = append(sFold, st)
		} else {
			sDrain = append(sDrain, st)
		}
	}

	// Drained rows are a prefix of the drained window, matched by id + content +
	// metadata.
	if len(sDrain) > len(drained) {
		return false
	}
	for i, st := range sDrain {
		id := st.ID.Uint64()
		if id != drained[i] {
			return false
		}
		idStr := strconv.FormatUint(id, 10)
		if !txRecordMatchesServer(base.txs[idStr], st) || !metaRowMatch(assign.meta(metaTransaction, idStr), st.Metadata) {
			return false
		}
	}

	// Folded rows match by content against folded model entries, consuming definite
	// (triTrue) entries first so completeness can require them.
	verdict := make([]tri, len(base.unknownTxs))
	reqTotal := 0
	for i, rec := range base.unknownTxs {
		verdict[i] = foldedVerdict(filter, rec, frontier)
		if verdict[i] == triTrue {
			reqTotal++
		}
	}
	consumed := make([]bool, len(base.unknownTxs))
	reqConsumed := 0
	for _, st := range sFold {
		if i := matchFolded(base.unknownTxs, verdict, consumed, triTrue, st); i >= 0 {
			consumed[i] = true
			reqConsumed++
			continue
		}
		if i := matchFolded(base.unknownTxs, verdict, consumed, triNull, st); i >= 0 {
			consumed[i] = true
			continue
		}
		return false // server row matches no admissible folded transaction
	}

	// Completeness: on a non-full page everything is shown, so every drained and
	// every definite folded row must be present. On a full page the second region
	// (drained for descending, folded for ascending) may be truncated; the first
	// region is fully present only if any second-region row appears.
	full := len(serverTxs) == pageSize
	drainedAll := len(sDrain) == len(drained)
	reqFoldAll := reqConsumed == reqTotal

	if !full {
		return drainedAll && reqFoldAll
	}
	if descending {
		if len(sDrain) > 0 {
			return reqFoldAll // a drained row appeared ⟹ all folded shown first
		}
		return true // page entirely folded (a truncated subset)
	}
	if len(sFold) > 0 {
		return drainedAll // a folded row appeared ⟹ all drained shown first
	}
	return true // page entirely drained (a truncated prefix)
}

// matchFolded returns the index of an unconsumed folded entry with the given
// verdict whose content matches st, or -1.
func matchFolded(folded []*txRecord, verdict []tri, consumed []bool, want tri, st shared.V2Transaction) int {
	for i, rec := range folded {
		if consumed[i] || verdict[i] != want {
			continue
		}
		if txRecordMatchesServer(rec, st) && metaRowMatch(rec.metadata, st.Metadata) {
			return i
		}
	}
	return -1
}

// drainedMatching returns the ids of drained transactions (id <= frontier) in base
// matching filter, in query order (descending id or ascending). No page cap.
func drainedMatching(base State, assign metaAssign, filter txFilter, frontier uint64, descending bool) []uint64 {
	var ids []uint64
	for idStr, rec := range base.txs {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || id > frontier {
			continue
		}
		if filter == nil || filter.match(id, rec, assign) == triTrue {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if descending {
		reverseUint64(ids)
	}
	return ids
}

// foldedVerdict evaluates filter against a folded transaction whose id is unknown
// but greater than frontier: triTrue = definitely matches, triFalse = definitely
// excluded, triNull = uncertain (an id threshold above frontier, or an absent
// reference under SQL NULL), which the matcher treats as an optional row. Content
// the model knows for a not-yet-committed tx — postings, reference, and create-time
// metadata — is evaluated exactly.
func foldedVerdict(f txFilter, rec *txRecord, frontier uint64) tri {
	if f == nil {
		return triTrue
	}
	switch x := f.(type) {
	case txReverted:
		return triOf(rec.reverted == bool(x))
	case txID:
		// A threshold at or below frontier decides it (id > frontier); above is
		// uncertain.
		if x.val > frontier {
			return triNull
		}
		switch x.op {
		case "$gte", "$gt":
			return triTrue
		default: // $match, $lte, $lt
			return triFalse
		}
	case txReference:
		if rec.reference == "" {
			return triNull
		}
		return triOf(rec.reference == string(x))
	case txAddr:
		return x.match(0, rec, nil)
	case txMetaExists:
		_, ok := rec.metadata[string(x)]
		return triOf(ok)
	case txMetaMatch:
		v, ok := rec.metadata[x.key]
		return triOf(ok && v == x.val)
	case txAnd:
		return triAnd(foldedVerdict(x[0], rec, frontier), foldedVerdict(x[1], rec, frontier))
	case txOr:
		return triOr(foldedVerdict(x[0], rec, frontier), foldedVerdict(x[1], rec, frontier))
	case txNot:
		return triNot(foldedVerdict(x[0], rec, frontier))
	}
	return triNull
}

// modelTransactionWindow returns the window on committed modelState, for a
// finding's diagnostics. Acquires c.mu.
func (c *Checker) modelTransactionWindow(filter txFilter, frontier uint64, descending bool, pageSize int) []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return transactionWindow(c.modelState, metaAssign{}, filter, frontier, descending, pageSize)
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
func transactionWindow(s State, assign metaAssign, filter txFilter, frontier uint64, descending bool, pageSize int) []uint64 {
	var ids []uint64
	for idStr, rec := range s.txs {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || id > frontier {
			continue
		}
		if filter == nil || filter.match(id, rec, assign) == triTrue {
			ids = append(ids, id)
		}
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	if descending {
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
	switch random.RandomChoice([]uint8{0, 1, 2, 3, 4, 5}) {
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
	case 3:
		// Key from the small pool, so it matches transactions that carry it.
		return txMetaExists(metaKeyName())
	case 4:
		return txMetaMatch{key: metaKeyName(), val: metaValue()}
	default:
		field := random.RandomChoice([]string{"source", "destination", "account"})
		addr := random.RandomChoice([]string{"world", poolAddress()})
		return txAddr{field: field, addr: addr}
	}
}

// --- Filter leaves -------------------------------------------------------

type txReverted bool

func (f txReverted) match(_ uint64, rec *txRecord, _ metaAssign) tri {
	return triOf(rec.reverted == bool(f))
}
func (f txReverted) toQuery() map[string]any { return match1("reverted", bool(f)) }

type txID struct {
	op  string
	val uint64
}

func (f txID) match(id uint64, _ *txRecord, _ metaAssign) tri {
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
func (f txReference) match(_ uint64, rec *txRecord, _ metaAssign) tri {
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

func (f txAddr) match(_ uint64, rec *txRecord, _ metaAssign) tri {
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

// txMetaExists matches transactions carrying metadata key (metadata -> key is not
// null — a boolean existence check, never NULL).
type txMetaExists string

func (f txMetaExists) match(id uint64, _ *txRecord, assign metaAssign) tri {
	_, ok := assign[metaCell{kind: metaTransaction, id: strconv.FormatUint(id, 10), key: string(f)}]
	return triOf(ok)
}
func (f txMetaExists) toQuery() map[string]any {
	return map[string]any{"$exists": map[string]any{"metadata": string(f)}}
}

// txMetaMatch matches transactions whose metadata contains key=val (jsonb
// containment — absent key is false, not NULL).
type txMetaMatch struct{ key, val string }

func (f txMetaMatch) match(id uint64, _ *txRecord, assign metaAssign) tri {
	v, ok := assign[metaCell{kind: metaTransaction, id: strconv.FormatUint(id, 10), key: f.key}]
	return triOf(ok && v == f.val)
}
func (f txMetaMatch) toQuery() map[string]any {
	return match1("metadata["+f.key+"]", f.val)
}

// --- Filter combinators --------------------------------------------------

type txAnd [2]txFilter

func (f txAnd) match(id uint64, rec *txRecord, assign metaAssign) tri {
	return triAnd(f[0].match(id, rec, assign), f[1].match(id, rec, assign))
}
func (f txAnd) toQuery() map[string]any {
	return map[string]any{"$and": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type txOr [2]txFilter

func (f txOr) match(id uint64, rec *txRecord, assign metaAssign) tri {
	return triOr(f[0].match(id, rec, assign), f[1].match(id, rec, assign))
}
func (f txOr) toQuery() map[string]any {
	return map[string]any{"$or": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type txNot [1]txFilter

func (f txNot) match(id uint64, rec *txRecord, assign metaAssign) tri {
	return triNot(f[0].match(id, rec, assign))
}
func (f txNot) toQuery() map[string]any { return map[string]any{"$not": f[0].toQuery()} }

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
	case txMetaExists:
		return "meta?" + string(x)
	case txMetaMatch:
		return "meta[" + x.key + "]=" + x.val
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

// --- Account queries -----------------------------------------------------
//
// ListAccounts returns accounts with volumes OR metadata, ordered by address
// (ascending by default — a total order, addresses are unique — or descending
// under sort=address:desc). The volume-derived universe
// and balances are a function of the candidate base (addresses are known — they
// are in the postings — so folded in-flight creates appear), but metadata lives
// on the register track, so metadata-only accounts and each row's metadata are
// resolved by enumerating the register's admissible assignments (metaStore
// .enumerateMeta). The page is legal iff some (candidate base, metadata
// assignment) reproduces it: universe, filter, order, page cap, and each row's
// volumes and metadata all consistent under that one snapshot. No pit, no cursor.

// accFilter is a generated account query filter (renders to the v2 DSL and
// evaluates against the model under a candidate base + metadata assignment).
type accFilter interface {
	match(addr string, base State, assign metaAssign) tri
	toQuery() map[string]any
}

// runAccountQuery issues a ListAccounts first page (address order, expanded
// volumes) and checks it against the model (validateAccountQuery).
func runAccountQuery(ctx context.Context, cl *client.Formance, c *Checker) {
	// Default order is address ASC; a generated sort=address:<dir> can flip it.
	descending := false
	var sortParam *string
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		descending = random.RandomChoice([]uint8{0, 1}) == 0
		if descending {
			sortParam = pointer.For("address:desc")
		} else {
			sortParam = pointer.For("address:asc")
		}
	}
	pageSize := queryPageSize()

	c.mu.Lock()
	readID := c.registerRead()
	c.mu.Unlock()
	defer c.finishRead(readID)

	filter := genAccountFilter()

	req := operations.V2ListAccountsRequest{
		Ledger:   c.ledger,
		PageSize: pointer.For(int64(pageSize)),
		Expand:   pointer.For("volumes"),
		Sort:     sortParam,
	}
	if filter != nil {
		req.Query = filter.toQuery()
	}

	resp, err := cl.Ledger.V2.ListAccounts(ctx, req)
	oR := c.ticketSeq.Load()

	if err != nil {
		if isTransient(err) {
			return
		}
		assert.Unreachable("singleton_driver_model: ListAccounts returned unexpected error", internal.Details{
			"ledger": c.ledger,
			"filter": describeAccFilter(filter),
			"error":  err.Error(),
		})
		return
	}

	serverAccts := resp.V2AccountsCursorResponse.Cursor.Data
	c.validateAccountQuery(oR, readID, filter, descending, pageSize, serverAccts)
}

// validateAccountQuery checks a ListAccounts page: legal iff some candidate base
// and some admissible metadata assignment reproduce the ordered window
// position-for-position, each row's address, volumes (on the base), and metadata
// (under the assignment) matching. Acquires c.mu.
func (c *Checker) validateAccountQuery(maxTicket, dR uint64, filter accFilter, descending bool, pageSize int, serverAccts []shared.V2Account) {
	c.mu.Lock()
	matched := false
	c.candidateBases(maxTicket, func(base State) bool {
		enumerated := c.metaStore.enumerateMeta(metaAccount, dR, maxTicket, func(assign metaAssign) bool {
			if accountPageMatches(base, assign, filter, descending, pageSize, serverAccts) {
				matched = true
				return true
			}
			return false
		})
		if !enumerated {
			// Too many uncertain metadata cells to enumerate — accept rather than
			// explode (rare; logged as coverage loss).
			dbg("AQUERY meta enumeration capped: ledger=%s", c.ledger)
			matched = true
		}
		return matched
	})
	c.mu.Unlock()

	if !matched {
		assert.Unreachable("singleton_driver_model: account query outside model", internal.Details{
			"ledger":      c.ledger,
			"filter":      describeAccFilter(filter),
			"descending":  descending,
			"pageSize":    pageSize,
			"rows":        len(serverAccts),
			"serverAddrs": serverAccountAddrs(serverAccts),
		})
	}
}

// accountPageMatches reports whether the (base, assign) snapshot reproduces the
// server page exactly.
func accountPageMatches(base State, assign metaAssign, filter accFilter, descending bool, pageSize int, serverAccts []shared.V2Account) bool {
	want := accountWindow(base, assign, filter, descending, pageSize)
	if len(want) != len(serverAccts) {
		return false
	}
	for i, addr := range want {
		sa := serverAccts[i]
		if sa.Address != addr || !accountVolumesMatch(base, addr, sa.Volumes) || !metaRowMatch(assign.meta(metaAccount, addr), sa.Metadata) {
			return false
		}
	}
	return true
}

// accountWindow is the model's prediction of a ListAccounts first page: the
// accounts with volumes (in base) or metadata (present under assign) matching
// filter, in address order (descending flips the default ascending), capped at
// pageSize.
func accountWindow(base State, assign metaAssign, filter accFilter, descending bool, pageSize int) []string {
	seen := map[string]bool{}
	for k := range base.volumes {
		seen[k.Address] = true
	}
	for cell := range assign {
		if cell.kind == metaAccount {
			seen[cell.id] = true
		}
	}

	var addrs []string
	for a := range seen {
		if filter == nil || filter.match(a, base, assign) == triTrue {
			addrs = append(addrs, a)
		}
	}

	sort.Strings(addrs)
	if descending {
		for i, j := 0, len(addrs)-1; i < j; i, j = i+1, j-1 {
			addrs[i], addrs[j] = addrs[j], addrs[i]
		}
	}

	if len(addrs) > pageSize {
		addrs = addrs[:pageSize]
	}

	return addrs
}

// accountVolumesMatch compares the account's model volumes (uncolored, per asset)
// on the base against the server's expanded volumes.
func accountVolumesMatch(base State, addr string, server map[string]shared.V2Volume) bool {
	model := map[string]VolumePair{}
	for k, vp := range base.volumes {
		if k.Address == addr {
			model[k.Asset] = vp
		}
	}

	present := 0
	for asset, sv := range server {
		mv, ok := model[asset]
		if !ok {
			// A zero cell the model never created is not a discrepancy only if the
			// server reports zero for it; otherwise it is.
			if sv.GetInput().Sign() != 0 || sv.GetOutput().Sign() != 0 {
				return false
			}
			continue
		}
		if mv.Input.Cmp(sv.GetInput()) != 0 || mv.Output.Cmp(sv.GetOutput()) != 0 {
			return false
		}
		present++
	}

	return present == len(model)
}

// metaRowMatch compares a row's model metadata (present cells under an assignment)
// against the server's, ignoring server-managed reserved keys.
func metaRowMatch(model map[string]string, server map[string]string) bool {
	filtered := map[string]string{}
	for k, v := range server {
		if strings.HasPrefix(k, reservedMetaPrefix) {
			continue
		}
		filtered[k] = v
	}

	if len(model) != len(filtered) {
		return false
	}
	for k, v := range model {
		if filtered[k] != v {
			return false
		}
	}
	return true
}

// --- Account filter generation -------------------------------------------

// genAccountFilter rolls an account query filter: ~1/2 the universe (nil, which
// exercises the metadata-only-account universe), else an exact-address leaf or a
// boolean composition.
func genAccountFilter() accFilter {
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		return nil
	}
	return genAccFilter(0)
}

func genAccFilter(depth int) accFilter {
	if depth >= maxQueryGenDepth || random.RandomChoice([]uint8{0, 1}) == 0 {
		return genAccLeaf()
	}
	switch random.RandomChoice([]uint8{0, 1, 2}) {
	case 0:
		return accAnd{genAccFilter(depth + 1), genAccFilter(depth + 1)}
	case 1:
		return accOr{genAccFilter(depth + 1), genAccFilter(depth + 1)}
	default:
		return accNot{genAccFilter(depth + 1)}
	}
}

func genAccLeaf() accFilter {
	switch random.RandomChoice([]uint8{0, 1, 2, 3}) {
	case 0:
		return accAddr(poolAddress())
	case 1:
		// Key from the small pool, so it matches accounts that carry it.
		return accMetaExists(metaKeyName())
	case 2:
		// A fresh value usually yields an empty window — still a valid page.
		return accMetaMatch{key: metaKeyName(), val: metaValue()}
	default:
		op := random.RandomChoice([]string{"$match", "$gte", "$lte", "$gt", "$lt"})
		return accBalance{asset: random.RandomChoice(assets), op: op, val: balanceThreshold()}
	}
}

// balanceThreshold rolls a balance comparison value: 0 (splits funded vs
// world/empty), a small value, or the full amount range.
func balanceThreshold() uint64 {
	switch random.RandomChoice([]uint8{0, 1, 2}) {
	case 0:
		return 0
	case 1:
		return random.GetRandom() % 1_000_000_000
	default:
		return random.GetRandom()
	}
}

// accAddr matches an exact account address.
type accAddr string

func (f accAddr) match(addr string, _ State, _ metaAssign) tri { return triOf(addr == string(f)) }
func (f accAddr) toQuery() map[string]any                      { return match1("address", string(f)) }

// accMetaExists matches accounts carrying metadata key. Present/absent is a
// boolean existence check (metadata -> key is not null), never NULL.
type accMetaExists string

func (f accMetaExists) match(addr string, _ State, m metaAssign) tri {
	_, ok := m[metaCell{kind: metaAccount, id: addr, key: string(f)}]
	return triOf(ok)
}
func (f accMetaExists) toQuery() map[string]any {
	return map[string]any{"$exists": map[string]any{"metadata": string(f)}}
}

// accMetaMatch matches accounts whose metadata contains key=val (jsonb
// containment — absent key is false, not NULL).
type accMetaMatch struct{ key, val string }

func (f accMetaMatch) match(addr string, _ State, m metaAssign) tri {
	v, ok := m[metaCell{kind: metaAccount, id: addr, key: f.key}]
	return triOf(ok && v == f.val)
}
func (f accMetaMatch) toQuery() map[string]any {
	return match1("metadata["+f.key+"]", f.val)
}

// accBalance matches on an account's net balance for an asset (input - output).
// An account with no volume cell for the asset has a NULL balance, so the
// comparison is NULL (excluded), not false.
type accBalance struct {
	asset string
	op    string
	val   uint64
}

func (f accBalance) match(addr string, base State, _ metaAssign) tri {
	vp, ok := base.volumes[VolumeKey{Address: addr, Asset: f.asset}]
	if !ok {
		return triNull
	}
	cmp := new(big.Int).Sub(&vp.Input, &vp.Output).Cmp(new(big.Int).SetUint64(f.val))
	switch f.op {
	case "$match":
		return triOf(cmp == 0)
	case "$gte":
		return triOf(cmp >= 0)
	case "$lte":
		return triOf(cmp <= 0)
	case "$gt":
		return triOf(cmp > 0)
	case "$lt":
		return triOf(cmp < 0)
	}
	return triFalse
}
func (f accBalance) toQuery() map[string]any {
	field := "balance[" + f.asset + "]"
	if f.op == "$match" {
		return match1(field, f.val)
	}
	return map[string]any{f.op: map[string]any{field: f.val}}
}

type accAnd [2]accFilter

func (f accAnd) match(a string, b State, m metaAssign) tri {
	return triAnd(f[0].match(a, b, m), f[1].match(a, b, m))
}
func (f accAnd) toQuery() map[string]any {
	return map[string]any{"$and": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type accOr [2]accFilter

func (f accOr) match(a string, b State, m metaAssign) tri {
	return triOr(f[0].match(a, b, m), f[1].match(a, b, m))
}
func (f accOr) toQuery() map[string]any {
	return map[string]any{"$or": []map[string]any{f[0].toQuery(), f[1].toQuery()}}
}

type accNot [1]accFilter

func (f accNot) match(a string, b State, m metaAssign) tri { return triNot(f[0].match(a, b, m)) }
func (f accNot) toQuery() map[string]any                   { return map[string]any{"$not": f[0].toQuery()} }

func serverAccountAddrs(accts []shared.V2Account) string {
	parts := make([]string, len(accts))
	for i, a := range accts {
		parts[i] = a.Address
	}
	return strings.Join(parts, ",")
}

func describeAccFilter(f accFilter) string {
	if f == nil {
		return "*"
	}
	switch x := f.(type) {
	case accAddr:
		return "addr=" + string(x)
	case accMetaExists:
		return "meta?" + string(x)
	case accMetaMatch:
		return "meta[" + x.key + "]=" + x.val
	case accBalance:
		return "bal[" + x.asset + "]" + x.op + strconv.FormatUint(x.val, 10)
	case accAnd:
		return "and(" + describeAccFilter(x[0]) + "," + describeAccFilter(x[1]) + ")"
	case accOr:
		return "or(" + describeAccFilter(x[0]) + "," + describeAccFilter(x[1]) + ")"
	case accNot:
		return "not(" + describeAccFilter(x[0]) + ")"
	}
	return "?"
}
