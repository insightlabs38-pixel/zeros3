package dagz3

import (
	"bytes"
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func smallChain(n int) [][]byte {
	var vs [][]byte
	for r := 0; r < n; r++ {
		vs = append(vs, ChainA(r, 48, 3))
	}
	return vs
}

func tarOf(name string, d []byte) []byte { return Tar([]string{name}, map[string][]byte{name: d}) }

// TestSameBytesDifferentRepresentations: object identity (the output digest) and representation
// identity are different things; one byte string can be live under several roots at once.
func TestSameBytesDifferentRepresentations(t *testing.T) {
	data := Gzip(tarOf("a.db", Join(DB(81, 24))), 6)
	cdc, deep := discover(t, data, 0), discover(t, data, 4)
	if cdc.ID == deep.ID {
		t.Fatal("two different representations share an identity")
	}
	a, err := CandFromBundle("x/cdc", "x", cdc, data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CandFromBundle("x/deep", "x", deep, data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Out != b.Out || a.Root == b.Root {
		t.Fatal("candidates must agree on the object and differ on the representation")
	}
	st := repz3.NewMemStore()
	r := NewRoots(st)
	r.Add("cdc", cdc, data)
	r.Add("deep", deep, data)
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	r.Remove("deep")
	if _, _, err := r.Sweep(); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil { // the other representation of the same bytes is untouched
		t.Fatal(err)
	}
}

// TestPlanAccountingIsExact: the planner's prediction equals what the store actually grew by,
// and the plan reconstructs every object exactly.
func TestPlanAccountingIsExact(t *testing.T) {
	vs := smallChain(3)
	sets, datas := map[string]*CandSet{}, map[string][]byte{}
	order := []string{"v0", "v1", "v2"}
	for i, o := range order {
		cs, err := Candidates(o, vs[i], []int{0, 1, 2, 3, 4})
		if err != nil {
			t.Fatal(err)
		}
		sets[o], datas[o] = cs, vs[i]
	}
	for _, per := range []int64{0, 64} {
		for _, solve := range []func(*Problem) (*Plan, error){(*Problem).Independent, (*Problem).Marginal, (*Problem).Refine, func(p *Problem) (*Plan, error) { return p.Exact(0) }} {
			p := Assemble(sets, order, datas, nil, per)
			pl, err := solve(p)
			if err != nil {
				t.Fatal(err)
			}
			st := repz3.NewMemStore()
			bytesNew, objs, err := Apply(st, sets, pl, order)
			if err != nil {
				t.Fatal(err)
			}
			if got := bytesNew + per*int64(objs); got != pl.Total || objs != pl.NewObjs {
				t.Fatalf("%s overhead %d: plan %d (%d objs), store grew %d (%d objs)", pl.Method, per, pl.Total, pl.NewObjs, got, objs)
			}
			env := kinds.NewEnvLimits(st, Lim)
			for _, o := range order {
				if err := Exact(env, sets[o].Bundles[pl.Choice[o]].ID, datas[o]); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// a revision chain: the optimum over the whole chain beats choosing each revision on its own
	// (the investment in the deep representation amortizes over later revisions)
	ind, online, hind, err := Hindsight(sets, order, datas, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("independent %d online %d hindsight %d", ind, online, hind)
	if hind > ind || hind > online {
		t.Fatal("the global optimum is worse than a heuristic")
	}
}

// TestPlannerMayChooseTheShallowerWinner: when nesting captures no extra redundancy the planner
// must not be forced to the deepest valid candidate.
func TestPlannerMayChooseTheShallowerWinner(t *testing.T) {
	base := Random(7, 120<<10)
	var vs [][]byte
	for r := 0; r < 3; r++ {
		vs = append(vs, Gzip(append(append([]byte(nil), base...), Random(uint64(2*r+11), 1500)...), 6))
	}
	sets, datas := map[string]*CandSet{}, map[string][]byte{}
	order := []string{"v0", "v1", "v2"}
	for i, o := range order {
		cs, err := Candidates(o, vs[i], []int{0, 1, 2, 3, 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Cands) < 2 {
			t.Fatalf("expected a deeper valid candidate for %s", o)
		}
		sets[o], datas[o] = cs, vs[i]
	}
	pl, err := Assemble(sets, order, datas, nil, 64).Exact(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range order {
		if pl.Choice[o] != o+"/d0" {
			t.Fatalf("%s: chose %s; the exact gzip layer exposes nothing the compressed bytes' chunks do not share", o, pl.Choice[o])
		}
	}
}

// TestCrossFormatChildIsShared: the same database inside a tar.gz and a tar.zst (and loose) is
// one SQLite representation with one set of page objects; the second container costs only its
// own codec description.
func TestCrossFormatChildIsShared(t *testing.T) {
	x := Join(DB(91, 48))
	tp := tarOf("x.db", x)
	st := repz3.NewMemStore()
	var added []int64
	for _, d := range [][]byte{Gzip(tp, 6), Zstd(tp), x} {
		b, err := Discover(d, DefaultBudget(4))
		if err != nil {
			t.Fatal(err)
		}
		n, _, err := persistNew(st, b)
		if err != nil {
			t.Fatal(err)
		}
		added = append(added, n)
	}
	t.Logf("bytes added: tar.gz %d, tar.zst %d, loose %d (database %d B)", added[0], added[1], added[2], len(x))
	if added[1] > added[0]/2 || added[2] > 4096 {
		t.Fatalf("the semantic child was not shared: %v", added)
	}
}

// TestExistingChildIsReused: a child representation already in the store makes the parent that
// arrives later cheap, with no cooperation from the parent's analysis.
func TestExistingChildIsReused(t *testing.T) {
	db := Join(DB(28, 48))
	arch := Gzip(tarOf("data/app.db", db), 6)
	loose := discover(t, db, 1)
	empty, withChild := repz3.NewMemStore(), repz3.NewMemStore()
	loose.Persist(withChild)
	b := discover(t, arch, 4)
	n0, _, _ := persistNew(empty, b)
	n1, _, _ := persistNew(withChild, b)
	t.Logf("archive costs %d B alone, %d B when the database is already stored", n0, n1)
	if n1 >= n0/3 {
		t.Fatalf("existing child not reused: %d vs %d", n1, n0)
	}
}

// TestNestedPointReadAvoidsWholeParent: one page of a database at depth 1..4 costs the page,
// its nodes and one small representation object per level; never the parent's payload.
func TestNestedPointReadAvoidsWholeParent(t *testing.T) {
	db := Join(DB(28, 96))
	want := db[40*4096 : 41*4096]
	arts := [][]byte{db, tarOf("a.db", db), Gzip(tarOf("a.db", db), 6), Zstd(tarOf("a.db.gz", Gzip(db, 6)))}
	var costs []int64
	for i, a := range arts {
		b := discover(t, a, 4)
		if Depth(b) != i+1 {
			t.Fatalf("artifact %d has depth %d", i, Depth(b))
		}
		st, env, id := newStore(t, b)
		rc, err := PointReadPage(st, env, id, int64(len(db)), 40, want)
		if err != nil {
			t.Fatal(err)
		}
		if rc.Whole {
			t.Fatalf("depth %d fell back to a whole reconstruction", i+1)
		}
		costs = append(costs, rc.Bytes)
		if rc.Reps != i+1 {
			t.Fatalf("depth %d read %d representation objects", i+1, rc.Reps)
		}
	}
	t.Logf("point read bytes by depth: %v", costs)
	if costs[3]-costs[0] > 2048 {
		t.Fatalf("three extra levels cost %d bytes", costs[3]-costs[0])
	}
}

// TestNaturalGraphNeedsNoGraphWire: revisions of graph-shaped data (a Merkle trie) share
// nodes through content-addressed components and kind-local index sections only; the wire
// carries no DERIVED edge for them, and removing one revision keeps the others exact.
func TestNaturalGraphNeedsNoGraphWire(t *testing.T) {
	ch, err := kinds.MerkleChain(merkleHash, 600)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	cat := kinds.NewCatalog(st)
	r := NewRoots(st)
	var ids []repz3.SHA
	for i, stg := range ch.Stages {
		b, err := ch.Encode(st, cat, stg.Data)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := kinds.Commit(st, cat, b)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		r.names[string(rune('a'+i))], r.want[string(rune('a'+i))] = id, stg.Data
	}
	g, err := LoadGraph(r.Env, ids...)
	if err != nil {
		t.Fatal(err)
	}
	if g.Edges() != 0 {
		t.Fatalf("%d DERIVED edges for a graph-shaped format", g.Edges())
	}
	_, rs, err := Union(st, r.Env, ids)
	if err != nil {
		t.Fatal(err)
	}
	if rs.UnionBytes*2 > rs.SumBytes {
		t.Fatalf("revisions share little: union %d sum %d", rs.UnionBytes, rs.SumBytes)
	}
	delete(r.names, "a")
	delete(r.want, "a")
	if _, _, err := r.Sweep(); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
}

// TestHostileBudgets: cumulative budgets bound nested analysis; a refused payload falls back to the
// parent's ordinary representation and the result still reconstructs exactly.
func TestHostileBudgets(t *testing.T) {
	// thousands of tiny magic-looking members
	var names []string
	files := map[string][]byte{}
	for i := 0; i < 3000; i++ {
		n := string([]byte{'m', byte('a' + i%26), byte('a' + i/26%26), byte('a' + i/676)})
		names = append(names, n)
		f := make([]byte, 600)
		copy(f, "SQLite format 3\x00")
		f[16], f[17], f[100], f[101] = 0, 7, byte(i), byte(i>>8)
		files[n] = f
	}
	data := Tar(names, files)
	b := DefaultBudget(4)
	root, err := Discover(data, b)
	if err != nil {
		t.Fatal(err)
	}
	if b.Attempts > b.MaxChildren+1 || b.Stops["children"] == 0 {
		t.Fatalf("attempts %d stops %v", b.Attempts, b.Stops)
	}
	st, env, id := newStore(t, root)
	if err := Exact(env, id, data); err != nil {
		t.Fatal(err)
	}
	_ = st

	// decompression bomb under a small decode budget
	bomb := Gzip(make([]byte, 32<<20), 9)
	b = DefaultBudget(4)
	b.MaxDecoded = 1 << 20
	root, err = Discover(bomb, b)
	if err != nil {
		t.Fatal(err)
	}
	if !IsPlainCDC(root) || b.Stops["decoded"] != 1 || b.Decoded > b.MaxDecoded+1 {
		t.Fatalf("bomb: %v decoded %d", b.Stops, b.Decoded)
	}

	// cumulative: each member is fine alone, together they are not
	pat := Random(5, 700)
	var mn []string
	mf := map[string][]byte{}
	for i := 0; i < 6; i++ {
		n := string([]byte{'g', byte('0' + i)})
		mn = append(mn, n)
		var body []byte
		for len(body) < 200<<10 {
			body = append(body, pat...)
			body = append(body, byte(i), byte(len(body)))
		}
		mf[n] = Gzip(body, 6)
	}
	tarb := Tar(mn, mf)
	b = DefaultBudget(4)
	b.MaxDecoded = 500 << 10
	root, err = Discover(tarb, b)
	if err != nil {
		t.Fatal(err)
	}
	if b.Accepted["gzip"] != 2 || b.Stops["decoded"] != 4 || b.Decoded > b.MaxDecoded+1 {
		t.Fatalf("cumulative: accepted %v stops %v decoded %d", b.Accepted, b.Stops, b.Decoded)
	}
	_, env, id = newStore(t, root)
	if err := Exact(env, id, tarb); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(tarb, data) {
		t.Fatal("unreachable")
	}
}

// TestDetectionIsNotValidity: magic bytes only propose a parser. Garbage that looks like each
// format is rejected and the payload stays an ordinary member.
func TestDetectionIsNotValidity(t *testing.T) {
	junk := Random(3, 8000)
	for name, magic := range map[string][]byte{"gzip": {0x1f, 0x8b, 8}, "zstd": {0x28, 0xb5, 0x2f, 0xfd}, "sqlite": []byte("SQLite format 3\x00"), "car": {0x3a, 0xa2}} {
		d := append(append([]byte(nil), junk...), 0)
		copy(d, magic)
		b := DefaultBudget(4)
		root, err := Discover(d, b)
		if err != nil {
			t.Fatal(err)
		}
		if !IsPlainCDC(root) || len(b.Accepted) != 0 {
			t.Fatalf("%s: accepted %v", name, b.Accepted)
		}
		_, env, id := newStore(t, root)
		if err := Exact(env, id, d); err != nil {
			t.Fatal(err)
		}
	}
}
