package dcore

import "sort"

// Deterministic group planners over an exact accounting Model. All planners
// score complete assignments with Model.Eval (union semantics), so sharing
// between contents is always priced against the whole proposed set.

// PlanOpts constrains and scores plans.
type PlanOpts struct {
	RepackLive float64 // repack threshold used by the objective (default 1.0 = repack everything partly dead)
	Stage      string  // "logical", "gc", "repack" (default), "peak"
	MaxReadAmp float64 // options above this modeled read amplification are excluded (0 = none)
	KeepNewest int     // the newest K contents stay CDC (hot hybrid)
	TopK       int     // delta options considered per content (default 3)
	Neighbors  int     // pair-move window (default 2)
	AllowDelta bool
	AllowRep   bool
}

// Plan is a scored assignment.
type Plan struct {
	Assign []Form
	Total  int64
	Evals  int
	Name   string
}

func (o *PlanOpts) defaults() {
	if o.RepackLive == 0 {
		o.RepackLive = 1
	}
	if o.TopK == 0 {
		o.TopK = 3
	}
	if o.Neighbors == 0 {
		o.Neighbors = 2
	}
}

func (o PlanOpts) score(t Totals) int64 {
	switch o.Stage {
	case "logical":
		return t.Logical
	case "gc":
		return t.AfterGC
	case "peak":
		return t.Peak
	}
	return t.AfterRepack
}

// planner caches options and counts evaluations.
type planner struct {
	m     *Model
	o     PlanOpts
	evals int
	opts  [][]Form // allowed non-CDC forms per content, best first
}

func newPlanner(m *Model, o PlanOpts) *planner {
	o.defaults()
	p := &planner{m: m, o: o, opts: make([][]Form, len(m.Contents))}
	for i, c := range m.Contents {
		if i >= len(m.Contents)-o.KeepNewest {
			continue
		}
		type fo struct {
			f Form
			b int64
		}
		var fs []fo
		if o.AllowDelta {
			for k, d := range c.Delta {
				if o.MaxReadAmp > 0 && d.ReadAmp > o.MaxReadAmp {
					continue
				}
				fs = append(fs, fo{Form{FormDelta, k}, d.Bytes})
			}
		}
		sort.SliceStable(fs, func(a, b int) bool { return fs[a].b < fs[b].b })
		if len(fs) > o.TopK {
			fs = fs[:o.TopK]
		}
		for _, f := range fs {
			p.opts[i] = append(p.opts[i], f.f)
		}
		if o.AllowRep && c.Rep != nil && (o.MaxReadAmp == 0 || c.Rep.ReadAmp <= o.MaxReadAmp) {
			p.opts[i] = append(p.opts[i], Form{FormRep, 0})
		}
	}
	return p
}

const infeasible = int64(1) << 62

func (p *planner) cost(a []Form) int64 {
	p.evals++
	t, err := p.m.Eval(a, p.o.RepackLive)
	if err != nil {
		return infeasible
	}
	return p.o.score(t)
}

func (p *planner) result(name string, a []Form) Plan {
	return Plan{Assign: a, Total: p.cost(a), Evals: p.evals, Name: name}
}

// PlanCDC keeps everything as CDC (the baseline).
func PlanCDC(m *Model, o PlanOpts) Plan {
	p := newPlanner(m, o)
	return p.result("cdc", make([]Form, len(m.Contents)))
}

// complete fills contents from..n-1 with their cheapest FEASIBLE option (in
// index order, so earlier choices constrain depth), leaving the rest CDC.
func (p *planner) complete(a []Form, from int) {
	for i := from; i < len(a); i++ {
		a[i] = Form{}
		for _, f := range p.opts[i] {
			a[i] = f
			if p.cost(a) != infeasible {
				break
			}
			a[i] = Form{}
		}
	}
}

// PlanExclusiveGreedy reproduces the defective B2 group rule: for each pure
// strategy it compares the strategy's component/record bytes with the sum of
// EXCLUSIVE CDC bytes of the members (chunks referenced by one content only)
// and adopts the strategy with the better modeled gain. The adopted
// assignment is then scored exactly, which is where the loss shows up.
func PlanExclusiveGreedy(m *Model, o PlanOpts) Plan {
	best := PlanCDC(m, o)
	bestGain := int64(0)
	evals := best.Evals
	for _, kind := range []int{FormDelta, FormRep} {
		oo := o
		oo.AllowDelta, oo.AllowRep = kind == FormDelta, kind == FormRep
		p := newPlanner(m, oo)
		a := make([]Form, len(m.Contents))
		p.complete(a, 0)
		var ex, cost int64
		seen := map[int]bool{}
		for i, f := range a {
			if f.Kind == FormCDC {
				continue
			}
			ex += m.ExclusiveSaving(i)
			if f.Kind == FormDelta {
				cost += m.Contents[i].Delta[f.Opt].Bytes + m.RecordOverhead
			} else {
				r := m.Contents[i].Rep
				cost += r.RootBytes + m.RecordOverhead
				for _, c := range r.Comps {
					if !seen[c] {
						seen[c] = true
						cost += m.Chunks[c].Phys
					}
				}
			}
		}
		evals += p.evals
		if g := ex - cost; g > bestGain {
			bestGain = g
			best = p.result("exclusive-greedy", a)
		}
	}
	best.Name, best.Evals = "exclusive-greedy", evals
	return best
}

