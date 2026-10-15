package main

import (
	"os"
	"testing"

	"zeros3-z3/fstate"
)

// The sqlitez3 lab representation is exact through the shared runner (serialised
// recipe charged byte for byte, rebuilt from the recipe and stored components),
// and non-SQLite input falls back to CDC.
func TestSQLiteRepExact(t *testing.T) {
	db, err := os.ReadFile("../../sqlitez3/testdata/ps4096.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	other := append([]byte(nil), db...)
	other[5000] ^= 1
	st := []*fstate.State{
		{Label: "a", Files: []fstate.File{{Name: "db", Kind: "sqlite", Data: db}, {Name: "junk", Kind: "sqlite", Data: make([]byte, 100000)}}},
		{Label: "b", Files: []fstate.File{{Name: "db", Kind: "sqlite", Data: other}}},
	}
	r := &fstate.Runner{RefBytes: 96}
	rows := r.RunStates(st, []fstate.RepDef{sqlitezRep("tree-64", 64), laneRepByName("sqlite", "pages-norm")})
	for _, x := range rows {
		if !x.Exact || x.Err != "" {
			t.Errorf("%s/%s: exact=%v err=%q", x.State, x.Rep, x.Exact, x.Err)
		}
		if x.Rep == "tree-64" && x.State == "a" && x.Detail["fallback_files"] != 1 {
			t.Errorf("non-SQLite file not routed to CDC: %v", x.Detail)
		}
	}
}

func laneRepByName(lane, name string) fstate.RepDef {
	for _, r := range laneReps(lane) {
		if r.Name == name {
			return r
		}
	}
	panic(name)
}

func TestScanners(t *testing.T) {
	db, _ := os.ReadFile("../../sqlitez3/testdata/ps4096.sqlite")
	segs, err := sqlitePagesNorm(db)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, s := range segs {
		n += s.n
	}
	if n != int64(len(db)) {
		t.Fatalf("segments cover %d of %d", n, len(db))
	}
	if _, err := sqlitePages(db[:4000]); err == nil {
		t.Error("truncated page accepted")
	}
	if _, err := ldbBlocks(db); err == nil {
		t.Error("sqlite parsed as a leveldb table")
	}
}
