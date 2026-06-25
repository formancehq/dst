package main

import (
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// Checker drives validation against the model for ONE ledger. v2 ledgers are
// independent — each has its own commit sequence (the transaction id) — so the
// fleet runs one Checker per ledger. It owns the in-flight/pending operations
// (a re-order buffer ordered by transaction id) and the committed model state.
//
// Concurrency: mu guards every field. Workers hold mu only for the brief
// register-inflight window; the processor goroutine (processor.go) drains
// responses under mu. Expensive read validation runs candidateBases under mu.
type Checker struct {
	mu sync.Mutex

	// ledger is the ledger this checker validates. Immutable.
	ledger string

	// ticketSeq hands out a monotonic ticket per dispatched operation or read —
	// the dispatch order the drain gate compares against. Atomic so a worker can
	// snapshot the high-water at observe time without taking the lock.
	ticketSeq atomic.Uint64

	// inflight: dispatched operations whose response hasn't been observed yet,
	// keyed by ticket. The serialization search (candidateBases) folds these.
	inflight map[uint64]Operation

	// pending: observed successes not yet drained, sorted ascending by seq (the
	// committed transaction id).
	pending []*pendingObservation

	// reads: tickets of outstanding reads. Holding one gates draining (tryDrain),
	// so a read needs no drain-race skip.
	reads map[uint64]struct{}

	// Worker → processor channel.
	incoming chan observation

	// modelState is the committed (drained) state. Successes drain in id order,
	// so it is always the exact predecessor of the next operation to validate,
	// and the base candidateBases folds the in-flight set onto.
	modelState State

	// metaStore validates metadata on its own track (see metadata.go): metadata
	// writes have no observable commit sequence, so they are not linearized
	// through the re-order buffer.
	metaStore *metaStore
}

// observation is one worker → processor message. observeTicket is the ticket
// high-water when the response was received; the drain gate uses it to tell which
// outstanding ops were dispatched after this one was observed.
type observation struct {
	ticket        uint64
	op            Operation
	data          *shared.V2Transaction
	err           error
	observeTicket uint64
}

// pendingObservation is a buffered success awaiting in-order replay. seq is the
// committed transaction id.
type pendingObservation struct {
	seq *big.Int
	obs observation
}

// NewChecker returns an empty checker for one ledger; the caller spawns its
// processor goroutine.
func NewChecker(ledger string) *Checker {
	return &Checker{
		ledger:     ledger,
		inflight:   map[uint64]Operation{},
		reads:      map[uint64]struct{}{},
		incoming:   make(chan observation, incomingBuffer),
		modelState: NewState(),
		metaStore:  newMetaStore(),
	}
}
