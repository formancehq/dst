package main

import (
	"context"
	"log"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/dst/workload/internal"
)

func main() {
	log.Println("composer: parallel_driver_transactions")

	ctx := context.Background()
	client := internal.NewClient()

	ledger, err := internal.GetRandomLedger(ctx, client)
	assert.Sometimes(err == nil, "should be able to get a random ledger", internal.Details{
		"error": err,
	})
	if err != nil {
		return
	}

	const count = 100

	pool := pond.New(10, 10e3)

	for range count {
		pool.Submit(func() {
			CreateTransaction(ctx, client, ledger)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_transactions: done")
}

type Postings []shared.V2Posting

func CreateTransaction(
	ctx context.Context,
	client *client.Formance,
	ledger string,
) {
	postings := RandomPostings()
	_, err := client.Ledger.V2.CreateTransaction(ctx, operations.V2CreateTransactionRequest{
		Ledger: ledger,
		V2PostTransaction: shared.V2PostTransaction{
			Postings: postings,
		},
	})

	assert.Sometimes(err == nil, "transaction was committed successfully", internal.Details{
		"ledger": ledger,
		"postings": postings,
		"error": err,
	})
}

func RandomPostings() []shared.V2Posting {
	postings := []shared.V2Posting{}

	for range random.GetRandom()%2+1 {
		source := internal.GetRandomAddress()
		destination := internal.GetRandomAddress()
		amount := internal.RandomBigInt()
		asset := random.RandomChoice([]string{"USD/2", "EUR/2", "COIN"})

		postings = append(postings, shared.V2Posting{
			Amount:      amount,
			Asset:       asset,
			Destination: destination,
			Source:      source,
		})
	}

	return postings
}
