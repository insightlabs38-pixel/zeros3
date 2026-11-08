package planner2

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// AParams selects the mechanisms of the adaptive family; named presets give the
// policies compared in the study and their ablations.
type AParams struct {
	Name                                                   string
	Lineage, Class, Conf, Life, Read, Bandit, Group, Defer bool
	PremiumCap                                             float64 // max first-sight premium as a fraction of the CDC standalone bytes
	K                                                      float64 // prior strength (pseudo-observations)
	MinConf                                                float64 // probe reuse below this skips rep analysis for related-looking objects
	Horizon                                                int     // future related arrivals considered
	PUTAnalyzeMS                                           float64 // analyse on the PUT path only when the estimate is below this
	ColdAge                                                int     // days before archival analysis of CDC objects
	MaxMoves                                               int     // group local-search move budget
	FixedCap                                               bool    // PremiumCap is part of the policy definition, not tuned
	QueuePerTick                                           int
	Margin                                                 float64 // required net benefit as a fraction of the object size
}

// Prior is calibrated on earlier workloads only (see Calibrate); it seeds, never replaces, online learning.
type Prior struct {
	Ratio  float64 `json:"ratio"`   // std_rep / std_cdc, geometric mean over rep schemes
	RhoRep float64 `json:"rho_rep"` // reuse of a later related arrival under rep
	RhoCDC float64 `json:"rho_cdc"`
	Q      float64 `json:"q"` // P(a lineage receives another arrival)
	N      int     `json:"n"`
}

func DefaultPrior() Prior { return Prior{Ratio: 1.0, RhoRep: 0.5, RhoCDC: 0.5, Q: 0.5} }

type ema struct {
	V, N float64
}

func (e *ema) obs(x, alpha float64) {
	if e.N == 0 {
		e.V = x
	} else {
		e.V += alpha * (x - e.V)
	}
	e.N++
}
func (e *ema) est(prior, k float64) float64 { return (e.V*e.N + prior*k) / (e.N + k) }

type linStat struct {
	N      int
	Last   int
	Gap    ema
	RhoC   ema
	RhoS   map[string]*ema
	Scheme string
}

type clsStat struct {
	Reach, End            float64
	Ratio, RhoRep, RhoCDC ema
	Life                  ema
	ReadsDay              ema
	WinA, WinB            map[string]float64 // bandit Beta counts per scheme
}

type aState struct {
	Lin      map[string]*linStat `json:"lin"`
	Cls      map[string]*clsStat `json:"cls"`
	ObjKey   map[int]string      `json:"objkey"`
	Pending  map[string]int      `json:"pending"`
	Seen     int                 `json:"seen"`
	LastTick int                 `json:"lasttick"`
}

// PredRec records one prediction for post-hoc calibration (not persisted state).
type PredRec struct {
	Obj         int
	Scheme      string
	Rho, Q, Net float64
	Admitted    bool
}

type Adaptive struct {
	P     AParams
	Prior Prior
	S     aState
	Trace []PredRec `json:"-"`
}

func NewAdaptive(p AParams, pr Prior) *Adaptive {
	a := &Adaptive{P: p, Prior: pr}
	a.S = aState{Lin: map[string]*linStat{}, Cls: map[string]*clsStat{}, ObjKey: map[int]string{}, Pending: map[string]int{}}
	return a
}

func (a *Adaptive) Name() string { return a.P.Name }

func (a *Adaptive) Snapshot() ([]byte, error) {
	return json.Marshal(struct {
		P  AParams
		Pr Prior
		S  aState
	}{a.P, a.Prior, a.S})
}

func (a *Adaptive) Restore(b []byte) error {
	var x struct {
		P  AParams
		Pr Prior
		S  aState
	}
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	a.P, a.Prior, a.S = x.P, x.Pr, x.S
	if a.S.Lin == nil {
		a.S.Lin = map[string]*linStat{}
	}
	if a.S.Cls == nil {
		a.S.Cls = map[string]*clsStat{}
	}
	if a.S.ObjKey == nil {
		a.S.ObjKey = map[int]string{}
	}
	if a.S.Pending == nil {
		a.S.Pending = map[string]int{}
	}
	return nil
}

func (a *Adaptive) cls(sig string) *clsStat {
	c := a.S.Cls[sig]
	if c == nil {
		c = &clsStat{WinA: map[string]float64{}, WinB: map[string]float64{}}
		a.S.Cls[sig] = c
	}
	return c
}

