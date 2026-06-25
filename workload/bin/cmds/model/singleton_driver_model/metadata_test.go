package main

import "testing"

// set registers and commits a write to a cell, returning it.
func commitWrite(m *metaStore, c metaCell, value string, dispatch, observe uint64, deleted bool) *metaWrite {
	w := &metaWrite{value: value, deleted: deleted, dispatch: dispatch}
	m.register(c, w)
	m.commit(c, w, observe)
	return w
}

func acct(key string) metaCell { return metaCell{kind: metaAccount, id: "a", key: key} }

// A write committed strictly before a read must be the read's value; a value the
// model never wrote (or a lost value) is rejected.
func TestMetaReadAfterWrite(t *testing.T) {
	m := newMetaStore()
	commitWrite(m, acct("k"), "v1", 1, 2, false)

	// read spanning [5,6], after the write
	if k, _, ok := m.validateRead(metaAccount, "a", 5, 6, map[string]string{"k": "v1"}); !ok {
		t.Fatalf("expected v1 valid, got bad key %q", k)
	}
	if _, _, ok := m.validateRead(metaAccount, "a", 5, 6, map[string]string{"k": "v2"}); ok {
		t.Fatal("expected v2 (never written) rejected")
	}
	if _, _, ok := m.validateRead(metaAccount, "a", 5, 6, map[string]string{}); ok {
		t.Fatal("expected absent (lost value) rejected")
	}
}

// A read concurrent with a write may return either the old or the new value.
func TestMetaConcurrentReadWrite(t *testing.T) {
	m := newMetaStore()
	commitWrite(m, acct("k"), "v1", 1, 2, false) // settled before the read
	// w2 overlaps the read: dispatched at 4, committed at 7; read spans [3,8].
	commitWrite(m, acct("k"), "v2", 4, 7, false)

	for _, v := range []string{"v1", "v2"} {
		if _, _, ok := m.validateRead(metaAccount, "a", 3, 8, map[string]string{"k": v}); !ok {
			t.Fatalf("expected %s valid (read overlaps the write)", v)
		}
	}
}

// Two writes that overlap each other (neither finishes before the other starts)
// are both legal for a later read — last-writer-wins under concurrency.
func TestMetaConcurrentWrites(t *testing.T) {
	m := newMetaStore()
	commitWrite(m, acct("k"), "v1", 1, 5, false) // [1,5]
	commitWrite(m, acct("k"), "v2", 2, 6, false) // [2,6] overlaps v1

	for _, v := range []string{"v1", "v2"} {
		if _, _, ok := m.validateRead(metaAccount, "a", 10, 11, map[string]string{"k": v}); !ok {
			t.Fatalf("expected %s valid (concurrent writes, either can win)", v)
		}
	}
}

// After a settled delete, a read must see the key absent, not its old value.
func TestMetaDelete(t *testing.T) {
	m := newMetaStore()
	commitWrite(m, acct("k"), "v1", 1, 2, false)
	commitWrite(m, acct("k"), "", 3, 4, true) // delete, settled before the read

	if _, _, ok := m.validateRead(metaAccount, "a", 5, 6, map[string]string{}); !ok {
		t.Fatal("expected absent valid after delete")
	}
	if _, _, ok := m.validateRead(metaAccount, "a", 5, 6, map[string]string{"k": "v1"}); ok {
		t.Fatal("expected stale v1 rejected after delete")
	}
}

// An in-flight (uncommitted) write is a legal read value — it may have committed
// at the server before the read's point even if its response isn't seen yet.
func TestMetaInflightWriteVisible(t *testing.T) {
	m := newMetaStore()
	w := &metaWrite{value: "v1", dispatch: 1}
	m.register(acct("k"), w) // never committed

	if _, _, ok := m.validateRead(metaAccount, "a", 2, 3, map[string]string{"k": "v1"}); !ok {
		t.Fatal("expected in-flight value to be a legal read")
	}
	// absent is also legal (the write may not have committed yet)
	if _, _, ok := m.validateRead(metaAccount, "a", 2, 3, map[string]string{}); !ok {
		t.Fatal("expected absent also legal while the write is in flight")
	}
}
