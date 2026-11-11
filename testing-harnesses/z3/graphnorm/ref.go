package graphnorm

// Reference implementation of the B4 SCC-aware identities exactly as shipped before Z4-P6
// (kept verbatim, renamed). It is the oracle of the differential tests and the "before" side
// of the memory/throughput benchmarks; production code uses sccIdentities.

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

var _ = sha256.New
var _ = binary.LittleEndian
var _ = sort.Ints

// ---- SCC-aware canonical identities ----







type refSccInfo struct {
	comp   []int32 // node -> component
	comps  [][]int32
	cyclic []bool
}

func (g *Graph) refTarjan() *refSccInfo {
	n := len(g.Nodes)
	idx := make([]int32, n)
	low := make([]int32, n)
	on := make([]bool, n)
	for i := range idx {
		idx[i] = -1
	}
	comp := make([]int32, n)
	var comps [][]int32
	var stack []int32
	type frame struct {
		v    int32
		next int
		adj  []int32
	}
	adjOf := func(v int32) []int32 {
		var a []int32
		for _, e := range g.Nodes[v].Out {
			if !g.dangling(e) {
				a = append(a, e.To)
			}
		}
		for _, e := range g.Nodes[v].Set {
			if !g.dangling(e) {
				a = append(a, e.To)
			}
		}
		return a
	}
	counter := int32(0)
	for s := 0; s < n; s++ {
		if idx[s] >= 0 {
			continue
		}
		fs := []frame{{v: int32(s), adj: adjOf(int32(s))}}
		idx[s], low[s] = counter, counter
		counter++
		stack = append(stack, int32(s))
		on[s] = true
		for len(fs) > 0 {
			f := &fs[len(fs)-1]
			if f.next < len(f.adj) {
				w := f.adj[f.next]
				f.next++
				if idx[w] < 0 {
					idx[w], low[w] = counter, counter
					counter++
					stack = append(stack, w)
					on[w] = true
					fs = append(fs, frame{v: w, adj: adjOf(w)})
				} else if on[w] && idx[w] < low[f.v] {
					low[f.v] = idx[w]
				}
				continue
			}
			v := f.v
			if low[v] == idx[v] {
				var c []int32
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[w] = false
					comp[w] = int32(len(comps))
					c = append(c, w)
					if w == v {
						break
					}
				}
				comps = append(comps, c)
			}
			fs = fs[:len(fs)-1]
			if len(fs) > 0 {
				p := fs[len(fs)-1].v
				if low[v] < low[p] {
					low[p] = low[v]
				}
			}
		}
	}
	cyc := make([]bool, len(comps))
	for i, c := range comps {
		if len(c) > 1 {
			cyc[i] = true
			continue
		}
		v := c[0]
		for _, e := range append(append([]Edge(nil), g.Nodes[v].Out...), g.Nodes[v].Set...) {
			if e.To == v {
				cyc[i] = true
			}
		}
	}
	return &refSccInfo{comp: comp, comps: comps, cyclic: cyc}
}

// SCCIdentities computes exact Merkle identities on the condensation: acyclic nodes hash their
// children; each cyclic component is canonically labeled (color refinement + bounded
// individualization-refinement) so identities are independent of node numbering. Components
// with automorphisms keep one identity per orbit and are marked Symmetric; budget exhaustion
// falls back to the order-independent refined multiset and is also Symmetric.
func (g *Graph) refSCCIdentities() *Idents {
	n := len(g.Nodes)
	s := g.refTarjan()
	ids := make([]ID, n)
	amb := make([]uint8, n)
	pz := g.poison()
	res := &Idents{SCCs: len(s.comps)}
	// Tarjan emits components in reverse topological order (callees first).
	for ci, c := range s.comps {
		if !s.cyclic[ci] {
			v := int(c[0])
			ids[v] = g.nodeHash(v, ids, 'S')
			continue
		}
		res.Cyclic++
		g.refCanonSCC(c, ids, amb, res)
	}
	for i := range ids {
		if pz[i] {
			amb[i] = AmbigPoisoned
			ids[i] = hash2([]byte("poison"), u32(uint32(i)))
		} else if g.Nodes[i].Flags&FlagOpaque != 0 && amb[i] == AmbigNone {
			amb[i] = AmbigOpaque
		}
	}
	out := g.classify(ids, amb)
	out.SCCs, out.Cyclic, out.Search, out.Budget = res.SCCs, res.Cyclic, res.Search, res.Budget
	return out
}

// canonSCC labels one cyclic component. External children (outside c) already have final IDs.
func (g *Graph) refCanonSCC(c []int32, ids []ID, amb []uint8, res *Idents) {
	m := len(c)
	pos := make(map[int32]int, m)
	for i, v := range c {
		pos[v] = i
	}
	// base color: node tag with external children folded in, internal edges excluded
	base := make([]ID, m)
	for i, v := range c {
		nd := &g.Nodes[v]
		parts := [][]byte{[]byte("B")}
		l := LocalID(nd)
		parts = append(parts, l[:], u32(uint32(len(nd.Out))))
		for _, e := range nd.Out {
			var b [34]byte
			binary.LittleEndian.PutUint16(b[:2], e.Kind)
			if g.dangling(e) {
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
			if g.dangling(e) {
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
		for _, e := range g.Nodes[v].Out {
			if p, in := pos[e.To]; in && !g.dangling(e) {
				adj[i].out = append(adj[i].out, int32(p))
				adj[i].ok = append(adj[i].ok, e.Kind)
			}
		}
		for _, e := range g.Nodes[v].Set {
			if p, in := pos[e.To]; in && !g.dangling(e) {
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

// ReferenceSCCIdentities runs the pre-P6 identity algorithm (map-based classification,
// per-node adjacency slices) for comparison.
func (g *Graph) ReferenceSCCIdentities() *Idents { return g.refSCCIdentities() }