func signature(caps []SchemeInfo) string {
	var n []string
	for _, c := range caps {
		n = append(n, c.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

// lineageKey identifies the lineage causally: with Conf the byte-sketch probe's
// base wins over a (possibly misleading) key hint.
func (a *Adaptive) lineageKey(env *Env, ar Arrival) (string, Probe) {
	var pc Probe
	if a.P.Conf || a.P.Group || a.P.Lineage {
		if p, err := env.Probe(ar.Obj, CDC); err == nil {
			pc = p
		}
	}
	if a.P.Conf && pc.Related && pc.Base >= 0 {
		if k, ok := a.S.ObjKey[pc.Base]; ok {
			return k, pc
		}
	}
	if ar.Key != "" {
		return "k:" + ar.Key, pc
	}
	return fmt.Sprintf("o:%d", ar.Obj), pc
}

type cand struct {
	scheme, form string
	c            Cand
	marg, std    int64
	net, prem    float64
	feasible     bool
}

func (a *Adaptive) bytesPerRead(pb PriceBook) float64 {
	bv := pb.ByteValue()
	if bv == 0 {
		return 0
	}
	return pb.ReadIOUSDGB / 1e9 / bv
}

func (a *Adaptive) Arrive(env *Env, ar Arrival) Decision {
	cfg := env.Config()
	caps, err := env.Caps(ar.Obj)
	if err != nil {
		return Decision{Scheme: CDC}
	}
	sig := signature(caps)
	cl := a.cls(sig)
	lk, pc := a.lineageKey(env, ar)
	ls := a.S.Lin[lk]
	n := 0
	if ls == nil {
		ls = &linStat{RhoS: map[string]*ema{}}
		a.S.Lin[lk] = ls
	} else {
		n = ls.N
	}
	a.S.ObjKey[ar.Obj] = lk
	cdcForms, _ := env.Forms(ar.Obj, CDC)
	if len(cdcForms) == 0 {
		return Decision{Scheme: CDC}
	}
	cdcF := cdcForms[0]
	mC := env.Marginal(cdcF)
	stdC := env.Standalone(cdcF)
	cl.Reach++
	// ---- expectations ------------------------------------------------------
	qc := 1 - (cl.End+(1-a.Prior.Q)*a.P.K)/(cl.Reach+a.P.K)
	if !a.P.Class {
		qc = a.Prior.Q
	}
	q := qc
	if a.P.Lineage {
		k := a.P.K
		q = (k*qc + math.Max(float64(n), 0)) / (k + math.Max(float64(n), 0) + 1)
	}
	rhoC := a.Prior.RhoCDC
	if a.P.Class {
		rhoC = cl.RhoCDC.est(a.Prior.RhoCDC, a.P.K)
	}
	if a.P.Lineage && ls.RhoC.N > 0 {
		rhoC = ls.RhoC.est(rhoC, a.P.K/2)
	}
	if n > 0 && mC < stdC {
		o := 1 - float64(mC)/float64(stdC)
		ls.RhoC.obs(o, 0.5)
		cl.RhoCDC.obs(o, 0.2)
	}
	gap := 1.0
	if ls.Gap.N > 0 {
		gap = math.Max(ls.Gap.V, 1)
	}
	if n > 0 {
		ls.Gap.obs(float64(ar.Day-ls.Last), 0.4)
	}
	life := math.Inf(1)
	if a.P.Life && cl.Life.N > 0 {
		life = math.Max(cl.Life.V, 1)
	}
	var out Decision
	var all []cand
	bv := cfg.Prices.ByteValue()
	for _, s := range caps {
		if s.Name == CDC || !s.Ready {
			continue
		}
		est := float64(ar.Size) / (1 << 20) / math.Max(s.AnalysisMiBs, 0.1) * 1000
		// Cheap format probe: skip hopeless rep analysis for objects that look related to nothing.
		var pf Probe
		pfOK := false
		if a.P.Conf {
			if p, err := env.Probe(ar.Obj, s.Name); err == nil {
				pf, pfOK = p, true
			}
		}
		if a.P.Conf && pfOK && n > 0 && pf.Rho < a.P.MinConf && !pf.Related {
			// related lineage but this representation sees no reuse: still allow the
			// first-sight/archive path through deferral rather than synchronous analysis
			continue
		}
		if a.P.Defer && est > a.P.PUTAnalyzeMS {
			// expensive: store CDC now, schedule background analysis if the prior expects a net win
			gain := a.expectedNet(env, cl, ls, n, q, rhoC, stdC, a.Prior.Ratio, s, cfg, gap, life, ar.Size)
			if gain > 0 {
				out.Defer = append(out.Defer, Action{Kind: ActEnqueue, Obj: ar.Obj, Scheme: s.Name, Priority: gain / math.Max(est, 1), Reason: "defer-expensive"})
			}
			continue
		}
		forms, err := env.Analyze(ar.Obj, s.Name)
		if err != nil || len(forms) == 0 {
			if errors.Is(err, ErrBudget) && a.P.Defer {
				out.Defer = append(out.Defer, Action{Kind: ActEnqueue, Obj: ar.Obj, Scheme: s.Name, Priority: 1, Reason: "put-budget"})
			}
			continue
		}
		stdS := env.Standalone(forms[0])
		cl.Ratio.obs(float64(stdS)/math.Max(float64(stdC), 1), 0.2)
		best := cand{scheme: s.Name, net: math.Inf(1)}
		for _, f := range forms {
			if !a.feasible(env, ar, f) {
				continue
			}
			m := env.Marginal(f)
			if !best.feasible || m < best.marg {
				best = cand{scheme: s.Name, form: f.Form, c: f, marg: m, std: stdS, feasible: true}
			}
		}
		if !best.feasible {
			continue
		}
		// realized reuse of this scheme (delta form available => base stored in scheme)
		rhoS := a.rhoS(cl, ls, s.Name, pf, pfOK)
		if best.c.Depth > 0 || best.c.Base >= 0 || best.marg < best.std {
			r := 1 - float64(best.marg)/math.Max(float64(best.std), 1)
			if n > 0 {
				e := ls.RhoS[s.Name]
				if e == nil {
					e = &ema{}
					ls.RhoS[s.Name] = e
				}
				e.obs(r, 0.5)
				cl.RhoRep.obs(r, 0.2)
				rhoS = e.est(rhoS, a.P.K/2)
			}
		}
		prem := float64(best.marg - mC)
		delta := float64(stdC)*(1-rhoC) - float64(best.std)*(1-rhoS)
		if a.P.Bandit {
			theta := a.thompson(cl, s.Name, ar.Obj)
			delta *= theta
		}
		g := 0.0
		for j := 1; j <= a.P.Horizon; j++ {
			w := math.Pow(q, float64(j))
			if !math.IsInf(life, 1) {
				w *= math.Exp(-float64(j) * gap / life)
			}
			g += w * delta
		}
		readCost := 0.0
		if a.P.Read && bv > 0 {
			readCost = a.readPenalty(cl, best.c, cdcF, cfg)
		}
		best.prem = prem
		best.net = prem - g + readCost
		a.Trace = append(a.Trace, PredRec{Obj: ar.Obj, Scheme: s.Name, Rho: rhoS, Q: q})
		// investment cap: premium over CDC may not exceed PremiumCap of the standalone CDC size
		if prem > a.P.PremiumCap*float64(stdC) {
			best.net = math.Inf(1)
		}
		if pfOK && pf.Rho < a.P.MinConf && n > 0 {
			best.net += math.Max(prem, 0) // confidence veto: unrelated looking, pay no premium
		}
		all = append(all, best)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].net < all[j].net })
	out.Scheme, out.Reason = CDC, "cdc"
	if len(all) > 0 && all[0].net < -a.P.Margin*float64(ar.Size) {
		out.Scheme, out.Form, out.Reason = all[0].scheme, all[0].form, fmt.Sprintf("ev %.0f", all[0].net)
		for i := range a.Trace {
			if a.Trace[i].Obj == ar.Obj && a.Trace[i].Scheme == out.Scheme {
				a.Trace[i].Admitted, a.Trace[i].Net = true, all[0].net
			}
		}
	}
	// a rep that CDC cannot match but whose base is not stored: group bootstrap
	if a.P.Group && out.Scheme == CDC && n > 0 {
		out.Defer = append(out.Defer, a.groupHints(env, ar, caps, pc, ls, lk)...)
	}
	ls.N++
	ls.Last = ar.Day
	return out
}

func (a *Adaptive) feasible(env *Env, ar Arrival, f Cand) bool {
	cfg := env.Config()
	if a.P.Read && ar.TierHint == "hot" && cfg.HotAmp64Cap > 0 && f.Scheme != CDC && f.Amp64K > cfg.HotAmp64Cap {
		return false
	}
	return true
}

func (a *Adaptive) rhoS(cl *clsStat, ls *linStat, scheme string, pf Probe, pfOK bool) float64 {
	r := a.Prior.RhoRep
	if a.P.Class && cl.RhoRep.N > 0 {
		r = cl.RhoRep.est(r, a.P.K)
	}
	if e := ls.RhoS[scheme]; a.P.Lineage && e != nil {
		r = e.est(r, a.P.K/2)
	}
	if pfOK && pf.Related {
		r = (r + pf.Rho) / 2
	}
	return r
}

func (a *Adaptive) thompson(cl *clsStat, scheme string, obj int) float64 {
	al, be := cl.WinA[scheme]+1, cl.WinB[scheme]+1
	// deterministic Beta-ish draw: mean plus hash-derived deviation of one sd
	m := al / (al + be)
	sd := math.Sqrt(al * be / ((al + be) * (al + be) * (al + be + 1)))
	u := hash01(1, uint64(obj), strh(scheme), 5)
	return math.Max(0, math.Min(1, m+(u-0.5)*2*sd))
}

// readPenalty prices extra served bytes of rep vs CDC over the expected reads of the
// object, in stored-byte equivalents through the explicit price book.
func (a *Adaptive) readPenalty(cl *clsStat, rep, cdcF Cand, cfg Config) float64 {
	bpr := a.bytesPerRead(cfg.Prices)
	reads := cl.ReadsDay.est(0, 2) * 30
	mean := 0.5*float64(rep.LogicalBytes)*0.1 + 4096*0.5 + 65536*0.3 + (1<<20)*0.1
	ampDiff := 0.5*(rep.Amp4K-cdcF.Amp4K) + 0.3*(rep.Amp64K-cdcF.Amp64K) + 0.1*(rep.Amp1M-cdcF.Amp1M)
	_ = mean
	extra := ampDiff * (4096*0.5 + 65536*0.3 + (1<<20)*0.1)
	return math.Max(extra, 0) * reads * bpr
}

// expectedNet is the prior-only estimate used before expensive analysis exists.
func (a *Adaptive) expectedNet(env *Env, cl *clsStat, ls *linStat, n int, q, rhoC float64, stdC int64, ratio float64, s SchemeInfo, cfg Config, gap, life float64, size int64) float64 {
	r := ratio
	if a.P.Class && cl.Ratio.N > 0 {
		r = cl.Ratio.est(ratio, a.P.K)
	}
	rhoS := a.rhoS(cl, ls, s.Name, Probe{}, false)
	stdS := r * float64(stdC)
	prem := stdS - float64(stdC)*(1-0) // first-sight: no base yet
	if n > 0 {
		prem = stdS*(1-rhoS) - float64(stdC)*(1-rhoC)
	}
	delta := float64(stdC)*(1-rhoC) - stdS*(1-rhoS)
	g := 0.0
	for j := 1; j <= a.P.Horizon; j++ {
		w := math.Pow(q, float64(j))
		if !math.IsInf(life, 1) {
			w *= math.Exp(-float64(j) * gap / life)
		}
		g += w * delta
	}
	// deferred work reads the object back from disk, then reads/writes it again to migrate
	cost := 0.0
	if bv := cfg.Prices.ByteValue(); bv > 0 {
		io := (2*float64(size) + stdS) * cfg.Prices.MigrateIOUSDGB / 1e9
		cpu := float64(size) / (1 << 20) / math.Max(s.AnalysisMiBs, 0.1) * 2 / 3600 * cfg.Prices.CPUUSDCoreHour
		cost = (io + cpu) / bv
	}
	return g - math.Max(prem, 0) - cost
}

// groupHints notices lock-in: bytes reuse collapsed while a format probe still sees
// strong reuse. It schedules analysis of the base and this object for a bootstrap.
func (a *Adaptive) groupHints(env *Env, ar Arrival, caps []SchemeInfo, pc Probe, ls *linStat, lk string) []Action {
	var out []Action
	var prev int = -1
	if pc.Related && pc.Base >= 0 {
		prev = pc.Base
	} else {
		for id := ar.Obj - 1; id >= 0 && id >= ar.Obj-4; id-- {
			if k := a.S.ObjKey[id]; k == lk {
				prev = id
				break
			}
		}
	}
	if prev < 0 {
		return nil
	}
	for _, s := range caps {
		if s.Name == CDC || !s.Ready {
			continue
		}
		p, err := env.Probe(ar.Obj, s.Name)
		if err != nil || !p.Related || p.Rho < math.Max(a.P.MinConf, 0.3) {
			continue
		}
		if po, ok := env.Obj(prev); ok && po.Scheme == s.Name {
			continue
		}
		out = append(out, Action{Kind: ActEnqueue, Obj: prev, Scheme: s.Name, Priority: 10 + p.Rho, Reason: "group-base"},
			Action{Kind: ActEnqueue, Obj: ar.Obj, Scheme: s.Name, Priority: 9 + p.Rho, Reason: "group-head"})
	}
	return out
}

// ----------------------------------------------------------------- events

func (a *Adaptive) Event(env *Env, ev Event) []Action {
	switch ev.Kind {
	case Delete:
		o, ok := env.Obj(ev.Obj)
		if !ok {
			return nil
		}
		sig := ""
		if caps, err := env.Caps(ev.Obj); err == nil {
			sig = signature(caps)
		}
		a.cls(sig).Life.obs(float64(ev.Day-o.Day), 0.3)
		if a.P.Group {
			return a.flattenDependents(env, ev.Obj)
		}
	case Read:
		if o, ok := env.Obj(ev.Obj); ok && a.P.Read {
			sig := ""
			if caps, err := env.Caps(ev.Obj); err == nil {
				sig = signature(caps)
			}
			age := math.Max(float64(ev.Day-o.Day), 1)
			a.cls(sig).ReadsDay.obs(float64(o.Reads)/age, 0.05)
		}
	case Tick:
		a.S.LastTick = ev.Day
		return a.maintain(env, ev)
	}
	return nil
}

// flattenDependents rebases dependents of a deleted base when holding the pinned
// base costs more than a standalone form (exact, via Simulate).
func (a *Adaptive) flattenDependents(env *Env, base int) []Action {
	deps := env.Dependents(base)
	if len(deps) == 0 {
		return nil
	}
	var acts []Action
	for _, d := range deps {
		if !env.Analyzed(d, mustScheme(env, d)) {
			continue
		}
		cand := []Action{{Kind: ActFlatten, Obj: d}}
		if delta, _ := env.Simulate(cand); delta < 0 {
			acts = append(acts, Action{Kind: ActFlatten, Obj: d, Reason: "pin-release", ExpectedSaved: -delta})
		}
	}
	// all dependents must flatten together to release the base; evaluate jointly
	if len(acts) == len(deps) && len(deps) > 1 {
		if delta, _ := env.Simulate(acts); delta >= 0 {
			return nil
		}
	}
	return acts
}

func mustScheme(env *Env, id int) string {
	o, ok := env.Obj(id)
	if !ok {
		return CDC
	}
	return o.Scheme
}

// maintain queues archival analysis of cold CDC lineages. The decision is made per
// lineage and priced: freed CDC bytes minus the expected representation bytes minus
// the I/O and CPU of analysis and migration, all in stored-byte equivalents.
func (a *Adaptive) maintain(env *Env, ev Event) []Action {
	if !a.P.Defer && !a.P.Group {
		return nil
	}
	cfg := env.Config()
	bv := cfg.Prices.ByteValue()
	if bv <= 0 {
		return nil
	}
	groups := map[string][]int{}
	var order []string
	for _, id := range env.Objects() {
		o, _ := env.Obj(id)
		if !o.Live || o.Scheme != CDC {
			continue
		}
		cold := o.Tier == "cold" || ev.Day-o.Day >= a.P.ColdAge
		if !cold || (a.P.Read && o.Tier == "hot") {
			continue
		}
		k := a.S.ObjKey[id]
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], id)
	}
	type pick struct {
		ids  []int
		s    string
		prio float64
	}
	var picks []pick
	for _, k := range order {
		ids := groups[k]
		caps, err := env.Caps(ids[0])
		if err != nil {
			continue
		}
		cl := a.cls(signature(caps))
		free := float64(env.ReleasableSet(ids))
		for _, s := range caps {
			if s.Name == CDC || !s.Ready {
				continue
			}
			pending := false
			var size int64
			var expRep, cpu float64
			ratio := a.Prior.Ratio
			if a.P.Class && cl.Ratio.N > 0 {
				ratio = cl.Ratio.est(a.Prior.Ratio, a.P.K)
			}
			rho := a.rhoS(cl, a.S.Lin[k], s.Name, Probe{}, false)
			if a.S.Lin[k] == nil {
				rho = a.Prior.RhoRep
			}
			for i, id := range ids {
				o, _ := env.Obj(id)
				if a.S.Pending[fmt.Sprintf("%d|%s", id, s.Name)] > 0 {
					pending = true
				}
				size += o.Size
				own := float64(o.OwnBytes)
				if i == 0 {
					expRep += ratio * own
				} else {
					expRep += ratio * own * (1 - rho)
				}
				cpu += float64(o.Size) / (1 << 20) / math.Max(s.AnalysisMiBs, 0.1) * 2 / 3600 * cfg.Prices.CPUUSDCoreHour
			}
			if pending {
				continue
			}
			io := (2*float64(size) + expRep) * cfg.Prices.MigrateIOUSDGB / 1e9
			net := free - expRep - (io+cpu)/bv
			if net > a.P.Margin*float64(size) && net > 0 {
				picks = append(picks, pick{ids, s.Name, net / float64(size)})
			}
		}
	}
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].prio > picks[j].prio })
	limit := a.P.QueuePerTick
	if limit <= 0 {
		limit = 4
	}
	var acts []Action
	for i := 0; i < len(picks) && len(acts) < limit; i++ {
		for _, id := range picks[i].ids {
			a.S.Pending[fmt.Sprintf("%d|%s", id, picks[i].s)]++
			acts = append(acts, Action{Kind: ActEnqueue, Obj: id, Scheme: picks[i].s, Priority: picks[i].prio, Reason: "archive"})
		}
	}
	return acts
}

