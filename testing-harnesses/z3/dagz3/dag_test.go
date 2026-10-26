package dagz3

import (
	"errors"
	"io"
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func discover(t testing.TB, data []byte, cap int) *kinds.Bundle {
	t.Helper()
	b, err := Discover(data, DefaultBudget(cap))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newStore(t testing.TB, b *kinds.Bundle) (*repz3.MemStore, *repz3.Env, repz3.SHA) {
	t.Helper()
	st := repz3.NewMemStore()
	id, _, err := b.Persist(st)
	if err != nil {
		t.Fatal(err)
	}
	return st, kinds.NewEnvLimits(st, Lim), id
}

func wrap(t testing.TB, child *kinds.Bundle, out []byte, f repz3.Frame, head ...[]byte) (*kinds.Bundle, []byte) {
	t.Helper()
	b, o, err := Wrap(child, out, f, head...)
	if err != nil {
		t.Fatal(err)
	}
	return b, o
}

// TestHardDepth: a valid depth-5 dependency graph built from existing nodes is refused at the
// hard maximum by reconstruction, by the reachability walk and by the native catalog; the
// limit is not raised and a larger request is clamped.
func TestHardDepth(t *testing.T) {
	data := ChainA(0, 32, 2)
	b4 := discover(t, data, 4)
	if Depth(b4) != 4 {
		t.Fatalf("depth %d", Depth(b4))
	}
	b5, out := wrap(t, b4, data, repz3.Frame{Prefix: []byte("HDR")}, []byte("magic"))
	if Depth(b5) != 5 {
		t.Fatalf("depth %d", Depth(b5))
	}
	st, env, id := newStore(t, b5)
	if _, _, err := env.Reconstruct(id, io.Discard); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("reconstruct: %v", err)
	}
	if _, err := env.Reachable(id); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("reach: %v", err)
	}
	cat := kinds.NewCatalog(st)
	cat.SetLimits(Lim)
	if err := cat.Ingest(id); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("catalog: %v", err)
	}
	if _, _, err := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 99}).Reconstruct(id, io.Discard); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("clamp: %v", err)
	}
	// the same graph without the extra level is fine, and its output is the wrapped data
	if err := Exact(env, b4.ID, data); err != nil {
		t.Fatal(err)
	}
	_ = out
}

func TestFanOut(t *testing.T) {
	tarb, _, files := FanOut()
	bud := DefaultBudget(4)
	b, err := Discover(tarb, bud)
	if err != nil {
		t.Fatal(err)
	}
	st, env, id := newStore(t, b)
	if err := Exact(env, id, tarb); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(env, id)
	if err != nil {
		t.Fatal(err)
	}
	root := g.Reps[id]
	var derived, stored int
	kindsOf := map[repz3.Kind]int{}
	for _, m := range root.Members {
		if m.Role == repz3.Derived {
			derived++
			kindsOf[g.Reps[m.Child].Kind]++
		} else {
			stored++
		}
	}
	t.Logf("root: %d derived, %d stored, children by kind %v, %d distinct reps, %d edges", derived, stored, kindsOf, len(g.Reps), g.Edges())
	if kindsOf[repz3.KindSQLite] != 2 || kindsOf[repz3.KindCAR] != 1 || kindsOf[repz3.KindGzip] != 1 {
		t.Fatalf("children by kind: %v", kindsOf)
	}
	// one failed analysis (the broken database) fell back without touching its siblings
	if bud.Rejected["sqlite"] != 1 || bud.Accepted["sqlite"] != 1 {
		t.Fatalf("accepted %v rejected %v", bud.Accepted, bud.Rejected)
	}
	// the duplicate databases name one child representation: 2 parent edges, 1 child
	var shared repz3.SHA
	for _, s := range g.Shared() {
		if g.Reps[s].Kind == repz3.KindSQLite {
			shared = s
		}
	}
	if shared == (repz3.SHA{}) || g.Parent[shared] != 2 {
		t.Fatalf("shared SQLite child missing: parents %d", g.Parent[shared])
	}
	sq := 0
	for _, r := range g.Reps {
		if r.Kind == repz3.KindSQLite {
			sq++
		}
	}
	if sq != 1 {
		t.Fatalf("%d SQLite representations", sq)
	}
	// metadata is proportional to the members, not to the nesting
	raw, _ := st.Size(id)
	if int(raw) > 140*len(root.Members) {
		t.Fatalf("root representation %d bytes for %d members", raw, len(root.Members))
	}
	// the duplicate costs headers and member entries, not the database
	names := []string{"README", "db/main.sqlite", "export/blocks.car", "bin/app", "raw/blob.bin", "logs/app.log.gz", "db/broken.sqlite"}
	nodup := Tar(names, files)
	b2 := discover(t, nodup, 4)
	st2 := repz3.NewMemStore()
	if _, _, err := b2.Persist(st2); err != nil {
		t.Fatal(err)
	}
	var s1, s2 int64
	for _, x := range st.IDs() {
		n, _ := st.Size(x)
		s1 += n
	}
	for _, x := range st2.IDs() {
		n, _ := st2.Size(x)
		s2 += n
	}
	t.Logf("with duplicate %d B, without %d B, database %d B", s1, s2, len(files["db/main.sqlite"]))
	if s1-s2 > 2048 {
		t.Fatalf("the duplicate member cost %d bytes", s1-s2)
	}
}

