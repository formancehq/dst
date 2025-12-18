package main

import (
	"context"
	"fmt"
	"log"
	"maps"

	"github.com/alitto/pond"
	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"

	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"go.etcd.io/etcd/client/v3/concurrency"
)

func main() {
	log.Println("composer: parallel_driver_account_metadata")

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

	etcd, err := internal.NewEtcdClient()
	if err != nil {
		return
	}

	accounts, err := internal.ListAccounts(ctx, client, ledger)
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
			SetAccountMetadata(ctx, client, session, ledger, accounts)
		})
	}

	pool.StopAndWait()

	log.Println("composer: parallel_driver_account_metadata: done")
}

func SetAccountMetadata(
	ctx context.Context,
	client *client.Formance,
	session *concurrency.Session,
	ledger string,
	accounts []string,
) {

	account := accounts[int(random.GetRandom()%uint64(len(accounts)))]
	mutex := concurrency.NewMutex(session, fmt.Sprintf("/ledger/%v/account/%v", ledger, account))
	if err := mutex.Lock(ctx); err != nil {
		return
	}
	//nolint:errcheck
	defer mutex.Unlock(ctx)

	preAcc, err := client.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{
		Ledger:  ledger,
		Address: account,
	})
	assert.Sometimes(err == nil, "should be able to get existing account before metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	randomMetadata := internal.RandomMetadata()
	log.Printf("Adding metadata %v to account %v\n", randomMetadata, account)
	_, err = client.Ledger.V2.AddMetadataToAccount(ctx, operations.V2AddMetadataToAccountRequest{
		Ledger:      ledger,
		Address:     account,
		RequestBody: randomMetadata,
	})
	assert.Sometimes(err == nil, "should be able to set metadata on account", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	// Check that we can read it immediately
	getAccRes, err := client.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{
		Ledger:  ledger,
		Address: account,
	})
	assert.Sometimes(err == nil, "should be able to get existing account after metadata change", internal.Details{
		"ledger": ledger,
		"error":  err,
	})
	if err != nil {
		return
	}

	expectedMetadata := maps.Clone(preAcc.V2AccountResponse.Data.Metadata)
	for k, v := range randomMetadata {
		expectedMetadata[k] = v
	}
	assert.Always(maps.Equal(getAccRes.V2AccountResponse.Data.Metadata, expectedMetadata), "new account metadata should be correct", internal.Details{
		"ledger":   ledger,
		"account":  account,
		"original": preAcc.V2AccountResponse.Data.Metadata,
		"added":    randomMetadata,
		"actual":   getAccRes.V2AccountResponse.Data.Metadata,
		"expected": expectedMetadata,
	})
}
