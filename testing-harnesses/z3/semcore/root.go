package semcore

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// Group-aware canonical root selection.
//
// A component's identity (CID) does not depend on how it is stored, so which members of a
// family of related tensors are physical roots is a free choice made after the fact. The
// online planner takes arrival order; PlanRoots evaluates every exactly-provable derivation
// between members of a group (value-identical sets reachable through conversions, layouts and
// exact residuals), then chooses the set of roots minimizing total stored bytes under the
// depth-1 rule (every derived member names a physical root). Groups up to exactLimit members
// are solved exactly by subset enumeration; larger ones by greedy pruning plus swap search,
// and the heuristic's regret is measured against the exact solver on the small groups.

// exactLimit bounds exhaustive subset enumeration (2^n) per group.
const exactLimit = 14

// RootEdge is one proven derivation of To from From.
type RootEdge struct {
	From, To int
	Cost     int64
	Rec      *Record
	How      string
}

// RootGroup is a connected family of components with its candidate derivations.
type RootGroup struct {
	Nodes  []ID
	Raw    []int64 // stored bytes if the node is a physical root (blob + record)
	payLen []int64
	recLen []int64
	Pay    []ID // payload digest of each node (a blob is shared by equal digests)
	Edges  []RootEdge
	// Chosen roots (indexes into Nodes), the resulting cost, and the cost of the layout
	// currently in the store, for before/after accounting.
	Roots   []int
	Cost    int64
	Current int64
	Exact   bool // true when solved by exhaustive enumeration
	// Greedy, when exact, is the heuristic's cost on the same group (regret measurement).
	Greedy int64
}

// RootPlan is the result of PlanRoots.
type RootPlan struct {
	Groups          []*RootGroup
	Before, After   int64 // sum of group costs: current layout vs chosen layout
	Evaluated       int   // pairwise derivations evaluated
	Singletons      int
	ExactGroups     int
	HeuristicGroups int
	// Regret is the sum over exactly-solved groups of (greedy cost - exact cost).
	Regret int64
}

