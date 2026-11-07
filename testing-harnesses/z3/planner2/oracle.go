package planner2

import (
	"fmt"
	"math"
	"sort"
)

// PlanChoice fixes one object's arrival-time form. Form "" lets the store pick the
// cheapest valid form of the scheme; "std" forces the standalone (flattened) form.
type PlanChoice struct{ Scheme, Form string }

// PlanPolicy replays a fixed plan. It is CLAIRVOYANT by construction (the plan was
// searched with the whole history); it exists only as the labelled offline oracle.
type PlanPolicy struct {
	Plan map[int]PlanChoice
	Tag  string
}

func (p *PlanPolicy) Name() string                  { return "plan:" + p.Tag }
func (p *PlanPolicy) Snapshot() ([]byte, error)     { return nil, nil }
func (p *PlanPolicy) Restore([]byte) error          { return nil }
func (p *PlanPolicy) Event(*Env, Event) []Action    { return nil }
func (p *PlanPolicy) Completed(*Env, Task) []Action { return nil }
func (p *PlanPolicy) Arrive(env *Env, a Arrival) Decision {
	c := p.Plan[a.Obj]
	if c.Scheme == "" || c.Scheme == CDC {
		return Decision{Scheme: CDC}
	}
	if _, err := env.Analyze(a.Obj, c.Scheme); err != nil {
		return Decision{Scheme: CDC}
	}
	return Decision{Scheme: c.Scheme, Form: c.Form, Reason: "plan"}
}

// SubWorld keeps one lineage's events plus all ticks (lineages share no components).
func SubWorld(w *World, li int) *World {
	out := &World{Name: w.Name + "/" + w.Lineages[li].Name(), Lineages: []Lineage{w.Lineages[li]}, Simulated: w.Simulated}
	obj := map[int]int{}
	n := 0
	for _, e := range w.Events {
		switch e.Kind {
		case Tick:
		default:
			if e.Lineage != li {
				continue
			}
		}
		if e.Kind == Arrive {
			obj[e.Obj] = n
			e.Obj = n
			n++
		} else if e.Kind != Tick {
			e.Obj = obj[e.Obj]
		}
		e.Lineage = 0
		out.Events = append(out.Events, e)
	}
	for i := range out.Events {
		out.Events[i].Seq = i
	}
	return out
}

// OracleResult reports the best static clairvoyant plan found.
type OracleResult struct {
	Lineage    string
	Cost       float64 // objective (dollars) of the best plan
	FinalBytes int64   // final physical bytes of the best plan
	Plan       []PlanChoice
	Plans      int
	Exact      bool // every plan in the declared space was evaluated
	Space      int
}

func schemeOptions(l Lineage) []PlanChoice {
	opts := []PlanChoice{{CDC, ""}}
	for _, s := range l.Schemes() {
		if s.Name == CDC || !s.Ready {
			continue
		}
		opts = append(opts, PlanChoice{s.Name, ""})
		if s.Dep == "chain" {
			opts = append(opts, PlanChoice{s.Name, "std"})
		}
	}
	return opts
}

// Oracle enumerates all arrival-time plans of one lineage (up to maxPlans) and
// returns the cheapest under objective(ledger). When the space is larger it runs
// coordinate descent from the best baseline plans and reports Exact=false.
func Oracle(sub *World, cfg Config, objective func(*Ledger) float64, maxPlans int) (OracleResult, error) {
	l := sub.Lineages[0]
	n := l.Len()
	opts := schemeOptions(l)
	cfg.SkipValidate = true
	cfg.FailMigrate = 0
	res := OracleResult{Lineage: l.Name(), Cost: math.Inf(1)}
	space := 1.0
	for i := 0; i < n; i++ {
		space *= float64(len(opts))
	}
	res.Space = int(math.Min(space, 1e9))
	eval := func(idx []int) (float64, int64, error) {
		plan := map[int]PlanChoice{}
		for t, o := range idx {
			plan[t] = opts[o]
		}
		led, err := Run(sub, &PlanPolicy{Plan: plan, Tag: "oracle"}, cfg)
		if err != nil {
			return 0, 0, err
		}
		return objective(led), led.FinalPhys, nil
	}
	consider := func(idx []int) error {
		c, b, err := eval(idx)
		if err != nil {
			return err
		}
		res.Plans++
		if c < res.Cost-1e-18 {
			res.Cost, res.FinalBytes = c, b
			res.Plan = res.Plan[:0]
			for _, o := range idx {
				res.Plan = append(res.Plan, opts[o])
			}
		}
		return nil
	}
	if space <= float64(maxPlans) {
		idx := make([]int, n)
		for {
			if err := consider(idx); err != nil {
				return res, err
			}
			k := n - 1
			for k >= 0 {
				idx[k]++
				if idx[k] < len(opts) {
					break
				}
				idx[k] = 0
				k--
			}
			if k < 0 {
				break
			}
		}
		res.Exact = true
		return res, nil
	}
	// bounded: coordinate descent from every uniform plan; labelled an upper bound
	starts := make([][]int, 0, len(opts))
	for o := range opts {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = o
		}
		starts = append(starts, idx)
	}
	for _, idx := range starts {
		cur, _, err := eval(idx)
		if err != nil {
			return res, err
		}
		for improved := true; improved && res.Plans < maxPlans; {
			improved = false
			for t := 0; t < n; t++ {
				orig := idx[t]
				for o := range opts {
					if o == orig {
						continue
					}
					idx[t] = o
					c, _, err := eval(idx)
					if err != nil {
						return res, err
					}
					res.Plans++
					if c < cur-1e-18 {
						cur, orig, improved = c, o, true
					}
				}
				idx[t] = orig
			}
		}
		if err := consider(idx); err != nil {
			return res, err
		}
	}
	return res, nil
}

// PlanString renders a plan compactly.
func PlanString(p []PlanChoice) string {
	s := ""
	for i, c := range p {
		if i > 0 {
			s += " "
		}
		f := c.Scheme
		if c.Form != "" {
			f += "/" + c.Form
		}
		s += f
	}
	return s
}

var _ = fmt.Sprint
var _ = sort.Ints
