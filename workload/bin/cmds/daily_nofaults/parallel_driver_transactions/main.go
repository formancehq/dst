package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

func main() {
	log.Println("composer: parallel_driver_transactions")

	ctx := context.Background()
	client := internal.NewClient()
	
	ledger, err := internal.GetRandomLedger(ctx, client)
	assert.Always(err == nil, "should be able to get a random ledger", internal.Details{
		"error": err,
	})
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
			CreateTransaction(ctx, client, ledger, presentTime)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_transactions: done")
}

func CreateTransaction(
	ctx context.Context,
	client *client.Formance,
	ledger string,
	presentTime *time.Time,
) {
	offsetTime := presentTime.Add(time.Duration(-int64(random.GetRandom()%10)))
	txTime := random.RandomChoice([]*time.Time{
		nil,
		&offsetTime,
	})
	
	switch random.RandomChoice([]uint8{0, 1}) {
	case 0:
		CreateRandomPostingsTransaction(ctx, client, ledger, txTime)
	}
}

// Submits a transaction with random postings, and checks the response's account volumes.
func CreateRandomPostingsTransaction(
	ctx context.Context,
	client *client.Formance,
	ledger string,
	timestamp *time.Time,
) {
	postings := internal.RandomPostings()
	metadata := RandomTransactionMetadata()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	cancel()
	res, err := client.Ledger.V2.CreateTransaction(ctx, operations.V2CreateTransactionRequest{
		Ledger: ledger,
		V2PostTransaction: shared.V2PostTransaction{
			Postings: postings,
			Timestamp: timestamp,
			Metadata: metadata,
		},
	})
	assert.Always(err == nil, "should be able to create a postings transaction", internal.Details{
		"ledger": ledger,
		"postings": postings,
		"error": err,
	})
	if err != nil {
		return
	}

	// Check that we can read it immediately
	_, err = client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     res.V2CreateTransactionResponse.Data.ID,
	})
	assert.Always(err == nil, "should always be able to read committed postings transactions", internal.Details{
		"ledger": ledger,
		"txId": res.V2CreateTransactionResponse.Data.ID,
	})
}

func RandomTransactionMetadata() map[string]string {
	metadata := make(map[string]string)
	for range random.GetRandom()%3 {
		key := fmt.Sprintf("%v", random.GetRandom()%999)
		metadata[key] = fmt.Sprintf("%v", random.GetRandom()%999)
	}
	return metadata
}
