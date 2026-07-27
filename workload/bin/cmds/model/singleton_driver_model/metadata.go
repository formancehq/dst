package main

import (
	"strings"

	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// reservedMetaPrefix namespaces server-managed metadata (e.g. the
// com.formance.spec/state/reverts key stamped on a reverted transaction). The
// workload never writes these, so they are excluded from validation.
const reservedMetaPrefix = "com.formance.spec/"

// Metadata is validated on a track separate from volumes. v2's metadata
// endpoints return 204 with no commit sequence, so metadata writes can't be
// linearized into the volume re-order buffer. Instead each (target, key) cell is
// treated as a register: every write is kept with its dispatch/observe tickets,
// and a read is checked against the values that could be the cell's latest at the
// read's linearization point (real-time happens-before). This needs no observable
// sequence and handles concurrent last-writer-wins writes.

type metaKind uint8

const (
	metaAccount metaKind = iota
	metaTransaction
	metaLedger
)

// metaCell is one (target, key) register: an account address or transaction id,
// plus the metadata key. Ledger-level metadata uses an empty id (one ledger per
// checker).
type metaCell struct {
	kind metaKind
	id   string
	key  string
}

// metaTarget is a metadata read subject (a whole account or transaction).
type metaTarget struct {
	kind metaKind
	id   string
}

// metaWrite is one observed write to a cell. deleted marks a delete (the cell's
// value becomes absent). committed/observe are set when the response is seen.
type metaWrite struct {
	value     string
	deleted   bool
	dispatch  uint64
	observe   uint64
	committed bool
}

// metaStore holds the per-cell write history for read validation.
type metaStore struct {
	history map[metaCell][]*metaWrite
}

func newMetaStore() *metaStore {
	return &metaStore{history: map[metaCell][]*metaWrite{}}
}

// register records a dispatched write. Caller holds the checker's mu.
func (m *metaStore) register(c metaCell, w *metaWrite) {
	m.history[c] = append(m.history[c], w)
}

// commit marks a write committed at observe. Caller holds mu.
func (m *metaStore) commit(c metaCell, w *metaWrite, observe uint64) {
	w.committed = true
	w.observe = observe
	m.prune(c)
}

// drop removes a write whose outcome was transient/unknown — it didn't happen.
// Caller holds mu.
func (m *metaStore) drop(c metaCell, w *metaWrite) {
	h := m.history[c]
	for i, x := range h {
		if x == w {
			m.history[c] = append(h[:i], h[i+1:]...)
			return
		}
	}
}

// prune bounds per-cell history. Reads complete far faster than the cap-many
// writes it would take to a single cell to overwrite anything an active read
// could still need, so dropping the oldest is safe.
func (m *metaStore) prune(c metaCell) {
	const cap = 32
	if h := m.history[c]; len(h) > cap {
		m.history[c] = append([]*metaWrite(nil), h[len(h)-cap:]...)
	}
}

// committedCells returns the cells whose current committed value is present
// (a non-deleted latest committed write) — delete targets. Caller holds mu.
func (m *metaStore) presentCells() []metaCell {
	var out []metaCell
	for c, h := range m.history {
		for i := len(h) - 1; i >= 0; i-- {
			if h[i].committed {
				if !h[i].deleted {
					out = append(out, c)
				}
				break
			}
		}
	}
	return out
}

// metaValues is the set of values a cell could legally read as: a set of present
// values plus whether absent is legal.
type metaValues struct {
	vals   map[string]bool
	absent bool
}

// validValues enumerates the values cell c could return for a read spanning
// [dR, oR]. A write w is a candidate if it could be the cell's latest at the
// read's linearization point: dispatched before the read returned, and not
// definitely overwritten before the read began (no committed w2 that started
// after w finished and finished before the read began). In-flight writes are
// never dominated (their commit time is unknown), so they stay candidates. Absent
// is legal when no write had committed before the read began. Caller holds mu.
func (m *metaStore) validValues(c metaCell, dR, oR uint64) metaValues {
	v := metaValues{vals: map[string]bool{}}

	h := m.history[c]

	anyBeforeStart := false
	for _, w := range h {
		if w.committed && w.observe < dR {
			anyBeforeStart = true
			break
		}
	}
	if !anyBeforeStart {
		v.absent = true
	}

	for _, w := range h {
		if w.dispatch >= oR {
			continue
		}
		if w.committed {
			stale := false
			for _, w2 := range h {
				if w2 != w && w2.committed && w2.dispatch > w.observe && w2.observe < dR {
					stale = true
					break
				}
			}
			if stale {
				continue
			}
		}
		if w.deleted {
			v.absent = true
		} else {
			v.vals[w.value] = true
		}
	}

	return v
}

// createMetaWrite pairs a registered create-time metadata write with its cell so
// it can be committed or dropped once the create's outcome is known.
type createMetaWrite struct {
	cell  metaCell
	write *metaWrite
}

// registerCreateAccountMeta registers, at dispatch, the account-metadata writes a
// create transaction carries, so a concurrent read sees them as in-flight
// candidates before the create's outcome is known. settleCreateMeta commits or
// drops them. Transaction-level metadata is not registered here: its target id is
// assigned only on commit and no read can reach it before then. Caller holds c.mu.
func (c *Checker) registerCreateAccountMeta(op Operation, dispatch uint64) []createMetaWrite {
	var refs []createMetaWrite
	for addr, kv := range op.accountMeta {
		for key, val := range kv {
			cell := metaCell{kind: metaAccount, id: addr, key: key}
			w := &metaWrite{value: val, dispatch: dispatch}
			c.metaStore.register(cell, w)
			refs = append(refs, createMetaWrite{cell: cell, write: w})
		}
	}

	return refs
}

// settleCreateMeta finalizes a create's carried metadata against its outcome. On
// commit it marks the account-metadata writes committed and registers the
// transaction-level metadata — keyed by the id assigned on commit — as already
// committed; otherwise (transient or a business failure, in which case the atomic
// transaction and its metadata did not apply) it drops the account-metadata
// writes. Caller holds c.mu.
func (c *Checker) settleCreateMeta(op Operation, refs []createMetaWrite, data []*shared.V2Transaction, committed bool, dispatch, observe uint64) {
	if !committed {
		for _, r := range refs {
			c.metaStore.drop(r.cell, r.write)
		}
		return
	}

	for _, r := range refs {
		c.metaStore.commit(r.cell, r.write, observe)
	}

	if len(op.metadata) > 0 && len(data) == 1 {
		id := data[0].GetID().String()
		for key, val := range op.metadata {
			cell := metaCell{kind: metaTransaction, id: id, key: key}
			c.metaStore.register(cell, &metaWrite{value: val, dispatch: dispatch, observe: observe, committed: true})
		}
	}
}

// validateRead checks a target's whole server metadata map against the model:
// every key present in either the server response or the model must read a legal
// value (validValues). Returns the first offending key, false. Caller holds mu.
func (m *metaStore) validateRead(kind metaKind, id string, dR, oR uint64, server map[string]string) (key, serverVal string, ok bool) {
	keys := map[string]bool{}
	for k := range server {
		if strings.HasPrefix(k, reservedMetaPrefix) {
			continue
		}
		keys[k] = true
	}
	for c := range m.history {
		if c.kind == kind && c.id == id {
			keys[c.key] = true
		}
	}

	for k := range keys {
		vv := m.validValues(metaCell{kind: kind, id: id, key: k}, dR, oR)
		if sv, present := server[k]; present {
			if !vv.vals[sv] {
				return k, sv, false
			}
		} else if !vv.absent {
			return k, "<absent>", false
		}
	}

	return "", "", true
}
