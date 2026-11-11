package graphnorm

import (
	"bytes"
	"hash/maphash"
	"sort"
)

var localSeed = maphash.MakeSeed()

// localKey is a bucketing hash of a node's local content (class, flags, literal). It is never
// trusted: every candidate pair is confirmed with localEq.
func localKey(n *Node) uint64 {
	var h maphash.Hash
	h.SetSeed(localSeed)
	var b [5]byte
	b[0], b[1], b[2], b[3], b[4] = byte(n.Class), byte(n.Class>>8), byte(n.Class>>16), byte(n.Class>>24), n.Flags
	h.Write(b[:])
	h.Write(n.Lit)
	return h.Sum64()
}

func localEq(x, y *Node) bool {
	return x.Class == y.Class && x.Flags == y.Flags && bytes.Equal(x.Lit, y.Lit)
}

// revCSR is the reverse adjacency over Out edges in 12 bytes per edge: for every node, the
// (referrer, position, kind) triples in referrer order, built by counting sort.
type revCSR struct {
	off []int32
	e   []radj
}

func buildRev(g view) *revCSR {
	n := g.size()
	var tmp Node
	r := &revCSR{off: make([]int32, n+1)}
	for i := 0; i < n; i++ {
		for _, e := range g.node(i, &tmp).Out {
			if !vdangling(n, e) {
				r.off[e.To+1]++
			}
		}
	}
	for i := 0; i < n; i++ {
		r.off[i+1] += r.off[i]
	}
	r.e = make([]radj, r.off[n])
	pos := append([]int32(nil), r.off[:n]...)
	for i := 0; i < n; i++ {
		for p, e := range g.node(i, &tmp).Out {
			if !vdangling(n, e) {
				r.e[pos[e.To]] = radj{int32(i), int32(p), e.Kind}
				pos[e.To]++
			}
		}
	}
	return r
}

func (r *revCSR) of(i int32) []radj { return r.e[r.off[i]:r.off[i+1]] }

// Match reconciles graph a with graph b under renumbering, insertion, deletion and reorder.
func Match(a, b *Graph, ia, ib *Idents, opt MatchOptions) *MatchResult {
	return matchView(a, b, ia, ib, opt)
}

// MatchCompact is Match over compact graphs.
func MatchCompact(a, b *CGraph, ia, ib *Idents, opt MatchOptions) *MatchResult {
	return matchView(a, b, ia, ib, opt)
}

type bucketIndex struct {
	key []uint64
	ord []int32 // node indexes sorted by (key, index)
}

func buildBuckets(g view, include func(i int) bool) *bucketIndex {
	n := g.size()
	var tmp Node
	key := make([]uint64, n)
	ord := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		if include(i) {
			key[i] = localKey(g.node(i, &tmp))
			ord = append(ord, int32(i))
		}
	}
	sort.Slice(ord, func(x, y int) bool {
		if key[ord[x]] != key[ord[y]] {
			return key[ord[x]] < key[ord[y]]
		}
		return ord[x] < ord[y]
	})
	return &bucketIndex{key, ord}
}

// each calls f for every indexed node with the given key (ascending index).
func (bi *bucketIndex) each(k uint64, f func(j int32)) {
	lo := sort.Search(len(bi.ord), func(i int) bool { return bi.key[bi.ord[i]] >= k })
	for i := lo; i < len(bi.ord) && bi.key[bi.ord[i]] == k; i++ {
		f(bi.ord[i])
	}
}

