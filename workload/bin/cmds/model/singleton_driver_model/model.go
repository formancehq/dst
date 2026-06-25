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
// postings and reference. Recorded on commit (validate.go), not by Apply.
type txRecord struct {
	postings  []Posting
	reference string
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
}

// Result is the predicted outcome of applying one operation.
//   - OK: the operation committed.
//   - Reason: the rejection reason (a v2 error code) when !OK.
//   - State: the resulting state (equals the input state when !OK).
//   - PCV: the touched cells' post-commit volumes for a committed transaction.
type Result struct {
	OK     bool
	Reason string
	State  State
	PCV    map[VolumeKey]VolumePair
}

// Apply folds op into s, predicting the server's outcome and the resulting state.
// A world-sourced transaction always commits (world is overdraftable), so the
// only operation kind so far is total; OK/Reason is retained for the revert and
// metadata kinds to come.
func (s State) Apply(op Operation) Result {
	next := s.clone()

	switch op.kind {
	case opCreateTx:
		pcv := next.applyPostings(op.postings)
		return Result{OK: true, State: next, PCV: pcv}
	default:
		panic(fmt.Sprintf("model: unmodeled operation kind %d", op.kind))
	}
}

// applyPostings accumulates postings into volumes (source.output += amount,
// destination.input += amount) read-modify-write per cell so postings touching
// the same cell compose, returning the post-commit volumes of the touched cells.
// Each cell is replaced with freshly-allocated big.Ints so forks sharing the map
// never alias.
func (s *State) applyPostings(postings []Posting) map[VolumeKey]VolumePair {
	pcv := map[VolumeKey]VolumePair{}

	bump := func(key VolumeKey, addIn, addOut *big.Int) {
		cur := s.vol(key)
		np := VolumePair{
			Input:  *new(big.Int).Add(&cur.Input, addIn),
			Output: *new(big.Int).Add(&cur.Output, addOut),
		}
		s.volumes[key] = np
		pcv[key] = np
	}

	zero := big.NewInt(0)
	for _, p := range postings {
		bump(VolumeKey{Address: p.Source, Asset: p.Asset}, zero, p.Amount)
		bump(VolumeKey{Address: p.Destination, Asset: p.Asset}, p.Amount, zero)
	}

	return pcv
}

// recordTx records a committed transaction by its server-assigned id. Called by
// the commit cross-check, which learns the id from the response.
func (s *State) recordTx(id string, op Operation) {
	s.txs[id] = &txRecord{postings: op.postings, reference: op.reference}
}
