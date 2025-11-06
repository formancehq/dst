package internal

import (
	"math/big"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

func CheckVolumes(volumes map[string]shared.V2Volume, allowedOverdraft map[string]*big.Int, details Details) {
	for asset, volume := range volumes {
		balance := new(big.Int).Set(volume.Input)
		balance.Sub(balance, volume.Output)
		assert.Always(balance.Cmp(volume.Balance) == 0, "reported balance and volumes should be consistent", details.with(Details{
			"asset":  asset,
			"volume": volume,
		}))
		if allowedOverdraft != nil {
			minimumBalance := big.NewInt(0)
			if overdraft, ok := allowedOverdraft[asset]; ok {
				minimumBalance.Neg(overdraft)
			}
			assert.Always(volume.Balance.Cmp(minimumBalance) != -1, "balance exceeds allowed overdraft", details.with(Details{
				"asset":     asset,
				"volume":    volume,
				"overdraft": allowedOverdraft[asset],
			}))
		}
	}
}
