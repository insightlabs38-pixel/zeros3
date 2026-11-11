// Package graphnorm is the Z4-B4 laboratory: a generic exact graph-identity substrate, a
// restricted reversible transformation VM and an automatic program synthesizer. Adapters
// (LLVM bitcode, Portable PDB, DEX, ...) emit Graphs; nothing here depends on a format.
package graphcore

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// Node flags.
const (
	FlagOpaque  = 1 << iota // content cannot be interpreted: identity is its literal only, never merged by structure
	FlagUnknown             // adapter could not classify the node: identity is poisoned (never matches)
)

// Edge is a reference to another node by temporary format-local handle (index in Graph.Nodes).
// To < 0 or >= len(Nodes) is a dangling reference: it poisons the referrer's identity.
type Edge struct {
	Kind uint16
	To   int32
}

// Node is one entity. Class carries the semantic domain/kind. Lit is the exact local literal
// payload (never contains a numeric handle). Out edges are ordered (position is semantic);
// Set edges are an unordered multiset. Layout-only fields are the adapter's business and are
// deliberately not part of the node.
type Node struct {
	Class uint32
	Flags uint8
	Lit   []byte
	Out   []Edge
	Set   []Edge
}

type Graph struct{ Nodes []Node }

func (g *Graph) Add(n Node) int32 { g.Nodes = append(g.Nodes, n); return int32(len(g.Nodes) - 1) }

// ID is a stable structural identity. Ambig records how trustworthy the identity is.
type ID [32]byte

const (
	AmbigNone      uint8 = iota // unique structural identity within the graph
	AmbigDuplicate              // structurally identical to other nodes (true duplicates; occurrence index disambiguates)
	AmbigSymmetric              // inside a cyclic component with an automorphism / exhausted search: not individually distinguishable
	AmbigPoisoned               // dangling or unknown reference reachable: identity must never be used for sharing
	AmbigOpaque                 // opaque node
)

// Idents is the result of an identity computation.
type Idents struct {
	ID    []ID
	Ambig []uint8
	Occ   []int32 // occurrence index among nodes with the same ID (0 for unique)
	// diagnostics
	Rounds  int
	SCCs    int
	Cyclic  int // number of cyclic SCCs
	Search  int // individualization-refinement nodes explored
	Budget  bool
	Classes int
}

func hash2(parts ...[]byte) ID {
	h := sha256.New()
	for _, p := range parts {
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(p)))
		h.Write(l[:])
		h.Write(p)
	}
	var id ID
	h.Sum(id[:0])
	return id
}

func u32(v uint32) []byte { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); return b[:] }

// LocalID hashes only class and literal: the weakest tagged local-content identity.
func LocalID(n *Node) ID {
	return hash2([]byte("L"), u32(n.Class), []byte{n.Flags}, n.Lit)
}

func (g *Graph) dangling(e Edge) bool { return e.To < 0 || int(e.To) >= len(g.Nodes) }

