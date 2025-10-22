package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"sync"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/dst/workload/internal"
)

func main() {
	log.Println("composer: eventually_correct")
	ctx := context.Background()
	client := internal.NewClient()

	ledgers, err := client.Ledger.V2.ListLedgers(ctx, operations.V2ListLedgersRequest{})

	assert.Sometimes(err == nil, "error listing ledgers", internal.Details {
		"error": err,
	})
	if err != nil {
		return
	}

	wg := sync.WaitGroup{}
	for _, ledger := range ledgers.V2LedgerListResponse.Cursor.Data {
		wg.Add(1)
		go func(ledger string) {
			defer wg.Done()
			checkBalanced(ctx, client, ledger)
			checkAccountBalances(ctx, client, ledger)
			checkNeverAccounts(ctx, client, ledger)
		}(ledger.Name)
	}
	wg.Wait()
}

func checkBalanced(ctx context.Context, client *client.Formance, ledger string) {
	aggregated, err := client.Ledger.V2.GetBalancesAggregated(ctx, operations.V2GetBalancesAggregatedRequest{
		Ledger: ledger,
	})
	assert.Sometimes(
		err == nil,
		"Client can aggregate balances",
		internal.Details{
			"ledger": ledger,
			"error": err,
		},
	)
	if err != nil {
		return
	}

	for asset, volumes := range aggregated.V2AggregateBalancesResponse.Data {
		assert.Always(
			volumes.Cmp(new(big.Int)) == 0,
			fmt.Sprintf("aggregated volumes for asset %s should be 0",
				asset,
			), internal.Details{
				"asset": asset,
				"volumes": volumes,
			})
	}

	log.Printf("composer: balanced: done for ledger %s", ledger)
}

func checkAccountBalances(ctx context.Context, client *client.Formance, ledger string) {
	for i := range internal.USER_ACCOUNT_COUNT {
		address := fmt.Sprintf("users:%d", i)
		account, err := client.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{
			Ledger: ledger,
			Address: address,
		})
		assert.Sometimes(err == nil, "Client can aggregate account balances", internal.Details{
			"ledger": ledger,
			"address": address,
			"error": err,
		})
		if err != nil {
			continue
		}
		internal.CheckVolumes(account.V2AccountResponse.Data.Volumes, nil, internal.Details{
			"ledger": ledger,
			"address": address,
		})
	}

	log.Printf("composer: account balances check: done for ledger %s", ledger)
}

func checkNeverAccounts(ctx context.Context, client *client.Formance, ledger string) {
	txs, err := client.Ledger.V2.ListTransactions(ctx, operations.V2ListTransactionsRequest{
		Ledger:      ledger,
		RequestBody: map[string]interface{}{
			"$match": map[string]any{
				"address": "never:",
			},
		},
	})
	assert.Sometimes(err == nil, "should be able to get the latest transaction", internal.Details {
		"error": err,
	})
	if err != nil {
		return
	}

	assert.Always(len(txs.V2TransactionsCursorResponse.Cursor.Data) == 0, "no transaction should involve never:* accounts", internal.Details {
		"ledger": ledger,
		"transactions": txs.V2TransactionsCursorResponse.Cursor.Data,
	})
}
