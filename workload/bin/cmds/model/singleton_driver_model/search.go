package main

import (
	"crypto/sha256"
	"fmt"
)

// candidateBases enumerates the distinct committed states the server could be in
// relative to a not-yet-linearized observation (a failure or a read): modelState
// folded with the in-flight/pending operations in some commit-consistent order.
// Only operations dispatched no later than maxTicket — the observation's
// high-water — are folded; ones dispatched after the observation cannot precede
// it. visit is called for each distinct base; returning true stops the search
// early, once an observation is explained. Caller holds c.mu.
//
// The in-flight operations are of two kinds, with different freedom:
//
//   - pending: committed successes still buffered in the re-order queue. Their
//     commit order is KNOWN (c.pending is sorted by transaction id), so they may
//     only appear as an ordered prefix — pending[0], then pending[1], … — never
//     reordered or skipped.
//   - inflight: dispatched operations whose response hasn't arrived. Their order
//     is unknown, so each may be interleaved at any position (any ordered subset).
//
// Dedup collapses commutative orderings, keyed on a 256-bit hash of (state,
// pendingIndex, remaining-inflight): collisions are infeasible, so dedup is exact
// while the key stays 32 bytes rather than a full serialized state.
func (c *Checker) candidateBases(maxTicket uint64, visit func(State) bool) {
	pending := make([]Operation, 0, len(c.pending))
	for _, pe := range c.pending {
		// pending is id-ordered, so an entry dispatched after the observation
		// committed after it — and so did every later entry.
		if pe.obs.ticket > maxTicket {
			break
		}
		pending = append(pending, pe.obs.op)
	}

	inflight := make([]Operation, 0, len(c.inflight))
	for t, op := range c.inflight {
		if t <= maxTicket {
			inflight = append(inflight, op)
		}
	}

	allIdx := make([]int, len(inflight))
	for i := range inflight {
		allIdx[i] = i
	}

	seen := map[[sha256.Size]byte]bool{}
	hasher := sha256.New()
	key := func(base State, pIdx int, rem []int) [sha256.Size]byte {
		hasher.Reset()
		base.hash(hasher)
		fmt.Fprintf(hasher, "#%d#%v", pIdx, rem)

		var k [sha256.Size]byte
		hasher.Sum(k[:0])
		return k
	}

	var rec func(base State, pIdx int, rem []int) bool

	rec = func(base State, pIdx int, rem []int) bool {
		k := key(base, pIdx, rem)
		if seen[k] {
			return false
		}
		seen[k] = true

		if visit(base) {
			return true
		}

		// Advance the pending prefix by one, in id order.
		if pIdx < len(pending) {
			if res := base.Apply(pending[pIdx]); res.OK {
				if rec(res.State, pIdx+1, rem) {
					return true
				}
			}
		}

		// Fold in any one of the remaining in-flight operations (unknown position).
		for i, idx := range rem {
			res := base.Apply(inflight[idx])
			if !res.OK {
				// Could not have committed at this point — not a predecessor.
				continue
			}

			next := make([]int, 0, len(rem)-1)
			next = append(next, rem[:i]...)
			next = append(next, rem[i+1:]...)

			if rec(res.State, pIdx, next) {
				return true
			}
		}

		return false
	}

	rec(c.modelState, 0, allIdx)
}
