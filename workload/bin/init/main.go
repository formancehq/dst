package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/lifecycle"
	"github.com/formancehq/dst/workload/internal"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
)

func main() {
	ctx := context.Background()
	client := internal.NewClient()

	for {
		time.Sleep(time.Second)

		_, err := client.Ledger.V2.ListLedgers(ctx, operations.V2ListLedgersRequest{})
		if err != nil {
			fmt.Printf("Not ready: %s\n", err)
			continue
		}
		break
	}

	lifecycle.SetupComplete(map[string]any{})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	<-sigs
}
