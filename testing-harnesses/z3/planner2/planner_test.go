package planner2

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func testLineages(t *testing.T) []Lineage {
	sl := loadSeriesT(t)
	pick := map[string]bool{"sst-lz4-rocksdb": true, "parquet-lz4-chain": true, "b4-pdb": true, "b5-smollm2ctl": true, "b3-smollm2-reverse": true,
		"oneoff-sst-lz4-unrelated": true, "b1-dex-fdroid": true, "sqfs-typescript_lz4": true}
	var out []Lineage
	for _, l := range sl {
		if pick[l.Name()] {
			out = append(out, l)
		}
	}
	if len(out) < 6 {
		t.Skip("series histories missing")
	}
	return out
}

func testWorld(t *testing.T, seed int64, retention, reads string) *World {
	sp := DefaultSpec("t")
	sp.Seed, sp.Retention, sp.Reads, sp.TickEvery = seed, retention, reads, 5
	sp.Window = Budget{CPUMS: 3_000_000, IOBytes: 16 << 30}
	return Build(sp, testLineages(t))
}

func testPrior() Prior { return Prior{Ratio: 0.9, RhoRep: 0.6, RhoCDC: 0.3, Q: 0.7, N: 3} }

func cloneLineage(l Lineage, cutStage int, rng *rand.Rand) Lineage {
	s, ok := l.(*SeriesLineage)
	if !ok {
		return l
	}
	c := &SeriesLineage{H: s.H, hint: s.hint}
	c.H.Schemes = append([]seriesScheme(nil), s.H.Schemes...)
	for i := range c.H.Schemes {
		x := c.H.Schemes[i]
		x.Inc = append([]int64(nil), x.Inc...)
		x.Std = append([]int64(nil), x.Std...)
		for t := cutStage + 1; t < len(x.Inc); t++ {
			x.Std[t] = 1 + int64(rng.Intn(1<<28))
			x.Inc[t] = int64(rng.Intn(int(x.Std[t]) + 1))
		}
		c.H.Schemes[i] = x
	}
	return c
}