// sortedIDs returns the byte-wise sorted list of child identities with kind (multiset semantics).
func sortedEdgeIDs(es []Edge, cur []ID, g *Graph) [][]byte {
	out := make([][]byte, 0, len(es))
	for _, e := range es {
		var b [34]byte
		binary.LittleEndian.PutUint16(b[:2], e.Kind)
		if !g.dangling(e) {
			copy(b[2:], cur[e.To][:])
		}
		out = append(out, append([]byte(nil), b[:]...))
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

func (g *Graph) nodeHash(v int, cur []ID, tag byte) ID {
	n := &g.Nodes[v]
	parts := make([][]byte, 0, 6+len(n.Out)+len(n.Set))
	l := LocalID(n)
	parts = append(parts, []byte{tag}, l[:], u32(uint32(len(n.Out))))
	for _, e := range n.Out {
		var b [34]byte
		binary.LittleEndian.PutUint16(b[:2], e.Kind)
		if !g.dangling(e) {
			copy(b[2:], cur[e.To][:])
		} else {
			b[2] = 0xFF
		}
		parts = append(parts, append([]byte(nil), b[:]...))
	}
	parts = append(parts, u32(uint32(len(n.Set))))
	parts = append(parts, sortedEdgeIDs(n.Set, cur, g)...)
	return hash2(parts...)
}

// poison marks nodes that (transitively) reference dangling edges or unknown nodes.
func (g *Graph) poison() []bool {
	p := make([]bool, len(g.Nodes))
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Flags&FlagUnknown != 0 {
			p[i] = true
		}
		for _, e := range n.Out {
			if g.dangling(e) {
				p[i] = true
			}
		}
		for _, e := range n.Set {
			if g.dangling(e) {
				p[i] = true
			}
		}
	}
	// propagate backwards over reverse edges
	rev := make([][]int32, len(g.Nodes))
	for i := range g.Nodes {
		for _, e := range append(append([]Edge(nil), g.Nodes[i].Out...), g.Nodes[i].Set...) {
			if !g.dangling(e) {
				rev[e.To] = append(rev[e.To], int32(i))
			}
		}
	}
	var stack []int32
	for i, x := range p {
		if x {
			stack = append(stack, int32(i))
		}
	}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, u := range rev[v] {
			if !p[u] {
				p[u] = true
				stack = append(stack, u)
			}
		}
	}
	return p
}

func (g *Graph) classify(ids []ID, amb []uint8) *Idents {
	r := &Idents{ID: ids, Ambig: amb, Occ: make([]int32, len(ids))}
	cnt := map[ID]int32{}
	for i, id := range ids {
		r.Occ[i] = cnt[id]
		cnt[id]++
	}
	r.Classes = len(cnt)
	for i, id := range ids {
		if cnt[id] > 1 && r.Ambig[i] == AmbigNone {
			r.Ambig[i] = AmbigDuplicate
		}
	}
	return r
}

// LocalIdentities: tag-only identities (baseline: no context).
func (g *Graph) LocalIdentities() *Idents {
	ids := make([]ID, len(g.Nodes))
	amb := make([]uint8, len(g.Nodes))
	for i := range g.Nodes {
		ids[i] = LocalID(&g.Nodes[i])
		if g.Nodes[i].Flags&FlagOpaque != 0 {
			amb[i] = AmbigOpaque
		}
	}
	return g.classify(ids, amb)
}

// RefineIdentities is iterative neighborhood refinement (1-WL with ordered/unordered edges),
// run for at most rounds iterations or until the partition is stable. On DAGs, rounds >= height
// gives exact Merkle identities; on cyclic graphs the result is the bisimulation partition,
// which can merge non-isomorphic nodes, so cyclic nodes are marked Symmetric unless distinguished.
func (g *Graph) RefineIdentities(rounds int) *Idents {
	n := len(g.Nodes)
	cur := make([]ID, n)
	for i := range cur {
		cur[i] = LocalID(&g.Nodes[i])
	}
	classes := countClasses(cur)
	used := 0
	for r := 0; r < rounds; r++ {
		next := make([]ID, n)
		for v := 0; v < n; v++ {
			next[v] = g.nodeHash(v, cur, 'R')
		}
		used++
		c := countClasses(next)
		cur = next
		if c == classes {
			break
		}
		classes = c
	}
	pz := g.poison()
	amb := make([]uint8, n)
	for i := range amb {
		if pz[i] {
			amb[i] = AmbigPoisoned
			cur[i] = hash2([]byte("poison"), u32(uint32(i)))
		} else if g.Nodes[i].Flags&FlagOpaque != 0 {
			amb[i] = AmbigOpaque
		}
	}
	r := g.classify(cur, amb)
	r.Rounds = used
	return r
}

