package main

import (
	"math/big"
	"strconv"
	"testing"
)

func tx(postings ...Posting) Operation {
	return Operation{kind: opCreateTx, postings: postings}
}

func p(src, dst, asset string, amt int64) Posting {
	return Posting{Source: src, Destination: dst, Asset: asset, Amount: big.NewInt(amt)}
}

func vol(s State, addr, asset string) VolumePair {
	return s.volumes[VolumeKey{Address: addr, Asset: asset}]
}

func TestApplyAccumulatesVolumes(t *testing.T) {
	s := NewState()

	r1 := s.Apply(tx(p("world", "a", "USD", 100)))
	if !r1.OK {
		t.Fatal("expected OK")
	}

	r2 := r1.State.Apply(tx(p("world", "a", "USD", 50)))

	if got := vol(r2.State, "a", "USD").Input; got.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("a input = %s, want 150", got.String())
	}
	if got := vol(r2.State, "world", "USD").Output; got.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("world output = %s, want 150", got.String())
	}
	if got := r2.Orders[0].PCV[VolumeKey{Address: "a", Asset: "USD"}].Input; got.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("pcv a input = %s, want 150", got.String())
	}
}

// Two postings to the same cell in one transaction compose.
func TestApplyComposesSameCell(t *testing.T) {
	s := NewState()
	r := s.Apply(tx(p("world", "a", "USD", 10), p("world", "a", "USD", 5)))
	if got := vol(r.State, "a", "USD").Input; got.Cmp(big.NewInt(15)) != 0 {
		t.Fatalf("a input = %s, want 15", got.String())
	}
}

// Apply never mutates its receiver, and a clone is independent.
func TestCloneIndependence(t *testing.T) {
	s := NewState()
	r := s.Apply(tx(p("world", "a", "USD", 100)))
	c := r.State.clone()

	_ = r.State.Apply(tx(p("world", "a", "USD", 1)))

	if got := vol(r.State, "a", "USD").Input; got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("Apply mutated receiver: a input = %s, want 100", got.String())
	}
	if got := vol(c, "a", "USD").Input; got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("clone aliased: a input = %s, want 100", got.String())
	}
}

// A non-world source with no balance overdrafts: the transaction is rejected and
// the state is left unchanged.
func TestApplyRejectsOverdraft(t *testing.T) {
	s := NewState()
	r := s.Apply(tx(p("a", "b", "USD", 100)))
	if r.OK {
		t.Fatal("expected INSUFFICIENT_FUND rejection")
	}
	if r.Reason != "INSUFFICIENT_FUND" {
		t.Fatalf("reason = %q, want INSUFFICIENT_FUND", r.Reason)
	}
	if len(r.State.volumes) != 0 {
		t.Fatalf("rejected transaction mutated state: %d cells", len(r.State.volumes))
	}
}

// A funded account can be debited; world itself may overdraft without bound.
func TestApplyFundsChecks(t *testing.T) {
	funded := NewState().Apply(tx(p("world", "a", "USD", 100))).State

	if r := funded.Apply(tx(p("a", "b", "USD", 60))); !r.OK {
		t.Fatal("funded debit should commit")
	}
	if r := funded.Apply(tx(p("a", "b", "USD", 140))); r.OK {
		t.Fatal("debit beyond balance should be rejected")
	}
	if r := NewState().Apply(tx(p("world", "a", "USD", 1_000_000_000))); !r.OK {
		t.Fatal("world must be overdraftable")
	}
}

// candidateBases must not fold an operation that could not have committed at a
// base: a debit only appears in serializations where a covering credit precedes
// it, so b is never credited unless a already holds the funds.
func TestCandidateBasesPrunesUnaffordable(t *testing.T) {
	c := NewChecker("L")
	c.modelState = c.modelState.Apply(tx(p("world", "a", "USD", 50))).State
	c.inflight[1] = tx(p("a", "b", "USD", 100))
	c.inflight[2] = tx(p("world", "a", "USD", 100))
	c.ticketSeq.Store(2)

	bad := false
	c.candidateBases(2, func(s State) bool {
		bIn := vol(s, "b", "USD").Input
		aIn := vol(s, "a", "USD").Input
		if bIn.Sign() > 0 && aIn.Cmp(big.NewInt(150)) < 0 {
			bad = true
		}
		return false
	})
	if bad {
		t.Fatal("candidateBases folded a debit that could not have committed")
	}
}

