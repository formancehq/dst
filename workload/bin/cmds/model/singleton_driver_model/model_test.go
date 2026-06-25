package main

import (
	"math/big"
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
	if got := r2.PCV[VolumeKey{Address: "a", Asset: "USD"}].Input; got.Cmp(big.NewInt(150)) != 0 {
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
