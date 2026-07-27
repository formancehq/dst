package main

import (
	"math/big"
	"time"
)

type opKind int

const (
	opCreateTx opKind = iota
	opRevert
	opBulk
)

// Operation is one unit the model folds (Apply) and a worker dispatches.
//   - opCreateTx: postings + reference + idemKey.
//   - opRevert: targetID, the server-assigned id of a committed transaction to
//     revert. The reversed postings are derived from the tracked original at
//     Apply time, so they need not be carried here. force skips the balance floor
//     on the reversed postings.
//   - opBulk: bulk, a list of leaf operations applied atomically (all-or-nothing)
//     as a single /_bulk call.
type Operation struct {
	kind      opKind
	postings  []Posting
	reference string
	targetID  string
	force     bool
	idemKey   string
	bulk      []Operation

	// Metadata carried on a create transaction, applied atomically with it:
	// metadata is set on the new transaction; accountMeta[address] is set on that
	// account. Both are validated on the metadata register track (metadata.go).
	metadata    map[string]string
	accountMeta map[string]map[string]string

	// timestamp, when set on a create, is a backdated effective date the server
	// stores verbatim and echoes on reads (validateTransactionRead). It does not
	// affect actual post-commit volumes, which follow insertion (id) order.
	timestamp *time.Time

	// atEffectiveDate, when set on a revert, makes it inherit the original's
	// effective date. It does not affect actual volumes; only set on forced reverts
	// so the balance floor never depends on the effective date.
	atEffectiveDate bool
}

// subOps returns the leaf operations Apply folds: a bulk's elements, or the
// operation itself.
func (op Operation) subOps() []Operation {
	if op.kind == opBulk {
		return op.bulk
	}

	return []Operation{op}
}

// Posting is one source→destination movement of an asset amount.
type Posting struct {
	Source      string
	Destination string
	Asset       string
	Amount      *big.Int
}
