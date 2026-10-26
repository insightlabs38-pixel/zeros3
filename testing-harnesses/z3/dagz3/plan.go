package dagz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// The planner chooses one already-validated candidate representation per logical object.
// It is deliberately not part of repz3: it sees content-addressed object sets, never wire
// objects, and its choice leaves no trace in what is stored except the representations that
// were chosen. Candidate generation (sniffing, parsing, codec analysis, producer settings) is
// somebody else's job; the planner receives exact candidates.
//
// Cost is the persisted bytes of the UNION of the chosen candidates' objects that the store
// does not already hold, plus a configured per-object overhead, plus (optionally) a read
// term. Objects are deduplicated by SHA-256: a child representation used by two chosen parents
// is paid for once.

// Obj is one logical top-level object: the exact output every candidate must reproduce.
type Obj struct {
	Name string
	Out  repz3.SHA
	Len  int64
}

// Cand is one candidate representation DAG for one object.
type Cand struct {
	ID     string
	Object string
	Out    repz3.SHA // the exact output identity this candidate claims to rebuild
	Len    int64
	Root   repz3.SHA
	Objs   map[repz3.SHA]int64         // every object the candidate needs: representations, components, tables, indexes
	Class  map[repz3.SHA]repz3.DepRole // optional accounting class per object (default: payload)
	Reps   map[repz3.SHA][]repz3.SHA   // representation object -> child representation objects (DERIVED edges)
	Read   int64                       // expected bytes fetched by the reference read, for the read-aware objective
}

// Problem is a planning instance.
type Problem struct {
	Objects     []Obj
	Cands       []Cand
	Base        map[repz3.SHA]bool // objects the store already holds
	PerObject   int64              // physical overhead charged per newly stored object
	ReadPer1000 int64              // read term weight: sum(Read) * ReadPer1000 / 1000 (0 = storage only)
	MaxDepth    int                // 0 = repz3.HardMaxDepth
}

var ErrPlan = errors.New("dagz3: invalid planning problem")

func bad(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrPlan}, a...)...) }

func addChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

func mulChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	if a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}

// depthOf returns the nesting depth of the candidate (root = 1) or an error for a cycle or an
// edge to a representation the candidate does not contain.
func (c *Cand) depthOf() (int, error) {
	memo := map[repz3.SHA]int{}
	state := map[repz3.SHA]int{} // 1 visiting, 2 done
	var visit func(id repz3.SHA) (int, error)
	visit = func(id repz3.SHA) (int, error) {
		switch state[id] {
		case 1:
			return 0, bad("candidate %q: dependency cycle at %x", c.ID, id[:6])
		case 2:
			return memo[id], nil
		}
		if _, ok := c.Objs[id]; !ok {
			return 0, bad("candidate %q: representation %x is not among its objects", c.ID, id[:6])
		}
		state[id] = 1
		d := 1
		for _, ch := range c.Reps[id] {
			if _, known := c.Reps[ch]; !known {
				if _, ok := c.Objs[ch]; !ok {
					return 0, bad("candidate %q: unknown child %x", c.ID, ch[:6])
				}
				return 0, bad("candidate %q: child %x is not a representation of the candidate", c.ID, ch[:6])
			}
			cd, err := visit(ch)
			if err != nil {
				return 0, err
			}
			d = max(d, cd+1)
		}
		state[id], memo[id] = 2, d
		return d, nil
	}
	if _, ok := c.Reps[c.Root]; !ok {
		return 0, bad("candidate %q: root %x is not a representation of the candidate", c.ID, c.Root[:6])
	}
	d, err := visit(c.Root)
	if err != nil {
		return 0, err
	}
	for id := range c.Reps { // representations the root does not reach are unused objects, not free ones
		if state[id] != 2 {
			return 0, bad("candidate %q: representation %x is unreachable from the root", c.ID, id[:6])
		}
	}
	return d, nil
}

type ccand struct {
	id     string
	object int
	objs   []int
	depth  int
	read   int64 // weighted read term
	priv   int64 // cost of the objects only this logical object's candidates contain
	index  int   // position in Problem.Cands
}