func revert(id string) Operation { return Operation{kind: opRevert, targetID: id, force: true} }

func revertUnforced(id string) Operation { return Operation{kind: opRevert, targetID: id} }

func bulk(ops ...Operation) Operation { return Operation{kind: opBulk, bulk: ops} }

// New-transaction emission tapers with the ledger's transaction count: always
// below txEmitFull, never at or above txEmitStop.
func TestRollTransactionTaper(t *testing.T) {
	if !rollTransaction(NewState()) {
		t.Fatal("empty ledger should always mint a new transaction")
	}

	full := NewState()
	for i := 0; i < txEmitStop; i++ {
		full.txs[strconv.Itoa(i)] = &txRecord{}
	}
	if rollTransaction(full) {
		t.Fatal("past txEmitStop should never mint a new transaction")
	}
}

// A bulk commits every element atomically, or — if any element fails — rejects
// the whole bulk and leaves the state unchanged.
func TestApplyBulkAtomic(t *testing.T) {
	s := NewState().Apply(tx(p("world", "a", "USD", 100))).State

	ok := s.Apply(bulk(tx(p("world", "b", "USD", 50)), tx(p("a", "c", "USD", 30))))
	if !ok.OK || len(ok.Orders) != 2 {
		t.Fatalf("bulk should commit both elements, got OK=%v orders=%d", ok.OK, len(ok.Orders))
	}

	bad := s.Apply(bulk(tx(p("world", "b", "USD", 50)), tx(p("a", "c", "USD", 9999))))
	if bad.OK || bad.Reason != "INSUFFICIENT_FUND" {
		t.Fatalf("overdrafting bulk should reject, got OK=%v reason=%q", bad.OK, bad.Reason)
	}
	if _, present := bad.State.volumes[VolumeKey{Address: "b", Asset: "USD"}]; present {
		t.Fatal("rejected bulk mutated state (b credited despite rollback)")
	}
}

// A revert reverses the target's postings (moving volumes back) and marks it
// reverted; a second revert of the same transaction is rejected.
func TestApplyRevert(t *testing.T) {
	s := NewState()
	s = s.Apply(tx(p("world", "a", "USD", 100))).State
	s.recordTx("7", []Posting{p("world", "a", "USD", 100)}, "", nil, nil)

	r := s.Apply(revert("7"))
	if !r.OK {
		t.Fatal("revert should commit")
	}
	// Reversal a->world:100 leaves a at input 100 / output 100 (balance 0).
	if got := vol(r.State, "a", "USD"); got.Output.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("a output = %s, want 100", got.Output.String())
	}

	if r2 := r.State.Apply(revert("7")); r2.OK || r2.Reason != "ALREADY_REVERT" {
		t.Fatalf("double revert: OK=%v reason=%q, want !OK ALREADY_REVERT", r2.OK, r2.Reason)
	}
}

// A forced revert commits even when the reversal drives the original destination
// negative (the funds check is skipped); a non-forced one overdrafts and is
// rejected with INSUFFICIENT_FUND.
func TestApplyRevertForceHonoursFundsCheck(t *testing.T) {
	setup := func() State {
		s := NewState()
		s = s.Apply(tx(p("world", "a", "USD", 100))).State
		s.recordTx("1", []Posting{p("world", "a", "USD", 100)}, "", nil, nil)
		// Drain a so it can't cover the reversal.
		return s.Apply(tx(p("a", "b", "USD", 100))).State
	}

	if r := setup().Apply(revert("1")); !r.OK {
		t.Fatalf("forced revert should commit despite insufficient funds, got reason %q", r.Reason)
	}

	if r := setup().Apply(revertUnforced("1")); r.OK || r.Reason != "INSUFFICIENT_FUND" {
		t.Fatalf("non-forced revert should overdraft: OK=%v reason=%q, want !OK INSUFFICIENT_FUND", r.OK, r.Reason)
	}
}