// TestFramingKeepsChildShared: two parents frame the same semantic child differently
// (different prefixes and padding); the child representation and its components are
// stored once, the frames are parent-local.
func TestFramingKeepsChildShared(t *testing.T) {
	db := Join(DB(61, 32))
	child := discover(t, db, 1)
	p1, o1 := wrap(t, child, db, repz3.Frame{Prefix: []byte("HDR1"), Zeros: 100}, []byte("one"))
	p2, o2 := wrap(t, child, db, repz3.Frame{Prefix: []byte("another-header"), Zeros: 7}, []byte("two-two"))
	st := repz3.NewMemStore()
	r := NewRoots(st)
	id1 := must2(t)(r.Add("p1", p1, o1))
	id2 := must2(t)(r.Add("p2", p2, o2))
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(r.Env, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	if g.Parent[child.ID] != 2 || len(g.Reps) != 3 {
		t.Fatalf("parents %d reps %d", g.Parent[child.ID], len(g.Reps))
	}
	_, rs, err := Union(st, r.Env, r.IDs())
	if err != nil {
		t.Fatal(err)
	}
	if rs.UnionBytes >= rs.SumBytes {
		t.Fatalf("no sharing: union %d sum %d", rs.UnionBytes, rs.SumBytes)
	}
	if p1.Rep.Members[1].Frame.Zeros == p2.Rep.Members[1].Frame.Zeros {
		t.Fatal("frames should differ")
	}
}

func must2(t testing.TB) func(repz3.SHA, error) repz3.SHA {
	return func(id repz3.SHA, err error) repz3.SHA {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

// TestSharedRootsLiveness: the shared child lives while any root reaches it and is
// collected with the last one. Liveness is repz3 reachability over a lab root set, not a
// refcount; the representations carry no root list.
func TestSharedRootsLiveness(t *testing.T) {
	db := Join(DB(62, 40))
	mk := func(conf string) (*kinds.Bundle, []byte) {
		out := Tar([]string{conf, "db/main.sqlite"}, map[string][]byte{conf: []byte("owner=" + conf + "\n"), "db/main.sqlite": db})
		return discover(t, out, 2), out
	}
	ba, oa := mk("a.conf")
	bb, ob := mk("b.conf")
	child := discover(t, db, 1)

	st := repz3.NewMemStore()
	r := NewRoots(st)
	ida := must2(t)(r.Add("A", ba, oa))
	idb := must2(t)(r.Add("B", bb, ob))
	g, _ := LoadGraph(r.Env, ida, idb)
	if g.Parent[child.ID] != 2 {
		t.Fatalf("child parents %d", g.Parent[child.ID])
	}
	reachA, _ := r.Env.Reachable(ida)
	reachB, _ := r.Env.Reachable(idb)

	// state 1: both live
	live1, err := r.Live()
	if err != nil {
		t.Fatal(err)
	}
	if freed, _, _ := r.Sweep(); len(freed) != 0 {
		t.Fatalf("state 1 freed %d objects", len(freed))
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, ok := live1.Role[child.ID]; !ok {
		t.Fatal("child not live")
	}

	// state 2: remove A only: exactly the objects only A reaches are freed
	r.Remove("A")
	freed, _, err := r.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	onlyA := 0
	for id := range reachA.Role {
		if _, ok := reachB.Role[id]; !ok {
			onlyA++
		}
	}
	if len(freed) != onlyA || onlyA == 0 {
		t.Fatalf("freed %d, objects only A reaches %d", len(freed), onlyA)
	}
	for _, f := range freed {
		if _, ok := reachB.Role[f]; ok {
			t.Fatalf("freed %x, which B reaches", f[:6])
		}
	}
	if !st.Has(child.ID) {
		t.Fatal("shared child collected while B is live")
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}

	// another root that references the child keeps it after B goes
	r.Remove("B")
	cr := r
	idc := must2(t)(cr.Add("C", child, db))
	if freed, _, _ := r.Sweep(); len(freed) == 0 {
		t.Fatal("B's private objects were not freed")
	}
	if !st.Has(child.ID) || !st.Has(idc) {
		t.Fatal("child collected while a root still reaches it")
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}

	// state 3: last root removed: everything is unreachable
	r.Remove("C")
	if _, _, err := r.Sweep(); err != nil {
		t.Fatal(err)
	}
	if st.Len() != 0 {
		t.Fatalf("%d objects survive with no root", st.Len())
	}
}

func merkleSnap(t testing.TB, keys int) ([]byte, *kinds.Bundle) {
	t.Helper()
	ch, err := kinds.MerkleChain(merkleHash, keys)
	if err != nil {
		t.Fatal(err)
	}
	d := ch.Stages[0].Data
	b, err := ch.Encode(nil, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	return d, b
}

// TestSharedOwningIndexDescendants: the shared child owns its VIA targets through a record
// section (Merkle). Removing one parent must not free any of them.
func TestSharedOwningIndexDescendants(t *testing.T) {
	snap, child := merkleSnap(t, 400)
	pa, oa := wrap(t, child, snap, repz3.Frame{}, []byte("root A header\n"))
	pb, ob := wrap(t, child, snap, repz3.Frame{Prefix: []byte("B:")}, []byte("root B header\n"))
	st := repz3.NewMemStore()
	r := NewRoots(st)
	ida := must2(t)(r.Add("A", pa, oa))
	idb := must2(t)(r.Add("B", pb, ob))
	reachB, _ := r.Env.Reachable(idb)
	if reachB.Count(repz3.DepOwned) == 0 || reachB.Count(repz3.DepIndex) == 0 {
		t.Fatalf("expected owned targets and an index: owned %d index %d", reachB.Count(repz3.DepOwned), reachB.Count(repz3.DepIndex))
	}
	reachA, _ := r.Env.Reachable(ida)
	if reachA.Count(repz3.DepOwned) != reachB.Count(repz3.DepOwned) {
		t.Fatal("parents see different owned sets of the same child")
	}
	r.Remove("A")
	if _, _, err := r.Sweep(); err != nil {
		t.Fatal(err)
	}
	for id, role := range reachB.Role {
		if !st.Has(id) {
			t.Fatalf("object %x (%v) freed while B is live", id[:6], role)
		}
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	r.Remove("B")
	r.Sweep()
	if st.Len() != 0 {
		t.Fatalf("%d objects survive", st.Len())
	}
}
