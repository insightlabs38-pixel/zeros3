package graphnorm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"sort"
)

// view is what the identity engine needs from a graph: a node count and read access to one
// node at a time. Graph (slice of Node structs) and CGraph (structure of arrays) implement
// it, so both are driven by the same algorithm and cannot diverge.
type view interface {
	size() int
	node(i int, tmp *Node) *Node
}

func (g *Graph) size() int                 { return len(g.Nodes) }
func (g *Graph) node(i int, _ *Node) *Node { return &g.Nodes[i] }
func vdangling(nn int, e Edge) bool        { return e.To < 0 || int(e.To) >= nn }

// CGraph is the compact graph: fixed-width arrays, no per-node allocations. Offsets are
// uint32 (a graph is limited to 4 Gi edges and 4 GiB of literal bytes, checked on Add);
// edges are 8-byte {kind uint16, to int32} records shared by all nodes.
type CGraph struct {
	class  []uint32
	flags  []uint8
	litOff []uint32 // len n+1
	lits   []byte
	outOff []uint32 // len n+1
	out    []Edge
	setOff []uint32 // len n+1
	set    []Edge
}

// ErrTooLarge is returned when a CGraph would exceed its 32-bit offset space.
var ErrTooLarge = errors.New("graphnorm: compact graph offset space exceeded")

func NewCGraph(nodesHint, edgesHint int) *CGraph {
	c := &CGraph{
		class:  make([]uint32, 0, nodesHint),
		flags:  make([]uint8, 0, nodesHint),
		litOff: make([]uint32, 1, nodesHint+1),
		outOff: make([]uint32, 1, nodesHint+1),
		setOff: make([]uint32, 1, nodesHint+1),
		out:    make([]Edge, 0, edgesHint),
	}
	return c
}

// Add appends a node and returns its handle. The slices are copied into the arenas.
func (c *CGraph) Add(class uint32, flags uint8, lit []byte, out, set []Edge) (int32, error) {
	if len(c.class) >= 1<<31-1 || uint64(len(c.lits))+uint64(len(lit)) >= 1<<32 ||
		uint64(len(c.out))+uint64(len(out)) >= 1<<32 || uint64(len(c.set))+uint64(len(set)) >= 1<<32 {
		return -1, ErrTooLarge
	}
	c.class = append(c.class, class)
	c.flags = append(c.flags, flags)
	c.lits = append(c.lits, lit...)
	c.litOff = append(c.litOff, uint32(len(c.lits)))
	c.out = append(c.out, out...)
	c.outOff = append(c.outOff, uint32(len(c.out)))
	c.set = append(c.set, set...)
	c.setOff = append(c.setOff, uint32(len(c.set)))
	return int32(len(c.class) - 1), nil
}

func (c *CGraph) Len() int { return len(c.class) }

func (c *CGraph) size() int { return len(c.class) }

func (c *CGraph) node(i int, tmp *Node) *Node {
	a, b := c.litOff[i], c.litOff[i+1]
	x, y := c.outOff[i], c.outOff[i+1]
	p, q := c.setOff[i], c.setOff[i+1]
	tmp.Class, tmp.Flags = c.class[i], c.flags[i]
	tmp.Lit = c.lits[a:b:b]
	tmp.Out = c.out[x:y:y]
	tmp.Set = c.set[p:q:q]
	return tmp
}

// Node returns a copy of node i whose slices alias the arenas (read-only).
func (c *CGraph) Node(i int) Node { var t Node; return *c.node(i, &t) }

// Edges is the total edge count.
func (c *CGraph) Edges() int { return len(c.out) + len(c.set) }

// Compact converts a Graph (the arenas are copied; g can then be dropped).
func (g *Graph) Compact() (*CGraph, error) {
	ne := 0
	for i := range g.Nodes {
		ne += len(g.Nodes[i].Out)
	}
	c := NewCGraph(len(g.Nodes), ne)
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if _, err := c.Add(n.Class, n.Flags, n.Lit, n.Out, n.Set); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Expand converts back to a Graph (tests and adapters that need slices).
func (c *CGraph) Expand() *Graph {
	g := &Graph{Nodes: make([]Node, c.Len())}
	for i := range g.Nodes {
		n := c.Node(i)
		g.Nodes[i] = Node{Class: n.Class, Flags: n.Flags, Lit: append([]byte(nil), n.Lit...),
			Out: append([]Edge(nil), n.Out...), Set: append([]Edge(nil), n.Set...)}
	}
	return g
}

// HeapBytes is the retained size of the arenas (capacity), for bytes-per-node reporting.
func (c *CGraph) HeapBytes() int64 {
	return int64(cap(c.class))*4 + int64(cap(c.flags)) + int64(cap(c.litOff))*4 + int64(cap(c.lits)) +
		int64(cap(c.outOff))*4 + int64(cap(c.out))*8 + int64(cap(c.setOff))*4 + int64(cap(c.set))*8
}

// Trim releases arena slack after construction.
func (c *CGraph) Trim() {
	c.class = append([]uint32(nil), c.class...)
	c.flags = append([]uint8(nil), c.flags...)
	c.litOff = append([]uint32(nil), c.litOff...)
	c.lits = append([]byte(nil), c.lits...)
	c.outOff = append([]uint32(nil), c.outOff...)
	c.out = append([]Edge(nil), c.out...)
	c.setOff = append([]uint32(nil), c.setOff...)
	c.set = append([]Edge(nil), c.set...)
}

func (c *CGraph) SCCIdentities() *Idents { return sccIdentities(c) }

// SCCIdentities computes the SCC-aware canonical identities (see sccIdentities).
func (g *Graph) SCCIdentities() *Idents { return sccIdentities(g) }

// ---- identity engine ----

type hasher struct {
	h, l hash.Hash
	buf  [34]byte
	set  [][34]byte
}

func newHasher() *hasher { return &hasher{h: sha256.New(), l: sha256.New()} }

func (x *hasher) part(h hash.Hash, b []byte) {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(b)))
	h.Write(l[:])
	h.Write(b)
}

