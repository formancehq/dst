package main

import (
	"context"
	"fmt"
	"log"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
)

func main() {
	log.Println("composer: parallel_driver_ledger_create")
	ctx := context.Background()
	client := internal.NewClient()
	id := random.GetRandom() % 1e6
	ledger := fmt.Sprintf("ledger-%d", id)

	bucket := ledger
	if random.RandomChoice([]bool{false, true}) {
		id := random.GetRandom() % 5
		bucket = fmt.Sprintf("bucket-%d", id)
	}
	_, err := internal.CreateLedger(
		ctx,
		client,
		ledger,
		bucket,
	)
	if internal.FaultsActive(ctx) {
		assert.Sometimes(err == nil, "ledger should have been created properly", internal.Details{
			"error": err,
		})
	} else {
		assert.Always(err == nil, "ledger should have been created properly", internal.Details{
			"error": err,
		})
	}

	log.Println("composer: parallel_driver_ledger_create: done")
}
