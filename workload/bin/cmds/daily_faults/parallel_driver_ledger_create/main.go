package main

import (
	"context"
	"fmt"
	"log"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
)

func main() {
	log.Println("composer: parallel_driver_ledger_create")
	ctx := context.Background()
	client := internal.NewClient()
	id := random.GetRandom()%1e6
	ledger := fmt.Sprintf("ledger-%d", id)

	internal.CreateLedger(
		ctx,
		client,
		ledger,
	)

	log.Println("composer: parallel_driver_ledger_create: done")
}
