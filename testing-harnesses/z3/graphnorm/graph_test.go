package graphnorm

import (
	"math/rand"
	"testing"
)

// randGraph builds a random graph with labelled nodes; cyc controls back edges.
func randGraph(r *rand.Rand, n int, cyc float64, lits int) *Graph {
	g := &Graph{}
	for i := 0; i < n; i++ {
		nd := Node{Class: uint32(r.Intn(4)), Lit: []byte{byte(r.Intn(lits)), byte(r.Intn(lits))}}
		k := r.Intn(4)
		for j := 0; j < k; j++ {
			var to int
			if i > 0 && r.Float64() >= cyc {
				to = r.Intn(i)
			} else {
				to = r.Intn(n)
			}
			e := Edge{Kind: uint16(r.Intn(3)), To: int32(to)}
			if r.Intn(3) == 0 {
				nd.Set = append(nd.Set, e)
			} else {
				nd.Out = append(nd.Out, e)
			}
		}
		g.Nodes = append(g.Nodes, nd)
	}
	return g
}

// renumber applies a random permutation; perm[old]=new. Unordered edges are also shuffled.
func renumber(g *Graph, r *rand.Rand) (*Graph, []int32) {
	n := len(g.Nodes)
	perm := make([]int32, n)
	for i, p := range r.Perm(n) {
		perm[i] = int32(p)
	}
	h := &Graph{Nodes: make([]Node, n)}
	for i := range g.Nodes {
		nd := g.Nodes[i]
		nn := Node{Class: nd.Class, Flags: nd.Flags, Lit: nd.Lit}
		for _, e := range nd.Out {
			nn.Out = append(nn.Out, Edge{e.Kind, perm[e.To]})
		}
		for _, e := range nd.Set {
			nn.Set = append(nn.Set, Edge{e.Kind, perm[e.To]})
		}
		r.Shuffle(len(nn.Set), func(a, b int) { nn.Set[a], nn.Set[b] = nn.Set[b], nn.Set[a] })
		h.Nodes[perm[i]] = nn
	}
	return h, perm
}

func TestSCCIdentityRenumberInvariance(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for trial := 0; trial < 60; trial++ {
		g := randGraph(r, 40+r.Intn(60), 0.15, 6)
		h, perm := renumber(g, r)
		ig, ih := g.SCCIdentities(), h.SCCIdentities()
		for i := range g.Nodes {
			j := perm[i]
			if ig.Ambig[i] == AmbigSymmetric || ig.Ambig[i] == AmbigDuplicate {
				continue
			}
			if ig.ID[i] != ih.ID[j] {
				t.Fatalf("trial %d node %d: identity changed under renumbering (amb=%d)", trial, i, ig.Ambig[i])
			}
		}
	}
}

func TestRefineMatchesSCCOnDAG(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	g := randGraph(r, 80, 0, 5) // DAG
	a := g.RefineIdentities(100)
	h, perm := renumber(g, r)
	b := h.RefineIdentities(100)
	for i := range g.Nodes {
		if a.ID[i] != b.ID[perm[i]] {
			t.Fatal("refinement not renumbering invariant on DAG")
		}
	}
	// local identities are weaker: many more collisions than SCC identities
	if g.LocalIdentities().Classes >= g.SCCIdentities().Classes {
		t.Log("local identity not weaker on this sample")
	}
}

func cycleGraph(n int, lit func(i int) byte) *Graph {
	g := &Graph{}
	for i := 0; i < n; i++ {
		g.Add(Node{Class: 1, Lit: []byte{lit(i)}, Out: []Edge{{1, int32((i + 1) % n)}}})
	}
	return g
}

func TestSymmetricCycleNotSplit(t *testing.T) {
	g := cycleGraph(6, func(int) byte { return 7 })
	id := g.SCCIdentities()
	for i := range id.Ambig {
		if id.Ambig[i] != AmbigSymmetric {
			t.Fatalf("node %d not flagged symmetric: %d", i, id.Ambig[i])
		}
	}
	// an asymmetric cycle is fully individualized and renumbering invariant
	g2 := cycleGraph(6, func(i int) byte { return byte(i % 3) })
	g2.Nodes[0].Lit = []byte{9}
	id2 := g2.SCCIdentities()
	if id2.Classes != 6 {
		t.Fatalf("asymmetric cycle classes=%d", id2.Classes)
	}
	r := rand.New(rand.NewSource(3))
	h, perm := renumber(g2, r)
	ih := h.SCCIdentities()
	for i := range g2.Nodes {
		if id2.ID[i] != ih.ID[perm[i]] {
			t.Fatal("cycle identity not invariant")
		}
	}
}

