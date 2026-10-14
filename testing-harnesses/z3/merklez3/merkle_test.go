package merklez3

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

type collect struct{ news, reused map[ID]bool }

func newCollect() *collect { return &collect{map[ID]bool{}, map[ID]bool{}} }
func (c *collect) New(n Node, _ int) error {
	c.news[n.ID] = true
	return nil
}
func (c *collect) Reuse(id ID, _ int) { c.reused[id] = true }

func allNodes(t *testing.T, ms *MapStore, root ID) map[ID]Node {
	t.Helper()
	m := map[ID]Node{}
	if _, err := Walk(ms, root, func(n Node, _ int) error { m[n.ID] = n; return nil }); err != nil {
		t.Fatal(err)
	}
	return m
}

type pair struct {
	ms         *MapStore
	b, t       ID
	bst, tst   BuildStats
	base, targ *State
}

func mkPair(t *testing.T, cfg Config, n int, mut func(*State) *State) pair {
	t.Helper()
	ms := NewMapStore()
	base := NewState(7, n)
	targ := mut(base)
	b, bst := base.BuildInto(cfg, ms)
	tt, tst := targ.BuildInto(cfg, ms)
	return pair{ms, b, tt, bst, tst, base, targ}
}

// oracle: new nodes of target relative to base by brute-force set difference.
func (p pair) oracle(t *testing.T) map[ID]bool {
	bs, ts := allNodes(t, p.ms, p.b), allNodes(t, p.ms, p.t)
	out := map[ID]bool{}
	for id := range ts {
		if _, ok := bs[id]; !ok {
			out[id] = true
		}
	}
	return out
}