func matchView(a, b view, ia, ib *Idents, opt MatchOptions) *MatchResult {
	na, nb := a.size(), b.size()
	res := &MatchResult{Map: make([]int32, na), Basis: make([]uint8, na)}
	back := make([]int32, nb)
	for i := range res.Map {
		res.Map[i] = -1
	}
	for i := range back {
		back[i] = -1
	}
	link := func(x, y int32, basis uint8) {
		res.Map[x], back[y], res.Basis[x] = y, x, basis
		res.Count[basis]++
	}
	usable := func(am uint8) bool { return am != AmbigPoisoned }
	// phases 1/2: identity classes, by sorted order instead of maps
	oa := sortedByID(ia, na, usable)
	ob := sortedByID(ib, nb, usable)
	type class struct{ xs, ys []int32 }
	var classes []class // identity classes present in a, in ID order, with their b counterparts
	for i, j := 0, 0; i < len(oa); {
		ki := oa[i]
		k := ia.ID[ki]
		ei := i + 1
		for ei < len(oa) && ia.ID[oa[ei]] == k {
			ei++
		}
		for j < len(ob) && bytes.Compare(ib.ID[ob[j]][:], k[:]) < 0 {
			j++
		}
		ej := j
		for ej < len(ob) && ib.ID[ob[ej]] == k {
			ej++
		}
		classes = append(classes, class{oa[i:ei], ob[j:ej]})
		i = ei
	}
	for _, c := range classes {
		if len(c.ys) == 0 {
			continue
		}
		if len(c.xs) == 1 && len(c.ys) == 1 && ia.Ambig[c.xs[0]] != AmbigSymmetric && ib.Ambig[c.ys[0]] != AmbigSymmetric {
			link(c.xs[0], c.ys[0], BasisExact)
		}
	}
	revA, revB := buildRev(a), buildRev(b)
	bi := buildBuckets(b, func(j int) bool { return usable(ib.Ambig[j]) })
	var tmpA, tmpB Node
	// unmatched usable a-nodes, shrinking as matches are made
	pending := make([]int32, 0, na)
	for i := 0; i < na; i++ {
		if res.Map[i] < 0 && usable(ia.Ambig[i]) {
			pending = append(pending, int32(i))
		}
	}
	for pass := 0; pass < opt.MaxPasses; pass++ {
		res.Passes++
		progress := false
		type cand struct {
			a, b  int32
			score int
		}
		bestOfB := map[int32][]cand{}
		keep := pending[:0]
		for _, i := range pending {
			if res.Map[i] >= 0 {
				continue
			}
			keep = append(keep, i)
			an := a.node(int(i), &tmpA)
			var cs []cand
			bi.each(localKey(an), func(u int32) {
				if back[u] >= 0 || !localEq(an, b.node(int(u), &tmpB)) {
					return
				}
				sc, bad := contextScoreV(a, b, i, u, res.Map, back, revA, revB)
				if bad > 0 {
					return
				}
				if sc > 0 {
					cs = append(cs, cand{i, u, sc})
				}
			})
			if len(cs) == 0 {
				continue
			}
			sort.Slice(cs, func(x, y int) bool { return cs[x].score > cs[y].score })
			if len(cs) > 1 && cs[0].score == cs[1].score {
				res.Reject++
				continue
			}
			bestOfB[cs[0].b] = append(bestOfB[cs[0].b], cs[0])
		}
		pending = keep
		type pair struct{ a, b int32 }
		var acc []pair
		for _, cs := range bestOfB {
			if len(cs) != 1 {
				res.Reject += len(cs)
				continue
			}
			acc = append(acc, pair{cs[0].a, cs[0].b})
		}
		sort.Slice(acc, func(x, y int) bool { return acc[x].a < acc[y].a })
		for _, p := range acc {
			if res.Map[p.a] < 0 && back[p.b] < 0 {
				link(p.a, p.b, BasisContext)
				progress = true
			}
		}
		if !progress && opt.AssignLimit > 0 {
			progress = assignTiesV(a, b, ia, ib, res, back, revA, revB, opt.AssignLimit, link)
		}
		if !progress && opt.DupByOrder {
			for _, c := range classes {
				var xs, ys []int32
				for _, x := range c.xs {
					if res.Map[x] < 0 {
						xs = append(xs, x)
					}
				}
				for _, y := range c.ys {
					if back[y] < 0 {
						ys = append(ys, y)
					}
				}
				if len(xs) == len(ys) && len(xs) > 1 {
					for i := range xs {
						link(xs[i], ys[i], BasisDupOrder)
						progress = true
					}
				}
			}
		}
		if !progress {
			break
		}
	}
	return res
}

func sortedByID(id *Idents, n int, usable func(uint8) bool) []int32 {
	o := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		if usable(id.Ambig[i]) {
			o = append(o, int32(i))
		}
	}
	sort.Slice(o, func(x, y int) bool {
		if c := bytes.Compare(id.ID[o[x]][:], id.ID[o[y]][:]); c != 0 {
			return c < 0
		}
		return o[x] < o[y]
	})
	return o
}

