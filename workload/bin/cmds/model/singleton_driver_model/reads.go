package main

import (
	"context"
	"math/big"
	"slices"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v2/pointer"
)

// runRead picks a known cell, issues a GetAccount, and validates the returned
// volumes against the model (validateAccountRead).
func runRead(ctx context.Context, cl *client.Formance, c *Checker) {
	c.mu.Lock()
	addr, asset, ok := pickCell(c.modelState)
	if !ok {
		c.mu.Unlock()
		return
	}
	readID := c.registerRead()
	c.mu.Unlock()
	defer c.finishRead(readID)

	acct, err := cl.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{
		Ledger:  c.ledger,
		Address: addr,
		Expand:  pointer.For("volumes"),
	})
	// High-water at the read's response: only operations dispatched by now could
	// be reflected in what the server returned. Captured before validation so
	// later dispatches aren't folded into this read's candidate states.
	maxTicket := c.ticketSeq.Load()
	if err != nil {
		if isTransient(err) {
			return
		}
		// NotFound = no entries server-side: validate as zero volumes for the asset.
		if e, ok := errorResponse(err); ok && e.ErrorCode == shared.V2ErrorsEnumNotFound {
			c.validateAccountRead(maxTicket, addr, asset, big.NewInt(0), big.NewInt(0), false)
			return
		}
		assert.Unreachable("singleton_driver_model: GetAccount returned unexpected error", internal.Details{
			"ledger":  c.ledger,
			"address": addr,
			"asset":   asset,
			"error":   err.Error(),
		})
		return
	}

	gotIn, gotOut, found := accountAssetVolumes(acct.V2AccountResponse.Data, asset)
	c.validateAccountRead(maxTicket, addr, asset, gotIn, gotOut, found)
}

// pickCell returns a random readable cell from the committed state as
// (address, asset), or ok=false if there are none. Picks over a sorted slice so
// the choice is replayable / steerable, unlike map-iteration order. Caller holds
// c.mu.
func pickCell(s State) (addr, asset string, ok bool) {
	keys := make([]VolumeKey, 0, len(s.volumes))
	for k := range s.volumes {
		keys = append(keys, k)
	}

	if len(keys) == 0 {
		return "", "", false
	}

	slices.SortFunc(keys, compareVolumeKey)
	k := random.RandomChoice(keys)

	return k.Address, k.Asset, true
}

// accountAssetVolumes extracts (input, output) for one asset from a GetAccount
// response. found=false when the asset entry is missing.
func accountAssetVolumes(acct shared.V2Account, asset string) (in, out *big.Int, found bool) {
	v, ok := acct.Volumes[asset]
	if !ok {
		return big.NewInt(0), big.NewInt(0), false
	}

	return v.GetInput(), v.GetOutput(), true
}
