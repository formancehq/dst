package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

func main() {
	log.Println("composer: parallel_driver_transactions")

	ctx := context.Background()
	client := internal.NewClient()
	

	callCtx, cancel := internal.ApiCallContext(ctx, time.Second)
	defer cancel()
	ledger, err := internal.GetRandomLedger(callCtx, client)
	if internal.Faults {
		assert.Sometimes(err == nil, "should be able to get a random ledger", internal.Details{
			"error": err,
		})
		if err != nil {
			return
		}
	} else {
		assert.Always(err == nil, "should be able to get a random ledger", internal.Details{
			"error": err,
		})
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
	case 1:
		CreateRandomNumscriptTransaction(ctx, client, ledger, txTime)
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

	forced := random.RandomChoice([]bool{false, true})

	callCtx, cancel := internal.ApiCallContext(ctx, time.Second)
	defer cancel()
	res, err := client.Ledger.V2.CreateTransaction(callCtx, operations.V2CreateTransactionRequest{
		Ledger: ledger,
		V2PostTransaction: shared.V2PostTransaction{
			Postings: postings,
			Timestamp: timestamp,
			Metadata: metadata,
			Force: pointer.For(forced),
		},
	})
	details := internal.Details{
		"ledger": ledger,
		"postings": postings,
		"error": err,
	}
	if internal.Faults {
		assert.Sometimes(err == nil, "should be able to create a postings transaction", details)
		if err != nil {
			return
		}
	} else {
		assert.Always(err == nil, "should be able to create a postings transaction", details)
	}

	// Check that we can read it immediately
	_, err = client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     res.V2CreateTransactionResponse.Data.ID,
	})
	details = internal.Details{
		"ledger": ledger,
		"txId": res.V2CreateTransactionResponse.Data.ID,
	}
	if internal.Faults {
		var getTxError *sdkerrors.V2ErrorResponse
		if errors.As(err, &getTxError) {
			assert.Always(getTxError.ErrorCode != shared.V2ErrorsEnumNotFound, "should always be able to read committed postings transactions", details)
		}
	} else {
		assert.Always(err == nil, "should be able to read committed postings transactions", details)
	}

	if !forced {
		initialOverdrafts := make(map[string]map[string]*big.Int)
		for account, volumes := range res.V2CreateTransactionResponse.Data.PreCommitVolumes {
			initialOverdrafts[account] = make(map[string]*big.Int)
			for asset, volume := range volumes {
				if volume.Balance.Sign() == -1 {
					initialOverdrafts[account][asset] = (&big.Int{}).Neg(volume.Balance)
				}
			}
		}
	
		for account, volumes := range res.V2CreateTransactionResponse.Data.PostCommitVolumes {
			var allowedOverdraft map[string]*big.Int
			if account != "world" {
				allowedOverdraft = initialOverdrafts[account]
			}
			internal.CheckVolumes(volumes, allowedOverdraft, internal.Details{
				"ledger": ledger,
				"account": account,
			})
		}
	}
}

// Submits a random numscript transaction
func CreateRandomNumscriptTransaction(
	ctx context.Context,
	client *client.Formance,
	ledger string,
	timestamp *time.Time,
) {
	res, err := client.Ledger.V2.CreateTransaction(ctx, operations.V2CreateTransactionRequest{
		Ledger: ledger,
		V2PostTransaction: shared.V2PostTransaction{
			Script: &shared.V2PostTransactionScript{
				Plain: `
				vars {
					account $from
					account $to
					monetary $amount
				}
				send $amount (
					source = $from allowing unbounded overdraft
					destination = $to
				)
				`,
				Vars:  map[string]string{
					"from": internal.GetRandomAddress(),
					"to": internal.GetRandomAddress(),
					"amount": fmt.Sprintf("COIN %v", internal.RandomBigInt().String()),
				},
			},
			Timestamp: timestamp,
		},
	})
	assert.Sometimes(err==nil, "should be able to create a numscript transaction", internal.Details{
		"ledger": ledger,
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
	var getTxError *sdkerrors.V2ErrorResponse
	if errors.As(err, &getTxError) {
		assert.Always(getTxError.ErrorCode != shared.V2ErrorsEnumNotFound, "should always be able to read committed numscript transactions", internal.Details{
			"ledger": ledger,
			"txId": res.V2CreateTransactionResponse.Data.ID,
		})
	}

	for account, volumes := range res.V2CreateTransactionResponse.Data.PostCommitVolumes {
		internal.CheckVolumes(volumes, nil, internal.Details{
			"ledger": ledger,
			"account": account,
		})
	}
}

func RandomTransactionMetadata() map[string]string {
	metadata := make(map[string]string)
	for range random.GetRandom()%3 {
		key := fmt.Sprintf("%v", random.GetRandom()%999)
		metadata[key] = fmt.Sprintf("%v", random.GetRandom()%999)
	}
	return metadata
}
