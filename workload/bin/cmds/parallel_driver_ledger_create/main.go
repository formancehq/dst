package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

func main() {
	log.Println("composer: parallel_driver_ledger_create")
	ctx := context.Background()
	client := internal.NewClient()
	id := random.GetRandom()%1e6
	ledger := fmt.Sprintf("ledger-%d", id)

	_, err := internal.CreateLedger(
		ctx,
		client,
		ledger,
		ledger,
	)
	if internal.Faults {
		assert.Sometimes(err == nil, "ledger should have been created properly", internal.Details{
			"error": err,
		})
		if err != nil {
			return
		}
	} else {
		assert.Always(err == nil, "ledger should have been created properly", internal.Details{
			"error": err,
		})
	}

	// Check that we can read it immediately
	_, err = client.Ledger.V2.GetLedger(ctx, operations.V2GetLedgerRequest{
		Ledger: ledger,
	})
	var getLedgerError *sdkerrors.V2ErrorResponse
	if errors.As(err, &getLedgerError) {
		assert.Always(getLedgerError.ErrorCode != shared.V2ErrorsEnumNotFound, "should always be able to get created ledger", internal.Details{
			"ledger": ledger,
		})
	}

	log.Println("composer: parallel_driver_ledger_create: done")
}
