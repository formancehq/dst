// Command singleton_driver_model runs a model-based conformance test: workers
// fan out across a fleet of ledgers, dispatching transactions concurrently, and
// every observed server response is checked against a model that predicts the
// set of legal responses.
//
// v2 ledgers are independent — each has its own commit sequence (the
// server-assigned transaction id) — so the harness runs one Checker per ledger.
// Within a ledger the design mirrors a single log: one re-order buffer ordered by
// transaction id, one committed model state.
//
// Layout:
//
//   - model.go: State (one ledger's volume table + committed transactions) and
//     the pure forward Apply that predicts the server's outcome for an operation.
//   - checker.go: Checker — the per-ledger harness bookkeeping (in-flight set,
//     pending re-order buffer, committed modelState).
//   - processor.go: one goroutine per ledger; re-orders observed successes by
//     transaction id and drains them in order under the read/in-flight gate.
//   - search.go: candidateBases folds the in-flight operations onto modelState to
//     enumerate the states the server could be in.
//   - validate.go: the model-conformance checks (committed volumes, reads).
//   - actions.go: random operation generation + dispatch.
//   - reads.go: GetAccount read execution.
//   - main.go: fleet setup, workers, entry point.
//
// Invariant: every observed response is consistent with some serialization of
// the in-flight operations (see candidateBases).
package main

import (
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
)

// VolumeKey is one (address, asset) cell of the volume table.
type VolumeKey struct {
	Address string
	Asset   string
}

// compareVolumeKey orders VolumeKeys by address, then asset.
func compareVolumeKey(a, b VolumeKey) int {
	if c := strings.Compare(a.Address, b.Address); c != 0 {
		return c
	}

	return strings.Compare(a.Asset, b.Asset)
}

// VolumePair is the cumulative input/output for one cell. Entries are replaced,
// never mutated in place, so a shallow map copy in clone() never aliases.
type VolumePair struct {
	Input  big.Int
	Output big.Int
}

// txRecord is a committed transaction tracked by its server-assigned id: its
// postings, reference, and whether it has been reverted. Recorded on commit
// (validate.go). Records are replaced, never mutated in place, so clones can
// share the pointer.
type txRecord struct {
	postings  []Posting
	reference string
	reverted  bool
}

// State is one ledger's model: the per-cell volume table and the committed
// transactions. Every mutation returns a NEW State (copy-on-write) so the checker
// can fork it across hypothesized serializations without disturbing shared state.
type State struct {
	volumes map[VolumeKey]VolumePair
	txs     map[string]*txRecord
}

func NewState() State {
	return State{
		volumes: map[VolumeKey]VolumePair{},
		txs:     map[string]*txRecord{},
	}
}

// clone returns a copy whose maps can be mutated independently. VolumePair
// entries and *txRecord pointers are replaced (never mutated in place), so the
// shallow copy is safe.
func (s State) clone() State {
	volumes := make(map[VolumeKey]VolumePair, len(s.volumes))
	for k, v := range s.volumes {
		volumes[k] = v
	}

	txs := make(map[string]*txRecord, len(s.txs))
	for k, v := range s.txs {
		txs[k] = v
	}

	return State{volumes: volumes, txs: txs}
}

// vol returns the cell's volumes, or the zero pair if absent.
func (s *State) vol(key VolumeKey) VolumePair {
	return s.volumes[key]
}

// hash writes a canonical identity of the volume table into h. candidateBases
// dedups on it, collapsing bases reached via commutative serializations. Only
// volumes are hashed: tx records are recorded on commit, not folded by Apply, so
// they don't vary across the serializations the search explores.
func (s State) hash(h io.Writer) {
	keys := make([]VolumeKey, 0, len(s.volumes))
	for k := range s.volumes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return compareVolumeKey(keys[i], keys[j]) < 0 })

	for _, k := range keys {
		v := s.volumes[k]
		fmt.Fprintf(h, "V|%s|%s|%s|%s\n", k.Address, k.Asset, v.Input.String(), v.Output.String())
	}

	// Reverted status distinguishes states a revert prediction depends on (two
	// orderings can reach the same volumes but disagree on what is reverted).
	reverted := make([]string, 0, len(s.txs))
	for id, r := range s.txs {
		if r.reverted {
			reverted = append(reverted, id)
		}
	}
	sort.Strings(reverted)
	for _, id := range reverted {
		fmt.Fprintf(h, "R|%s\n", id)
	}
}

// OrderResult is the predicted outcome of one leaf operation. PCV holds the
// touched cells' post-commit volumes for a committed transaction/revert.
type OrderResult struct {
	OK     bool
	Reason string
	PCV    map[VolumeKey]VolumePair
}

