package main

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
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
	if random.RandomChoice([]uint8{0, 1, 2, 3}) == 0 {
		if op, ok := generateRevert(s); ok {
			return op
		}
	}

	if random.RandomChoice([]uint8{0, 1}) == 0 {
		return accountSourcedOp()
	}

	return worldSourcedOp()
}

// generateRevert targets a committed, not-yet-reverted transaction, chosen over a
// sorted slice (replayable, unlike map order). Concurrent picks of the same
// target exercise the ALREADY_REVERT / REVERT_OCCURRING rejection.
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

	return Operation{kind: opRevert, targetID: random.RandomChoice(ids), idemKey: idempotencyKey()}, true
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

// sendOperation dispatches op to the ledger and returns the server's response.
func sendOperation(ctx context.Context, cl *client.Formance, ledger string, op Operation) (*shared.V2Transaction, error) {
	switch op.kind {
	case opCreateTx:
		res, err := cl.Ledger.V2.CreateTransaction(ctx, operations.V2CreateTransactionRequest{
			Ledger:         ledger,
			IdempotencyKey: pointer.For(op.idemKey),
			V2PostTransaction: shared.V2PostTransaction{
				Postings:  toSDKPostings(op.postings),
				Reference: pointer.For(op.reference),
			},
		})
		if err != nil {
			return nil, err
		}

		return &res.V2CreateTransactionResponse.Data, nil

	case opRevert:
		id, ok := new(big.Int).SetString(op.targetID, 10)
		if !ok {
			return nil, fmt.Errorf("invalid revert target id %q", op.targetID)
		}
		// Force skips the balance check so the reversal never fails on funds. The
		// idempotency key makes an ambiguous (committed-but-lost) revert replay to
		// its committed result on retry instead of returning ALREADY_REVERT.
		res, err := cl.Ledger.V2.RevertTransaction(ctx, operations.V2RevertTransactionRequest{
			Ledger:         ledger,
			ID:             id,
			Force:          pointer.For(true),
			IdempotencyKey: pointer.For(op.idemKey),
		})
		if err != nil {
			return nil, err
		}

		return &res.V2RevertTransactionResponse.Data, nil

	default:
		panic(fmt.Sprintf("sendOperation: unmodeled kind %d", op.kind))
	}
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
