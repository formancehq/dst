package main

import (
	"context"
	"fmt"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

// generateOperation plans the next operation. Only world-sourced create
// transactions so far: every posting debits world (overdraftable), so the
// operation always commits and the model needs no insufficient-funds modeling.
// The small address pool makes cells recur, so volumes accumulate and concurrent
// reads land on contended cells.
func generateOperation() Operation {
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