// PlanRoots builds the groups of the store's tensor components and solves each. It reads
// payloads (bounded cache) and proves every candidate edge but changes nothing.
func (p *Planner) PlanRoots() (*RootPlan, error) {
	recs := p.S.All()
	var tens []*Record
	for _, r := range recs {
		if r.Desc.Kind == KindTensor && int(r.PayloadLen) >= p.Opt.MinBytes {
			tens = append(tens, r)
		}
	}
	idx := map[ID]int{}
	for i, r := range tens {
		idx[r.CID] = i
	}
	// union-find over: existing parent links and proven candidate edges
	uf := make([]int, len(tens))
	for i := range uf {
		uf[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for uf[x] != x {
			uf[x] = uf[uf[x]]
			x = uf[x]
		}
		return x
	}
	union := func(a, b int) { uf[find(a)] = find(b) }
	type pair struct{ from, to int }
	edges := map[pair]*RootEdge{}
	plan := &RootPlan{}
	// 1. candidate edges: every ordered pair of same-element-count tensors that passes the
	// signature gate, evaluated by the same proven-derivation search the online planner uses.
	byElems := map[uint64][]int{}
	for i, r := range tens {
		n, err := r.Desc.Elems()
		if err != nil {
			continue
		}
		byElems[n] = append(byElems[n], i)
	}
	keys := make([]uint64, 0, len(byElems))
	for k := range byElems {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		mem := byElems[k]
		if len(mem) < 2 || (p.PlanFilter != nil && !p.PlanFilter(k)) {
			continue
		}
		for _, to := range mem {
			tr := tens[to]
			payload, err := p.parentPayload(tr.CID)
			if err != nil {
				return nil, err
			}
			tsig, haveSig := SigOf(int(tr.Desc.DType), payload)
			d := tr.Desc
			rb := rowBytes(&d)
			limit := (float64(len(payload)) + 96) * p.Opt.AcceptFrac
			type cand struct {
				from  int
				score float64
			}
			var cs []cand
			for _, from := range mem {
				if from == to || tens[from].Payload == tr.Payload {
					continue // same bytes: the blob is shared, no derivation needed
				}
				fr := tens[from]
				pd := int(fr.Desc.DType)
				if _, ok := convOps(pd, int(d.DType)); !ok && pd != int(d.DType) {
					continue
				}
				sc := 0.0
				if haveSig {
					ps, ok := p.sigOf(fr)
					if !ok {
						continue
					}
					if sc = SigDist(tsig, ps); sc > 0.08 {
						continue
					}
				} else if pd != int(d.DType) {
					continue
				}
				cs = append(cs, cand{from, sc})
			}
			sort.SliceStable(cs, func(i, j int) bool { return cs[i].score < cs[j].score })
			if len(cs) > 12 {
				cs = cs[:12]
			}
			for _, c := range cs {
				r, cost, how := p.evalCandidate(&d, tens[c.from], payload, tr.Payload, rb, limit)
				plan.Evaluated++
				if r == nil {
					continue
				}
				edges[pair{c.from, to}] = &RootEdge{From: c.from, To: to, Cost: int64(len(r.Marshal())), Rec: r, How: how}
				_ = cost
				union(c.from, to)
			}
		}
	}
	// existing derivations also tie components together (their parents may sit outside the
	// candidate set only through a recipe-valued chain, which is not rooted here)
	for i, r := range tens {
		for _, par := range r.Parents {
			if j, ok := idx[par]; ok {
				union(i, j)
			}
		}
	}
	// 2. groups
	gm := map[int]*RootGroup{}
	gi := map[int]map[int]int{} // group root -> tens index -> position
	var order []int
	for i := range tens {
		g := find(i)
		if gm[g] == nil {
			gm[g] = &RootGroup{}
			gi[g] = map[int]int{}
			order = append(order, g)
		}
		gi[g][i] = len(gm[g].Nodes)
		gm[g].Nodes = append(gm[g].Nodes, tens[i].CID)
		gm[g].Pay = append(gm[g].Pay, tens[i].Payload)
		rawRec := &Record{CID: tens[i].CID, Desc: tens[i].Desc, PayloadLen: tens[i].PayloadLen, Payload: tens[i].Payload}
		gm[g].Raw = append(gm[g].Raw, tens[i].PayloadLen+int64(len(rawRec.Marshal())))
		gm[g].payLen = append(gm[g].payLen, tens[i].PayloadLen)
		gm[g].recLen = append(gm[g].recLen, int64(len(rawRec.Marshal())))
	}
	var pairs []pair
	for k := range edges {
		pairs = append(pairs, k)
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].from != pairs[b].from {
			return pairs[a].from < pairs[b].from
		}
		return pairs[a].to < pairs[b].to
	})
	for _, k := range pairs {
		e := edges[k]
		g := find(k.from)
		m := gi[g]
		e.From, e.To = m[k.from], m[k.to]
		gm[g].Edges = append(gm[g].Edges, *e)
	}
	for _, g := range order {
		grp := gm[g]
		// current cost: physical roots (non-derived) pay raw, derived pay their record
		cur := map[ID]bool{}
		for i, id := range grp.Nodes {
			r := p.S.Rec(id)
			if r.Derived() {
				grp.Current += int64(len(r.Marshal()))
			} else {
				grp.Current += int64(len(r.Marshal()))
				if !cur[grp.Pay[i]] {
					cur[grp.Pay[i]] = true
					grp.Current += r.PayloadLen
				}
			}
		}
		if len(grp.Nodes) == 1 {
			plan.Singletons++
		}
		solveGroup(grp)
		if grp.Exact {
			plan.ExactGroups++
			plan.Regret += grp.Greedy - grp.Cost
		} else {
			plan.HeuristicGroups++
		}
		plan.Before += grp.Current
		plan.After += grp.Cost
		plan.Groups = append(plan.Groups, grp)
	}
	return plan, nil
}