// A create with no postings is rejected NO_POSTINGS; one reusing a committed
// reference is rejected CONFLICT; a fresh reference commits.
func TestApplyCreateRejections(t *testing.T) {
	s := NewState().Apply(tx(p("world", "a", "USD", 100))).State
	s.recordTx("1", []Posting{p("world", "a", "USD", 100)}, "r1", nil, nil)

	if r := s.Apply(Operation{kind: opCreateTx, reference: "fresh"}); r.OK || r.Reason != "NO_POSTINGS" {
		t.Fatalf("empty create: OK=%v reason=%q, want !OK NO_POSTINGS", r.OK, r.Reason)
	}

	dup := Operation{kind: opCreateTx, postings: []Posting{p("world", "b", "USD", 1)}, reference: "r1"}
	if r := s.Apply(dup); r.OK || r.Reason != "CONFLICT" {
		t.Fatalf("dup-ref create: OK=%v reason=%q, want !OK CONFLICT", r.OK, r.Reason)
	}

	fresh := Operation{kind: opCreateTx, postings: []Posting{p("world", "b", "USD", 1)}, reference: "fresh"}
	if r := s.Apply(fresh); !r.OK {
		t.Fatalf("fresh-ref create should commit, got reason %q", r.Reason)
	}
}

func TestApplyRevertNotFound(t *testing.T) {
	if r := NewState().Apply(revert("999")); r.OK || r.Reason != "NOT_FOUND" {
		t.Fatalf("revert of unknown id: OK=%v reason=%q, want !OK NOT_FOUND", r.OK, r.Reason)
	}
}

// Reverted status is part of the state identity, so a base where the target is
// reverted is enumerated distinctly from one where it is not — letting a
// concurrent ALREADY_REVERT be explained.
func TestCandidateBasesDistinguishesReverted(t *testing.T) {
	c := NewChecker("L")
	c.modelState = c.modelState.Apply(tx(p("world", "a", "USD", 100))).State
	c.modelState.recordTx("5", []Posting{p("world", "a", "USD", 100)}, "", nil, nil)
	c.inflight[1] = revert("5")
	c.ticketSeq.Store(1)

	var sawReverted, sawLive bool
	c.candidateBases(1, func(s State) bool {
		if s.txs["5"].reverted {
			sawReverted = true
		} else {
			sawLive = true
		}
		return false
	})
	if !sawReverted || !sawLive {
		t.Fatalf("missing bases: reverted=%v live=%v", sawReverted, sawLive)
	}
}

// candidateBases enumerates every ordered subset of the in-flight operations, so
// a concurrent read can match a state where neither, either, or both committed.
func TestCandidateBasesEnumeratesInflight(t *testing.T) {
	c := NewChecker("L")
	c.inflight[1] = tx(p("world", "a", "USD", 10))
	c.inflight[2] = tx(p("world", "b", "USD", 20))
	c.ticketSeq.Store(2)

	var seenNeither, seenAOnly, seenBoth bool
	c.candidateBases(2, func(s State) bool {
		a := vol(s, "a", "USD").Input
		b := vol(s, "b", "USD").Input
		switch {
		case a.Sign() == 0 && b.Sign() == 0:
			seenNeither = true
		case a.Cmp(big.NewInt(10)) == 0 && b.Sign() == 0:
			seenAOnly = true
		case a.Cmp(big.NewInt(10)) == 0 && b.Cmp(big.NewInt(20)) == 0:
			seenBoth = true
		}
		return false
	})

	if !seenNeither || !seenAOnly || !seenBoth {
		t.Fatalf("missing bases: neither=%v aOnly=%v both=%v", seenNeither, seenAOnly, seenBoth)
	}
}

// Only operations dispatched no later than maxTicket are folded.
func TestCandidateBasesRespectsMaxTicket(t *testing.T) {
	c := NewChecker("L")
	c.inflight[1] = tx(p("world", "a", "USD", 10))
	c.inflight[2] = tx(p("world", "b", "USD", 20))
	c.ticketSeq.Store(2)

	sawB := false
	c.candidateBases(1, func(s State) bool {
		b := vol(s, "b", "USD").Input
		if b.Sign() != 0 {
			sawB = true
		}
		return false
	})

	if sawB {
		t.Fatal("operation dispatched after maxTicket was folded")
	}
}
