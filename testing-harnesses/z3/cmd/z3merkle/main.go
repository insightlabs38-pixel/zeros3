// Command z3merkle runs the Z3-15 Merkle-guided revision measurements.
//
//	z3merkle sparse   [-n KEYS]            work and storage vs fraction of keys changed
//	z3merkle mutate   [-n KEYS]            mutation catalogue (insert/remove/cluster/subtree/layout/...)
//	z3merkle fork     [-n KEYS]            revision graph A->B->C->D, B->E->F (+ merge)
//	z3merkle hashes   [-n KEYS]            same engine under four native hashes
//	z3merkle scale    [-nodes ...]         disk-backed traversal: memory and time vs node count
//	z3merkle partial  [-n KEYS]            component / subtree / whole-snapshot reads
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/merklez3"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3merkle:", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3merkle sparse|mutate|fork|hashes|scale|partial [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "sparse":
		err = cmdSparse(args)
	case "mutate":
		err = cmdMutate(args)
	case "fork":
		err = cmdFork(args)
	case "hashes":
		err = cmdHashes(args)
	case "scale":
		err = cmdScale(args)
	case "partial":
		err = cmdPartial(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fatal(err)
	}
}

func kib(n int64) string { return fmt.Sprintf("%.1f", float64(n)/1024) }

func snapshot(ms *merklez3.MapStore, root merklez3.ID) []byte {
	var b bytes.Buffer
	if _, err := merklez3.Snapshot(ms, root, &b); err != nil {
		fatal(err)
	}
	return b.Bytes()
}

type revision struct {
	label string
	state *merklez3.State
	cfg   merklez3.Config
}

// build materialises every revision's snapshot into one shared MapStore and returns the byte chain.
func build(revs []revision) (ms *merklez3.MapStore, roots []merklez3.ID, stats []merklez3.BuildStats, chain []lab.NamedBytes) {
	ms = merklez3.NewMapStore()
	for _, r := range revs {
		root, st := r.state.BuildInto(r.cfg, ms)
		roots = append(roots, root)
		stats = append(stats, st)
		chain = append(chain, lab.NamedBytes{Label: r.label, Data: snapshot(ms, root)})
	}
	return
}

var repCols = []string{"cdc", "merkle-flat", "merkle-guided"}

func rows(family string, h merklez3.HashFn, chain []lab.NamedBytes) map[string][]lab.FRow {
	out := map[string][]lab.FRow{}
	for _, r := range lab.RunBytesFamily(family, "merkle", chain, lab.MerkleReps(h)) {
		if r.Status == "ok" && !r.Exact || r.Status == "invalid" || r.Status == "error" {
			fatal(fmt.Errorf("%s %s %s: %s %s", family, r.Rep, r.Label, r.Status, r.Err))
		}
		out[r.Rep] = append(out[r.Rep], r)
	}
	return out
}

func pct(c, s int64) string { return fmt.Sprintf("%.2f%%", 100*float64(c)/float64(s)) }

func keysFor(s *merklez3.State, mode string, frac float64) []uint64 {
	n := max(1, int(float64(len(s.Keys))*frac))
	if mode == "clustered" {
		return s.PickClustered(11, n)
	}
	return s.PickScattered(11, n)
}

func cmdSparse(args []string) error {
	fs := flag.NewFlagSet("sparse", flag.ExitOnError)
	n := fs.Int("n", 100000, "keys")
	fs.Parse(args)
	cfg := merklez3.DefaultConfig()
	base := merklez3.NewState(1, *n)
	fmt.Printf("state: %d keys, SHA3-256 native ids, leaf <= %d entries\n", *n, cfg.LeafMax)
	fmt.Printf("\n| change | mode | target nodes | new nodes | guided: nodes read / hash compares | full scan: nodes read | aligned diff: nodes read | physical cost %% of snapshot: cdc | flat | guided |\n|---|---|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, mode := range []string{"scattered", "clustered"} {
		for _, frac := range []float64{0.00001, 0.0001, 0.001, 0.01, 0.1, 0.5, 1.0} {
			t := base.Update(keysFor(base, mode, frac))
			ms, roots, bst, chain := build([]revision{{"base", base, cfg}, {"target", t, cfg}})
			rs := rows("sparse", cfg.Hash, chain)
			known := map[merklez3.ID]bool{}
			merklez3.Walk(ms, roots[0], func(nd merklez3.Node, _ int) error { known[nd.ID] = true; return nil })
			d, _ := merklez3.Discover(ms, roots[1], func(id merklez3.ID) bool { return known[id] }, nil, nil)
			a, _ := merklez3.DiffAligned(ms, ms, roots[0], roots[1], nil, nil)
			size := int64(len(chain[1].Data))
			fmt.Printf("| %.3g%% | %s | %d | %d | %d / %d | %d | %d | %s | %s | %s |\n", frac*100, mode, bst[1].Nodes, d.NewNodes, d.NodesRead, d.HashCompares, bst[1].Nodes, a.NodesRead,
				pct(rs["cdc"][1].Cost, size), pct(rs["merkle-flat"][1].Cost, size), pct(rs["merkle-guided"][1].Cost, size))
			if mode == "clustered" && frac >= 0.0001 {
				continue
			}
		}
	}
	return nil
}