// evalRoots prices a root set: roots pay raw (blobs shared per payload digest); every other
// node must be reachable by one proven edge from a root and pays the cheaper of that edge
// and promoting itself to a root is NOT assumed (infeasible sets return ok=false).
func evalRoots(g *RootGroup, isRoot []bool, best [][]*RootEdge) (int64, bool) {
	var cost int64
	seen := map[ID]bool{}
	for i, r := range isRoot {
		if !r {
			continue
		}
		cost += g.recLen[i]
		if !seen[g.Pay[i]] {
			seen[g.Pay[i]] = true
			cost += g.payLen[i]
		}
	}
	for v, r := range isRoot {
		if r {
			continue
		}
		var bestc int64 = -1
		for _, e := range best[v] {
			if isRoot[e.From] && (bestc < 0 || e.Cost < bestc) {
				bestc = e.Cost
			}
		}
		if bestc < 0 {
			return 0, false
		}
		cost += bestc
	}
	return cost, true
}

func solveGroup(g *RootGroup) {
	n := len(g.Nodes)
	in := make([][]*RootEdge, n)
	for i := range g.Edges {
		e := &g.Edges[i]
		in[e.To] = append(in[e.To], e)
	}
	// greedy / local search (always computed: it is the scalable solver)
	greedyRoots, greedyCost := greedyRoots(g, in)
	if n <= exactLimit {
		bestCost := int64(-1)
		var bestMask uint32
		isRoot := make([]bool, n)
		for mask := uint32(1); mask < 1<<n; mask++ {
			for i := 0; i < n; i++ {
				isRoot[i] = mask>>i&1 == 1
			}
			c, ok := evalRoots(g, isRoot, in)
			if ok && (bestCost < 0 || c < bestCost) {
				bestCost, bestMask = c, mask
			}
		}
		g.Exact = true
		g.Cost = bestCost
		g.Greedy = greedyCost
		for i := 0; i < n; i++ {
			if bestMask>>i&1 == 1 {
				g.Roots = append(g.Roots, i)
			}
		}
		return
	}
	g.Cost, g.Roots = greedyCost, greedyRoots
}

func greedyRoots(g *RootGroup, in [][]*RootEdge) ([]int, int64) {
	n := len(g.Nodes)
	isRoot := make([]bool, n)
	for i := range isRoot {
		isRoot[i] = true
	}
	cost, _ := evalRoots(g, isRoot, in)
	for {
		bi, bc := -1, cost
		for i := 0; i < n; i++ {
			if !isRoot[i] {
				continue
			}
			isRoot[i] = false
			if c, ok := evalRoots(g, isRoot, in); ok && c < bc {
				bi, bc = i, c
			}
			isRoot[i] = true
		}
		if bi < 0 {
			break
		}
		isRoot[bi], cost = false, bc
	}
	// swap improvement: replace one root by one non-root
	for improved := true; improved; {
		improved = false
		for a := 0; a < n && !improved; a++ {
			if !isRoot[a] {
				continue
			}
			for b := 0; b < n; b++ {
				if isRoot[b] {
					continue
				}
				isRoot[a], isRoot[b] = false, true
				if c, ok := evalRoots(g, isRoot, in); ok && c < cost {
					cost, improved = c, true
					break
				}
				isRoot[a], isRoot[b] = true, false
			}
		}
	}
	var roots []int
	for i, r := range isRoot {
		if r {
			roots = append(roots, i)
		}
	}
	return roots, cost
}

var errPlanStale = errors.New("semcore: root plan does not match the store")

