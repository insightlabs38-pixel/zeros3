package planner2

import "encoding/json"

// z3State is the bounded persistent state of the ported Z3-36 policies.
type z3State struct {
	Samples map[string][]string `json:"samples"`
	Stage   int                 `json:"stage"`
}

// Z3Policy ports the Z3-36 streaming policies exactly onto the planner interface:
// "greedy", "premium-cap", "late-bootstrap", "expected-value", plus "always-cdc".
type Z3Policy struct {
	Kind string
	st   z3State
}

func NewZ3(kind string) *Z3Policy {
	return &Z3Policy{Kind: kind, st: z3State{Samples: map[string][]string{}}}
}

func (p *Z3Policy) Name() string { return p.Kind }

func (p *Z3Policy) Snapshot() ([]byte, error) { return json.Marshal(p.st) }
func (p *Z3Policy) Restore(b []byte) error {
	st := z3State{}
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	if st.Samples == nil {
		st.Samples = map[string][]string{}
	}
	p.st = st
	return nil
}
func (p *Z3Policy) Event(*Env, Event) []Action    { return nil }
func (p *Z3Policy) Completed(*Env, Task) []Action { return nil }

type scored struct {
	scheme string
	c      Cand // cheapest form now
	std    Cand // standalone form (forms[0])
	cost   int64
}

func (p *Z3Policy) Arrive(env *Env, a Arrival) Decision {
	if p.Kind == "always-cdc" {
		return Decision{Scheme: CDC, Reason: "baseline"}
	}
	caps, err := env.Caps(a.Obj)
	if err != nil {
		return Decision{Scheme: CDC}
	}
	var all []scored
	dc := -1
	for _, s := range caps {
		forms, err := env.Analyze(a.Obj, s.Name)
		if err != nil || len(forms) == 0 {
			continue
		}
		if s.Name == CDC {
			dc = len(all)
		}
		best := forms[0]
		for _, f := range forms[1:] {
			if env.Marginal(f) < env.Marginal(best) {
				best = f
			}
		}
		all = append(all, scored{s.Name, best, forms[0], env.Marginal(best)})
	}
	if dc < 0 {
		return Decision{Scheme: CDC}
	}
	greedy := dc
	for i, x := range all {
		if x.cost < all[greedy].cost {
			greedy = i
		}
	}
	chosen := greedy
	at := p.st.Stage
	cc := all[dc].cost
	switch p.Kind {
	case "greedy":
	case "premium-cap":
		best := -1
		for i, x := range all {
			if i == dc {
				continue
			}
			if best < 0 || x.cost < all[best].cost {
				best = i
			}
		}
		if best >= 0 && float64(all[best].cost) <= 1.10*float64(cc) {
			chosen = best
		}
	case "late-bootstrap":
		if at > 0 {
			for i, x := range all {
				if i == dc {
					continue
				}
				if overlap(p.st.Samples[x.scheme], x.c.Sample) >= 0.25 && float64(x.cost) <= 1.25*float64(cc) {
					chosen = i
					break
				}
			}
		}
	case "expected-value":
		chosen = dc
		score := float64(cc)
		cdcOverlap := 0.10
		q := 0.25
		if at > 0 && a.Key != "" {
			q = 0.65
		}
		if prev := p.st.Samples[CDC]; len(prev) > 0 {
			cdcOverlap = overlap(prev, all[dc].c.Sample)
		}
		for i, x := range all {
			if i == dc {
				continue
			}
			reuse := 0.50
			if prev := p.st.Samples[x.scheme]; len(prev) > 0 {
				reuse = overlap(prev, x.c.Sample)
			}
			nextRep := float64(env.Standalone(x.std)) * (1 - reuse)
			nextCDC := float64(env.Standalone(all[dc].std)) * (1 - cdcOverlap)
			if s := float64(x.cost) + q*(nextRep-nextCDC); s < score {
				score, chosen = s, i
			}
		}
	}
	for _, x := range all {
		p.st.Samples[x.scheme] = append([]string(nil), x.c.Sample...)
	}
	p.st.Stage++
	return Decision{Scheme: all[chosen].scheme, Form: all[chosen].c.Form, Reason: p.Kind}
}