// PlanMarginalGreedy applies each content's best option when the exact total
// drops given the choices made so far (myopic, sees only already-decided sharing).
func PlanMarginalGreedy(m *Model, o PlanOpts) Plan {
	p := newPlanner(m, o)
	a := make([]Form, len(m.Contents))
	cur := p.cost(a)
	for i := range m.Contents {
		best := Form{}
		for _, f := range p.opts[i] {
			a[i] = f
			if c := p.cost(a); c < cur {
				cur, best = c, f
			}
		}
		a[i] = best
	}
	return p.result("marginal-greedy", a)
}

// PlanPure assigns every content its cheapest feasible option of one kind,
// then drops any content whose removal does not raise the exact group total.
func PlanPure(m *Model, o PlanOpts, kind int) Plan {
	o.AllowDelta, o.AllowRep = kind == FormDelta, kind == FormRep
	p := newPlanner(m, o)
	a := make([]Form, len(m.Contents))
	p.complete(a, 0)
	cur := p.cost(a)
	for improved := true; improved; {
		improved = false
		for i := range a {
			if a[i].Kind == FormCDC {
				continue
			}
			keep := a[i]
			a[i] = Form{}
			if c := p.cost(a); c <= cur {
				cur, improved = c, true
			} else {
				a[i] = keep
			}
		}
	}
	name := "delta-only"
	if kind == FormRep {
		name = "rep-only"
	}
	return p.result(name, a)
}

// PlanBestOfTwo is the B2 strategy with the exact (union) group score.
func PlanBestOfTwo(m *Model, o PlanOpts) Plan {
	d, r := PlanPure(m, o, FormDelta), PlanPure(m, o, FormRep)
	c := PlanCDC(m, o)
	best := c
	for _, x := range []Plan{d, r} {
		if x.Total < best.Total {
			best = x
		}
	}
	best.Name, best.Evals = "best-of-two", d.Evals+r.Evals+c.Evals
	return best
}

// PlanHillClimb improves start with single-content moves and neighbour-pair moves
// (contents within Neighbors positions that share a chunk or base) until no
// move lowers the exact total. Complexity per pass is O(n*(TopK+1)^2*Neighbors) evaluations.
func PlanHillClimb(m *Model, o PlanOpts, start []Form) Plan {
	p := newPlanner(m, o)
	a := append([]Form{}, start...)
	cur := p.cost(a)
	n := len(a)
	forms := func(i int) []Form { return append([]Form{{}}, p.opts[i]...) }
	for improved := true; improved; {
		improved = false
		for i := 0; i < n; i++ {
			for _, fi := range forms(i) {
				if fi == a[i] {
					continue
				}
				old := a[i]
				a[i] = fi
				if c := p.cost(a); c < cur {
					cur, improved = c, true
					continue
				}
				// pair move with each neighbour
				paired := false
				for j := max(0, i-p.o.Neighbors); j <= min(n-1, i+p.o.Neighbors) && !paired; j++ {
					if j == i {
						continue
					}
					oj := a[j]
					for _, fj := range forms(j) {
						if fj == oj {
							continue
						}
						a[j] = fj
						if c := p.cost(a); c < cur {
							cur, improved, paired = c, true, true
							break
						}
					}
					if !paired {
						a[j] = oj
					}
				}
				if !paired {
					a[i] = old
				}
			}
		}
	}
	return p.result("hill-climb", a)
}

// PlanBeam is a chronological beam search. A state's score completes the
// undecided suffix with each content's cheapest feasible option, so early
// choices are priced against the sharing the suffix will realistically provide.
func PlanBeam(m *Model, o PlanOpts, width int) Plan {
	p := newPlanner(m, o)
	n := len(m.Contents)
	type st struct {
		a []Form
		s int64
	}
	beam := []st{{make([]Form, n), 0}}
	for i := 0; i < n; i++ {
		var next []st
		for _, b := range beam {
			for _, f := range append([]Form{{}}, p.opts[i]...) {
				a := append([]Form{}, b.a...)
				a[i] = f
				if p.cost(a) == infeasible {
					continue
				}
				full := append([]Form{}, a...)
				p.complete(full, i+1)
				next = append(next, st{a, p.cost(full)})
			}
		}
		sort.SliceStable(next, func(x, y int) bool { return next[x].s < next[y].s })
		if len(next) > width {
			next = next[:width]
		}
		beam = next
	}
	return p.result("beam", beam[0].a)
}

