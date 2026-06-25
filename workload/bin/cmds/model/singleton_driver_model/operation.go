package main

import "math/big"

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
//     Apply time, so they need not be carried here.
//   - opBulk: bulk, a list of leaf operations applied atomically (all-or-nothing)
//     as a single /_bulk call.
type Operation struct {
	kind      opKind
	postings  []Posting
	reference string
	targetID  string
	idemKey   string
	bulk      []Operation
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
