package graphcore

import (
	"math/rand"
	"reflect"
	"testing"
)

// randGraph builds a graph that exercises every identity case: out/set edges, duplicates,
// cycles (including rings with automorphisms and self loops), dangling references, opaque
// and unknown nodes.
func rgFull(r *rand.Rand, n int, cyc, dang bool) *Graph {
	g := &Graph{}
	for i := 0; i < n; i++ {
		nd := Node{Class: uint32(r.Intn(4)), Lit: []byte{byte(r.Intn(3))}}
		if r.Intn(40) == 0 {
			nd.Flags |= FlagOpaque
		}
		if dang && r.Intn(60) == 0 {
			nd.Flags |= FlagUnknown
		}
		pick := func() int32 {
			if cyc && r.Intn(5) == 0 {
				return int32(r.Intn(n)) // may point forward: cycles
			}
			if i == 0 {
				return 0
			}
			return int32(r.Intn(i))
		}
		for k := r.Intn(3); k > 0; k-- {
			nd.Out = append(nd.Out, Edge{Kind: uint16(r.Intn(3)), To: pick()})
		}
		for k := r.Intn(3); k > 0; k-- {
			nd.Set = append(nd.Set, Edge{Kind: uint16(r.Intn(2)), To: pick()})
		}
		if dang && r.Intn(50) == 0 {
			nd.Out = append(nd.Out, Edge{Kind: 1, To: int32(n + r.Intn(5))})
		}
		if dang && r.Intn(80) == 0 {
			nd.Set = append(nd.Set, Edge{Kind: 1, To: -1})
		}
		g.Nodes = append(g.Nodes, nd)
	}
	if cyc && n >= 3 && r.Intn(2) == 0 { // a symmetric ring appended
		base := int32(len(g.Nodes))
		m := 3 + r.Intn(5)
		for i := 0; i < m; i++ {
			g.Nodes = append(g.Nodes, Node{Class: 9, Lit: []byte("ring"), Out: []Edge{{Kind: 0, To: base + int32((i+1)%m)}}})
		}
	}
	return g
}

func sameIdents(t *testing.T, tag string, a, b *Idents) {
	t.Helper()
	if !reflect.DeepEqual(a.ID, b.ID) || !reflect.DeepEqual(a.Ambig, b.Ambig) || !reflect.DeepEqual(a.Occ, b.Occ) ||
		a.Classes != b.Classes || a.SCCs != b.SCCs || a.Cyclic != b.Cyclic || a.Search != b.Search || a.Budget != b.Budget {
		t.Fatalf("%s: compact identities differ from the reference (classes %d/%d sccs %d/%d cyclic %d/%d search %d/%d)",
			tag, a.Classes, b.Classes, a.SCCs, b.SCCs, a.Cyclic, b.Cyclic, a.Search, b.Search)
	}
}

// The compact engine must reproduce the pre-P6 identities exactly, on Graph and on CGraph.
func TestCompactIdentitiesMatchReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for it := 0; it < 1500; it++ {
		n := 1 + r.Intn(120)
		g := rgFull(r, n, it%3 != 0, it%2 == 0)
		ref := g.refSCCIdentities()
		sameIdents(t, "graph", g.SCCIdentities(), ref)
		c, err := g.Compact()
		if err != nil {
			t.Fatal(err)
		}
		sameIdents(t, "cgraph", c.SCCIdentities(), ref)
		if !reflect.DeepEqual(c.Expand().Nodes, g.Nodes) && !equalNodes(c.Expand(), g) {
			t.Fatal("expand does not round-trip")
		}
	}
}

func equalNodes(a, b *Graph) bool {
	if len(a.Nodes) != len(b.Nodes) {
		return false
	}
	for i := range a.Nodes {
		x, y := a.Nodes[i], b.Nodes[i]
		if x.Class != y.Class || x.Flags != y.Flags || string(x.Lit) != string(y.Lit) || len(x.Out) != len(y.Out) || len(x.Set) != len(y.Set) {
			return false
		}
		for k := range x.Out {
			if x.Out[k] != y.Out[k] {
				return false
			}
		}
		for k := range x.Set {
			if x.Set[k] != y.Set[k] {
				return false
			}
		}
	}
	return true
}

// Larger cyclic components and repeated structures (budget paths included).
func TestCompactIdentitiesLarge(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for it := 0; it < 12; it++ {
		g := rgFull(r, 2000+r.Intn(3000), true, it%2 == 0)
		sameIdents(t, "large", g.SCCIdentities(), g.refSCCIdentities())
	}
}