// Completed runs the bounded group local search over the object's lineage using the
// analysed pairs only, then executes the improving migrations in order.
func (a *Adaptive) Completed(env *Env, t Task) []Action {
	delete(a.S.Pending, fmt.Sprintf("%d|%s", t.Obj, t.Scheme))
	lk := a.S.ObjKey[t.Obj]
	var members []int
	for _, id := range env.Objects() {
		if a.S.ObjKey[id] == lk {
			if o, _ := env.Obj(id); o.Live {
				members = append(members, id)
			}
		}
	}
	if len(members) > 12 {
		members = members[len(members)-12:]
	}
	return a.localSearch(env, members)
}

// localSearch applies the best simulated move until none improves or MaxMoves is
// reached. Moves are single migrations and "suffix" moves (migrate members[k:] to one
// scheme in order), which is what lets a lineage escape first-sight lock-in. Depth,
// fanout, capacity and budgets stay enforced by the runner.
func (a *Adaptive) localSearch(env *Env, members []int) []Action {
	moves := a.P.MaxMoves
	if moves <= 0 {
		moves = 6
	}
	cfg := env.Config()
	bv := cfg.Prices.ByteValue()
	ioCost := func(size int64) float64 {
		if bv == 0 {
			return 0
		}
		return float64(size) * 2 * cfg.Prices.MigrateIOUSDGB / 1e9 / bv
	}
	var chosen []Action
	for i := 0; i < moves; i++ {
		base, _ := env.Simulate(chosen)
		var best []Action
		bestGain := 0.0
		try := func(mv []Action, io float64) {
			d, peak := env.Simulate(append(append([]Action(nil), chosen...), mv...))
			if cfg.Capacity > 0 {
				if p, _, _, _ := env.Bytes(); p+peak > cfg.Capacity {
					return
				}
			}
			if gain := float64(base-d) - io; gain > bestGain+a.P.Margin*float64(env.sizeOf(mv)) {
				best, bestGain = mv, gain
			}
		}
		schemes := map[string]bool{}
		for _, id := range members {
			caps, err := env.Caps(id)
			if err != nil {
				continue
			}
			for _, s := range caps {
				schemes[s.Name] = true
			}
		}
		var names []string
		for n := range schemes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, s := range names {
			for k := range members {
				var mv []Action
				var io float64
				ok := true
				for _, id := range members[k:] {
					o, live := env.Obj(id)
					if !live || !o.Live || !env.Analyzed(id, s) {
						ok = false
						break
					}
					if a.P.Read && o.Tier == "hot" && cfg.HotAmp64Cap > 0 && s != CDC {
						if fs, err := env.Forms(id, s); err == nil && len(fs) > 0 && fs[0].Amp64K > cfg.HotAmp64Cap {
							ok = false
							break
						}
					}
					if o.Scheme == s && len(mv) == 0 {
						continue // already in scheme: suffix starts later
					}
					mv = append(mv, Action{Kind: ActMigrate, Obj: id, Scheme: s, Reason: "group"})
					io += ioCost(o.Size)
				}
				if ok && len(mv) > 0 {
					try(mv, io)
				}
			}
		}
		if bestGain <= 0 {
			break
		}
		chosen = append(chosen, best...)
	}
	return chosen
}
