package main

import (
	"flag"
	"fmt"

	"zeros3-z3/lab"
	"zeros3-z3/merklez3"
)

func cmdFork(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ExitOnError)
	n := fs.Int("n", 100000, "keys")
	fs.Parse(args)
	cfg := merklez3.DefaultConfig()
	A := merklez3.NewState(1, *n)
	B := A.Update(A.PickScattered(21, *n/100))
	C := B.Update(B.PickClustered(22, max(1, *n/1000)))
	D := C.Insert(C.FreshKeys(23, 10))
	E := B.Remove(B.PickScattered(24, 20))
	F := E.Update(E.KeysUnder(0x5a, 2))
	ms := merklez3.NewMapStore()
	var chain []lab.NamedBytes
	var sizes []merklez3.BuildStats
	roots := map[string]merklez3.ID{}
	for _, r := range []struct {
		n string
		s *merklez3.State
	}{{"A", A}, {"B (A+1% scattered)", B}, {"C (B+0.1% clustered)", C}, {"D (C+10 inserts)", D}, {"E (B-20 removes) fork", E}, {"F (E+subtree 1/256)", F}} {
		root, st := r.s.BuildInto(cfg, ms)
		roots[r.n[:1]] = root
		sizes = append(sizes, st)
		chain = append(chain, lab.NamedBytes{Label: r.n, Data: snapshot(ms, root)})
	}
	g, err := merklez3.MergeRoots(cfg, ms, roots["D"], roots["F"], 8)
	if err != nil {
		return err
	}
	chain = append(chain, lab.NamedBytes{Label: "G (merge: D slots 0-7, F slots 8-15)", Data: snapshot(ms, g)})
	rs := rows("fork", cfg.Hash, chain)
	var raw int64
	for _, c := range chain {
		raw += int64(len(c.Data))
	}
	fmt.Printf("%d keys; history A->B->C->D, B->E->F, G merges D and F. Snapshot sizes: ", *n)
	for _, c := range chain {
		fmt.Printf("%s %s KiB; ", c.Label[:1], kib(int64(len(c.Data))))
	}
	fmt.Printf("\n\n| revision | size KiB | cdc | merkle-flat | merkle-guided | guided: new nodes / compares / reads |\n|---|--:|--:|--:|--:|--|\n")
	for i, c := range chain {
		g := rs["merkle-guided"][i]
		d := g.Detail["discover"].(merklez3.Stats)
		fmt.Printf("| %s | %s | %s (%s) | %s (%s) | %s (%s) | %d / %d / %d |\n", c.Label, kib(int64(len(c.Data))),
			kib(rs["cdc"][i].Cost), pct(rs["cdc"][i].Cost, int64(len(c.Data))), kib(rs["merkle-flat"][i].Cost), pct(rs["merkle-flat"][i].Cost, int64(len(c.Data))),
			kib(g.Cost), pct(g.Cost, int64(len(c.Data))), d.NewNodes, d.HashCompares, d.NodesRead)
	}
	fmt.Printf("\n| storage for all %d snapshots | KiB | %% of raw |\n|---|--:|--:|\n| each snapshot saved raw | %s | 100%% |\n", len(chain), kib(raw))
	for _, rep := range []string{"cdc", "merkle-flat", "merkle-guided"} {
		var c int64
		for _, r := range rs[rep] {
			c += r.Cost
		}
		fmt.Printf("| %s | %s | %s |\n", rep, kib(c), pct(c, raw))
	}
	// unique content floor: bytes of the distinct nodes of all revisions
	uniq := map[merklez3.ID]int{}
	for _, root := range append(func() []merklez3.ID {
		var o []merklez3.ID
		for _, k := range []string{"A", "B", "C", "D", "E", "F"} {
			o = append(o, roots[k])
		}
		return o
	}(), g) {
		merklez3.Walk(ms, root, func(nd merklez3.Node, _ int) error { uniq[nd.ID] = len(nd.Payload); return nil })
	}
	var ub int64
	for _, l := range uniq {
		ub += int64(l)
	}
	fmt.Printf("| unique node bytes across all revisions (floor, no recipes/index) | %s | %s |\n", kib(ub), pct(ub, raw))
	return nil
}
