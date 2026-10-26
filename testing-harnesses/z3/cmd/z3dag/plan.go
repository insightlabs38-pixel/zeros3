package main

import (
	"fmt"
	"time"

	"zeros3-z3/dagz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func planSection() {
	fmt.Println("-- adversarial fixtures (total cost of the chosen union)")
	fmt.Printf("%-34s %11s %9s %13s %8s %6s\n", "fixture", "independent", "marginal", "marginal+swap", "optimum", "gap")
	fx := dagz3.Fixtures()
	for _, name := range dagz3.SortedKeys(fx) {
		p := fx[name]
		i, _ := p.Independent()
		m, _ := p.Marginal()
		r, _ := p.Refine()
		e, err := p.Exact(0)
		must(err)
		fmt.Printf("%-34s %11d %9d %13d %8d %5.0f%%\n", name, i.Total, m.Total, r.Total, e.Total, 100*float64(min(i.Total, m.Total, r.Total)-e.Total)/float64(e.Total))
	}

	fmt.Println("-- invalid candidates are rejected, never repaired")
	for _, c := range []string{"unknown child", "output mismatch", "cycle", "no candidate", "overflow"} {
		fmt.Printf("   %-16s -> %v\n", c, rejectDemo(c))
	}

	fmt.Println("-- synthetic scaling: n objects x 3 candidates, shared pool of n/3 children")
	fmt.Printf("%4s %12s %12s %12s %12s %9s %9s %9s  %s\n", "n", "independent", "marginal", "marg+swap", "exact", "ind/opt", "mar/opt", "swp/opt", "exact search")
	for _, n := range []int{8, 16, 32, 64, 128} {
		p := dagz3.Synthetic(n, 28)
		t0 := time.Now()
		i, _ := p.Independent()
		ti := time.Since(t0)
		t0 = time.Now()
		m, _ := p.Marginal()
		tm := time.Since(t0)
		t0 = time.Now()
		r, _ := p.Refine()
		tr := time.Since(t0)
		t0 = time.Now()
		e, err := p.Exact(30_000_000)
		must(err)
		te := time.Since(t0)
		state := fmt.Sprintf("%v, %d nodes", te.Round(time.Millisecond), e.Nodes)
		if !e.Complete {
			state = "CAPPED at " + state + " (best found)"
		}
		fmt.Printf("%4d %12d %12d %12d %12d %8.3fx %8.3fx %8.3fx  %s   [greedy %v/%v/%v]\n", n, i.Total, m.Total, r.Total, e.Total,
			float64(i.Total)/float64(e.Total), float64(m.Total)/float64(e.Total), float64(r.Total)/float64(e.Total), state,
			ti.Round(time.Microsecond), tm.Round(time.Microsecond), tr.Round(time.Microsecond))
	}

	fmt.Println("-- historical chains: cumulative persisted bytes under each planning policy")
	fmt.Printf("%-34s %12s %12s %12s %8s\n", "chain", "independent", "online", "hindsight", "chosen-by-hindsight")
	for _, w := range workloads() {
		if len(w.versions) < 3 {
			continue
		}
		sets, order, datas := candidateSets(w)
		ind, onl, hind, err := dagz3.Hindsight(sets, order, datas, 0)
		must(err)
		p := dagz3.Assemble(sets, order, datas, nil, 0)
		e, err := p.Exact(5_000_000)
		must(err)
		fmt.Printf("%-34s %12d %12d %12d   %v\n", w.name, ind, onl, hind, chosenCaps(e, order))
	}

	storeSensitivity()
	mixedWinner()
	overheadSweep()
	readAware()
}

func rejectDemo(kind string) error {
	f := dagz3.NewFB(0).Object("A")
	f.Cand("A", "a1", "a1", 5, nil, map[string]int64{"x": 10})
	switch kind {
	case "unknown child":
		c := &f.P.Cands[0]
		c.Reps[c.Root] = []repz3.SHA{{9}}
	case "output mismatch":
		f.P.Cands[0].Out = repz3.SHA{7}
	case "cycle":
		c := &f.P.Cands[0]
		k := repz3.SHA{5}
		c.Objs[k] = 1
		c.Reps[c.Root] = []repz3.SHA{k}
		c.Reps[k] = []repz3.SHA{c.Root}
	case "no candidate":
		f.Object("B")
	case "overflow":
		f.P.Cands[0].Objs[repz3.SHA{6}] = 1<<62 + 1<<61
		f.P.Cands[0].Objs[repz3.SHA{7}] = 1<<62 + 1<<61
	}
	_, err := f.P.Exact(0)
	return err
}

