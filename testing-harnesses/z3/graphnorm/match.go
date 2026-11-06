package graphnorm

import "sort"

// Match bases, strongest first. Only BasisExact is a structural proof (equal identity
// hash, unique on both sides); all others are contextual and flagged so callers can price
// or reject them. Nothing here is ever required for exactness: reconstruction verifies bytes.
const (
	BasisNone     uint8 = iota
	BasisExact          // unique identical identity in both graphs
	BasisDupOrder       // identical duplicates paired by occurrence order (interchangeable only if contexts agree)
	BasisContext        // unique best candidate by matched-neighborhood agreement, zero conflicts
	BasisAssign         // optimal bounded bipartite assignment inside an ambiguous partition
)

type MatchResult struct {
	Map     []int32 // a -> b, -1 unmatched
	Basis   []uint8
	Count   [5]int
	Reject  int // candidates rejected for ambiguity or conflict
	Passes  int
	Assigns int
}

type MatchOptions struct {
	MaxPasses   int
	AssignLimit int  // max partition size for exact bipartite assignment (0 = disabled)
	DupByOrder  bool // pair equal-size duplicate classes by occurrence order
}

func DefaultMatch() MatchOptions {
	return MatchOptions{MaxPasses: 64, AssignLimit: 24, DupByOrder: true}
}

type radj struct {
	from int32
	pos  int32
	kind uint16
}

func (g *Graph) reverse() [][]radj {
	r := make([][]radj, len(g.Nodes))
	for i := range g.Nodes {
		for p, e := range g.Nodes[i].Out {
			if !g.dangling(e) {
				r[e.To] = append(r[e.To], radj{int32(i), int32(p), e.Kind})
			}
		}
	}
	return r
}