func cmdMutate(args []string) error {
	fs := flag.NewFlagSet("mutate", flag.ExitOnError)
	n := fs.Int("n", 100000, "keys")
	fs.Parse(args)
	cfg := merklez3.DefaultConfig()
	base := merklez3.NewState(1, *n)
	lay := cfg
	lay.Layout = 1
	small := cfg
	small.LeafMax = 6
	type m struct {
		name string
		t    *merklez3.State
		cfg  merklez3.Config
	}
	muts := []m{
		{"identical rewrite", base, cfg},
		{"1 leaf value changed", base.Update(base.PickScattered(2, 1)), cfg},
		{"10 clustered keys changed", base.Update(base.PickClustered(3, 10)), cfg},
		{"10 scattered keys changed", base.Update(base.PickScattered(4, 10)), cfg},
		{"insert 10 keys", base.Insert(base.FreshKeys(5, 10)), cfg},
		{"remove 10 keys", base.Remove(base.PickScattered(6, 10)), cfg},
		{"subtree replaced (1/16 of keys)", base.Update(base.KeysUnder(5, 1)), cfg},
		{"subtree replaced (1/256 of keys)", base.Update(base.KeysUnder(0x5a, 2)), cfg},
		{"layout-only rewrite (encoding byte)", base, lay},
		{"rebalance (leaf capacity 8 -> 6)", base, small},
	}
	fmt.Printf("| mutation | target nodes | new nodes | new %% | guided reads | cdc | merkle-flat | merkle-guided | (cost %% of snapshot) |\n|---|--:|--:|--:|--:|--:|--:|--:|--|\n")
	for _, mu := range muts {
		ms, roots, bst, chain := build([]revision{{"base", base, cfg}, {"target", mu.t, mu.cfg}})
		rs := rows("mut", cfg.Hash, chain)
		known := map[merklez3.ID]bool{}
		merklez3.Walk(ms, roots[0], func(nd merklez3.Node, _ int) error { known[nd.ID] = true; return nil })
		d, _ := merklez3.Discover(ms, roots[1], func(id merklez3.ID) bool { return known[id] }, nil, nil)
		size := int64(len(chain[1].Data))
		fmt.Printf("| %s | %d | %d | %.2f%% | %d | %s | %s | %s | |\n", mu.name, bst[1].Nodes, d.NewNodes, 100*float64(d.NewNodes)/float64(bst[1].Nodes), d.NodesRead,
			pct(rs["cdc"][1].Cost, size), pct(rs["merkle-flat"][1].Cost, size), pct(rs["merkle-guided"][1].Cost, size))
	}
	return nil
}

func cmdHashes(args []string) error {
	fs := flag.NewFlagSet("hashes", flag.ExitOnError)
	n := fs.Int("n", 50000, "keys")
	fs.Parse(args)
	fmt.Printf("| native hash | id bytes | nodes | new nodes (1%% scattered) | guided reads | compares | guided cost %% | native index bytes |\n|---|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, h := range []merklez3.HashFn{merklez3.HashSHA3_256, merklez3.HashSHA512_256, merklez3.HashSHA256, merklez3.HashShort16} {
		cfg := merklez3.Config{Hash: h, LeafMax: 8}
		base := merklez3.NewState(1, *n)
		t := base.Update(keysFor(base, "scattered", 0.01))
		ms, roots, bst, chain := build([]revision{{"base", base, cfg}, {"target", t, cfg}})
		rs := rows("h", h, chain)
		known := map[merklez3.ID]bool{}
		merklez3.Walk(ms, roots[0], func(nd merklez3.Node, _ int) error { known[nd.ID] = true; return nil })
		d, _ := merklez3.Discover(ms, roots[1], func(id merklez3.ID) bool { return known[id] }, nil, merklez3.VerifyWith(h))
		size := int64(len(chain[1].Data))
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %s | %d |\n", h.Name, len(roots[0]), bst[1].Nodes, d.NewNodes, d.NodesRead, d.HashCompares, pct(rs["merkle-guided"][1].Cost, size), rs["merkle-guided"][1].Acct.IndexBytes)
	}
	return nil
}

var _ = sort.Strings
var _ = strings.Join