func candidateSets(w workload) (map[string]*dagz3.CandSet, []string, map[string][]byte) {
	sets, datas := map[string]*dagz3.CandSet{}, map[string][]byte{}
	var order []string
	for i, v := range w.versions {
		o := fmt.Sprintf("v%d", i)
		cs, err := dagz3.Candidates(o, v, caps)
		must(err)
		sets[o], datas[o] = cs, v
		order = append(order, o)
	}
	return sets, order, datas
}

func chosenCaps(p *dagz3.Plan, order []string) string {
	s := ""
	for _, o := range order {
		id := p.Choice[o]
		s += id[len(id)-2:] + " "
	}
	return s
}

// storeSensitivity: the same object planned against four different existing stores.
func storeSensitivity() {
	fmt.Println("-- existing-store sensitivity: version 3 of chain A (zstd>tar>gzip>sqlite) against four stores")
	var vs [][]byte
	for r := 0; r < 4; r++ {
		vs = append(vs, dagz3.ChainA(r, 160, 6))
	}
	cs, err := dagz3.Candidates("v3", vs[3], caps)
	must(err)
	sets := map[string]*dagz3.CandSet{"v3": cs}
	build := func(name string, put func(st *repz3.MemStore)) {
		st := repz3.NewMemStore()
		put(st)
		p := dagz3.Assemble(sets, []string{"v3"}, map[string][]byte{"v3": vs[3]}, st, 0)
		e, err := p.Exact(0)
		must(err)
		i, _ := p.Independent()
		fmt.Printf("   %-46s store %4d objs  -> %-6s cost %8d B  (every candidate:", name, st.Len(), e.Choice["v3"][3:], e.Total)
		for _, c := range cs.Cands {
			q := dagz3.Assemble(sets, []string{"v3"}, map[string][]byte{"v3": vs[3]}, st, 0)
			q.Cands = []dagz3.Cand{c}
			pl, _ := q.Independent()
			fmt.Printf(" %s=%d", c.ID[3:], pl.Total)
		}
		fmt.Printf(")  indep=%s\n", i.Choice["v3"][3:])
	}
	build("empty", func(*repz3.MemStore) {})
	build("earlier revisions, stored as CDC", func(st *repz3.MemStore) {
		for r := 0; r < 3; r++ {
			b, err := dagz3.Discover(vs[r], dagz3.DefaultBudget(0))
			must(err)
			b.Persist(st)
		}
	})
	build("earlier revisions, stored at depth 4", func(st *repz3.MemStore) {
		for r := 0; r < 3; r++ {
			b, err := dagz3.Discover(vs[r], dagz3.DefaultBudget(4))
			must(err)
			b.Persist(st)
		}
	})
	build("an unrelated top-level holding the database", func(st *repz3.MemStore) {
		pg := dagz3.DB(28, 160)
		for k := 1; k <= 3; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 6)
		}
		b, err := dagz3.Discover(dagz3.Join(pg), dagz3.DefaultBudget(1))
		must(err)
		b.Persist(st)
	})
}

