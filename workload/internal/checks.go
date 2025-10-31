package internal

import (
	"math/big"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// overdraft will only be checked if allowedOverdraft is not nil
func CheckVolumes(volumes map[string]shared.V2Volume, allowedOverdraft map[string]*big.Int, details Details) {
	for asset, volume := range volumes {
		balance := new(big.Int).Set(volume.Input)
		balance.Sub(balance, volume.Output)
		CheckVolume(volume.Input, volume.Output, volume.Balance, details.With(Details{
			"asset": asset,
		}))
		if allowedOverdraft != nil {
			minimumBalance := big.NewInt(0)
			if overdraft, ok := allowedOverdraft[asset]; ok {
				minimumBalance.Neg(overdraft)
			}
			assert.Always(volume.Balance.Cmp(minimumBalance) != -1, "balance exceeds allowed overdraft", details.With(Details{
				"asset":     asset,
				"volume":    volume,
				"overdraft": allowedOverdraft[asset],
			}))
		}
	}
}

func CheckVolume(input *big.Int, output *big.Int, balance *big.Int, details Details) {
	actualBalance := new(big.Int).Set(input)
	actualBalance.Sub(actualBalance, output)
	assert.Always(balance.Cmp(balance) == 0, "reported balance and volumes should be consistent", details.With(Details{
		"input":         input,
		"output":        output,
		"balance":       balance,
		"actualBalance": actualBalance,
	}))
}
