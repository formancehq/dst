package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math/big"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"

	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"go.etcd.io/etcd/client/v3/concurrency"
)

func main() {
	log.Println("composer: parallel_driver_transaction_metadata")

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

	lastTxID, err := internal.GetLastTransactionID(ctx, client, ledger)
	if err != nil {
		return
	}
	if lastTxID == nil {
		return
	}

	etcd, err := internal.NewEtcdClient()
	if err != nil {
		return
	}

	for range count {
		pool.Submit(func() {
			session, err := concurrency.NewSession(etcd)
			if err != nil {
				return
			}
			//nolint:errcheck
			defer session.Close()
			SetTransactionMetadata(ctx, client, session, ledger, *lastTxID)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_transaction_metadata: done")
}

func SetTransactionMetadata(
	ctx context.Context,
	client *client.Formance,
	session *concurrency.Session,
	ledger string,
	lastTxID big.Int,
) {
	id := random.GetRandom()

	txID := big.NewInt(int64(random.GetRandom() % lastTxID.Uint64()))
	mutex := concurrency.NewMutex(session, fmt.Sprintf("/ledger/%v/transaction/%v", ledger, txID))
	if err := mutex.Lock(ctx); err != nil {
		return
	}
	//nolint:errcheck
	defer mutex.Unlock(ctx)

	log.Printf("%v -> Locked tx %v (%v)\n", id, txID, ledger)

	preTx, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     txID,
	})
	assert.Sometimes(err == nil, "should be able to get existing transaction before metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	randomMetadata := internal.RandomMetadata()
	log.Printf("%v -> Adding metadata %v to transaction %v\n", id, randomMetadata, txID)
	_, err = client.Ledger.V2.AddMetadataOnTransaction(ctx, operations.V2AddMetadataOnTransactionRequest{
		Ledger:      ledger,
		ID:          txID,
		RequestBody: randomMetadata,
	})
	assert.Sometimes(err == nil, "should be able to set metadata on transaction", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	// Check that we can read it immediately
	getTxRes, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     txID,
	})
	assert.Sometimes(err == nil, "should be able to get existing transaction after metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	expectedMetadata := maps.Clone(preTx.V2GetTransactionResponse.Data.Metadata)
	for k, v := range randomMetadata {
		expectedMetadata[k] = v
	}
	assert.Always(maps.Equal(getTxRes.V2GetTransactionResponse.Data.Metadata, expectedMetadata), "new transaction metadata should be correct", internal.Details{
		"ledger":   ledger,
		"txID":     txID,
		"original": preTx.V2GetTransactionResponse.Data.Metadata,
		"added":    randomMetadata,
		"actual":   getTxRes.V2GetTransactionResponse.Data.Metadata,
		"expected": expectedMetadata,
	})
	fmt.Printf("%v -> finished", id)
}