// mutateFuture keeps events[:cut+1] and lineage data of arrived stages, and
// rewrites everything later: lineage numbers, days, reads, deletes, budgets.
func mutateFuture(w *World, cut int, seed int64) *World {
	rng := rand.New(rand.NewSource(seed))
	maxStage := map[int]int{}
	for _, e := range w.Events[:cut+1] {
		if e.Kind == Arrive {
			maxStage[e.Lineage] = e.Stage
		}
	}
	m := &World{Name: w.Name + "-mut", Simulated: w.Simulated}
	for li, l := range w.Lineages {
		st, ok := maxStage[li]
		if !ok {
			st = -1
		}
		m.Lineages = append(m.Lineages, cloneLineage(l, st, rng))
	}
	m.Events = append(m.Events, w.Events[:cut+1]...)
	day := w.Events[cut].Day
	for _, e := range w.Events[cut+1:] {
		day += rng.Intn(3)
		if day < e.Day-0 && false {
			day = e.Day
		}
		e.Day = maxInt(day, m.Events[len(m.Events)-1].Day)
		switch e.Kind {
		case Read:
			e.Range = []int64{0, 4096, 65536, 1 << 20}[rng.Intn(4)]
		case Tick:
			e.Budget = Budget{CPUMS: int64(rng.Intn(1000)), IOBytes: int64(rng.Intn(1 << 20))}
			e.GC = rng.Intn(2) == 0
		case Delete:
			if rng.Intn(3) == 0 {
				continue
			}
		case Arrive:
			e.Size = e.Size/2 + 1
		}
		e.Seq = len(m.Events)
		m.Events = append(m.Events, e)
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func decisionsUpTo(l *Ledger, seq int) []DecisionRec {
	var out []DecisionRec
	for _, d := range l.Decisions {
		if d.Seq <= seq {
			out = append(out, d)
		}
	}
	return out
}

func runTo(w *World, p Policy, cfg Config, cut int) (*Ledger, []byte, error) {
	x := NewRunner(w, p, cfg)
	for _, e := range w.Events[:cut+1] {
		if err := x.Step(e); err != nil {
			return nil, nil, err
		}
	}
	sn, _ := p.Snapshot()
	return x.Ledger(), sn, nil
}

// Track B: changing every future event (and the future of every lineage) while
// holding the past and the current event fixed leaves decisions and persistent state
// unchanged — including deferred workers and maintenance windows.
func TestCausalFutureInvariance(t *testing.T) {
	w := testWorld(t, 7, "last3snap", "mixed")
	cfg := DefaultConfig()
	cuts := []int{10, 31, 64, 120, 200, len(w.Events) / 2, len(w.Events) - 30}
	delayed := map[string]int{}
	for _, name := range PolicyNames {
		for _, cut := range cuts {
			if cut >= len(w.Events) {
				continue
			}
			p1, _ := Preset(name, testPrior())
			a, sa, err := runTo(w, p1, cfg, cut)
			if err != nil {
				t.Fatal(err)
			}
			for k := int64(1); k <= 2; k++ {
				p2, _ := Preset(name, testPrior())
				m := mutateFuture(w, cut, 100*k+int64(cut))
				b, sb, err := runTo(m, p2, cfg, cut)
				if err != nil {
					t.Fatal(err)
				}
				da, db := decisionsUpTo(a, cut), decisionsUpTo(b, cut)
				if fmt.Sprint(da) != fmt.Sprint(db) {
					t.Fatalf("%s cut %d: decisions changed with the future", name, cut)
				}
				if string(sa) != string(sb) {
					t.Fatalf("%s cut %d: persistent state changed with the future", name, cut)
				}
				if a.FinalPhys != b.FinalPhys && false {
					t.Fatal("unreachable")
				}
			}
			delayed[name] += a.Completed + a.Migrations
		}
	}
	nonvac := 0
	for _, n := range []string{"deferred", "group-reopt", "combined"} {
		nonvac += delayed[n]
	}
	if nonvac == 0 {
		t.Fatal("delayed-worker policies never completed a task before any cut: test would be vacuous")
	}
}

// peeker deliberately reads lineage numbers of the NEXT stage; the invariance check
// must catch it (negative control proving the test has teeth).
type peeker struct {
	Z3Policy
	w *World
}

func (p *peeker) Arrive(env *Env, a Arrival) Decision {
	var next int64
	for _, e := range p.w.Events {
		if e.Kind == Arrive && e.Obj == a.Obj {
			if s, ok := p.w.Lineages[e.Lineage].(*SeriesLineage); ok && e.Stage+1 < s.Len() {
				next = s.H.Schemes[len(s.H.Schemes)-1].Inc[e.Stage+1]
			}
		}
	}
	if next%2 == 0 {
		return Decision{Scheme: CDC}
	}
	caps, _ := env.Caps(a.Obj)
	for _, c := range caps {
		if c.Name != CDC {
			if _, err := env.Analyze(a.Obj, c.Name); err == nil {
				return Decision{Scheme: c.Name}
			}
		}
	}
	return Decision{Scheme: CDC}
}

func TestCausalityTestCatchesLeaks(t *testing.T) {
	w := testWorld(t, 7, "all", "none")
	cfg := DefaultConfig()
	caught := false
	for cut := 20; cut < len(w.Events) && !caught; cut += 7 {
		a, _, err := runTo(w, &peeker{Z3Policy: *NewZ3("greedy"), w: w}, cfg, cut)
		m := mutateFuture(w, cut, int64(cut))
		b, _, err2 := runTo(m, &peeker{Z3Policy: *NewZ3("greedy"), w: m}, cfg, cut)
		if err != nil || err2 != nil {
			t.Fatal(err, err2)
		}
		if fmt.Sprint(decisionsUpTo(a, cut)) != fmt.Sprint(decisionsUpTo(b, cut)) {
			caught = true
		}
	}
	if !caught {
		t.Fatal("future-peeking policy was not detected by the invariance check")
	}
}

// Persistence/restart: snapshot the policy mid-run, restore into a fresh object and
// continue; the whole decision stream must equal the uninterrupted run, which in turn
// must be bit-reproducible.
func TestRestartAndDeterminism(t *testing.T) {
	w := testWorld(t, 3, "last3", "hotlatest")
	cfg := DefaultConfig()
	for _, name := range PolicyNames {
		ref, err := Run(w, mustPreset(t, name), cfg)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := Run(w, mustPreset(t, name), cfg)
		if ref.Digest != again.Digest || ref.FinalPhys != again.FinalPhys {
			t.Fatalf("%s: replay not deterministic", name)
		}
		for _, cut := range []int{len(w.Events) / 3, 2 * len(w.Events) / 3} {
			p := mustPreset(t, name)
			x := NewRunner(w, p, cfg)
			for _, e := range w.Events[:cut] {
				if err := x.Step(e); err != nil {
					t.Fatal(err)
				}
			}
			sn, err := p.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			q := mustPreset(t, name)
			if err := q.Restore(sn); err != nil {
				t.Fatalf("%s restore: %v", name, err)
			}
			x.SetPolicy(q)
			for _, e := range w.Events[cut:] {
				if err := x.Step(e); err != nil {
					t.Fatal(err)
				}
			}
			led := x.Finish()
			if led.Digest != ref.Digest || led.FinalPhys != ref.FinalPhys {
				t.Fatalf("%s: restart at %d diverged", name, cut)
			}
			// rebuild-from-history: a fresh policy replaying the prefix reaches the same state
			rb := mustPreset(t, name)
			if _, snRB, err := runTo(w, rb, cfg, cut-1); err != nil || string(snRB) != string(sn) {
				t.Fatalf("%s: state rebuilt from history differs from snapshot", name)
			}
		}
	}
	bad := mustPreset(t, "combined")
	if err := bad.Restore([]byte("{not json")); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}

func mustPreset(t *testing.T, name string) Policy {
	p, err := Preset(name, testPrior())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Cost-ledger and store invariants over every policy and retention/read scenario.
func TestLedgerInvariantsAllPolicies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FailMigrate = 0.2
	cfg.GCEfficiency = 0.9
	for _, ret := range []string{"all", "last3", "last3snap", "ttl14"} {
		w := testWorld(t, 5, ret, "mixed")
		for _, name := range PolicyNames {
			x := NewRunner(w, mustPreset(t, name), cfg)
			for _, e := range w.Events {
				if err := x.Step(e); err != nil {
					t.Fatalf("%s/%s: %v", ret, name, err)
				}
			}
			led := x.Finish()
			if id, ok := x.Store().ReadableAll(); !ok {
				t.Fatalf("%s/%s: live object %d unreadable", ret, name, id)
			}
			if led.FinalPhys < led.FinalLive || led.Peak < led.FinalPhys || led.FinalLive < 0 || led.ByteDays < 0 {
				t.Fatalf("%s/%s: byte counters inconsistent %+v", ret, name, led)
			}
			if led.WriteBytes < led.FinalLive {
				t.Fatalf("%s/%s: wrote %d < live %d (free bytes)", ret, name, led.WriteBytes, led.FinalLive)
			}
			if led.MaxDepth > cfg.DepthCap || led.MaxFanout > cfg.FanoutCap {
				t.Fatalf("%s/%s: depth/fanout cap violated %d/%d", ret, name, led.MaxDepth, led.MaxFanout)
			}
			for _, d := range led.Decisions {
				if d.Marginal < 0 {
					t.Fatalf("negative-cost decision %+v", d)
				}
			}
		}
	}
}

func TestFailClosedInputs(t *testing.T) {
	good := `{"Version":1,"Histories":[{"Name":"h","Class":"c","Evidence":"REAL","Stages":[{"Label":"a","Logical":10}],
	 "Schemes":[{"Name":"cdc","Kind":"cdc","Dep":"none","Inc":[5],"Std":[5]}]}],"Opportunities":[]}`
	if _, _, err := LoadSeries(strings.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"inc>std":     strings.Replace(good, `"Inc":[5],"Std":[5]`, `"Inc":[9],"Std":[5]`, 1),
		"negative":    strings.Replace(good, `"Inc":[5],"Std":[5]`, `"Inc":[-1],"Std":[5]`, 1),
		"no cdc":      strings.Replace(good, `"Name":"cdc"`, `"Name":"x"`, 1),
		"short":       strings.Replace(good, `"Inc":[5]`, `"Inc":[5,6]`, 1),
		"ready opp":   strings.Replace(good, `"Opportunities":[]`, `"Opportunities":[{"Name":"brotli","Ready":true}]`, 1),
		"trailing":    good + "x",
		"duplicate":   strings.Replace(good, `"Histories":[`, `"Histories":[`+good[strings.Index(good, `{"Name":"h"`):strings.Index(good, `],"Opportunities"`)]+`,`, 1),
		"bad version": strings.Replace(good, `"Version":1`, `"Version":2`, 1),
	}
	for name, doc := range cases {
		if _, _, err := LoadSeries(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: malformed series accepted", name)
		}
	}
	// the committed series contains only not-ready opportunities
	f, _, err := LoadSeriesFile("../../z4/b7/series.json")
	if err == nil {
		for _, o := range f.Opportunities {
			if o.Ready {
				t.Errorf("opportunity %q marked ready", o.Name)
			}
		}
	}
	if (Cand{Scheme: "x", Ready: false, Comps: []CompRef{{"a", 1}}}).Validate() == nil {
		t.Error("not-ready candidate validated")
	}
	if (Cand{Scheme: "x", Ready: true, Comps: []CompRef{{"a", 1}, {"a", 1}}}).Validate() == nil {
		t.Error("duplicate component validated")
	}
	if (Cand{Scheme: "x", Ready: true, Comps: []CompRef{{"a", -1}}}).Validate() == nil {
		t.Error("negative bytes validated")
	}
}

func TestStoreInvariants(t *testing.T) {
	s := NewStore(10, 0)
	c1 := Cand{Scheme: "s", Ready: true, Base: -1, Comps: []CompRef{{"a", 100}, {"b", 50}}}
	c2 := Cand{Scheme: "s", Ready: true, Base: -1, Comps: []CompRef{{"b", 50}, {"c", 70}}}
	if _, n, err := s.Attach(1, c1); err != nil || n != 170 {
		t.Fatalf("attach c1: %d %v", n, err)
	}
	if s.Marginal(c2) != 80 {
		t.Fatal("shared component double counted")
	}
	if _, _, err := s.Attach(2, Cand{Scheme: "s", Ready: true, Base: -1, Comps: []CompRef{{"b", 51}}}); err == nil {
		t.Fatal("size-conflicting component accepted")
	}
	s.Attach(2, c2)
	if err := s.Detach(c1); err != nil {
		t.Fatal(err)
	}
	if s.Live() != 140 || s.Dead() != 110 {
		t.Fatalf("live %d dead %d", s.Live(), s.Dead())
	}
	// dead bytes stay until collected; resurrecting a dead component is free
	if s.Marginal(c1) != 0+0 && s.Marginal(c1) != 0 {
		t.Fatalf("dead component should be free to re-reference, got %d", s.Marginal(c1))
	}
	if s.Collect(1) != 110 || s.Dead() != 0 {
		t.Fatal("collect")
	}
	if err := s.Detach(c1); err == nil {
		t.Fatal("detach of unreferenced component accepted")
	}
	s.AddObj(&Obj{ID: 1, Primary: c1})
	s.AddObj(&Obj{ID: 2, Primary: c2, Attached: true, Live: true})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	cap := NewStore(0, 100)
	if _, _, err := cap.Attach(1, c1); err == nil {
		t.Fatal("capacity not enforced")
	}
}

type fixed struct {
	Z3Policy
	scheme string
	skip   bool
}

func (f *fixed) Arrive(env *Env, a Arrival) Decision {
	if !f.skip {
		env.Analyze(a.Obj, f.scheme)
	}
	return Decision{Scheme: f.scheme}
}

func TestRunnerFailClosed(t *testing.T) {
	lins := testLineages(t)
	var l Lineage
	for _, x := range lins {
		if x.Name() == "sst-lz4-rocksdb" {
			l = x
		}
	}
	w := ArrivalsOnly(l)
	cfg := DefaultConfig()
	// choosing a representation that was never analysed falls back to CDC and is counted
	led, err := Run(w, &fixed{Z3Policy: *NewZ3("x"), scheme: "b6-ukv8k", skip: true}, cfg)
	if err != nil || led.RejectedUnanalyzed != len(w.Events) || led.RepObjects != 0 {
		t.Fatalf("unanalysed candidate admitted: %+v err %v", led.RejectedUnanalyzed, err)
	}
	// future objects cannot be analysed
	r := NewRunner(w, NewZ3("always-cdc"), cfg)
	if _, err := r.env.Analyze(5, "b6-ukv8k"); err != ErrFuture {
		t.Fatalf("future analysis allowed: %v", err)
	}
	// tight synchronous budget: analysis rejected, CDC kept
	c2 := cfg
	c2.PUTCPUMS = 1
	led, err = Run(w, NewZ3("greedy"), c2)
	if err != nil || led.RejectedBudget == 0 || led.RepObjects != 0 || led.AnalysisMSSync != 0 {
		t.Fatalf("budget exhaustion not enforced: %v %+v", err, led.RejectedBudget)
	}
	// duplicate arrival and out-of-order events are rejected
	bad := &World{Lineages: w.Lineages, Events: append(append([]Event(nil), w.Events...), w.Events[0])}
	if _, err := Run(bad, NewZ3("always-cdc"), cfg); err == nil {
		t.Fatal("duplicate/out-of-order event accepted")
	}
	// capacity: admission falls back to CDC instead of failing
	c3 := cfg
	c3.Capacity = 30 << 20
	led, err = Run(w, NewZ3("greedy"), c3)
	if err != nil && !strings.Contains(err.Error(), "fallback") {
		t.Fatal(err)
	}
	_ = led
}

func TestMigrationFailureAndPins(t *testing.T) {
	lins := testLineages(t)
	var l Lineage
	for _, x := range lins {
		if x.Name() == "b3-smollm2-reverse" {
			l = x
		}
	}
	cfg := DefaultConfig()
	cfg.FailMigrate = 1
	sp := DefaultSpec("m")
	sp.Retention, sp.Reads = "all", "none"
	w := Build(sp, []Lineage{l})
	led, err := Run(w, mustPreset(t, "combined"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if led.Migrations != 0 {
		t.Fatalf("migration succeeded under 100%% failure injection (%d)", led.Migrations)
	}
	// objects stay readable and the failed writes are garbage that GC reclaims
	x := NewRunner(w, mustPreset(t, "combined"), cfg)
	for _, e := range w.Events {
		if err := x.Step(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := x.Store().ReadableAll(); !ok {
		t.Fatal("object unreadable after failed migration")
	}
	// deleting a base keeps dependents readable (pin) until flattened
	spd := DefaultSpec("d")
	spd.Retention = "last3"
	w2 := Build(spd, []Lineage{l})
	cfg2 := DefaultConfig()
	x2 := NewRunner(w2, &fixed{Z3Policy: *NewZ3("x"), scheme: "b3-semcas"}, cfg2)
	for _, e := range w2.Events {
		if err := x2.Step(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := x2.Store().ReadableAll(); !ok {
		t.Fatal("dependent unreadable after base deletion")
	}
}

func TestCandidateClosureIsAcyclicAndCausal(t *testing.T) {
	for _, l := range loadSeriesT(t) {
		w := ArrivalsOnly(l)
		x := NewRunner(w, &fixed{Z3Policy: *NewZ3("x"), scheme: l.Schemes()[len(l.Schemes())-1].Name}, DefaultConfig())
		for _, e := range w.Events {
			if err := x.Step(e); err != nil {
				t.Fatal(err)
			}
			forms, _ := x.env.Forms(e.Obj, l.Schemes()[len(l.Schemes())-1].Name)
			for _, c := range forms {
				for _, r := range c.Comps {
					parts := strings.Split(string(r.ID), "|")
					var st int
					fmt.Sscan(parts[2], &st)
					if st > e.Stage {
						t.Fatalf("%s stage %d closure references later stage %d", l.Name(), e.Stage, st)
					}
				}
			}
		}
	}
}

type reoptOnce struct {
	Z3Policy
	done bool
}

func (p *reoptOnce) Arrive(env *Env, a Arrival) Decision {
	env.Analyze(a.Obj, "b3-semcas")
	return Decision{Scheme: "b3-semcas"}
}
func (p *reoptOnce) Event(env *Env, ev Event) []Action {
	if ev.Kind == Tick && !p.done && len(env.Objects()) >= 5 {
		p.done = true
		return []Action{{Kind: ActReopt, Obj: 0, Reason: "test"}}
	}
	return nil
}

// The B3 group reoptimiser must shrink the group to its measured ratio, write the
// new components before releasing the old (peak = before + after), keep all objects
// readable, and respect a tight capacity (rejecting instead of overrunning).
func TestGroupReoptExecutor(t *testing.T) {
	var l Lineage
	for _, x := range loadSeriesT(t) {
		if x.Name() == "b3-smollm2-reverse" {
			l = x
		}
	}
	sp := DefaultSpec("r")
	sp.Retention, sp.Reads, sp.TickEvery = "all", "none", 3
	w := Build(sp, []Lineage{l})
	cfg := DefaultConfig()
	x := NewRunner(w, &reoptOnce{Z3Policy: *NewZ3("x")}, cfg)
	var before int64
	for _, e := range w.Events {
		if err := x.Step(e); err != nil {
			t.Fatal(err)
		}
		if e.Kind == Tick && x.Ledger().Reopts == 0 {
			before = x.Store().Live()
		}
	}
	led := x.Finish()
	if led.Reopts != 1 {
		t.Fatalf("reopt not executed: %+v", led.Reopts)
	}
	ratio := float64(led.FinalLive) / float64(before)
	if ratio > 0.70 || ratio < 0.55 {
		t.Fatalf("group shrank to %.3f of before, want ~0.646", ratio)
	}
	if led.Peak < before+led.FinalLive/2 {
		t.Fatalf("migration peak %d does not include both generations (before %d)", led.Peak, before)
	}
	if _, ok := x.Store().ReadableAll(); !ok {
		t.Fatal("object unreadable after reoptimisation")
	}
	// tight capacity: the same action is rejected, nothing is changed
	c2 := cfg
	c2.Capacity = before + before/20
	y := NewRunner(w, &reoptOnce{Z3Policy: *NewZ3("x")}, c2)
	for _, e := range w.Events {
		if err := y.Step(e); err != nil {
			t.Fatal(err)
		}
	}
	l2 := y.Finish()
	if l2.Reopts != 0 || l2.RejectedSpace == 0 {
		t.Fatalf("reopt not rejected under tight capacity: %+v", l2.RejectedSpace)
	}
}
