package main

import "math/big"

type opKind int

const (
	opCreateTx opKind = iota
	opRevert
)

// Operation is one unit the model folds (Apply) and a worker dispatches.
//   - opCreateTx: postings + reference + idemKey.
//   - opRevert: targetID, the server-assigned id of a committed transaction to
//     revert. The reversed postings are derived from the tracked original at
//     Apply time, so they need not be carried here.
type Operation struct {
	kind      opKind
	postings  []Posting
	reference string
	targetID  string
	idemKey   string
}

// Posting is one source→destination movement of an asset amount.
type Posting struct {
	Source      string
	Destination string
	Asset       string
	Amount      *big.Int
}