// contextScoreV counts matched-neighborhood agreements (and conflicts) between a-node i and
// b-node u; identical to the reference except for the set-membership structure (a small
// slice scan instead of a map for short sets).
func contextScoreV(a, b view, i, u int32, m, back []int32, ra, rb *revCSR) (score, bad int) {
	var ta, tb, tw Node
	na, nb := a.node(int(i), &ta), b.node(int(u), &tb)
	nna, nnb := a.size(), b.size()
	if len(na.Out) != len(nb.Out) {
		return 0, 1
	}
	for p, e := range na.Out {
		if vdangling(nna, e) || vdangling(nnb, nb.Out[p]) {
			return 0, 1
		}
		if e.Kind != nb.Out[p].Kind {
			return 0, 1
		}
		if t := m[e.To]; t >= 0 {
			if nb.Out[p].To == t {
				score++
			} else {
				bad++
			}
		}
	}
	if len(na.Set) != len(nb.Set) {
		return 0, 1
	}
	if len(na.Set) > 0 {
		var have map[[2]int32]int
		if len(nb.Set) > 16 {
			have = map[[2]int32]int{}
			for _, e := range nb.Set {
				have[[2]int32{int32(e.Kind), e.To}]++
			}
		}
		for _, e := range na.Set {
			if vdangling(nna, e) {
				return 0, 1
			}
			if t := m[e.To]; t >= 0 {
				present := false
				if have != nil {
					present = have[[2]int32{int32(e.Kind), t}] > 0
				} else {
					for _, f := range nb.Set {
						if int32(f.Kind) == int32(e.Kind) && f.To == t {
							present = true
							break
						}
					}
				}
				if present {
					score++
				} else {
					bad++
				}
			}
		}
	}
	for _, r := range ra.of(i) {
		w := m[r.from]
		if w < 0 {
			continue
		}
		wn := b.node(int(w), &tw)
		if int(r.pos) < len(wn.Out) && wn.Out[r.pos].To == u && wn.Out[r.pos].Kind == r.kind {
			score++
		} else {
			bad++
		}
	}
	for _, r := range rb.of(u) {
		w := back[r.from]
		if w < 0 {
			continue
		}
		wn := a.node(int(w), &tw)
		if !(int(r.pos) < len(wn.Out) && wn.Out[r.pos].To == i) {
			bad++
		}
	}
	return score, bad
}

// assignTiesV resolves small ambiguous partitions by exact maximum-weight assignment, like
// the reference; partitions are built from the bucket hash confirmed by exact local equality
// and visited in LocalID byte order (the reference's order), so results are identical.
func assignTiesV(a, b view, ia, ib *Idents, res *MatchResult, back []int32, ra, rb *revCSR, limit int, link func(x, y int32, basis uint8)) bool {
	var tmp Node
	type grp struct {
		lid    ID
		xs, ys []int32
	}
	part := func(g view, n int, ok func(i int) bool) map[uint64][][]int32 {
		m := map[uint64][][]int32{}
		for i := 0; i < n; i++ {
			if !ok(i) {
				continue
			}
			nd := g.node(i, &tmp)
			k := localKey(nd)
			placed := false
			var t2 Node
			for gi, grp := range m[k] {
				if localEq(g.node(int(grp[0]), &t2), nd) {
					m[k][gi] = append(m[k][gi], int32(i))
					placed = true
					break
				}
			}
			if !placed {
				m[k] = append(m[k], []int32{int32(i)})
			}
		}
		return m
	}
	pa := part(a, a.size(), func(i int) bool { return res.Map[i] < 0 && ia.Ambig[i] != AmbigPoisoned })
	pb := part(b, b.size(), func(j int) bool { return back[j] < 0 && ib.Ambig[j] != AmbigPoisoned })
	var groups []grp
	var t2 Node
	for k, ags := range pa {
		for _, xs := range ags {
			var ys []int32
			for _, cand := range pb[k] {
				if localEq(a.node(int(xs[0]), &tmp), b.node(int(cand[0]), &t2)) {
					ys = cand
					break
				}
			}
			if len(xs) < 2 || len(ys) < 2 || len(xs) > limit || len(ys) > limit {
				continue
			}
			groups = append(groups, grp{LocalID(a.node(int(xs[0]), &tmp)), xs, ys})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return bytes.Compare(groups[i].lid[:], groups[j].lid[:]) < 0 })
	progress := false
	for _, g := range groups {
		xs, ys := g.xs, g.ys
		w := make([][]int, len(xs))
		any := false
		for i, x := range xs {
			w[i] = make([]int, len(ys))
			for j, y := range ys {
				sc, bad := contextScoreV(a, b, x, y, res.Map, back, ra, rb)
				if bad == 0 {
					w[i][j] = sc
					if sc > 0 {
						any = true
					}
				} else {
					w[i][j] = -1
				}
			}
		}
		if !any {
			continue
		}
		asg, unique := maxAssign(w)
		res.Assigns++
		if !unique {
			res.Reject++
			continue
		}
		for i, j := range asg {
			if j >= 0 && w[i][j] > 0 && res.Map[xs[i]] < 0 && back[ys[j]] < 0 {
				link(xs[i], ys[j], BasisAssign)
				progress = true
			}
		}
	}
	return progress
}