func countClasses(ids []ID) int {
	m := make(map[ID]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return len(m)
}

// ---- SCC-aware canonical identities ----

// SCCBudget bounds the individualization-refinement search per cyclic component.
const SCCBudget = 2000

// SCCWork bounds total refinement work (node updates) per cyclic component.
const SCCWork = 3_000_000

// canonSCC labels one cyclic component. External children (outside c) already have final IDs.
func canonSCC(g view, c []int32, ids []ID, amb []uint8, res *Idents) {
	var tmp Node
	nn := g.size()
	dangling := func(e Edge) bool { return e.To < 0 || int(e.To) >= nn }
	m := len(c)
	pos := make(map[int32]int, m)
	for i, v := range c {
		pos[v] = i
	}
	// base color: node tag with external children folded in, internal edges excluded
	base := make([]ID, m)
	for i, v := range c {
		nd := g.node(int(v), &tmp)
		parts := [][]byte{[]byte("B")}
		l := LocalID(nd)
		parts = append(parts, l[:], u32(uint32(len(nd.Out))))
		for _, e := range nd.Out {
			var b [34]byte
			binary.LittleEndian.PutUint16(b[:2], e.Kind)
			if dangling(e) {
				b[2] = 0xFF
			} else if _, in := pos[e.To]; in {
				b[2] = 0x01 // internal: resolved by refinement
			} else {
				b[2] = 0x02
				copy(b[3:], ids[e.To][:31])
			}
			parts = append(parts, append([]byte(nil), b[:]...))
		}
		var ss [][]byte
		for _, e := range nd.Set {
			var b [34]byte
			binary.LittleEndian.PutUint16(b[:2], e.Kind)
			if dangling(e) {
				b[2] = 0xFF
			} else if _, in := pos[e.To]; in {
				b[2] = 0x01
			} else {
				b[2] = 0x02
				copy(b[3:], ids[e.To][:31])
			}
			ss = append(ss, append([]byte(nil), b[:]...))
		}
		sort.Slice(ss, func(a, b int) bool { return string(ss[a]) < string(ss[b]) })
		parts = append(parts, u32(uint32(len(nd.Set))))
		parts = append(parts, ss...)
		base[i] = hash2(parts...)
	}
	// local adjacency (positions)
	type ladj struct {
		out []int32
		set []int32
		ok  []uint16
		sk  []uint16
	}
	adj := make([]ladj, m)
	for i, v := range c {
		for _, e := range g.node(int(v), &tmp).Out {
			if p, in := pos[e.To]; in && !dangling(e) {
				adj[i].out = append(adj[i].out, int32(p))
				adj[i].ok = append(adj[i].ok, e.Kind)
			}
		}
		for _, e := range g.node(int(v), &tmp).Set {
			if p, in := pos[e.To]; in && !dangling(e) {
				adj[i].set = append(adj[i].set, int32(p))
				adj[i].sk = append(adj[i].sk, e.Kind)
			}
		}
	}
	type rpair struct {
		from int32
		kind uint16
		tag  byte
	}
	radj := make([][]rpair, m)
	for j := 0; j < m; j++ {
		for k, o := range adj[j].out {
			radj[o] = append(radj[o], rpair{int32(j), adj[j].ok[k], 'i'})
		}
		for k, o := range adj[j].set {
			radj[o] = append(radj[o], rpair{int32(j), adj[j].sk[k], 's'})
		}
	}
	work := 0
	exhausted := false
	refine := func(col []ID) []ID {
		cur := append([]ID(nil), col...)
		classes := countClasses(cur)
		for it := 0; it < m+1; it++ {
			work += m
			if work > SCCWork {
				exhausted = true
				return cur
			}
			next := make([]ID, m)
			for i := 0; i < m; i++ {
				parts := [][]byte{[]byte("C"), cur[i][:]}
				for k, o := range adj[i].out {
					var b [34]byte
					binary.LittleEndian.PutUint16(b[:2], adj[i].ok[k])
					copy(b[2:], cur[o][:32-2])
					parts = append(parts, append([]byte(nil), b[:]...))
				}
				var ss [][]byte
				for k, o := range adj[i].set {
					var b [34]byte
					binary.LittleEndian.PutUint16(b[:2], adj[i].sk[k])
					copy(b[2:], cur[o][:32-2])
					ss = append(ss, append([]byte(nil), b[:]...))
				}
				sort.Slice(ss, func(a, b int) bool { return string(ss[a]) < string(ss[b]) })
				parts = append(parts, ss...)
				// in-neighbors matter too, otherwise a sink-side difference is invisible
				var ins [][]byte
				for _, rp := range radj[i] {
					var b [34]byte
					binary.LittleEndian.PutUint16(b[:2], rp.kind)
					b[2] = rp.tag
					copy(b[3:], cur[rp.from][:29])
					ins = append(ins, append([]byte(nil), b[:]...))
				}
				sort.Slice(ins, func(a, b int) bool { return string(ins[a]) < string(ins[b]) })
				parts = append(parts, ins...)
				next[i] = hash2(parts...)
			}
			c2 := countClasses(next)
			cur = next
			if c2 == classes {
				break
			}
			classes = c2
		}
		return cur
	}
	var best []byte
	var bestLabel []int
	nodes := 0
	var search func(col []ID)
	// certificate: nodes sorted by (color), ties broken by the explored individualization
	search = func(col []ID) {
		nodes++
		res.Search++
		if nodes > SCCBudget {
			exhausted = true
			return
		}
		col = refine(col)
		if exhausted {
			return
		}
		// find first smallest non-singleton cell (by color order)
		cells := map[ID][]int{}
		for i, x := range col {
			cells[x] = append(cells[x], i)
		}
		var target []int
		var tkey ID
		for k, cell := range cells {
			if len(cell) < 2 {
				continue
			}
			if target == nil || len(cell) < len(target) || (len(cell) == len(target) && string(k[:]) < string(tkey[:])) {
				target, tkey = cell, k
			}
		}
		if target == nil {
			// discrete: certificate = sorted colors + adjacency by rank
			order := make([]int, m)
			for i := range order {
				order[i] = i
			}
			sort.Slice(order, func(a, b int) bool { return string(col[order[a]][:]) < string(col[order[b]][:]) })
			rank := make([]int, m)
			for r, i := range order {
				rank[i] = r
			}
			var cert []byte
			for _, i := range order {
				cert = append(cert, col[i][:]...)
				for _, o := range adj[i].out {
					cert = binary.LittleEndian.AppendUint32(cert, uint32(rank[o]))
				}
				cert = append(cert, 0xFF)
				var sr []int
				for _, o := range adj[i].set {
					sr = append(sr, rank[o])
				}
				sort.Ints(sr)
				for _, r := range sr {
					cert = binary.LittleEndian.AppendUint32(cert, uint32(r))
				}
				cert = append(cert, 0xFE)
			}
			if best == nil || string(cert) < string(best) {
				best = cert
				bestLabel = rank
			}
			return
		}
		for _, i := range target {
			nc := append([]ID(nil), col...)
			nc[i] = hash2([]byte("I"), nc[i][:])
			search(nc)
			if exhausted {
				return
			}
		}
	}
	search(base)
	if best == nil || exhausted {
		// order-independent fallback: refined multiset
		col := refine(base)
		res.Budget = res.Budget || exhausted
		var all [][]byte
		for _, x := range col {
			all = append(all, x[:])
		}
		sort.Slice(all, func(a, b int) bool { return string(all[a]) < string(all[b]) })
		comp := hash2(append([][]byte{[]byte("F")}, all...)...)
		for i, v := range c {
			ids[v] = hash2([]byte("Fm"), comp[:], col[i][:])
			amb[v] = AmbigSymmetric
		}
		return
	}
	comp := hash2([]byte("K"), best)
	// members that remain in one color class after refinement (orbit candidates) are Symmetric
	finalCol := refine(base)
	cc := map[ID]int{}
	for _, x := range finalCol {
		cc[x]++
	}
	for i, v := range c {
		ids[v] = hash2([]byte("Km"), comp[:], u32(uint32(bestLabel[i])))
		if cc[finalCol[i]] > 1 {
			// automorphic orbit: individual labels are arbitrary
			ids[v] = hash2([]byte("Ko"), comp[:], finalCol[i][:])
			amb[v] = AmbigSymmetric
		}
	}
}