// ApplyRoots rewrites the store to the planned layout. Every new derivation was proven when
// the plan was built; here it is proven again against the current store before anything is
// written, new root blobs are written before any record changes, freed blobs are deleted only
// after the whole-store closure check passes, and a failed check restores every record.
func (p *Planner) ApplyRoots(plan *RootPlan) (freed int64, err error) {
	type change struct {
		old *Record
		neu *Record
	}
	var changes []change
	var newRoots []ID
	for _, g := range plan.Groups {
		isRoot := make([]bool, len(g.Nodes))
		for _, r := range g.Roots {
			isRoot[r] = true
		}
		in := make([][]*RootEdge, len(g.Nodes))
		for i := range g.Edges {
			e := &g.Edges[i]
			in[e.To] = append(in[e.To], e)
		}
		for v, id := range g.Nodes {
			old := p.S.Rec(id)
			if old == nil || old.CID != id {
				return 0, errPlanStale
			}
			if isRoot[v] {
				if old.Derived() {
					newRoots = append(newRoots, id)
				}
				continue
			}
			var pick *RootEdge
			for _, e := range in[v] {
				if isRoot[e.From] && (pick == nil || e.Cost < pick.Cost) {
					pick = e
				}
			}
			if pick == nil {
				return 0, errPlanStale
			}
			if len(old.Parents) == 1 && old.Parents[0] == g.Nodes[pick.From] && bytes.Equal(old.Marshal(), pick.Rec.Marshal()) {
				continue // already in the planned form
			}
			changes = append(changes, change{old, pick.Rec})
		}
	}
	// prove every new record from its parent's authenticated payload (value level: a parent that
	// is currently derived from the target is not a cycle in the planned layout, whose
	// parents are all physical roots)
	for _, c := range changes {
		if len(c.neu.Parents) != 1 {
			return 0, errPlanStale
		}
		pp, err := p.parentPayload(c.neu.Parents[0])
		if err != nil {
			return 0, err
		}
		if err := p.S.proveDerived(c.neu, pp); err != nil {
			return 0, fmt.Errorf("semcore: planned derivation of %s failed its proof: %w", c.neu.CID.Short(), err)
		}
	}
	// 1. new roots: write blobs (identity unchanged)
	var rolled []*Record
	for _, id := range newRoots {
		rolled = append(rolled, p.S.Rec(id))
		if err := p.S.Materialize(id); err != nil {
			return 0, err
		}
	}
	// 2. swap records
	var blobs []ID
	for _, c := range changes {
		rolled = append(rolled, c.old)
		p.S.mu.Lock()
		p.S.rec[c.old.CID] = c.neu
		p.S.mu.Unlock()
		if err := p.S.B.PutRecord(c.neu.CID, c.neu.Marshal()); err != nil {
			return 0, p.rollback(rolled, err)
		}
		if !c.old.Derived() {
			blobs = append(blobs, c.old.Payload)
			freed += c.old.PayloadLen
		}
	}
	// 3. whole-store closure check (depth, cycles, missing components), then free blobs
	for _, r := range p.S.All() {
		if _, err := p.S.Closure(r.CID); err != nil {
			return 0, p.rollback(rolled, err)
		}
	}
	for _, id := range blobs {
		shared := false
		for _, o := range p.S.ByPayload(id) {
			if or := p.S.Rec(o); !or.Derived() {
				shared = true
				break
			}
		}
		if !shared {
			p.S.B.DeleteBlob(id)
		} else {
			freed -= 0
		}
	}
	p.pcache, p.porder, p.pbytes, p.rtabs = map[ID][]byte{}, nil, 0, nil
	return freed, nil
}

func (p *Planner) rollback(old []*Record, cause error) error {
	for _, r := range old {
		p.S.mu.Lock()
		p.S.rec[r.CID] = r
		p.S.mu.Unlock()
		p.S.B.PutRecord(r.CID, r.Marshal())
	}
	return fmt.Errorf("semcore: root rewrite rolled back: %w", cause)
}

// Describe renders a group for diagnostics: members (dtype, shape, payload bytes) and every
// proven edge with its cost and derivation.
func (g *RootGroup) Describe(s *Store) string {
	var b []byte
	for i, id := range g.Nodes {
		r := s.Rec(id)
		b = append(b, fmt.Sprintf("  n%d %s%v pay=%d derived=%v\n", i, DTypeName(int(r.Desc.DType)), r.Desc.Shape, r.PayloadLen, r.Derived())...)
	}
	for _, e := range g.Edges {
		b = append(b, fmt.Sprintf("  e n%d->n%d cost=%d %s patches=%d\n", e.From, e.To, e.Cost, e.How, len(e.Rec.Patches))...)
	}
	b = append(b, fmt.Sprintf("  roots=%v cost=%d current=%d\n", g.Roots, g.Cost, g.Current)...)
	return string(b)
}
