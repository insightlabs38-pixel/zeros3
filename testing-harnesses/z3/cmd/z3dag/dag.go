package main

import (
	"bytes"
	"fmt"

	"zeros3-z3/dagz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

type dagRow struct {
	name                       string
	depth, roots, reps, shared int
	exact                      bool
	unexplained                int
	union, sum                 int
	unionB, sumB               int64
	edges                      int
}

func measure(name string, st *repz3.MemStore, roots []repz3.SHA, wants [][]byte) dagRow {
	env := kinds.NewEnvLimits(st, dagz3.Lim)
	g, err := dagz3.LoadGraph(env, roots...)
	must(err)
	_, rs, err := dagz3.Union(st, env, roots)
	must(err)
	_, bad, err := dagz3.ReadSet(st, env, roots, wants)
	must(err)
	r := dagRow{name: name, roots: len(roots), reps: len(g.Reps), shared: len(g.Shared()), exact: err == nil, unexplained: len(bad), union: rs.Union, sum: rs.Sum, unionB: rs.UnionBytes, sumB: rs.SumBytes, edges: g.Edges()}
	for _, id := range roots {
		d := heightOf(g, id)
		r.depth = max(r.depth, d)
	}
	return r
}

func heightOf(g *dagz3.Graph, id repz3.SHA) int {
	h := 1
	for _, m := range g.Reps[id].Members {
		if m.Role == repz3.Derived {
			h = max(h, 1+heightOf(g, m.Child))
		}
	}
	return h
}

func persist(st *repz3.MemStore, b *kinds.Bundle) repz3.SHA {
	id, _, err := b.Persist(st)
	must(err)
	return id
}

const gitSHA1 = gitz3.SHA1

func dagSection() {
	var rows []dagRow
	// depth-4 A and B (cap 4 discovery over the real mechanisms)
	for _, w := range []struct {
		name string
		data []byte
	}{{"depth-4 A zstd>tar>gzip>sqlite", dagz3.ChainA(0, 160, 6)}, {"depth-4 B gzip>tar>zstd>car", dagz3.ChainB(0)}} {
		b, err := dagz3.Discover(w.data, dagz3.DefaultBudget(4))
		must(err)
		st := repz3.NewMemStore()
		rows = append(rows, measure(w.name, st, []repz3.SHA{persist(st, b)}, [][]byte{w.data}))
	}
	// fan-out
	fan, _, _ := dagz3.FanOut()
	b, err := dagz3.Discover(fan, dagz3.DefaultBudget(4))
	must(err)
	st := repz3.NewMemStore()
	rows = append(rows, measure("fan-out (8 members)", st, []repz3.SHA{persist(st, b)}, [][]byte{fan}))

	// shared roots: three roots (two tars, one loose) share one SQLite child
	db := dagz3.Join(dagz3.DB(62, 96))
	st = repz3.NewMemStore()
	var roots []repz3.SHA
	var wants [][]byte
	for _, conf := range []string{"a.conf", "b.conf"} {
		t := dagz3.Tar([]string{conf, "db/main.sqlite"}, map[string][]byte{conf: []byte("owner=" + conf + "\n"), "db/main.sqlite": db})
		bb, err := dagz3.Discover(t, dagz3.DefaultBudget(2))
		must(err)
		roots, wants = append(roots, persist(st, bb)), append(wants, t)
	}
	loose, err := dagz3.Discover(db, dagz3.DefaultBudget(1))
	must(err)
	roots, wants = append(roots, persist(st, loose)), append(wants, db)
	rows = append(rows, measure("shared roots A,B + loose C", st, roots, wants))

	// natural graph: Merkle revisions + a wrapper of revision 1, a reordered CAR family, a Git repack pair
	ch, err := kinds.MerkleChain(merklez3.HashSHA3_256, 2000)
	must(err)
	st = repz3.NewMemStore()
	cat := kinds.NewCatalog(st)
	roots, wants = nil, nil
	var mbs []*kinds.Bundle
	for i := range ch.Stages {
		mb, err := ch.Encode(st, cat, ch.Stages[i].Data)
		must(err)
		id, _, err := kinds.Commit(st, cat, mb)
		must(err)
		roots, wants, mbs = append(roots, id), append(wants, ch.Stages[i].Data), append(mbs, mb)
	}
	w, wout, err := dagz3.Wrap(mbs[1], ch.Stages[1].Data, repz3.Frame{Prefix: []byte("W:")}, []byte("wrapper"))
	must(err)
	roots, wants = append(roots, persist(st, w)), append(wants, wout)
	rows = append(rows, measure("graph: Merkle r0,r1,r2 + wrapper", st, roots, wants))

	cars, err := kinds.CARChains(kinds.Small.CAR)[0], error(nil)
	must(err)
	st = repz3.NewMemStore()
	roots, wants = nil, nil
	for _, stg := range cars.Stages {
		cb, err := cars.Encode(st, nil, stg.Data)
		must(err)
		roots, wants = append(roots, persist(st, cb)), append(wants, stg.Data)
	}
	rows = append(rows, measure("graph: CAR reorder x"+fmt.Sprint(len(cars.Stages)), st, roots, wants))

	gc, err := kinds.GitFixtureChain(gitSHA1)
	must(err)
	st = repz3.NewMemStore()
	roots, wants = nil, nil
	for _, stg := range gc.Stages {
		gb, err := gc.Encode(st, nil, stg.Data)
		must(err)
		roots, wants = append(roots, persist(st, gb)), append(wants, stg.Data)
	}
	rows = append(rows, measure("graph: Git ofs + ref repack", st, roots, wants))

	// cross-format shared child: one SQLite file as tar.gz, tar.zst and loose
	x := dagz3.Join(dagz3.DB(91, 96))
	tarb := dagz3.Tar([]string{"x.db"}, map[string][]byte{"x.db": x})
	st = repz3.NewMemStore()
	roots, wants = nil, nil
	for _, d := range [][]byte{dagz3.Gzip(tarb, 6), dagz3.Zstd(tarb), x} {
		bb, err := dagz3.Discover(d, dagz3.DefaultBudget(4))
		must(err)
		roots, wants = append(roots, persist(st, bb)), append(wants, d)
	}
	rows = append(rows, measure("cross-format tar.gz, tar.zst, loose", st, roots, wants))

	fmt.Printf("%-38s %5s %5s %5s %6s %5s %6s %9s %9s %s\n", "workload", "depth", "roots", "reps", "shared", "edges", "exact", "union obj", "sum obj", "read⊈reach gap")
	for _, r := range rows {
		fmt.Printf("%-38s %5d %5d %5d %6d %5d %6v %4d/%-4d %9d %d  (union %d B, sum %d B)\n", r.name, r.depth, r.roots, r.reps, r.shared, r.edges, r.exact, r.union, r.sum, r.sum, r.unexplained, r.unionB, r.sumB)
	}
	_ = bytes.Equal
}
