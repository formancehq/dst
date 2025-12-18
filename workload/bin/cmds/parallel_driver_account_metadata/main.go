package main

import (
	"context"
	"errors"
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
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"go.etcd.io/etcd/client/v3/concurrency"
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
			defer session.Close()
			SetAccountMetadata(ctx, client, session, ledger, *lastTxID)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_transactions: done")
}

func SetAccountMetadata(
	ctx context.Context,
	client *client.Formance,
	session *concurrency.Session,
	ledger string,
	lastAccountID big.Int,
) {

	accountID := big.NewInt(int64(random.GetRandom() % lastAccountID.Uint64()))
	mutex := concurrency.NewMutex(session, fmt.Sprintf("/ledger/%v/transaction/%v", ledger, accountID))
	if err := mutex.Lock(ctx); err != nil {
		return
	}
	//nolint:errcheck
	defer mutex.Unlock(ctx)

	preTx, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     accountID,
	})
	assert.Sometimes(err == nil, "should be able to get an existing transaction before metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	randomMetadata := internal.RandomTransactionMetadata()
	_, err = client.Ledger.V2.AddMetadataOnTransaction(ctx, operations.V2AddMetadataOnTransactionRequest{
		Ledger:      ledger,
		ID:          accountID,
		RequestBody: randomMetadata,
	})
	assert.Sometimes(err == nil, "should be able to set metadata on a transaction", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	// Check that we can read it immediately
	getTxRes, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     accountID,
	})
	assert.Sometimes(err == nil, "should be able to get existing transaction", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		var getTxError *sdkerrors.V2ErrorResponse
		if errors.As(err, &getTxError) {
			assert.AlwaysOrUnreachable(getTxError.ErrorCode != shared.V2ErrorsEnumNotFound, "existing transaction should never be NOT_FOUND", internal.Details{
				"ledger": ledger,
				"txId":   accountID,
			})
		}
		return
	}

	expectedMetadata := maps.Clone(preTx.V2GetTransactionResponse.Data.Metadata)
	for k, v := range randomMetadata {
		expectedMetadata[k] = v
	}
	assert.Always(maps.Equal(getTxRes.V2GetTransactionResponse.Data.Metadata, expectedMetadata), "new transaction metadata should be correct", internal.Details{
		"ledger":   ledger,
		"actual":   getTxRes.V2GetTransactionResponse.Data.Metadata,
		"expected": expectedMetadata,
	})
}

func SetTransactionMetadata(
	ctx context.Context,
	client *client.Formance,
	session *concurrency.Session,
	ledger string,
	lastTxID big.Int,
) {

	txID := big.NewInt(int64(random.GetRandom() % lastTxID.Uint64()))
	mutex := concurrency.NewMutex(session, fmt.Sprintf("/ledger/%v/transaction/%v", ledger, txID))
	if err := mutex.Lock(ctx); err != nil {
		return
	}
	//nolint:errcheck
	defer mutex.Unlock(ctx)

	preTx, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
		ID:     txID,
	})
	assert.Sometimes(err == nil, "should be able to get an existing transaction before metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	randomMetadata := internal.RandomTransactionMetadata()
	_, err = client.Ledger.V2.AddMetadataOnTransaction(ctx, operations.V2AddMetadataOnTransactionRequest{
		Ledger:      ledger,
		ID:          txID,
		RequestBody: randomMetadata,
	})
	assert.Sometimes(err == nil, "should be able to set metadata on a transaction", internal.Details{
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
	assert.Sometimes(err == nil, "should be able to get existing transaction", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		var getTxError *sdkerrors.V2ErrorResponse
		if errors.As(err, &getTxError) {
			assert.AlwaysOrUnreachable(getTxError.ErrorCode != shared.V2ErrorsEnumNotFound, "existing transaction should never be NOT_FOUND", internal.Details{
				"ledger": ledger,
				"txId":   txID,
			})
		}
		return
	}

	expectedMetadata := maps.Clone(preTx.V2GetTransactionResponse.Data.Metadata)
	for k, v := range randomMetadata {
		expectedMetadata[k] = v
	}
	assert.Always(maps.Equal(getTxRes.V2GetTransactionResponse.Data.Metadata, expectedMetadata), "new transaction metadata should be correct", internal.Details{
		"ledger":   ledger,
		"actual":   getTxRes.V2GetTransactionResponse.Data.Metadata,
		"expected": expectedMetadata,
	})
}