// Identities are independent of node numbering (renumbering a compact graph keeps the
// multiset of identities).
func TestCompactStableUnderRenumbering(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for it := 0; it < 100; it++ {
		g := rgFull(r, 5+r.Intn(60), false, false)
		perm := r.Perm(len(g.Nodes))
		h := &Graph{Nodes: make([]Node, len(g.Nodes))}
		for old, nw := range perm {
			nd := g.Nodes[old]
			var out, set []Edge
			for _, e := range nd.Out {
				out = append(out, Edge{e.Kind, int32(perm[e.To])})
			}
			for _, e := range nd.Set {
				set = append(set, Edge{e.Kind, int32(perm[e.To])})
			}
			h.Nodes[nw] = Node{Class: nd.Class, Flags: nd.Flags, Lit: nd.Lit, Out: out, Set: set}
		}
		a, b := g.SCCIdentities(), h.SCCIdentities()
		for old, nw := range perm {
			if a.Ambig[old] == AmbigNone && a.ID[old] != b.ID[nw] {
				t.Fatalf("identity of node %d changed under renumbering", old)
			}
		}
	}
}

func TestCGraphLimitsAndBuilder(t *testing.T) {
	c := NewCGraph(0, 0)
	a, err := c.Add(1, 0, []byte("x"), nil, nil)
	if err != nil || a != 0 {
		t.Fatal(err)
	}
	b, _ := c.Add(2, 0, nil, []Edge{{Kind: 3, To: a}}, []Edge{{Kind: 4, To: a}})
	if n := c.Node(int(b)); len(n.Out) != 1 || len(n.Set) != 1 || n.Out[0].To != a {
		t.Fatal("builder lost edges")
	}
	ids := c.SCCIdentities()
	if ids.ID[0] == ids.ID[1] || len(ids.ID) != 2 {
		t.Fatal("bad identities")
	}
}

// perturb returns a renumbered copy of g with random edits (lit changes, deleted and inserted nodes).
func perturb(r *rand.Rand, g *Graph, edits int) *Graph {
	n := len(g.Nodes)
	perm := r.Perm(n)
	h := &Graph{Nodes: make([]Node, n)}
	for old, nw := range perm {
		nd := g.Nodes[old]
		var out, set []Edge
		for _, e := range nd.Out {
			t := e.To
			if t >= 0 && int(t) < n {
				t = int32(perm[t])
			}
			out = append(out, Edge{e.Kind, t})
		}
		for _, e := range nd.Set {
			t := e.To
			if t >= 0 && int(t) < n {
				t = int32(perm[t])
			}
			set = append(set, Edge{e.Kind, t})
		}
		h.Nodes[nw] = Node{Class: nd.Class, Flags: nd.Flags, Lit: append([]byte(nil), nd.Lit...), Out: out, Set: set}
	}
	for k := 0; k < edits && n > 0; k++ {
		i := r.Intn(n)
		switch r.Intn(3) {
		case 0:
			h.Nodes[i].Lit = []byte{byte(r.Intn(250))}
		case 1:
			h.Nodes[i].Class = uint32(r.Intn(6))
		case 2:
			if len(h.Nodes[i].Out) > 0 {
				h.Nodes[i].Out = h.Nodes[i].Out[:len(h.Nodes[i].Out)-1]
			}
		}
	}
	return h
}

// The view-based matcher must reproduce the pre-P6 matcher exactly (every field of the result).
func TestCompactMatchEqualsReference(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for it := 0; it < 400; it++ {
		g := rgFull(r, 3+r.Intn(150), it%3 == 0, it%5 == 0)
		h := perturb(r, g, r.Intn(8))
		ig, ih := g.SCCIdentities(), h.SCCIdentities()
		for _, opt := range []MatchOptions{DefaultMatch(), {MaxPasses: 64}, {MaxPasses: 3, AssignLimit: 4, DupByOrder: true}} {
			want := refMatch(g, h, ig, ih, opt)
			got := Match(g, h, ig, ih, opt)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("iteration %d: matcher result differs from reference\nwant %+v\n got %+v", it, want.Count, got.Count)
			}
			cg, _ := g.Compact()
			ch, _ := h.Compact()
			if cm := MatchCompact(cg, ch, ig, ih, opt); !reflect.DeepEqual(want, cm) {
				t.Fatalf("iteration %d: compact matcher differs", it)
			}
		}
	}
}
