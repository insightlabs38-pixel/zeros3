package dagz3

import (
	"reflect"
	"testing"

	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// TestReachUnionAndReadSet: for the depth-4 chains and the fan-out outer, the union of
// transitive reachability is the exact object set a reconstruction needs (the read set is a
// subset of it, nothing is charged twice) and sharing shows up as union < sum.
func TestReachUnionAndReadSet(t *testing.T) {
	fan, _, _ := FanOut()
	for name, data := range map[string][]byte{"A": ChainA(0, 48, 3), "B": ChainB(0), "fan-out": fan} {
		b := discover(t, data, 4)
		st, env, id := newStore(t, b)
		union, rs, err := Union(st, env, []repz3.SHA{id})
		if err != nil {
			t.Fatal(err)
		}
		read, bad, err := ReadSet(st, env, []repz3.SHA{id}, [][]byte{data})
		if err != nil {
			t.Fatal(err)
		}
		if len(bad) != 0 {
			t.Fatalf("%s: %d objects read but not reachable", name, len(bad))
		}
		if len(union.Order) != st.Len() {
			t.Fatalf("%s: reach %d objects, store %d (hidden or unreachable storage)", name, len(union.Order), st.Len())
		}
		t.Logf("%s: reach %d objects (%d B), read %d objects, union %d sum %d", name, rs.Union, rs.UnionBytes, len(read), rs.Union, rs.Sum)
	}
}

// TestDeleteEachReachableObject: removing any object a root reaches either breaks its
// reconstruction with a bounded contract error or (for a lookup-only object) leaves it
// exact; never a panic, a different output or an unclassified error.
func TestDeleteEachReachableObject(t *testing.T) {
	snap, mchild := merkleSnap(t, 300)
	mw, mout := wrap(t, mchild, snap, repz3.Frame{}, []byte("hdr"))
	cases := map[string]struct {
		b    *kinds.Bundle
		data []byte
	}{"A": {nil, ChainA(0, 24, 2)}, "B": {nil, ChainB(0)}, "merkle": {mw, mout}}
	for name, c := range cases {
		if c.b == nil {
			c.b = discover(t, c.data, 4)
		}
		st, env, id := newStore(t, c.b)
		res, err := DeleteEach(st, env, id, c.data)
		if err != nil {
			t.Fatal(err)
		}
		for role, d := range res {
			t.Logf("%s %-6v objects %4d needed %4d", name, role, d.Total, d.Needed)
			if d.Unbounded != 0 || d.WrongBytes != 0 {
				t.Errorf("%s %v: %d unbounded errors, %d wrong outputs", name, role, d.Unbounded, d.WrongBytes)
			}
			if role != repz3.DepIndex && d.Needed != d.Total {
				t.Errorf("%s %v: %d of %d reachable objects are not needed", name, role, d.Total-d.Needed, d.Total)
			}
		}
	}
}

// TestCatalogOverSharedRoots: the native catalog is derived state. Over revisions that share
// subtrees and parents that share a revision, removing a root and rebuilding from the
// remaining roots gives the same deterministic mappings as the incremental catalog.
func TestCatalogOverSharedRoots(t *testing.T) {
	ch, err := kinds.MerkleChain(merkleHash, 500)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	cat := kinds.NewCatalog(st)
	var ids []repz3.SHA
	for i := 0; i < 2; i++ {
		b, err := ch.Encode(st, cat, ch.Stages[i].Data)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := kinds.Commit(st, cat, b)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// a parent that wraps revision 1 (shares the child; frames are parent-local)
	b1, err := ch.Encode(st, nil, ch.Stages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	w, wout := wrap(t, b1, ch.Stages[1].Data, repz3.Frame{Prefix: []byte("W:")}, []byte("wrapper"))
	wid, _, err := w.Persist(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.Ingest(wid); err != nil {
		t.Fatal(err)
	}
	_ = wout
	roots := []repz3.SHA{ids[0], ids[1], wid}
	full := cat.Mappings()
	rb := func(rs ...repz3.SHA) map[string][]repz3.SHA {
		c, err := repz3.Rebuild(st, kinds.Domain, rs)
		if err != nil {
			t.Fatal(err)
		}
		return c.Mappings()
	}
	if !reflect.DeepEqual(full, rb(roots...)) || !reflect.DeepEqual(full, rb(wid, ids[1], ids[0])) {
		t.Fatal("rebuild differs from the incremental catalog or depends on root order")
	}
	// several roots expose the same native ids: one target each, no ambiguity
	for key, ts := range full {
		if len(ts) != 1 {
			t.Fatalf("native key %q maps to %d targets", key, len(ts))
		}
	}
	cat.Remove(ids[0])
	if !reflect.DeepEqual(cat.Mappings(), rb(ids[1], wid)) {
		t.Fatal("after removing revision 0 the catalog differs from a rebuild")
	}
	cat.Remove(wid)
	if !reflect.DeepEqual(cat.Mappings(), rb(ids[1])) {
		t.Fatal("after removing the wrapper the catalog differs from a rebuild")
	}
	// a second trie under another native hash exposes disjoint identities: both coexist, removal of
	// one leaves the other's mappings exactly as a rebuild gives them
	ch2, err := kinds.MerkleChain(merklez3.HashSHA512_256, 300)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := ch2.Encode(st, cat, ch2.Stages[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := kinds.Commit(st, cat, b2)
	if err != nil {
		t.Fatal(err)
	}
	both := cat.Mappings()
	only2 := rb(id2)
	for k := range only2 {
		if _, dup := rb(ids[1])[k]; dup {
			t.Fatalf("native key %q appears under both hash domains", k)
		}
	}
	if len(both) != len(rb(ids[1]))+len(only2) {
		t.Fatalf("catalog holds %d keys, domains hold %d + %d", len(both), len(rb(ids[1])), len(only2))
	}
	cat.Remove(id2)
	if !reflect.DeepEqual(cat.Mappings(), rb(ids[1])) {
		t.Fatal("removing the second domain changed the first")
	}
	cat.Remove(ids[1])
	if cat.Entries() != 0 {
		t.Fatalf("%d entries with no root", cat.Entries())
	}

	// same native identity -> different component stays ambiguous, shared graph or not
	mk := func(target repz3.SHA) repz3.SHA {
		recs, n, _ := repz3.BuildIndex(8, map[string]repz3.SHA{"NATIVE01": target})
		ref, _ := st.Put(recs)
		rep := &repz3.Rep{Kind: repz3.KindCAR, Indexes: []repz3.Index{{KeyWidth: 8, Count: n, Ref: ref}}}
		raw, _ := rep.Marshal()
		id, _ := st.Put(raw)
		return id
	}
	x, y := mk(repz3.SHA{1}), mk(repz3.SHA{2})
	amb := repz3.NewCatalog(st, nil)
	amb.SetLimits(Lim)
	if err := amb.Ingest(x); err != nil {
		t.Fatal(err)
	}
	if err := amb.Ingest(y); err != nil {
		t.Fatal(err)
	}
	if _, _, err := amb.Resolve(repz3.DefaultDomain(&repz3.Rep{Kind: repz3.KindCAR}, repz3.Index{KeyWidth: 8}), []byte("NATIVE01")); err == nil {
		t.Fatal("conflicting owners resolved silently")
	}
}
