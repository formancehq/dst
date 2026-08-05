package main

import "testing"

// A revert carries no reference (NULL). SQL "reference = X" over NULL is NULL, and
// NOT(NULL) is NULL, so a NOT(reference=X) filter must not return the revert.
func TestTxReferenceNullLogic(t *testing.T) {
	revert := &txRecord{reference: "", postings: []Posting{p("a", "world", "USD", 10)}}
	create := &txRecord{reference: "r1", postings: []Posting{p("world", "a", "USD", 10)}}

	if txReference("X").match(1, revert, nil) != triNull {
		t.Fatal("reference=X over a NULL reference should be NULL")
	}
	notRef := txNot{txReference("X")}
	if notRef.match(1, revert, nil) == triTrue {
		t.Fatal("not(reference=X) must not match a NULL-reference revert")
	}
	if notRef.match(2, create, nil) != triTrue {
		t.Fatal("not(reference=X) must match a present reference != X")
	}
}

func TestTriOps(t *testing.T) {
	cases := []struct {
		name string
		got  tri
		want tri
	}{
		{"T and NULL", triAnd(triTrue, triNull), triNull},
		{"F and NULL", triAnd(triFalse, triNull), triFalse},
		{"T or NULL", triOr(triTrue, triNull), triTrue},
		{"F or NULL", triOr(triFalse, triNull), triNull},
		{"not NULL", triNot(triNull), triNull},
		{"not T", triNot(triTrue), triFalse},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// The window is committed txs with id <= frontier matching the filter, ordered by
// id (descending or ascending), capped at pageSize.
func TestTransactionWindow(t *testing.T) {
	s := NewState()
	s.recordTx("1", []Posting{p("world", "a", "USD", 10)}, "r1", nil)
	s.recordTx("2", []Posting{p("world", "b", "USD", 10)}, "r2", nil)
	s.recordTx("3", []Posting{p("world", "c", "USD", 10)}, "r3", nil)

	if got := transactionWindow(s, nil, nil, 2, true, 10); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("DESC frontier=2 = %v, want [2 1]", got)
	}
	if got := transactionWindow(s, nil, nil, 3, false, 10); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("ASC = %v, want [1 2 3]", got)
	}
	if got := transactionWindow(s, nil, nil, 3, false, 2); len(got) != 2 || got[1] != 2 {
		t.Fatalf("pageSize=2 = %v, want [1 2]", got)
	}
}
