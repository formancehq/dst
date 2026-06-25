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

// Per-worker breathing room.
const workerLoopPause = 50 * time.Millisecond

// Worker → processor channel cap, well above steady-state inflight.
const incomingBuffer = 256
