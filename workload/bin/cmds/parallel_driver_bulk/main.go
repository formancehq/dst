package main

import (
	"context"
	"log"
	"time"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

func main() {
	log.Println("composer: parallel_driver_bulk")

	ctx := context.Background()
	client := internal.NewClient()

	ledger, err := internal.GetRandomLedger(ctx, client)
	if internal.FaultsActive(ctx) {
		assert.Sometimes(err == nil, "should be able to get a random ledger", internal.Details{
			"error": err,
		})
	} else {
		assert.Always(err == nil, "should be able to get a random ledger", internal.Details{
			"error": err,
		})
	}
	if err != nil {
		return
	}

	const count = 100

	pool := pond.New(10, 10e3)

	presentTime, err := internal.GetPresentTime(ctx, client, ledger)
	if err != nil {
		return
	}

	for range count {
		pool.Submit(func() {
			SubmitInvalidAtomicBulk(ctx, client, ledger, *presentTime)
			SubmitBulk(ctx, client, ledger, *presentTime)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_bulk: done")
}

func SubmitBulk(
	ctx context.Context,
	client *client.Formance,
	ledger string,
	timestamp time.Time,
) {
	txCount := random.GetRandom() % 5

	elements := make([]shared.V2BulkElement, 0)
	for range txCount {
		elements = append(elements, RandomBulkElement(timestamp))
	}
	_, err := client.Ledger.V2.CreateBulk(ctx, operations.V2CreateBulkRequest{
		Ledger:            ledger,
		ContinueOnFailure: pointer.For(false),
		Atomic:            pointer.For(false),
		Parallel:          pointer.For(true),
		RequestBody:       elements,
	})

	if internal.FaultsActive(ctx) {
		assert.Sometimes(err == nil, "bulk should be committed successfully", internal.Details{
			"ledger":   ledger,
			"elements": elements,
			"error":    err,
		})
	} else if !internal.SuccessOrInsufficientFunds(err) {
		assert.Unreachable("bulk should be committed successfully", internal.Details{
			"ledger":   ledger,
			"elements": elements,
			"error":    err,
		})
	}
	if err != nil {
		return
	}
}

func RandomBulkElement(time time.Time) shared.V2BulkElement {
	switch random.RandomChoice([]uint8{0}) {
	case 0:
		return shared.CreateV2BulkElementCreateTransaction(
			shared.V2BulkElementCreateTransaction{
				Ik: nil,
				Data: &shared.V2PostTransaction{
					Timestamp: internal.RandomTimestamp(time),
					Postings:  internal.RandomPostings(),
				},
			},
		)
	default:
		panic("unreachable")
	}
}