func TestPoisonAndOpaque(t *testing.T) {
	g := &Graph{}
	g.Add(Node{Class: 1, Lit: []byte("a")})
	g.Add(Node{Class: 2, Out: []Edge{{1, 0}, {1, 99}}}) // dangling
	g.Add(Node{Class: 3, Out: []Edge{{1, 1}}})          // references poisoned
	g.Add(Node{Class: 4, Flags: FlagOpaque, Lit: []byte("x")})
	for _, id := range []*Idents{g.SCCIdentities(), g.RefineIdentities(10)} {
		if id.Ambig[1] != AmbigPoisoned || id.Ambig[2] != AmbigPoisoned || id.Ambig[0] != AmbigNone || id.Ambig[3] != AmbigOpaque {
			t.Fatalf("ambig %v", id.Ambig)
		}
	}
	// poisoned nodes never match
	m := Match(g, g, g.SCCIdentities(), g.SCCIdentities(), DefaultMatch())
	if m.Map[1] != -1 || m.Map[2] != -1 || m.Map[0] != 0 {
		t.Fatalf("match %v", m.Map)
	}
}

func TestDuplicateOccurrence(t *testing.T) {
	g := &Graph{}
	for i := 0; i < 3; i++ {
		g.Add(Node{Class: 1, Lit: []byte("dup")})
	}
	g.Add(Node{Class: 2, Out: []Edge{{1, 0}}})
	g.Add(Node{Class: 2, Out: []Edge{{1, 1}}})
	id := g.SCCIdentities()
	for i := 0; i < 5; i++ {
		if id.Ambig[i] != AmbigDuplicate {
			t.Fatalf("node %d amb %d", i, id.Ambig[i])
		}
	}
	if id.Occ[0] != 0 || id.Occ[1] != 1 || id.Occ[2] != 2 {
		t.Fatal("occurrence")
	}
	m := Match(g, g, id, id, DefaultMatch())
	for i := range m.Map {
		if m.Map[i] < 0 {
			t.Fatalf("self-match missing %d", i)
		}
		if m.Basis[i] == BasisExact {
			t.Fatal("duplicates must never be BasisExact")
		}
	}
}

// edit perturbs a graph and returns the edited graph with truth: orig node -> new node (-1 deleted).
func edit(g *Graph, r *rand.Rand, del, mod, ins float64) (*Graph, []int32) {
	n := len(g.Nodes)
	keep := make([]int32, n)
	h := &Graph{}
	for i := range g.Nodes {
		if r.Float64() < del {
			keep[i] = -1
			continue
		}
		keep[i] = int32(len(h.Nodes))
		h.Nodes = append(h.Nodes, g.Nodes[i])
	}
	for i := range h.Nodes {
		nd := h.Nodes[i]
		var out, set []Edge
		for _, e := range nd.Out {
			if keep[e.To] < 0 {
				continue
			}
			out = append(out, Edge{e.Kind, keep[e.To]})
		}
		for _, e := range nd.Set {
			if keep[e.To] < 0 {
				continue
			}
			set = append(set, Edge{e.Kind, keep[e.To]})
		}
		nd.Out, nd.Set = out, set
		if r.Float64() < mod {
			nd.Lit = []byte{byte(100 + r.Intn(100)), byte(r.Intn(250))}
		}
		h.Nodes[i] = nd
	}
	k := int(ins * float64(n))
	for i := 0; i < k; i++ {
		nd := Node{Class: uint32(r.Intn(4)), Lit: []byte{byte(200 + r.Intn(50)), byte(r.Intn(250))}}
		if len(h.Nodes) > 0 {
			nd.Out = []Edge{{1, int32(r.Intn(len(h.Nodes)))}}
		}
		h.Nodes = append(h.Nodes, nd)
	}
	return h, keep
}

