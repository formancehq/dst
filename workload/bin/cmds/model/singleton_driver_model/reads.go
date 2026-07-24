package main

import (
	"context"
	"math/big"
	"slices"
	"strings"

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
// runMetaRead picks a target the model has metadata for, reads it, and validates
// the returned metadata against the model's per-cell registers (validateRead).
func runMetaRead(ctx context.Context, cl *client.Formance, c *Checker) {
	c.mu.Lock()
	target, ok := pickMetaTarget(c)
	dR := c.ticketSeq.Add(1)
	c.mu.Unlock()
	if !ok {
		return
	}

	var server map[string]string
	var err error
	switch target.kind {
	case metaAccount:
		var res *operations.V2GetAccountResponse
		res, err = cl.Ledger.V2.GetAccount(ctx, operations.V2GetAccountRequest{Ledger: c.ledger, Address: target.id})
		if err == nil {
			server = res.V2AccountResponse.Data.Metadata
		}
	case metaTransaction:
		id, _ := new(big.Int).SetString(target.id, 10)
		var res *operations.V2GetTransactionResponse
		res, err = cl.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{Ledger: c.ledger, ID: id})
		if err == nil {
			server = res.V2GetTransactionResponse.Data.Metadata
		}
	case metaLedger:
		var res *operations.V2GetLedgerResponse
		res, err = cl.Ledger.V2.GetLedger(ctx, operations.V2GetLedgerRequest{Ledger: c.ledger})
		if err == nil {
			server = res.V2GetLedgerResponse.Data.Metadata
		}
	}
	// High-water at the read's response: writes dispatched after this can't be in
	// what the server returned.
	oR := c.ticketSeq.Load()

	if err != nil {
		if isTransient(err) {
			return
		}
		// An account with no committed volumes or metadata reads as NOT_FOUND: its
		// metadata is legitimately absent (e.g. its only writes are still in-flight),
		// so validate the empty map rather than flag a fault. Transactions and the
		// ledger always exist once targetable, so NOT_FOUND there is a finding.
		if !(isNotFound(err) && target.kind == metaAccount) {
			assert.Unreachable("singleton_driver_model: metadata read returned unexpected error", internal.Details{
				"ledger": c.ledger,
				"target": target.id,
				"error":  err.Error(),
			})
			return
		}
		server = nil
	}

	c.mu.Lock()
	key, serverVal, valid := c.metaStore.validateRead(target.kind, target.id, dR, oR, server)
	c.mu.Unlock()
	if valid {
		dbg("META READ OK: ledger=%s target=%s", c.ledger, target.id)
		return
	}

	assert.Unreachable("singleton_driver_model: metadata read outside model", internal.Details{
		"ledger":    c.ledger,
		"target":    target.id,
		"key":       key,
		"serverVal": serverVal,
	})
}

// pickMetaTarget returns a random target (account or transaction) the model has
// written metadata to, over a sorted list for replayability. Caller holds c.mu.
func pickMetaTarget(c *Checker) (metaTarget, bool) {
	seen := map[metaTarget]bool{}
	for cell := range c.metaStore.history {
		seen[metaTarget{kind: cell.kind, id: cell.id}] = true
	}
	if len(seen) == 0 {
		return metaTarget{}, false
	}

	targets := make([]metaTarget, 0, len(seen))
	for t := range seen {
		targets = append(targets, t)
	}
	slices.SortFunc(targets, func(a, b metaTarget) int {
		if a.kind != b.kind {
			return int(a.kind) - int(b.kind)
		}
		return strings.Compare(a.id, b.id)
	})

	return random.RandomChoice(targets), true
}

func accountAssetVolumes(acct shared.V2Account, asset string) (in, out *big.Int, found bool) {
	v, ok := acct.Volumes[asset]
	if !ok {
		return big.NewInt(0), big.NewInt(0), false
	}

	return v.GetInput(), v.GetOutput(), true
}