// mixedWinner: one outer archive whose members want different treatments, plus a second root that
// shares a child with one of them.
func mixedWinner() {
	fmt.Println("-- mixed winner: one tar, members m1 (db.gz), m2 (random.gz), m3 (db), m4 (blob); store holds the previous revision")
	dbPrev := dagz3.DB(28, 96)
	prevM3 := dagz3.Join(dbPrev)
	blob := dagz3.Random(33, 64<<10)
	rnd0 := dagz3.Gzip(dagz3.Random(35, 200<<10), 6)
	mk := func(m1db [][]byte, m2 []byte, m3 []byte, m4 []byte) []byte {
		return dagz3.Tar([]string{"m1.db.gz", "m2.rnd.gz", "m3.db", "m4.bin"}, map[string][]byte{
			"m1.db.gz": dagz3.Gzip(dagz3.Join(m1db), 6), "m2.rnd.gz": m2, "m3.db": m3, "m4.bin": m4})
	}
	prev := mk(dbPrev, rnd0, prevM3, blob)
	blob2 := append([]byte(nil), blob...)
	copy(blob2[20000:], dagz3.Random(37, 1024))
	next := mk(dagz3.Mutate(dbPrev, 103, 5), dagz3.Gzip(dagz3.Random(39, 200<<10), 6), dagz3.Join(dagz3.Mutate(dbPrev, 105, 3)), blob2)

	st := repz3.NewMemStore()
	pb, err := dagz3.Discover(prev, dagz3.DefaultBudget(4))
	must(err)
	pb.Persist(st)

	opts := [][]int{{0, 2}, {0, 2}, {0, 1}} // levels below the tar for m1, m2, m3; m4 is raw
	set := &dagz3.CandSet{Bundles: map[string]*kinds.Bundle{}}
	for mask := 0; mask < 8; mask++ {
		lv := [4]int{opts[0][mask&1], opts[1][mask>>1&1], opts[2][mask>>2&1], 0}
		bud := dagz3.DefaultBudget(4)
		bud.MemberCap = func(i int) int { return lv[i] }
		b, err := dagz3.Discover(next, bud)
		must(err)
		id := fmt.Sprintf("tar/m1=%d,m2=%d,m3=%d", lv[0], lv[1], lv[2])
		c, err := dagz3.CandFromBundle(id, "tar", b, next)
		must(err)
		set.Cands, set.Bundles[id] = append(set.Cands, c), b
	}
	// a second top-level root: the same revision of m3's database, loose
	loose, err := dagz3.Discover(dagz3.Join(dagz3.Mutate(dbPrev, 105, 3)), dagz3.DefaultBudget(1))
	must(err)
	lc, err := dagz3.CandFromBundle("loose/sqlite", "loose", loose, dagz3.Join(dagz3.Mutate(dbPrev, 105, 3)))
	must(err)
	lcdc, err := dagz3.Discover(dagz3.Join(dagz3.Mutate(dbPrev, 105, 3)), dagz3.DefaultBudget(0))
	must(err)
	lc0, err := dagz3.CandFromBundle("loose/cdc", "loose", lcdc, dagz3.Join(dagz3.Mutate(dbPrev, 105, 3)))
	must(err)
	sets := map[string]*dagz3.CandSet{"tar": set, "loose": {Cands: []dagz3.Cand{lc0, lc}, Bundles: map[string]*kinds.Bundle{"loose/cdc": lcdc, "loose/sqlite": loose}}}
	datas := map[string][]byte{"tar": next, "loose": dagz3.Join(dagz3.Mutate(dbPrev, 105, 3))}

	single := dagz3.Assemble(sets, []string{"tar"}, datas, st, 0)
	fmt.Printf("   %-26s %10s\n", "candidate for the tar", "new bytes")
	for _, c := range set.Cands {
		q := dagz3.Assemble(sets, []string{"tar"}, datas, st, 0)
		q.Cands = []dagz3.Cand{c}
		pl, err := q.Independent()
		must(err)
		fmt.Printf("   %-26s %10d\n", c.ID[4:], pl.Total)
	}
	e, _ := single.Exact(0)
	fmt.Printf("   best single-object plan: %s (%d B)\n", e.Choice["tar"], e.Total)
	both := dagz3.Assemble(sets, []string{"tar", "loose"}, datas, st, 0)
	i, _ := both.Independent()
	m, _ := both.Marginal()
	x, _ := both.Exact(0)
	fmt.Printf("   with the loose root:  independent %d (%s, %s)  marginal %d  exact %d (%s, %s)\n", i.Total, i.Choice["tar"][4:], i.Choice["loose"][6:], m.Total, x.Total, x.Choice["tar"][4:], x.Choice["loose"][6:])
	b, objs, err := dagz3.Apply(st.Clone(), sets, x, []string{"tar", "loose"})
	must(err)
	fmt.Printf("   plan applied to the store: %d B in %d objects (plan said %d B)\n", b, objs, x.Total)
}