// Result is the predicted outcome of applying an operation (one leaf, or a bulk
// of leaves applied atomically).
//   - OK: the operation committed.
//   - Reason: the rejection reason (a v2 error code) when !OK — the first failing
//     leaf's reason.
//   - State: the resulting state (equals the input state when !OK).
//   - Orders: per-leaf outcomes, index-aligned with subOps, truncated at the
//     first failing leaf.
type Result struct {
	OK     bool
	Reason string
	State  State
	Orders []OrderResult
}

// Apply folds op into s, predicting the server's outcome and the resulting state.
// A bulk is atomic: the first failing leaf rejects the whole bulk and leaves the
// state unchanged; otherwise every leaf commits. A single operation is a bulk of
// one.
func (s State) Apply(op Operation) Result {
	next := s.clone()
	subs := op.subOps()
	orders := make([]OrderResult, 0, len(subs))

	for _, sub := range subs {
		oc := next.applyOne(sub)
		orders = append(orders, oc)
		if !oc.OK {
			// Atomic: discard the working copy, nothing commits.
			return Result{OK: false, Reason: oc.Reason, State: s, Orders: orders}
		}
	}

	return Result{OK: true, State: next, Orders: orders}
}

// applyOne mutates the (already-forked) state for one leaf operation and returns
// its predicted outcome.
//   - opCreateTx commits unless a non-world source would overdraft
//     (INSUFFICIENT_FUND).
//   - opRevert reverses the target's postings. It is forced (the reversal skips
//     the balance check, like the server), so it commits unless the target is
//     unknown (NOT_FOUND) or already reverted (ALREADY_REVERT).
func (s *State) applyOne(op Operation) OrderResult {
	switch op.kind {
	case opCreateTx:
		pcv, ok := s.applyPostings(op.postings, false)
		if !ok {
			return OrderResult{Reason: reasonInsufficientFund}
		}
		return OrderResult{OK: true, PCV: pcv}

	case opRevert:
		rec, ok := s.txs[op.targetID]
		if !ok {
			return OrderResult{Reason: reasonNotFound}
		}
		if rec.reverted {
			return OrderResult{Reason: reasonAlreadyReverted}
		}

		pcv, _ := s.applyPostings(reversePostings(rec.postings), true)
		next := &txRecord{postings: rec.postings, reference: rec.reference, reverted: true}
		s.txs[op.targetID] = next

		return OrderResult{OK: true, PCV: pcv}

	default:
		panic(fmt.Sprintf("model: unmodeled operation kind %d", op.kind))
	}
}

// applyPostings applies postings in order: each credits its destination and
// debits its source (read-modify-write per cell, so postings touching the same
// cell compose), returning the touched cells' post-commit volumes. Unless force
// is set, a non-world source whose balance would go negative rejects the whole
// transaction (ok=false); v2 applies a transaction atomically, so the caller
// discards the working state. Each cell is replaced with freshly-allocated
// big.Ints so forks sharing the map never alias.
func (s *State) applyPostings(postings []Posting, force bool) (map[VolumeKey]VolumePair, bool) {
	pcv := map[VolumeKey]VolumePair{}

	bump := func(key VolumeKey, addIn, addOut *big.Int) VolumePair {
		cur := s.vol(key)
		np := VolumePair{
			Input:  *new(big.Int).Add(&cur.Input, addIn),
			Output: *new(big.Int).Add(&cur.Output, addOut),
		}
		s.volumes[key] = np
		pcv[key] = np
		return np
	}

	zero := big.NewInt(0)
	for _, p := range postings {
		bump(VolumeKey{Address: p.Destination, Asset: p.Asset}, p.Amount, zero)
		src := bump(VolumeKey{Address: p.Source, Asset: p.Asset}, zero, p.Amount)

		// world is the system source and may overdraft without bound.
		if !force && p.Source != "world" {
			bal := new(big.Int).Sub(&src.Input, &src.Output)
			if bal.Sign() < 0 {
				return nil, false
			}
		}
	}

	return pcv, true
}

// reversePostings swaps source and destination of each posting, preserving order.
func reversePostings(ps []Posting) []Posting {
	out := make([]Posting, len(ps))
	for i, p := range ps {
		out[i] = Posting{Source: p.Destination, Destination: p.Source, Asset: p.Asset, Amount: p.Amount}
	}

	return out
}

// recordTx records a committed transaction by its server-assigned id. Called by
// the commit cross-check, which learns the id from the response.
func (s *State) recordTx(id string, postings []Posting, reference string) {
	s.txs[id] = &txRecord{postings: postings, reference: reference}
}