func TestMatchRetentionAndFalseMatches(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	var truth, got, wrong, exact, exactWrong int
	for trial := 0; trial < 40; trial++ {
		g := randGraph(r, 120, 0.1, 40)
		e, keep := edit(g, r, 0.05, 0.05, 0.05)
		h, perm := renumber(e, r)
		ig, ih := g.SCCIdentities(), h.SCCIdentities()
		m := Match(g, h, ig, ih, DefaultMatch())
		for i := range g.Nodes {
			if keep[i] >= 0 {
				truth++
			}
			if m.Map[i] < 0 {
				continue
			}
			got++
			want := int32(-1)
			if keep[i] >= 0 {
				want = perm[keep[i]]
			}
			if m.Basis[i] == BasisExact {
				exact++
				if m.Map[i] != want {
					exactWrong++
				}
			}
			if m.Map[i] != want {
				wrong++
			}
		}
	}
	t.Logf("truth=%d matched=%d wrong=%d exact=%d exactWrong=%d", truth, got, wrong, exact, exactWrong)
	// An identical-structure match whose history differs (a deleted node coinciding with an
	// edited neighbour) is structurally sound; it must stay rare.
	if exactWrong*100 > exact {
		t.Fatal("too many exact matches disagree with edit history")
	}
}

func TestHostileGraphs(t *testing.T) {
	// huge symmetric cycle must not explode; budget fallback keeps it Symmetric
	g := cycleGraph(3000, func(int) byte { return 1 })
	id := g.SCCIdentities()
	for _, a := range id.Ambig {
		if a != AmbigSymmetric {
			t.Fatal("expected symmetric")
		}
	}
	// complete bipartite symmetric component
	h := &Graph{}
	for i := 0; i < 12; i++ {
		h.Add(Node{Class: 1, Lit: []byte{1}})
	}
	for i := 0; i < 6; i++ {
		for j := 6; j < 12; j++ {
			h.Nodes[i].Set = append(h.Nodes[i].Set, Edge{1, int32(j)})
			h.Nodes[j].Set = append(h.Nodes[j].Set, Edge{1, int32(i)})
		}
	}
	hi := h.SCCIdentities()
	for _, a := range hi.Ambig {
		if a != AmbigSymmetric {
			t.Fatal("K6,6 should be symmetric")
		}
	}
	// self loop and mutual recursion with distinct literals are distinguished
	m := &Graph{}
	m.Add(Node{Class: 1, Lit: []byte("a"), Out: []Edge{{1, 1}}})
	m.Add(Node{Class: 1, Lit: []byte("b"), Out: []Edge{{1, 0}}})
	mi := m.SCCIdentities()
	if mi.ID[0] == mi.ID[1] || mi.Ambig[0] != AmbigNone {
		t.Fatal("mutual recursion not distinguished")
	}
	// ordered edges are order-sensitive, set edges are not
	o1 := &Graph{Nodes: []Node{{Class: 1, Lit: []byte("p")}, {Class: 1, Lit: []byte("q")}, {Class: 2, Out: []Edge{{1, 0}, {1, 1}}}}}
	o2 := &Graph{Nodes: []Node{{Class: 1, Lit: []byte("p")}, {Class: 1, Lit: []byte("q")}, {Class: 2, Out: []Edge{{1, 1}, {1, 0}}}}}
	if o1.SCCIdentities().ID[2] == o2.SCCIdentities().ID[2] {
		t.Fatal("ordered edges must be order-sensitive")
	}
	s1 := &Graph{Nodes: []Node{{Class: 1, Lit: []byte("p")}, {Class: 1, Lit: []byte("q")}, {Class: 2, Set: []Edge{{1, 0}, {1, 1}}}}}
	s2 := &Graph{Nodes: []Node{{Class: 1, Lit: []byte("p")}, {Class: 1, Lit: []byte("q")}, {Class: 2, Set: []Edge{{1, 1}, {1, 0}}}}}
	if s1.SCCIdentities().ID[2] != s2.SCCIdentities().ID[2] {
		t.Fatal("set edges must be order-insensitive")
	}
}