// localID is LocalID(n) without allocating a part list.
func (x *hasher) localID(n *Node, out *ID) {
	x.l.Reset()
	var b [4]byte
	x.part(x.l, []byte("L"))
	binary.LittleEndian.PutUint32(b[:], n.Class)
	x.part(x.l, b[:])
	x.part(x.l, []byte{n.Flags})
	x.part(x.l, n.Lit)
	x.l.Sum(out[:0])
}

// nodeHash is Graph.nodeHash over a view: same bytes hashed, no per-node allocations.
func (x *hasher) nodeHash(nn int, n *Node, cur []ID, tag byte) ID {
	x.h.Reset()
	var l ID
	x.localID(n, &l)
	x.part(x.h, []byte{tag})
	x.part(x.h, l[:])
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], uint32(len(n.Out)))
	x.part(x.h, cnt[:])
	for _, e := range n.Out {
		b := &x.buf
		*b = [34]byte{}
		binary.LittleEndian.PutUint16(b[:2], e.Kind)
		if !vdangling(nn, e) {
			copy(b[2:], cur[e.To][:])
		} else {
			b[2] = 0xFF
		}
		x.part(x.h, b[:])
	}
	binary.LittleEndian.PutUint32(cnt[:], uint32(len(n.Set)))
	x.part(x.h, cnt[:])
	x.set = x.set[:0]
	for _, e := range n.Set {
		var b [34]byte
		binary.LittleEndian.PutUint16(b[:2], e.Kind)
		if !vdangling(nn, e) {
			copy(b[2:], cur[e.To][:])
		}
		x.set = append(x.set, b)
	}
	if len(x.set) > 1 {
		sort.Slice(x.set, func(i, j int) bool { return bytes.Compare(x.set[i][:], x.set[j][:]) < 0 })
	}
	for i := range x.set {
		x.part(x.h, x.set[i][:])
	}
	var id ID
	x.h.Sum(id[:0])
	return id
}

// sccIdentities computes exact Merkle identities on the condensation: acyclic nodes hash
// their children; each cyclic component is canonically labeled (see canonSCC). Poisoned
// (dangling/unknown-reachable) and opaque nodes are marked exactly as before.
func sccIdentities(g view) *Idents {
	n := g.size()
	ids := make([]ID, n)
	amb := make([]uint8, n)
	res := &Idents{}
	hs := newHasher()
	var tmp Node
	cs := tarjanView(g)
	res.SCCs = cs.count()
	for ci := 0; ci < cs.count(); ci++ {
		c := cs.comp(ci)
		if !cs.cyclic[ci] {
			v := int(c[0])
			ids[v] = hs.nodeHash(n, g.node(v, &tmp), ids, 'S')
			continue
		}
		res.Cyclic++
		canonSCC(g, c, ids, amb, res)
	}
	cs = nil
	pz := poisonView(g)
	for i := range ids {
		if pz[i] {
			amb[i] = AmbigPoisoned
			ids[i] = hash2([]byte("poison"), u32(uint32(i)))
		} else if g.node(i, &tmp).Flags&FlagOpaque != 0 && amb[i] == AmbigNone {
			amb[i] = AmbigOpaque
		}
	}
	out := classifySorted(ids, amb)
	out.SCCs, out.Cyclic, out.Search, out.Budget = res.SCCs, res.Cyclic, res.Search, res.Budget
	return out
}

// classifySorted is Graph.classify without a map: nodes are ordered by (identity, index), so
// the occurrence index is the rank among equal identities in node order.
func classifySorted(ids []ID, amb []uint8) *Idents {
	n := len(ids)
	r := &Idents{ID: ids, Ambig: amb, Occ: make([]int32, n)}
	ord := make([]int32, n)
	for i := range ord {
		ord[i] = int32(i)
	}
	sort.Slice(ord, func(a, b int) bool {
		if c := bytes.Compare(ids[ord[a]][:], ids[ord[b]][:]); c != 0 {
			return c < 0
		}
		return ord[a] < ord[b]
	})
	classes := 0
	for i := 0; i < n; {
		j := i + 1
		for j < n && ids[ord[j]] == ids[ord[i]] {
			j++
		}
		classes++
		for k := i; k < j; k++ {
			r.Occ[ord[k]] = int32(k - i)
			if j-i > 1 && r.Ambig[ord[k]] == AmbigNone {
				r.Ambig[ord[k]] = AmbigDuplicate
			}
		}
		i = j
	}
	r.Classes = classes
	return r
}

