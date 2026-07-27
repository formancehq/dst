package main

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

// generateOperation plans the next operation against the committed state s:
//   - ~1/4: revert a committed, not-yet-reverted transaction (when one exists),
//   - else ~half world-sourced (always commits, 1..maxPostings — funds the pool),
//   - else a single account-sourced posting that overdrafts whenever the source
//     lacks the balance, exercising INSUFFICIENT_FUND.
//
// The small address pool makes cells recur, so volumes accumulate, concurrent
// reads land on contended cells, and account sources sometimes have funds.
func generateOperation(s State) Operation {
	if random.RandomChoice([]uint8{0, 1, 2, 3, 4, 5}) == 0 {
		return generateBulk(s)
	}

	if random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
		if op, ok := generateRevert(s); ok {
			return op
		}
	}

	// ~1/16: a deliberately-rejected create, exercising the admission/forging
	// rejection branches valid traffic never triggers.
	if random.RandomChoice([]uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}) == 0 {
		if op, ok := generateRejectedCreate(s); ok {
			return op
		}
	}

	var op Operation
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		op = accountSourcedOp()
	} else {
		op = worldSourcedOp()
	}

	return withCreateMeta(op)
}

// generateRejectedCreate builds a create the server must reject, reproducing the
// reason in the model: an empty transaction (no postings) → NO_POSTINGS, or a
// create reusing a committed reference → CONFLICT. Returns ok=false when the
// duplicate-reference branch is chosen but no committed reference exists yet.
func generateRejectedCreate(s State) (Operation, bool) {
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		// No postings, fresh reference so only the empty-postings branch applies.
		return Operation{kind: opCreateTx, reference: reference(), idemKey: idempotencyKey()}, true
	}

	ref := pickCommittedRef(s)
	if ref == "" {
		return Operation{}, false
	}

	// A single affordable posting so the reused reference is the only rejection.
	return Operation{
		kind:      opCreateTx,
		postings:  []Posting{{Source: "world", Destination: poolAddress(), Asset: assets[0], Amount: big.NewInt(1)}},
		reference: ref,
		idemKey:   idempotencyKey(),
	}, true
}

// pickCommittedRef returns a deterministically-chosen committed transaction
// reference, or "" when the model holds none.
func pickCommittedRef(s State) string {
	refs := make([]string, 0, len(s.refs))
	for r := range s.refs {
		refs = append(refs, r)
	}

	if len(refs) == 0 {
		return ""
	}

	sort.Strings(refs)

	return random.RandomChoice(refs)
}

// withCreateMeta attaches create-time metadata to a standalone create: ~half carry
// transaction metadata, ~1/3 carry account metadata on the last posting's
// destination (already a legal account when the transaction commits). Kept out of
// newOp so bulk elements — whose payloads carry no metadata — don't generate any.
func withCreateMeta(op Operation) Operation {
	if random.RandomChoice([]uint8{0, 1}) == 0 {
		op.metadata = randomMetaMap()
	}
	if random.RandomChoice([]uint8{0, 1, 2}) == 0 && len(op.postings) > 0 {
		dst := op.postings[len(op.postings)-1].Destination
		op.accountMeta = map[string]map[string]string{dst: randomMetaMap()}
	}

	return op
}

// randomMetaMap builds a 1-2 key metadata map from the small key/value pools —
// keys collide across writers, values are globally unique so a read pinpoints the
// write it observed.
func randomMetaMap() map[string]string {
	n := 1 + int(random.GetRandom()%2)
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[metaKeyName()] = metaValue()
	}

	return m
}

// generateBulk plans an atomic bulk of 2-4 elements: mostly create transactions
// (world-sourced ones always commit; ~1/4 account-sourced so a bulk occasionally
// overdrafts and rolls back atomically), plus ~1/4 reverts of committed
// transactions. A revert of an already-reverted target — a concurrent or
// duplicate-within-bulk pick — rejects the whole atomic bulk with ALREADY_REVERT.
func generateBulk(s State) Operation {
	n := 2 + int(random.GetRandom()%3)
	subs := make([]Operation, 0, n)
	for i := 0; i < n; i++ {
		if random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
			if op, ok := generateRevert(s); ok {
				subs = append(subs, op)
				continue
			}
		}
		if random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
			subs = append(subs, accountSourcedOp())
		} else {
			subs = append(subs, worldSourcedOp())
		}
	}

	return Operation{kind: opBulk, bulk: subs}
}

