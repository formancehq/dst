package main

import "time"

// Hand-tunable knobs for the model driver.

// Fleet shape. Overridable via MODEL_LEDGERS / MODEL_WORKERS.
const (
	defaultLedgers = 4
	defaultWorkers = 8
)

// Address-space size. Small so cells are re-touched often and volumes
// accumulate, making concurrent reads and post-commit volumes interesting.
const numAccounts = 50

// Postings per transaction (1..maxPostings).
const maxPostings = 3

var assets = []string{"USD/2", "EUR/2", "COIN"}

// Metadata key pool size. Small so concurrent writes contend on the same cell.
const numMetaKeys = 6

// Transaction back-pressure. Each committed transaction stays tracked forever
// (it remains a revert/read target), and the re-order buffer clones and hashes
// the committed transactions on every candidate fold, so new-transaction emission
// tapers with a ledger's transaction count and stops past txEmitStop, shifting the
// workload toward exercising existing transactions.
const (
	txEmitFull  = 500  // below this count: always create new transactions
	txEmitTaper = 2000 // below this: ~half the time
	txEmitStop  = 4000 // below this: ~1-in-8; at or above: stop creating new ones
)

// Per-worker breathing room.
const workerLoopPause = 50 * time.Millisecond

// Worker → processor channel cap, well above steady-state inflight.
const incomingBuffer = 256
