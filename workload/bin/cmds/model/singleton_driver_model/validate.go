package main

import (
	"math/big"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/dst/workload/internal"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// The model-conformance checks: every observed server outcome — a committed
// transaction, a failure, or a read — is checked against the model over the
// candidate states candidateBases enumerates. Each check asserts at its own
// callsite with a literal message; Antithesis catalogues assertions by callsite
// and literal, so these must not be factored behind a shared assert.

// validateCommit cross-checks a committed transaction against the forward model.
// Successes drain in transaction-id order, so modelState is this transaction's
// exact predecessor and the prediction is deterministic: the model must predict
// commit AND the identical post-commit volumes. modelState advances only on
// agreement. Caller holds c.mu.
func (c *Checker) validateCommit(op Operation, data []*shared.V2Transaction) {
	res := c.modelState.Apply(op)

	if !res.OK {
		assert.Unreachable("singleton_driver_model: model rejects a server-committed transaction", internal.Details{
			"ledger": c.ledger,
			"reason": res.Reason,
			"op":     renderOp(op),
		})

		return
	}

	subs := op.subOps()
	if len(subs) != len(data) || len(subs) != len(res.Orders) {
		assert.Unreachable("singleton_driver_model: bulk element count mismatch", internal.Details{
			"ledger": c.ledger,
			"op":     renderOp(op),
			"model":  len(res.Orders),
			"server": len(data),
			"subOps": len(subs),
		})

		return
	}

	for i, order := range res.Orders {
		server := data[i].GetPostCommitVolumes()
		for key, vp := range order.PCV {
			gotIn, gotOut, ok := pcvVolume(server, key)
			if !ok || vp.Input.Cmp(gotIn) != 0 || vp.Output.Cmp(gotOut) != 0 {
				assert.Unreachable("singleton_driver_model: post-commit volume mismatch", internal.Details{
					"ledger":      c.ledger,
					"cell":        renderCell(key),
					"modelInput":  vp.Input.String(),
					"modelOutput": vp.Output.String(),
					"serverHad":   ok,
				})

				return
			}
		}
	}

	// Record each resulting transaction so it can be a future revert target. A
	// revert's new transaction carries the reversed postings and no reference.
	for i, sub := range subs {
		switch sub.kind {
		case opCreateTx:
			res.State.recordTx(data[i].GetID().String(), sub.postings, sub.reference)
		case opRevert:
			orig := c.modelState.txs[sub.targetID]
			res.State.recordTx(data[i].GetID().String(), reversePostings(orig.postings), "")
		}
	}

	c.modelState = res.State
	dbg("COMMIT OK: ledger=%s ids=%s op=%s", c.ledger, txIDs(data), renderOp(op))
}

// validateFailure accepts the observed failure iff some candidate base reproduces
// the observed error reason when the operation is applied to it — i.e. there is a
// serialization of the in-flight operations under which the server would reject
// it exactly this way. An account-sourced transaction overdrafts (or not)
// depending on which concurrent credits committed first, so this is where the
// candidate search earns its keep on the write path. Caller holds c.mu.
func (c *Checker) validateFailure(maxTicket uint64, op Operation, reqErr error) {
	matched := false
	c.candidateBases(maxTicket, func(base State) bool {
		res := base.Apply(op)
		if !res.OK && reasonMatches(reqErr, res.Reason) {
			matched = true
			return true
		}

		return false
	})

	if matched {
		return
	}

	assert.Unreachable("singleton_driver_model: operation failure not explained by any serialization", internal.Details{
		"ledger": c.ledger,
		"error":  reqErr.Error(),
		"op":     renderOp(op),
	})
}

// validateAccountRead checks one GetAccount snapshot against the model: the read
// is legal iff some candidate base holds the read's (input, output, found) for the
// cell. The read is registered outstanding for its whole window
// (registerRead/finishRead), so modelState stays at or behind the prefix the read
// saw; candidateBases then folds only the operations dispatched no later than
// maxTicket — the ticket high-water when the read returned. Acquires c.mu.
func (c *Checker) validateAccountRead(maxTicket uint64, addr, asset string, gotIn, gotOut *big.Int, found bool) {
	key := VolumeKey{Address: addr, Asset: asset}

	c.mu.Lock()
	matched := false
	c.candidateBases(maxTicket, func(base State) bool {
		matched = volumeCellMatches(base, key, gotIn, gotOut, found)
		return matched
	})
	c.mu.Unlock()

	if matched {
		dbg("READ OK: ledger=%s cell=%s", c.ledger, renderCell(key))
		return
	}

	assert.Unreachable("singleton_driver_model: account read outside model", internal.Details{
		"ledger":    c.ledger,
		"cell":      renderCell(key),
		"serverIn":  bigString(gotIn),
		"serverOut": bigString(gotOut),
		"hadAsset":  found,
	})
}

// volumeCellMatches reports whether s holds exactly the (gotIn, gotOut, found)
// reading for cell key: present with matching volumes, or absent when the server
// returned no entry for the asset.
func volumeCellMatches(s State, key VolumeKey, gotIn, gotOut *big.Int, found bool) bool {
	vp, present := s.volumes[key]
	switch {
	case found && present && vp.Input.Cmp(gotIn) == 0 && vp.Output.Cmp(gotOut) == 0:
		return true
	case !found && !present:
		return true
	}

	return false
}

// pcvVolume extracts (input, output) for one cell from a server's post-commit
// volumes. ok is false when the cell is absent.
func pcvVolume(pcv map[string]map[string]shared.V2Volume, key VolumeKey) (in, out *big.Int, ok bool) {
	byAsset, found := pcv[key.Address]
	if !found {
		return nil, nil, false
	}

	v, found := byAsset[key.Asset]
	if !found {
		return nil, nil, false
	}

	return v.GetInput(), v.GetOutput(), true
}