// generateRevert targets a committed, not-yet-reverted transaction, chosen over a
// sorted slice (replayable, unlike map order). Concurrent picks of the same
// target exercise the ALREADY_REVERT / REVERT_OCCURRING rejection. ~half are
// non-forced, applying the balance floor to the reversed postings — the reversed
// source (the original destination) may since have spent the funds — so they also
// exercise the revert path's INSUFFICIENT_FUND rejection.
func generateRevert(s State) (Operation, bool) {
	ids := make([]string, 0, len(s.txs))
	for id, r := range s.txs {
		if !r.reverted {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return Operation{}, false
	}

	sort.Strings(ids)

	return Operation{
		kind:     opRevert,
		targetID: random.RandomChoice(ids),
		force:    random.RandomChoice([]uint8{0, 1}) == 0,
		idemKey:  idempotencyKey(),
	}, true
}

// worldSourcedOp credits 1..maxPostings pool accounts from world. world is
// overdraftable, so it always commits.
func worldSourcedOp() Operation {
	n := 1 + int(random.GetRandom()%uint64(maxPostings))
	postings := make([]Posting, n)
	for i := range postings {
		postings[i] = Posting{
			Source:      "world",
			Destination: poolAddress(),
			Asset:       random.RandomChoice(assets),
			Amount:      internal.RandomBigInt(),
		}
	}

	return newOp(postings)
}

// accountSourcedOp moves a random amount between two distinct pool accounts. The
// source overdrafts (INSUFFICIENT_FUND) unless it has accumulated enough from
// prior world credits. Single-posting so the funds check is unambiguous (no
// intra-transaction ordering of multiple sources).
func accountSourcedOp() Operation {
	src := poolAddress()
	dst := poolAddress()
	for dst == src {
		dst = poolAddress()
	}

	return newOp([]Posting{{
		Source:      src,
		Destination: dst,
		Asset:       random.RandomChoice(assets),
		Amount:      internal.RandomBigInt(),
	}})
}

func newOp(postings []Posting) Operation {
	return Operation{
		kind:      opCreateTx,
		postings:  postings,
		reference: reference(),
		idemKey:   idempotencyKey(),
	}
}

// poolAddress returns "account:N" from the small address pool.
func poolAddress() string {
	return fmt.Sprintf("account:%d", random.GetRandom()%numAccounts)
}

// reference returns a globally-unique transaction reference, so no two
// transactions ever collide on it.
func reference() string {
	return fmt.Sprintf("model-ref-%016x%016x", random.GetRandom(), random.GetRandom())
}

// idempotencyKey returns a fresh key per operation, reused across the client's
// retries so an ambiguous commit replays to the committed result instead of
// re-applying.
func idempotencyKey() string {
	return fmt.Sprintf("model-%016x%016x", random.GetRandom(), random.GetRandom())
}

// sendOperation dispatches op to the ledger and returns one transaction per
// committed leaf (one for a single op, N for a bulk). A definitive business
// failure (including a bulk's atomic rejection) is returned as an error for the
// failure path; a transient error surfaces as-is.
func sendOperation(ctx context.Context, cl *client.Formance, ledger string, op Operation) ([]*shared.V2Transaction, error) {
	switch op.kind {
	case opCreateTx:
		res, err := cl.Ledger.V2.CreateTransaction(ctx, operations.V2CreateTransactionRequest{
			Ledger:         ledger,
			IdempotencyKey: pointer.For(op.idemKey),
			V2PostTransaction: shared.V2PostTransaction{
				Postings:        toSDKPostings(op.postings),
				Reference:       pointer.For(op.reference),
				Metadata:        op.metadata,
				AccountMetadata: op.accountMeta,
			},
		})
		if err != nil {
			return nil, err
		}

		return []*shared.V2Transaction{&res.V2CreateTransactionResponse.Data}, nil

	case opRevert:
		id, ok := new(big.Int).SetString(op.targetID, 10)
		if !ok {
			return nil, fmt.Errorf("invalid revert target id %q", op.targetID)
		}
		// Force (when set) skips the balance check so the reversal can't fail on
		// funds. The idempotency key makes an ambiguous (committed-but-lost) revert
		// replay to its committed result on retry instead of returning ALREADY_REVERT.
		res, err := cl.Ledger.V2.RevertTransaction(ctx, operations.V2RevertTransactionRequest{
			Ledger:         ledger,
			ID:             id,
			Force:          pointer.For(op.force),
			IdempotencyKey: pointer.For(op.idemKey),
		})
		if err != nil {
			return nil, err
		}

		return []*shared.V2Transaction{&res.V2RevertTransactionResponse.Data}, nil

	case opBulk:
		return sendBulk(ctx, cl, ledger, op)

	default:
		panic(fmt.Sprintf("sendOperation: unmodeled kind %d", op.kind))
	}
}

// sendBulk dispatches an atomic bulk and normalizes the response: the per-element
// transactions on success, or a synthesized error carrying the first failing
// element's code (the bulk endpoint returns the body on both 200 and 400, so a
// rejection is not a transport error).
func sendBulk(ctx context.Context, cl *client.Formance, ledger string, op Operation) ([]*shared.V2Transaction, error) {
	elements := make([]shared.V2BulkElement, len(op.bulk))
	for i, sub := range op.bulk {
		switch sub.kind {
		case opRevert:
			id, ok := new(big.Int).SetString(sub.targetID, 10)
			if !ok {
				return nil, fmt.Errorf("invalid revert target id %q", sub.targetID)
			}
			// A non-forced element that overdrafts, or a revert of an
			// already-reverted target, rejects the whole atomic bulk.
			elements[i] = shared.CreateV2BulkElementRevertTransaction(shared.V2BulkElementRevertTransaction{
				Action: string(shared.V2BulkElementTypeRevertTransaction),
				Ik:     pointer.For(sub.idemKey),
				Data: &shared.V2BulkElementRevertTransactionData{
					ID:    id,
					Force: pointer.For(sub.force),
				},
			})
		default:
			elements[i] = shared.CreateV2BulkElementCreateTransaction(shared.V2BulkElementCreateTransaction{
				Action: string(shared.V2BulkElementTypeCreateTransaction),
				Ik:     pointer.For(sub.idemKey),
				Data: &shared.V2PostTransaction{
					Postings:  toSDKPostings(sub.postings),
					Reference: pointer.For(sub.reference),
				},
			})
		}
	}

	res, err := cl.Ledger.V2.CreateBulk(ctx, operations.V2CreateBulkRequest{
		Ledger:      ledger,
		Atomic:      pointer.For(true),
		RequestBody: elements,
	})
	if err != nil {
		return nil, err
	}

	return parseBulkResponse(res.V2BulkResponse)
}

// parseBulkResponse extracts the per-element transactions from a bulk response,
// or returns the first element error / top-level error as a business error.
func parseBulkResponse(b *shared.V2BulkResponse) ([]*shared.V2Transaction, error) {
	if b == nil {
		return nil, fmt.Errorf("bulk: empty response")
	}
	if b.ErrorCode != nil {
		return nil, &sdkerrors.V2ErrorResponse{ErrorCode: *b.ErrorCode}
	}

	data := make([]*shared.V2Transaction, 0, len(b.Data))
	for _, r := range b.Data {
		switch r.Type {
		case shared.V2BulkElementResultTypeCreateTransaction:
			tx := r.V2BulkElementResultCreateTransactionSchemas.Data
			data = append(data, &tx)
		case shared.V2BulkElementResultTypeRevertTransaction:
			tx := r.V2BulkElementResultRevertTransactionSchemas.Data
			data = append(data, &tx)
		case shared.V2BulkElementResultTypeError:
			return nil, &sdkerrors.V2ErrorResponse{ErrorCode: shared.V2ErrorsEnum(r.V2BulkElementResultErrorSchemas.ErrorCode)}
		default:
			return nil, fmt.Errorf("bulk: unexpected result type %q", r.Type)
		}
	}

	return data, nil
}

// --- Metadata --------------------------------------------------------------

// metaOp is a dispatched metadata write.
type metaOp struct {
	cell    metaCell
	write   *metaWrite
	idemKey string
}

// generateMetaWrite plans a metadata set or delete: ~1/4 delete a currently
// present cell, the rest set a key on a random account or committed transaction
// to a fresh unique value (unique so a read pinpoints which write it observed).
// Registers the write and returns it. Caller holds c.mu.
func generateMetaWrite(c *Checker) metaOp {
	if random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
		if present := c.metaStore.presentCells(); len(present) > 0 {
			slices.SortFunc(present, compareMetaCell)
			cell := random.RandomChoice(present)
			w := &metaWrite{deleted: true, dispatch: c.ticketSeq.Add(1)}
			c.metaStore.register(cell, w)
			return metaOp{cell: cell, write: w, idemKey: idempotencyKey()}
		}
	}

	cell := pickMetaSetCell(c)
	w := &metaWrite{value: metaValue(), dispatch: c.ticketSeq.Add(1)}
	c.metaStore.register(cell, w)
	return metaOp{cell: cell, write: w, idemKey: idempotencyKey()}
}

