package main

import (
	"context"
	"errors"
	"log"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

func checkVolumesConsistent(ctx context.Context, client *client.Formance, ledger string) {
	details := internal.Details{
		"ledger": ledger,
	}
	var cursor *string
	for {
		volumes, err := client.Ledger.V2.GetVolumesWithBalances(ctx, operations.V2GetVolumesWithBalancesRequest{
			Ledger: ledger,
			Cursor: cursor,
		})
		assert.Sometimes(
			err == nil,
			"can get volumes with balances",
			details.With(internal.Details{
				"error": err,
			}),
		)
		if err != nil {
			return
		}
		for _, volume := range volumes.V2VolumesWithBalanceCursorResponse.Cursor.Data {
			details := details.With(internal.Details{
				"volumes": volume,
			})
			internal.CheckVolume(volume.Input, volume.Output, volume.Balance, details)

			account, err := client.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{
				Ledger:  ledger,
				Address: volume.Account,
				Expand:  pointer.For("volumes"),
			})
			assert.Sometimes(
				err == nil,
				"can get account",
				details.With(internal.Details{
					"error": err,
				}),
			)
			var getAccountError *sdkerrors.V2ErrorResponse
			if errors.As(err, &getAccountError) {
				assert.AlwaysOrUnreachable(getAccountError.ErrorCode != shared.V2ErrorsEnumNotFound, "account reported by volumes endpoint should always exist", details)
				continue
			}
			actualVolumes := account.V2AccountResponse.Data.Volumes
			if actualVolume, ok := actualVolumes[volume.Asset]; ok {
				assert.Always(volume.Balance.Cmp(actualVolume.Balance) == 0, "volumes endpoint balance should match getaccount", details.With(internal.Details{
					"actualBalance": actualVolume.Balance,
				}))
			} else {
				assert.Unreachable("should get requested volumes", details)
			}
		}
		if !volumes.V2VolumesWithBalanceCursorResponse.Cursor.HasMore {
			break
		}
		cursor = volumes.V2VolumesWithBalanceCursorResponse.Cursor.Next
	}
	assert.Reachable(
		"can get all volumes with balances",
		details,
	)
	log.Printf("composer: volumes_consistent: done for ledger %s", ledger)
}