type compiled struct {
	p      *Problem
	names  []string
	size   []int64 // size + overhead of each distinct object (0 when the store holds it)
	raw    []int64
	shas   []repz3.SHA
	cands  []ccand
	byObj  [][]int
	suffix []int64 // admissible lower bound on the remaining objects, in search order
	order  []int   // search order of logical objects
}

// Validate checks everything a plan relies on and rejects the instance otherwise; the planner
// never repairs or silently skips a candidate.
func (p *Problem) Validate() error {
	maxDepth := p.MaxDepth
	if maxDepth <= 0 {
		maxDepth = repz3.HardMaxDepth
	}
	maxDepth = min(maxDepth, repz3.HardMaxDepth)
	if p.PerObject < 0 || p.ReadPer1000 < 0 {
		return bad("negative cost parameter")
	}
	objs := map[string]Obj{}
	for _, o := range p.Objects {
		if _, dup := objs[o.Name]; dup {
			return bad("duplicate object %q", o.Name)
		}
		objs[o.Name] = o
	}
	seen := map[string]*Cand{}
	have := map[string]int{}
	for i := range p.Cands {
		c := &p.Cands[i]
		o, ok := objs[c.Object]
		if !ok {
			return bad("candidate %q names unknown object %q", c.ID, c.Object)
		}
		if c.Out != o.Out || c.Len != o.Len {
			return bad("candidate %q: output identity differs from object %q", c.ID, c.Object)
		}
		if prev, dup := seen[c.ID]; dup {
			if !sameCand(prev, c) {
				return bad("duplicate candidate id %q with different content", c.ID)
			}
			return bad("duplicate candidate id %q", c.ID)
		}
		seen[c.ID] = c
		d, err := c.depthOf()
		if err != nil {
			return err
		}
		if d > maxDepth {
			return fmt.Errorf("%w: candidate %q has depth %d (maximum %d): %w", ErrPlan, c.ID, d, maxDepth, repz3.ErrDepth)
		}
		var sum int64
		for id, n := range c.Objs {
			if n < 0 {
				return bad("candidate %q: negative size for %x", c.ID, id[:6])
			}
			var ok bool
			if sum, ok = addChecked(sum, n); !ok {
				return bad("candidate %q: size overflow", c.ID)
			}
		}
		if _, ok := mulChecked(int64(len(c.Objs)), p.PerObject); !ok {
			return bad("candidate %q: overhead overflow", c.ID)
		}
		if _, ok := mulChecked(c.Read, p.ReadPer1000); !ok || c.Read < 0 {
			return bad("candidate %q: read cost overflow", c.ID)
		}
		have[c.Object]++
	}
	for _, o := range p.Objects {
		if have[o.Name] == 0 {
			return bad("object %q has no valid candidate", o.Name)
		}
	}
	return nil
}