// PlanExhaustive enumerates every combination of {CDC, TopK options}; only
// for small groups (the oracle). It returns false when the space exceeds limit.
func PlanExhaustive(m *Model, o PlanOpts, limit int) (Plan, bool) {
	p := newPlanner(m, o)
	space := 1
	for i := range m.Contents {
		space *= 1 + len(p.opts[i])
		if space > limit {
			return Plan{}, false
		}
	}
	a := make([]Form, len(m.Contents))
	best := append([]Form{}, a...)
	bc := p.cost(a)
	var rec func(i int)
	rec = func(i int) {
		if i == len(a) {
			if c := p.cost(a); c < bc {
				bc, best = c, append([]Form{}, a...)
			}
			return
		}
		a[i] = Form{}
		rec(i + 1)
		for _, f := range p.opts[i] {
			a[i] = f
			rec(i + 1)
		}
		a[i] = Form{}
	}
	rec(0)
	return Plan{Assign: best, Total: bc, Evals: p.evals, Name: "exhaustive"}, true
}

// assignFromRoots gives every content outside roots its cheapest delta whose
// bases are already placed (lower index) and keep depth <= MaxDepth; a content
// with no feasible delta becomes a root (CDC). roots[i] selects the root form.
func (p *planner) assignFromRoots(roots []Form, isRoot []bool) []Form {
	m := p.m
	n := len(m.Contents)
	a := make([]Form, n)
	depth := make([]int, n)
	for i := 0; i < n; i++ {
		if isRoot[i] {
			a[i] = roots[i]
			continue
		}
		bestB, best := int64(-1), Form{}
		bestD := 0
		for _, f := range p.opts[i] {
			if f.Kind != FormDelta {
				continue
			}
			d := m.Contents[i].Delta[f.Opt]
			dep, ok := 0, true
			for _, b := range d.Bases {
				if b >= i {
					ok = false
					break
				}
				dep = max(dep, depth[b]+1)
			}
			if !ok || (m.MaxDepth > 0 && dep > m.MaxDepth) || (m.MaxBases > 0 && len(d.Bases) > m.MaxBases) {
				continue
			}
			if bestB < 0 || d.Bytes < bestB {
				bestB, best, bestD = d.Bytes, f, dep
			}
		}
		if bestB >= 0 {
			a[i], depth[i] = best, bestD
		} else {
			a[i] = roots[i]
		}
	}
	return a
}

// PlanRoots is the depth-aware group optimizer: local search (add, drop,
// swap) over the set of contents kept as roots, each candidate set completed
// optimally per content and scored with the exact union model. Each pass costs
// O(n^2) evaluations; the number of passes is bounded by n.
func PlanRoots(m *Model, o PlanOpts) Plan {
	p := newPlanner(m, o)
	n := len(m.Contents)
	rootForm := make([]Form, n)
	isRoot := make([]bool, n)
	for i := range isRoot {
		isRoot[i] = true
	}
	score := func() (int64, []Form) {
		a := p.assignFromRoots(rootForm, isRoot)
		return p.cost(a), a
	}
	cur, _ := score()
	for pass := 0; pass <= n; pass++ {
		improved := false
		for i := 0; i < n; i++ {
			// toggle root-ness of i
			isRoot[i] = !isRoot[i]
			if c, _ := score(); c < cur {
				cur, improved = c, true
				continue
			}
			isRoot[i] = !isRoot[i]
			// switch a root between CDC and rep
			if isRoot[i] && m.Contents[i].Rep != nil && len(p.opts[i]) > 0 {
				old := rootForm[i]
				for _, f := range p.opts[i] {
					if f.Kind == FormRep && f != old {
						rootForm[i] = f
					} else if old.Kind == FormRep {
						rootForm[i] = Form{}
					} else {
						continue
					}
					if c, _ := score(); c < cur {
						cur, improved = c, true
						old = rootForm[i]
					} else {
						rootForm[i] = old
					}
				}
			}
		}
		// swap moves: replace root i by root j (i is demoted, j promoted)
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if i == j || !isRoot[i] || isRoot[j] {
					continue
				}
				isRoot[i], isRoot[j] = false, true
				if c, _ := score(); c < cur {
					cur, improved = c, true
				} else {
					isRoot[i], isRoot[j] = true, false
				}
			}
		}
		if !improved {
			break
		}
	}
	_, a := score()
	return p.result("roots-local-search", a)
}