// pickMetaSetCell chooses the target of a metadata set: ~1/4 the ledger itself,
// ~1/4 a committed transaction (falling back to an account when none exists yet),
// else a pool account.
func pickMetaSetCell(c *Checker) metaCell {
	switch random.RandomChoice([]uint8{0, 1, 2, 3}) {
	case 0:
		return metaCell{kind: metaLedger, key: metaKeyName()}
	case 1:
		if ids := committedTxIDs(c.modelState); len(ids) > 0 {
			return metaCell{kind: metaTransaction, id: random.RandomChoice(ids), key: metaKeyName()}
		}
	}

	return metaCell{kind: metaAccount, id: poolAddress(), key: metaKeyName()}
}

// sendMetaOp dispatches a metadata set or delete to the ledger.
func sendMetaOp(ctx context.Context, cl *client.Formance, ledger string, op metaOp) error {
	switch op.cell.kind {
	case metaAccount:
		if op.write.deleted {
			_, err := cl.Ledger.V2.DeleteAccountMetadata(ctx, operations.V2DeleteAccountMetadataRequest{
				Ledger: ledger, Address: op.cell.id, Key: op.cell.key,
				IdempotencyKey: pointer.For(op.idemKey),
			})
			return err
		}
		_, err := cl.Ledger.V2.AddMetadataToAccount(ctx, operations.V2AddMetadataToAccountRequest{
			Ledger: ledger, Address: op.cell.id,
			IdempotencyKey: pointer.For(op.idemKey),
			RequestBody:    map[string]string{op.cell.key: op.write.value},
		})
		return err

	case metaTransaction:
		id, ok := new(big.Int).SetString(op.cell.id, 10)
		if !ok {
			return fmt.Errorf("invalid transaction id %q", op.cell.id)
		}
		if op.write.deleted {
			_, err := cl.Ledger.V2.DeleteTransactionMetadata(ctx, operations.V2DeleteTransactionMetadataRequest{
				Ledger: ledger, ID: id, Key: op.cell.key,
				IdempotencyKey: pointer.For(op.idemKey),
			})
			return err
		}
		_, err := cl.Ledger.V2.AddMetadataOnTransaction(ctx, operations.V2AddMetadataOnTransactionRequest{
			Ledger: ledger, ID: id,
			IdempotencyKey: pointer.For(op.idemKey),
			RequestBody:    map[string]string{op.cell.key: op.write.value},
		})
		return err

	case metaLedger:
		// Ledger metadata endpoints carry no idempotency key; set and delete are
		// naturally idempotent, so a replayed retry re-applies the same effect.
		if op.write.deleted {
			_, err := cl.Ledger.V2.DeleteLedgerMetadata(ctx, operations.V2DeleteLedgerMetadataRequest{
				Ledger: ledger, Key: op.cell.key,
			})
			return err
		}
		_, err := cl.Ledger.V2.UpdateLedgerMetadata(ctx, operations.V2UpdateLedgerMetadataRequest{
			Ledger:      ledger,
			RequestBody: map[string]string{op.cell.key: op.write.value},
		})
		return err
	}

	return nil
}

