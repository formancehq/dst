package main

import "math/big"

type opKind int

const (
	opCreateTx opKind = iota
)

// Operation is one unit the model folds (Apply) and a worker dispatches. A
// world-sourced create transaction is the only kind so far; revert and metadata
// kinds will extend it.
type Operation struct {
	kind      opKind
	postings  []Posting
	reference string
	idemKey   string
}

// Posting is one source→destination movement of an asset amount.
type Posting struct {
	Source      string
	Destination string
	Asset       string
	Amount      *big.Int
}
