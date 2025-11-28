package main

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

// Submit a bulk of transactions containing an invalid one, and check that no transaction was commited.
// We send the correct transactions to `never:*` accounts which will get later checked.
func SubmitInvalidAtomicBulk(
	ctx context.Context,
	client *client.Formance,
	ledger string,
	timestamp time.Time,
) {
	txCount := random.GetRandom() % 20

	// insert valid transactions
	elements := []shared.V2BulkElement{}
	for range txCount {
		destination := random.RandomChoice([]string{"world", fmt.Sprintf("never:bulk_atomicity:%d", random.GetRandom()%internal.USER_ACCOUNT_COUNT)})
		elements = append(elements, shared.CreateV2BulkElementCreateTransaction(
			shared.V2BulkElementCreateTransaction{
				Ik: nil,
				Data: &shared.V2PostTransaction{
					Timestamp: internal.RandomTimestamp(timestamp),
					Postings: []shared.V2Posting{
						{
							Amount:      big.NewInt(100),
							Asset:       "COIN",
							Destination: destination,
							Source:      "world",
						},
					},
				},
			},
		))
	}

	// insert an invalid transaction
	elements = slices.Insert(elements, int(random.GetRandom()%(txCount+1)), shared.CreateV2BulkElementCreateTransaction(
		shared.V2BulkElementCreateTransaction{
			Ik: nil,
			Data: &shared.V2PostTransaction{
				Timestamp: internal.RandomTimestamp(timestamp),
				Postings: []shared.V2Posting{
					{
						Amount:      big.NewInt(100),
						Asset:       "COIN",
						Destination: "world",
						Source:      "never:eee7dc04-a122-47a5-bac3-7118d5dec363",
					},
				},
			},
		},
	))
	_, err := client.Ledger.V2.CreateBulk(ctx, operations.V2CreateBulkRequest{
		Ledger:            ledger,
		ContinueOnFailure: pointer.For(false),
		Atomic:            pointer.For(true),
		Parallel:          pointer.For(false),
		RequestBody:       elements,
	})
	if internal.FaultsActive() {
		assert.Sometimes(err == nil, "bulk should be committed successfully", internal.Details{
			"ledger":   ledger,
			"elements": elements,
			"error":    err,
		})
	} else {
		assert.Always(err == nil, "bulk should be committed successfully", internal.Details{
			"ledger":   ledger,
			"elements": elements,
			"error":    err,
		})
	}
	if err != nil {
		return
	}
}