func sameSet(a, b map[ID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func TestSparseChange(t *testing.T) {
	for _, h := range []HashFn{HashSHA3_256, HashSHA512_256, HashSHA256, HashShort16} {
		cfg := Config{Hash: h, LeafMax: 8}
		p := mkPair(t, cfg, 20000, func(s *State) *State { return s.Update(s.PickScattered(1, 1)) })
		want := p.oracle(t)
		known := allNodes(t, p.ms, p.b)
		d := newCollect()
		sd, err := Discover(p.ms, p.t, func(id ID) bool { _, ok := known[id]; return ok }, d, VerifyWith(h))
		if err != nil {
			t.Fatal(err)
		}
		a := newCollect()
		sa, err := DiffAligned(p.ms, p.ms, p.b, p.t, a, VerifyWith(h))
		if err != nil {
			t.Fatal(err)
		}
		if !sameSet(d.news, want) || !sameSet(a.news, want) {
			t.Fatalf("%s: discovered %d / aligned %d new nodes, oracle %d", h.Name, len(d.news), len(a.news), len(want))
		}
		full, _ := Walk(p.ms, p.t, nil)
		if len(want) > p.tst.Depth+1 || sd.NodesRead != len(want) || sa.NodesRead > 2*len(want) {
			t.Errorf("%s: new %d depth %d reads %d/%d", h.Name, len(want), p.tst.Depth, sd.NodesRead, sa.NodesRead)
		}
		if full.NodesRead != p.tst.Nodes || sd.NodesRead*50 > full.NodesRead {
			t.Errorf("%s: guided read %d of %d nodes", h.Name, sd.NodesRead, full.NodesRead)
		}
		t.Logf("%s: nodes %d, new %d, discover reads %d (compares %d), aligned reads %d, full %d", h.Name, p.tst.Nodes, len(want), sd.NodesRead, sd.HashCompares, sa.NodesRead, full.NodesRead)
	}
}

func TestDenseChange(t *testing.T) {
	cfg := DefaultConfig()
	p := mkPair(t, cfg, 5000, func(s *State) *State { return s.Update(s.Keys) })
	want := p.oracle(t)
	if len(want) != p.tst.Nodes {
		t.Fatalf("every node should be new: %d of %d", len(want), p.tst.Nodes)
	}
	known := allNodes(t, p.ms, p.b)
	st, err := Discover(p.ms, p.t, func(id ID) bool { _, ok := known[id]; return ok }, nil, nil)
	if err != nil || st.NodesRead != p.tst.Nodes || st.Reused != 0 {
		t.Fatalf("dense: %+v %v", st, err)
	}
}

func TestLayoutOnlyRewriteDefeatsHashes(t *testing.T) {
	ms := NewMapStore()
	s := NewState(3, 4000)
	a, _ := s.BuildInto(Config{Hash: HashSHA3_256, LeafMax: 8}, ms)
	b, bst := s.BuildInto(Config{Hash: HashSHA3_256, LeafMax: 8, Layout: 1}, ms)
	known := allNodes(t, ms, a)
	st, _ := Discover(ms, b, func(id ID) bool { _, ok := known[id]; return ok }, nil, nil)
	if st.NewNodes != bst.Nodes || st.Reused != 0 {
		t.Fatalf("layout rewrite: %+v of %d", st, bst.Nodes)
	}
}

func TestInsertRemoveSubtreeReplace(t *testing.T) {
	cfg := DefaultConfig()
	for name, mut := range map[string]func(*State) *State{
		"insert":  func(s *State) *State { return s.Insert(s.FreshKeys(5, 10)) },
		"remove":  func(s *State) *State { return s.Remove(s.PickScattered(2, 10)) },
		"cluster": func(s *State) *State { return s.Update(s.PickClustered(4, 40)) },
		"subtree": func(s *State) *State { return s.Update(s.KeysUnder(3, 1)) },
	} {
		p := mkPair(t, cfg, 20000, mut)
		want := p.oracle(t)
		known := allNodes(t, p.ms, p.b)
		d, a := newCollect(), newCollect()
		sd, err := Discover(p.ms, p.t, func(id ID) bool { _, ok := known[id]; return ok }, d, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DiffAligned(p.ms, p.ms, p.b, p.t, a, nil); err != nil {
			t.Fatal(err)
		}
		if !sameSet(d.news, want) || !sameSet(a.news, want) {
			t.Errorf("%s: discover %d aligned %d oracle %d", name, len(d.news), len(a.news), len(want))
		}
		t.Logf("%s: target %d nodes, new %d (%.1f%%), guided reads %d", name, p.tst.Nodes, len(want), 100*float64(len(want))/float64(p.tst.Nodes), sd.NodesRead)
	}
}

func TestMergeAndShare(t *testing.T) {
	cfg := DefaultConfig()
	ms := NewMapStore()
	base := NewState(1, 8000)
	a := base.Update(base.KeysUnder(1, 1))
	b := base.Update(base.KeysUnder(12, 1))
	rb, _ := base.BuildInto(cfg, ms)
	ra, _ := a.BuildInto(cfg, ms)
	rbb, _ := b.BuildInto(cfg, ms)
	m, err := MergeRoots(cfg, ms, ra, rbb, 8)
	if err != nil {
		t.Fatal(err)
	}
	known := allNodes(t, ms, ra)
	for id := range allNodes(t, ms, rbb) {
		known[id] = Node{}
	}
	st, _ := Discover(ms, m, func(id ID) bool { _, ok := known[id]; return ok }, nil, nil)
	if st.NewNodes != 1 {
		t.Errorf("merge of two known lines should add only its root, added %d", st.NewNodes)
	}
	_ = rb
	child, _ := (&State{Keys: base.Keys[:50], Ver: map[uint64]uint32{}}).BuildInto(cfg, ms)
	sr := ShareRoot(cfg, ms, child, 4)
	w, _ := Walk(ms, sr, nil)
	cw, _ := Walk(ms, child, nil)
	if w.NodesRead != cw.NodesRead+1 {
		t.Errorf("shared subtree should be visited once: %d vs %d+1", w.NodesRead, cw.NodesRead)
	}
}

func TestVerifyRejectsTamperedNode(t *testing.T) {
	cfg := DefaultConfig()
	p := mkPair(t, cfg, 3000, func(s *State) *State { return s.Update(s.PickScattered(1, 1)) })
	// flip a byte in some changed node's payload but keep its claimed identity
	d := newCollect()
	Discover(p.ms, p.t, func(ID) bool { return false }, d, nil)
	var victim ID
	for id := range d.news {
		victim = id
		break
	}
	n := p.ms.M[victim]
	bad := append([]byte(nil), n.Payload...)
	bad[len(bad)-1] ^= 1
	p.ms.M[victim] = Node{ID: victim, Payload: bad, Kids: n.Kids}
	if _, err := Discover(p.ms, p.t, func(ID) bool { return false }, nil, VerifyWith(cfg.Hash)); err == nil {
		t.Fatal("tampered node accepted")
	}
	// Without Verify the traversal itself still terminates: the hash is a hint.
	if _, err := Discover(p.ms, p.t, func(ID) bool { return false }, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	ms := NewMapStore()
	root, _ := NewState(2, 3000).BuildInto(cfg, ms)
	var b1 bytes.Buffer
	if _, err := Snapshot(ms, root, &b1); err != nil {
		t.Fatal(err)
	}
	r2, nodes, err := ParseSnapshot(b1.Bytes(), cfg.Hash)
	if err != nil || r2 != root {
		t.Fatal(err)
	}
	ms2 := NewMapStore()
	for _, n := range nodes {
		ms2.Put(n)
	}
	var b2 bytes.Buffer
	Snapshot(ms2, root, &b2)
	if sha256.Sum256(b1.Bytes()) != sha256.Sum256(b2.Bytes()) {
		t.Fatal("snapshot not reproduced")
	}
}

func TestDiskStoreMatchesMap(t *testing.T) {
	cfg := DefaultConfig()
	ms := NewMapStore()
	s := NewState(9, 6000)
	root, _ := s.BuildInto(cfg, ms)
	db, err := NewDiskBuilder(t.TempDir(), 32)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ms.M {
		db.Put(n)
	}
	ds, err := db.Finish(DecodeKids)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	if ds.Len() != len(ms.M) {
		t.Fatalf("%d vs %d", ds.Len(), len(ms.M))
	}
	var b1, b2 bytes.Buffer
	Snapshot(ms, root, &b1)
	Snapshot(ds, root, &b2)
	if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
		t.Fatal("disk-backed snapshot differs")
	}
}