func sameCand(a, b *Cand) bool {
	if a.Root != b.Root || a.Object != b.Object || len(a.Objs) != len(b.Objs) {
		return false
	}
	for k, v := range a.Objs {
		if w, ok := b.Objs[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func (p *Problem) compile() (*compiled, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	cp := &compiled{p: p}
	idx := map[repz3.SHA]int{}
	oi := map[string]int{}
	for i, o := range p.Objects {
		oi[o.Name] = i
		cp.names = append(cp.names, o.Name)
	}
	cp.byObj = make([][]int, len(p.Objects))
	owner := map[int]map[int]bool{} // object index -> logical objects whose candidates contain it
	for i := range p.Cands {
		c := &p.Cands[i]
		d, _ := c.depthOf()
		cc := ccand{id: c.ID, object: oi[c.Object], depth: d, index: i}
		ids := make([]repz3.SHA, 0, len(c.Objs))
		for id := range c.Objs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(a, b int) bool { return bytes.Compare(ids[a][:], ids[b][:]) < 0 })
		for _, id := range ids {
			k, ok := idx[id]
			if !ok {
				k = len(cp.shas)
				idx[id] = k
				cp.shas = append(cp.shas, id)
				cp.raw = append(cp.raw, c.Objs[id])
				if p.Base[id] {
					cp.size = append(cp.size, 0)
				} else {
					cp.size = append(cp.size, c.Objs[id]+p.PerObject)
				}
			} else if cp.raw[k] != c.Objs[id] {
				return nil, bad("object %x listed with sizes %d and %d", id[:6], cp.raw[k], c.Objs[id])
			}
			cc.objs = append(cc.objs, k)
			if owner[k] == nil {
				owner[k] = map[int]bool{}
			}
			owner[k][cc.object] = true
		}
		cc.read = c.Read * p.ReadPer1000 / 1000
		cp.byObj[cc.object] = append(cp.byObj[cc.object], len(cp.cands))
		cp.cands = append(cp.cands, cc)
	}
	// total cost must not overflow: bound it by the sum of every distinct object
	var tot int64
	for _, s := range cp.size {
		var ok bool
		if tot, ok = addChecked(tot, s); !ok {
			return nil, bad("total cost overflow")
		}
	}
	// private lower bound per candidate
	minPriv := make([]int64, len(p.Objects))
	for o := range p.Objects {
		minPriv[o] = math.MaxInt64
	}
	for ci := range cp.cands {
		c := &cp.cands[ci]
		for _, k := range c.objs {
			if len(owner[k]) == 1 {
				c.priv += cp.size[k]
			}
		}
		minPriv[c.object] = min(minPriv[c.object], c.priv+c.read)
	}
	// search order: the most expensive decisions first
	cp.order = make([]int, len(p.Objects))
	for i := range cp.order {
		cp.order[i] = i
	}
	sort.SliceStable(cp.order, func(a, b int) bool { return minPriv[cp.order[a]] > minPriv[cp.order[b]] })
	cp.suffix = make([]int64, len(cp.order)+1)
	for i := len(cp.order) - 1; i >= 0; i-- {
		cp.suffix[i] = cp.suffix[i+1] + minPriv[cp.order[i]]
	}
	return cp, nil
}

// selection tracks reference counts of objects under a partial choice.
type selection struct {
	cp     *compiled
	count  []int32
	choice []int // candidate index (into cp.cands) per logical object, -1 = none
	cost   int64
}

func newSel(cp *compiled) *selection {
	s := &selection{cp: cp, count: make([]int32, len(cp.shas)), choice: make([]int, len(cp.byObj))}
	for i := range s.choice {
		s.choice[i] = -1
	}
	return s
}

func (s *selection) marginal(ci int) int64 {
	c := &s.cp.cands[ci]
	m := c.read
	for _, k := range c.objs {
		if s.count[k] == 0 {
			m += s.cp.size[k]
		}
	}
	return m
}

func (s *selection) add(ci int) {
	c := &s.cp.cands[ci]
	s.cost += c.read
	for _, k := range c.objs {
		if s.count[k] == 0 {
			s.cost += s.cp.size[k]
		}
		s.count[k]++
	}
	s.choice[c.object] = ci
}

func (s *selection) remove(ci int) {
	c := &s.cp.cands[ci]
	s.cost -= c.read
	for _, k := range c.objs {
		s.count[k]--
		if s.count[k] == 0 {
			s.cost -= s.cp.size[k]
		}
	}
	s.choice[c.object] = -1
}

// Plan is a chosen candidate per object and its exact accounting.
type Plan struct {
	Method   string
	Choice   map[string]string // object -> candidate id
	NewObjs  int
	Payload  int64 // new component bytes
	Recipe   int64 // new representation objects
	Index    int64 // new record sections
	Table    int64 // new format tables and nodes
	Overhead int64 // per-object overhead on the new objects
	Read     int64 // weighted read term
	Total    int64
	Shared   int   // new objects used by more than one chosen candidate
	SharedB  int64 // their bytes (counted once)
	Edges    int   // DERIVED edges among the chosen representation objects
	SharedRe int   // chosen representation objects with more than one parent edge
	Depth    int
	Nodes    int64 // search nodes (exact method)
	Complete bool  // exact search finished
}

func (cp *compiled) plan(method string, s *selection) *Plan {
	p := cp.p
	pl := &Plan{Method: method, Choice: map[string]string{}, Complete: true}
	used := make([]int32, len(cp.shas))
	reps := map[repz3.SHA]int{} // rep object -> parent edges among chosen candidates (deduplicated per edge)
	edge := map[[2]repz3.SHA]bool{}
	isRep := map[repz3.SHA]bool{}
	for o, ci := range s.choice {
		if ci < 0 {
			continue
		}
		c := &cp.cands[ci]
		cand := &p.Cands[c.index]
		pl.Choice[cp.names[o]] = c.id
		pl.Read += c.read
		pl.Depth = max(pl.Depth, c.depth)
		for _, k := range c.objs {
			used[k]++
		}
		for r, kids := range cand.Reps {
			isRep[r] = true
			for _, ch := range kids {
				if e := [2]repz3.SHA{r, ch}; !edge[e] {
					edge[e] = true
					reps[ch]++
				}
			}
		}
	}
	class := map[repz3.SHA]repz3.DepRole{}
	for _, ci := range s.choice {
		if ci >= 0 {
			for id, r := range p.Cands[cp.cands[ci].index].Class {
				class[id] = r
			}
		}
	}
	for k, n := range used {
		if n == 0 || p.Base[cp.shas[k]] {
			continue
		}
		id := cp.shas[k]
		pl.NewObjs++
		b := cp.raw[k]
		switch r := class[id]; {
		case isRep[id] || r == repz3.DepRep:
			pl.Recipe += b
		case r == repz3.DepIndex:
			pl.Index += b
		case r == repz3.DepTable:
			pl.Table += b
		default:
			pl.Payload += b
		}
		pl.Overhead += p.PerObject
		if n > 1 {
			pl.Shared++
			pl.SharedB += b
		}
	}
	for _, n := range reps {
		pl.Edges += n
		if n > 1 {
			pl.SharedRe++
		}
	}
	pl.Total = pl.Payload + pl.Recipe + pl.Index + pl.Table + pl.Overhead + pl.Read
	return pl
}

// Check verifies that the plan's accounting equals the planner's objective (exact sum).
func (pl *Plan) check(cost int64) error {
	if pl.Total != cost {
		return fmt.Errorf("dagz3: plan accounting %d differs from the objective %d", pl.Total, cost)
	}
	return nil
}

func (s *selection) pickCheapest(o int) int {
	best, bm := -1, int64(math.MaxInt64)
	for _, ci := range s.cp.byObj[o] {
		if m := s.marginal(ci); m < bm || (m == bm && s.cp.cands[ci].id < s.cp.cands[best].id) {
			best, bm = ci, m
		}
	}
	return best
}

// Independent chooses, for every object on its own, the candidate that is cheapest against
// the existing store, ignoring every other object's choice (the admission rule of the lab).
func (p *Problem) Independent() (*Plan, error) {
	cp, err := p.compile()
	if err != nil {
		return nil, err
	}
	s := newSel(cp)
	for o := range cp.byObj {
		alone := newSel(cp)
		s.add(alone.pickCheapest(o))
	}
	pl := cp.plan("independent", s)
	return pl, pl.check(s.cost)
}

// Marginal chooses objects in input order, each against the store plus everything chosen
// before it, so later objects see earlier objects' new components and children.
func (p *Problem) Marginal() (*Plan, error) {
	cp, err := p.compile()
	if err != nil {
		return nil, err
	}
	s := newSel(cp)
	for o := range cp.byObj {
		s.add(s.pickCheapest(o))
	}
	pl := cp.plan("marginal", s)
	return pl, pl.check(s.cost)
}

// Refine is single-swap local search from the marginal choice: change one object's candidate
// while the total keeps dropping.
func (p *Problem) Refine() (*Plan, error) {
	cp, err := p.compile()
	if err != nil {
		return nil, err
	}
	s := newSel(cp)
	for o := range cp.byObj {
		s.add(s.pickCheapest(o))
	}
	s.improve()
	pl := cp.plan("marginal+swap", s)
	return pl, pl.check(s.cost)
}

func (s *selection) improve() {
	for improved := true; improved; {
		improved = false
		for o := range s.cp.byObj {
			cur := s.choice[o]
			best, bc := cur, s.cost
			s.remove(cur)
			for _, ci := range s.cp.byObj[o] {
				if ci == cur {
					continue
				}
				if c := s.cost + s.marginal(ci); c < bc {
					best, bc = ci, c
				}
			}
			s.add(best)
			if best != cur {
				improved = true
			}
		}
	}
}

// Exact is branch and bound over one candidate per object. The bound is the sum, over the
// objects not yet decided, of the cheapest cost of the components only that object's
// candidates can contain (nothing another object chooses can ever share them). maxNodes
// caps the search (0 = unlimited); when it is hit the best plan found so far is returned
// with Complete=false.
func (p *Problem) Exact(maxNodes int64) (*Plan, error) {
	cp, err := p.compile()
	if err != nil {
		return nil, err
	}
	// incumbent from the refined heuristic
	inc := newSel(cp)
	for o := range cp.byObj {
		inc.add(inc.pickCheapest(o))
	}
	inc.improve()
	bestCost := inc.cost
	best := append([]int(nil), inc.choice...)
	s := newSel(cp)
	var nodes int64
	complete := true
	var dfs func(i int)
	dfs = func(i int) {
		if !complete {
			return
		}
		if nodes++; maxNodes > 0 && nodes > maxNodes {
			complete = false
			return
		}
		if s.cost+cp.suffix[i] >= bestCost {
			return
		}
		if i == len(cp.order) {
			bestCost = s.cost
			copy(best, s.choice)
			return
		}
		o := cp.order[i]
		type opt struct {
			ci int
			m  int64
		}
		var opts []opt
		for _, ci := range cp.byObj[o] {
			opts = append(opts, opt{ci, s.marginal(ci)})
		}
		sort.Slice(opts, func(a, b int) bool {
			if opts[a].m != opts[b].m {
				return opts[a].m < opts[b].m
			}
			return cp.cands[opts[a].ci].id < cp.cands[opts[b].ci].id
		})
		for _, op := range opts {
			s.add(op.ci)
			dfs(i + 1)
			s.remove(op.ci)
		}
	}
	dfs(0)
	fin := newSel(cp)
	for _, ci := range best {
		fin.add(ci)
	}
	pl := cp.plan("exact", fin)
	pl.Nodes, pl.Complete = nodes, complete
	return pl, pl.check(fin.cost)
}

// ---------------------------------------------------------------------------------------
// Candidates from real representations

// CandFromBundle persists a bundle into an empty scratch store, proves it, and describes its
// object set, classes and DERIVED edges (read from the serialized objects, not the bundle).
func CandFromBundle(id, object string, b *kinds.Bundle, out []byte) (Cand, error) {
	st := repz3.NewMemStore()
	root, _, err := b.Persist(st)
	if err != nil {
		return Cand{}, err
	}
	return CandFromStore(id, object, st, kinds.NewEnvLimits(st, Lim), root, out)
}

// CandFromStore describes the candidate rooted at root in st: it must rebuild out exactly
// and every object persisted must be reachable (no hidden storage).
func CandFromStore(id, object string, st *repz3.MemStore, env *repz3.Env, root repz3.SHA, out []byte) (Cand, error) {
	if err := Exact(env, root, out); err != nil {
		return Cand{}, err
	}
	reach, err := env.Reachable(root)
	if err != nil {
		return Cand{}, err
	}
	g, err := LoadGraph(env, root)
	if err != nil {
		return Cand{}, err
	}
	c := Cand{ID: id, Object: object, Out: sha256.Sum256(out), Len: int64(len(out)), Root: root,
		Objs: map[repz3.SHA]int64{}, Class: map[repz3.SHA]repz3.DepRole{}, Reps: map[repz3.SHA][]repz3.SHA{}}
	for _, oid := range reach.Order {
		n, err := st.Size(oid)
		if err != nil {
			return Cand{}, err
		}
		c.Objs[oid] = n
		c.Class[oid] = reach.Role[oid]
	}
	for rid, rep := range g.Reps {
		kids := []repz3.SHA{}
		for _, m := range rep.Members {
			if m.Role == repz3.Derived {
				kids = append(kids, m.Child)
			}
		}
		c.Reps[rid] = kids
	}
	return c, nil
}
