package main

import (
	"math/big"
	"testing"

	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

func txResp(id int64) *shared.V2Transaction { return &shared.V2Transaction{ID: big.NewInt(id)} }

// A committed atomic bulk's transactions get individual ids that can interleave
// with concurrent commits (its id span is not contiguous). They must be buffered
// as independent transactions at their own ids so a commit that landed between
// them is replayed in id order — not folded into one unit at the bulk's minimum
// id, which would skip it.
func TestBufferSuccessSplitsBulkByID(t *testing.T) {
	c := NewChecker("L")
	// A concurrent transaction committed at id 98, between the bulk's 97 and 99.
	c.bufferSuccess(observation{
		ticket:        7,
		op:            bulk(tx(p("world", "a", "COIN", 100)), revert("5")),
		data:          []*shared.V2Transaction{txResp(97), txResp(99)},
		observeTicket: 12,
	})

	if len(c.pending) != 2 {
		t.Fatalf("bulk should split into 2 pending entries, got %d", len(c.pending))
	}
	if c.pending[0].seq.Cmp(big.NewInt(97)) != 0 || c.pending[1].seq.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("pending seqs = %s,%s want 97,99", c.pending[0].seq, c.pending[1].seq)
	}
	if c.pending[0].obs.op.kind != opCreateTx || c.pending[1].obs.op.kind != opRevert {
		t.Fatal("split elements should carry their individual ops")
	}
	for _, pe := range c.pending {
		if pe.obs.ticket != 7 || pe.obs.observeTicket != 12 {
			t.Fatal("split elements should inherit the bulk's ticket/observeTicket")
		}
		if len(pe.obs.data) != 1 {
			t.Fatalf("split element should carry its single transaction, got %d", len(pe.obs.data))
		}
	}
}

// A single operation is buffered at its own id; a bulk whose element count
// disagrees with the response is kept whole (validateCommit reports the mismatch).
func TestBufferSuccessSingleAndCountMismatch(t *testing.T) {
	c := NewChecker("L")
	c.bufferSuccess(observation{op: tx(p("world", "a", "COIN", 1)), data: []*shared.V2Transaction{txResp(5)}})
	if len(c.pending) != 1 || c.pending[0].seq.Cmp(big.NewInt(5)) != 0 {
		t.Fatal("single operation should buffer at its id")
	}

	c2 := NewChecker("L")
	c2.bufferSuccess(observation{
		op:   bulk(tx(p("world", "a", "COIN", 1)), tx(p("world", "b", "COIN", 1))),
		data: []*shared.V2Transaction{txResp(3)},
	})
	if len(c2.pending) != 1 {
		t.Fatalf("count-mismatched bulk should not split, got %d", len(c2.pending))
	}
}