type compSet struct {
	start  []int32 // len comps+1
	nodes  []int32
	cyclic []bool
}

func (c *compSet) count() int         { return len(c.cyclic) }
func (c *compSet) comp(i int) []int32 { return c.nodes[c.start[i]:c.start[i+1]] }

// tarjanView is iterative Tarjan with the same traversal order as the reference (Out edges,
// then Set edges, dangling skipped), so components and their member order are identical.
func tarjanView(g view) *compSet {
	n := g.size()
	idx := make([]int32, n)
	low := make([]int32, n)
	on := make([]bool, n)
	for i := range idx {
		idx[i] = -1
	}
	cs := &compSet{start: []int32{0}, nodes: make([]int32, 0, n)}
	var stack []int32
	type frame struct {
		v    int32
		next int32 // position over Out then Set
	}
	var tmp Node
	adj := func(v int32, k int32) (int32, bool) { // k-th non-dangling neighbour at or after position k
		nd := g.node(int(v), &tmp)
		no := int32(len(nd.Out))
		for ; k < no+int32(len(nd.Set)); k++ {
			var e Edge
			if k < no {
				e = nd.Out[k]
			} else {
				e = nd.Set[k-no]
			}
			if !vdangling(n, e) {
				return e.To, true
			}
		}
		return 0, false
	}
	counter := int32(0)
	for s := 0; s < n; s++ {
		if idx[s] >= 0 {
			continue
		}
		fs := []frame{{v: int32(s)}}
		idx[s], low[s] = counter, counter
		counter++
		stack = append(stack, int32(s))
		on[s] = true
		for len(fs) > 0 {
			f := &fs[len(fs)-1]
			// advance to the next non-dangling neighbour
			var w int32
			found := false
			nd := g.node(int(f.v), &tmp)
			no := int32(len(nd.Out))
			total := no + int32(len(nd.Set))
			for f.next < total {
				var e Edge
				if f.next < no {
					e = nd.Out[f.next]
				} else {
					e = nd.Set[f.next-no]
				}
				f.next++
				if !vdangling(n, e) {
					w, found = e.To, true
					break
				}
			}
			if found {
				if idx[w] < 0 {
					idx[w], low[w] = counter, counter
					counter++
					stack = append(stack, w)
					on[w] = true
					fs = append(fs, frame{v: w})
				} else if on[w] && idx[w] < low[f.v] {
					low[f.v] = idx[w]
				}
				continue
			}
			v := f.v
			if low[v] == idx[v] {
				first := len(cs.nodes)
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[w] = false
					cs.nodes = append(cs.nodes, w)
					if w == v {
						break
					}
				}
				cs.start = append(cs.start, int32(len(cs.nodes)))
				cyc := len(cs.nodes)-first > 1
				if !cyc {
					nd := g.node(int(v), &tmp)
					for _, e := range nd.Out {
						if e.To == v {
							cyc = true
						}
					}
					for _, e := range nd.Set {
						if e.To == v {
							cyc = true
						}
					}
				}
				cs.cyclic = append(cs.cyclic, cyc)
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
	_ = adj
	return cs
}

// poisonView marks nodes that transitively reference dangling edges or unknown nodes. The
// reverse adjacency is a counting-sort CSR (4 bytes per edge), not a slice per node.
func poisonView(g view) []bool {
	n := g.size()
	p := make([]bool, n)
	var tmp Node
	cnt := make([]int32, n+1)
	for i := 0; i < n; i++ {
		nd := g.node(i, &tmp)
		if nd.Flags&FlagUnknown != 0 {
			p[i] = true
		}
		for _, es := range [2][]Edge{nd.Out, nd.Set} {
			for _, e := range es {
				if vdangling(n, e) {
					p[i] = true
				} else {
					cnt[e.To+1]++
				}
			}
		}
	}
	for i := 0; i < n; i++ {
		cnt[i+1] += cnt[i]
	}
	rev := make([]int32, cnt[n])
	pos := append([]int32(nil), cnt[:n]...)
	for i := 0; i < n; i++ {
		nd := g.node(i, &tmp)
		for _, es := range [2][]Edge{nd.Out, nd.Set} {
			for _, e := range es {
				if !vdangling(n, e) {
					rev[pos[e.To]] = int32(i)
					pos[e.To]++
				}
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
		for _, u := range rev[cnt[v]:cnt[v+1]] {
			if !p[u] {
				p[u] = true
				stack = append(stack, u)
			}
		}
	}
	return p
}