// overheadSweep: many tiny representation nodes are cheap on paper and costly physically.
func overheadSweep() {
	fmt.Println("-- per-object overhead: winner among CDC/d1..d4 over a 6-revision chain (2 pages change per revision), by database size")
	fmt.Printf("   %-6s %-8s", "pages", "objects")
	pers := []int64{0, 32, 64, 128, 256, 512, 1024}
	for _, p := range pers {
		fmt.Printf(" %6d", p)
	}
	fmt.Println("   (overhead B/object -> winning policy)")
	for _, pages := range []int{24, 40, 64, 96, 160} {
		var vs [][]byte
		pg := dagz3.DB(41, pages)
		for r := 0; r < 6; r++ {
			if r > 0 {
				pg = dagz3.Mutate(pg, uint64(2*r+111), 2)
			}
			vs = append(vs, dagz3.Gzip(dagz3.Tar([]string{"a.db"}, map[string][]byte{"a.db": dagz3.Join(pg)}), 6))
		}
		curves, err := dagz3.LevelCurve(vs, caps)
		must(err)
		objs := ""
		for _, c := range curves {
			n := 0
			for _, o := range c.Objects {
				n += o
			}
			objs += fmt.Sprintf("%d/", n)
		}
		fmt.Printf("   %-6d %-8s", pages, objs[:len(objs)-1])
		for _, per := range pers {
			best, bk := int64(1)<<62, 0
			for _, c := range curves {
				if t := c.Total(per); t < best {
					best, bk = t, c.Cap
				}
			}
			w := fmt.Sprintf("d%d", bk)
			if bk == 0 {
				w = "CDC"
			}
			fmt.Printf(" %6s", w)
		}
		fmt.Println()
	}
}

// readAware: storage-only planning against a point-read term.
func readAware() {
	fmt.Println("-- read-aware objective: storage + lambda * bytes fetched by one nested page read")
	db := dagz3.Join(dagz3.DB(28, 160))
	want := db[readPage*4096 : (readPage+1)*4096]
	var vs [][]byte
	for r := 0; r < 4; r++ {
		vs = append(vs, dagz3.ChainA(r, 160, 6))
	}
	_ = want
	for _, ver := range []int{0, 3} {
		cs, err := dagz3.Candidates("v", vs[ver], caps)
		must(err)
		pg := dagz3.DB(28, 160)
		for k := 1; k <= ver; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 6)
		}
		dbv := dagz3.Join(pg)
		w := dbv[readPage*4096 : (readPage+1)*4096]
		for i := range cs.Cands {
			st := repz3.NewMemStore()
			id, _, err := cs.Bundles[cs.Cands[i].ID].Persist(st)
			must(err)
			rc, err := dagz3.PointReadPage(st, kinds.NewEnvLimits(st, dagz3.Lim), id, int64(len(dbv)), readPage, w)
			must(err)
			cs.Cands[i].Read = rc.Bytes
		}
		var base *repz3.MemStore
		if ver > 0 {
			base = repz3.NewMemStore()
			for r := 0; r < ver; r++ {
				b, err := dagz3.Discover(vs[r], dagz3.DefaultBudget(4))
				must(err)
				b.Persist(base)
			}
		}
		for _, lam := range []int64{0, 100, 1000, 2000, 4000} {
			p := dagz3.Assemble(map[string]*dagz3.CandSet{"v": cs}, []string{"v"}, map[string][]byte{"v": vs[ver]}, base, 0)
			p.ReadPer1000 = lam
			e, err := p.Exact(0)
			must(err)
			fmt.Printf("   version %d  lambda %4.2f  -> %s  storage %8d B  read %8d B (objective %d)\n", ver, float64(lam)/1000, e.Choice["v"][2:], e.Total-e.Read, e.Read*1000/max(lam, 1), e.Total)
		}
		fmt.Printf("   version %d candidates:", ver)
		for _, c := range cs.Cands {
			fmt.Printf(" %s read=%d", c.ID[2:], c.Read)
		}
		fmt.Println()
	}
}