// Match reconciles graph a with graph b under renumbering, insertion, deletion and reorder.
func Match(a, b *Graph, ia, ib *Idents, opt MatchOptions) *MatchResult {
	na, nb := len(a.Nodes), len(b.Nodes)
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
	// phase 1/2: identity classes
	ca := map[ID][]int32{}
	cb := map[ID][]int32{}
	for i := 0; i < na; i++ {
		if usable(ia.Ambig[i]) {
			ca[ia.ID[i]] = append(ca[ia.ID[i]], int32(i))
		}
	}
	for i := 0; i < nb; i++ {
		if usable(ib.Ambig[i]) {
			cb[ib.ID[i]] = append(cb[ib.ID[i]], int32(i))
		}
	}
	keys := make([]ID, 0, len(ca))
	for k := range ca {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	for _, k := range keys {
		xs, ys := ca[k], cb[k]
		if len(ys) == 0 {
			continue
		}
		if len(xs) == 1 && len(ys) == 1 && ia.Ambig[xs[0]] != AmbigSymmetric && ib.Ambig[ys[0]] != AmbigSymmetric {
			link(xs[0], ys[0], BasisExact)
		}
	}
	revA, revB := a.reverse(), b.reverse()
	for pass := 0; pass < opt.MaxPasses; pass++ {
		res.Passes++
		progress := false
		// index unmatched B nodes by local identity
		idx := map[ID][]int32{}
		for j := 0; j < nb; j++ {
			if back[j] < 0 && usable(ib.Ambig[j]) {
				l := LocalID(&b.Nodes[j])
				idx[l] = append(idx[l], int32(j))
			}
		}
		type cand struct {
			a, b  int32
			score int
		}
		bestFor := map[int32][]cand{}
		for i := 0; i < na; i++ {
			if res.Map[i] >= 0 || !usable(ia.Ambig[i]) {
				continue
			}
			vs := idx[LocalID(&a.Nodes[i])]
			for _, u := range vs {
				sc, bad := contextScore(a, b, int32(i), u, res.Map, back, revA, revB)
				if bad > 0 {
					continue
				}
				if sc > 0 {
					bestFor[int32(i)] = append(bestFor[int32(i)], cand{int32(i), u, sc})
				}
			}
		}
		// accept mutual unique-best candidates
		type pair struct{ a, b int32 }
		var acc []pair
		bestOfB := map[int32][]cand{}
		for _, cs := range bestFor {
			sort.Slice(cs, func(x, y int) bool { return cs[x].score > cs[y].score })
			if len(cs) > 1 && cs[0].score == cs[1].score {
				res.Reject++
				continue
			}
			bestOfB[cs[0].b] = append(bestOfB[cs[0].b], cs[0])
		}
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
			progress = assignTies(a, b, ia, ib, res, back, revA, revB, opt.AssignLimit, link)
		}
		if !progress && opt.DupByOrder {
			// pair remaining equal-size duplicate classes by occurrence order
			for _, k := range keys {
				var xs, ys []int32
				for _, x := range ca[k] {
					if res.Map[x] < 0 {
						xs = append(xs, x)
					}
				}
				for _, y := range cb[k] {
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

// contextScore counts matched-neighborhood agreements (and conflicts) between a-node i and
// b-node u: out-edge positions, set-edge membership, and in-edges.
func contextScore(a, b *Graph, i, u int32, m, back []int32, ra, rb [][]radj) (score, bad int) {
	na, nb := &a.Nodes[i], &b.Nodes[u]
	if len(na.Out) != len(nb.Out) {
		return 0, 1
	}
	for p, e := range na.Out {
		if a.dangling(e) || b.dangling(nb.Out[p]) {
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
		have := map[[2]int32]int{}
		for _, e := range nb.Set {
			have[[2]int32{int32(e.Kind), e.To}]++
		}
		for _, e := range na.Set {
			if a.dangling(e) {
				return 0, 1
			}
			if t := m[e.To]; t >= 0 {
				if have[[2]int32{int32(e.Kind), t}] > 0 {
					score++
				} else {
					bad++
				}
			}
		}
	}
	// in-edges: for every matched referrer w of i at pos p, b-referrer m[w] must hold u at p
	for _, r := range ra[i] {
		w := m[r.from]
		if w < 0 {
			continue
		}
		wn := &b.Nodes[w]
		if int(r.pos) < len(wn.Out) && wn.Out[r.pos].To == u && wn.Out[r.pos].Kind == r.kind {
			score++
		} else {
			bad++
		}
	}
	for _, r := range rb[u] {
		w := back[r.from]
		if w < 0 {
			continue
		}
		wn := &a.Nodes[w]
		if !(int(r.pos) < len(wn.Out) && wn.Out[r.pos].To == i) {
			bad++
		}
	}
	return score, bad
}

// assignTies resolves small ambiguous partitions (same local identity, tied context scores)
// by exact maximum-weight bipartite assignment; only strictly positive, unique optima are linked.
func assignTies(a, b *Graph, ia, ib *Idents, res *MatchResult, back []int32, ra, rb [][]radj, limit int, link func(x, y int32, basis uint8)) bool {
	groupA := map[ID][]int32{}
	groupB := map[ID][]int32{}
	for i := range a.Nodes {
		if res.Map[i] < 0 && ia.Ambig[i] != AmbigPoisoned {
			l := LocalID(&a.Nodes[i])
			groupA[l] = append(groupA[l], int32(i))
		}
	}
	for j := range b.Nodes {
		if back[j] < 0 && ib.Ambig[j] != AmbigPoisoned {
			l := LocalID(&b.Nodes[j])
			groupB[l] = append(groupB[l], int32(j))
		}
	}
	progress := false
	ks := make([]ID, 0, len(groupA))
	for k := range groupA {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return string(ks[i][:]) < string(ks[j][:]) })
	for _, k := range ks {
		xs, ys := groupA[k], groupB[k]
		if len(xs) < 2 || len(ys) < 2 || len(xs) > limit || len(ys) > limit {
			continue
		}
		w := make([][]int, len(xs))
		any := false
		for i, x := range xs {
			w[i] = make([]int, len(ys))
			for j, y := range ys {
				sc, bad := contextScore(a, b, x, y, res.Map, back, ra, rb)
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

// maxAssign finds a maximum-weight assignment (entries < 0 forbidden) by Hungarian-style
// DP over the smaller side's subsets is exponential; instead use the O(n^3) Kuhn-Munkres on a
// padded square matrix, and report uniqueness by re-solving with each chosen edge forbidden.
func maxAssign(w [][]int) ([]int, bool) {
	n := max(len(w), len(w[0]))
	const forb = -1 << 20
	c := make([][]int, n)
	for i := range c {
		c[i] = make([]int, n)
		for j := range c[i] {
			v := 0
			if i < len(w) && j < len(w[0]) {
				v = w[i][j]
				if v < 0 {
					v = forb
				}
			}
			c[i][j] = -v // minimize cost
		}
	}
	solve := func() ([]int, int) {
		inf := 1 << 40
		u := make([]int, n+1)
		v := make([]int, n+1)
		p := make([]int, n+1)
		way := make([]int, n+1)
		for i := 1; i <= n; i++ {
			p[0] = i
			j0 := 0
			minv := make([]int, n+1)
			used := make([]bool, n+1)
			for j := range minv {
				minv[j] = inf
			}
			for {
				used[j0] = true
				i0, delta, j1 := p[j0], inf, 0
				for j := 1; j <= n; j++ {
					if !used[j] {
						cur := c[i0-1][j-1] - u[i0] - v[j]
						if cur < minv[j] {
							minv[j], way[j] = cur, j0
						}
						if minv[j] < delta {
							delta, j1 = minv[j], j
						}
					}
				}
				for j := 0; j <= n; j++ {
					if used[j] {
						u[p[j]] += delta
						v[j] -= delta
					} else {
						minv[j] -= delta
					}
				}
				j0 = j1
				if p[j0] == 0 {
					break
				}
			}
			for {
				j1 := way[j0]
				p[j0] = p[j1]
				j0 = j1
				if j0 == 0 {
					break
				}
			}
		}
		asg := make([]int, n)
		cost := 0
		for j := 1; j <= n; j++ {
			asg[p[j]-1] = j - 1
			cost += c[p[j]-1][j-1]
		}
		return asg, cost
	}
	asg, best := solve()
	// uniqueness: forbidding any positive chosen edge must strictly worsen the optimum
	unique := true
	for i := 0; i < len(w) && unique; i++ {
		j := asg[i]
		if j >= len(w[0]) || w[i][j] <= 0 {
			continue
		}
		old := c[i][j]
		c[i][j] = -forb
		_, alt := solve()
		c[i][j] = old
		if alt <= best {
			unique = false
		}
	}
	out := make([]int, len(w))
	for i := range out {
		out[i] = -1
		if asg[i] < len(w[0]) {
			out[i] = asg[i]
		}
	}
	return out, unique
}