// committedTxIDs returns the model's committed transaction ids, sorted.
func committedTxIDs(s State) []string {
	ids := make([]string, 0, len(s.txs))
	for id := range s.txs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return ids
}

// metaKeyName draws from a small key pool so concurrent writes contend.
func metaKeyName() string {
	return fmt.Sprintf("mk:%d", random.GetRandom()%numMetaKeys)
}

// metaValue is a globally-unique value, so a read pinpoints the write it saw.
func metaValue() string {
	return fmt.Sprintf("mv-%016x", random.GetRandom())
}

func compareMetaCell(a, b metaCell) int {
	if a.kind != b.kind {
		return int(a.kind) - int(b.kind)
	}
	if a.id != b.id {
		return strings.Compare(a.id, b.id)
	}

	return strings.Compare(a.key, b.key)
}

// toSDKPostings converts model postings to the SDK type.
func toSDKPostings(ps []Posting) []shared.V2Posting {
	out := make([]shared.V2Posting, len(ps))
	for i, p := range ps {
		out[i] = shared.V2Posting{
			Amount:      p.Amount,
			Asset:       p.Asset,
			Source:      p.Source,
			Destination: p.Destination,
		}
	}

	return out
}

// fromSDKPostings converts SDK postings back to the model type.
func fromSDKPostings(ps []shared.V2Posting) []Posting {
	out := make([]Posting, len(ps))
	for i, p := range ps {
		out[i] = Posting{
			Source:      p.Source,
			Destination: p.Destination,
			Asset:       p.Asset,
			Amount:      p.Amount,
		}
	}

	return out
}
