package main

import (
	"log"
	"math/big"
	"os"
	"strings"
)

// MODEL_DEBUG enables verbose per-operation logging.
var modelDebug = os.Getenv("MODEL_DEBUG") != ""

func dbg(format string, args ...any) {
	if modelDebug {
		log.Printf("[model-debug] "+format, args...)
	}
}

// renderOp formats an operation for debug lines and assertion details.
func renderOp(op Operation) string {
	switch op.kind {
	case opRevert:
		return "revert(" + op.targetID + ")"
	case opBulk:
		parts := make([]string, len(op.bulk))
		for i, sub := range op.bulk {
			parts[i] = renderOp(sub)
		}
		return "bulk[" + strings.Join(parts, ";") + "]"
	default:
		return renderPostings(op.postings)
	}
}

// renderPostings formats a posting list for debug lines and assertion details.
func renderPostings(ps []Posting) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Source + "->" + p.Destination + ":" + bigString(p.Amount) + p.Asset
	}

	return "[" + strings.Join(parts, ",") + "]"
}

// renderCell formats a volume cell as address/asset.
func renderCell(k VolumeKey) string {
	return k.Address + "/" + k.Asset
}

// bigString renders a possibly-nil big.Int.
func bigString(v *big.Int) string {
	if v == nil {
		return "<nil>"
	}

	return v.String()
}
