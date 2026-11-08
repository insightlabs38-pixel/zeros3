package planner2

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"math"
	"sort"
	"time"
)

// Config holds every resource, constraint and simulation parameter of one replay.
type Config struct {
	Overhead                          int64   // bytes per newly created component
	Capacity                          int64   // physical capacity (0 = unlimited)
	DepthCap, FanoutCap               int     // reliability constraints on dependency chains
	GCEfficiency                      float64 // fraction of dead bytes a collection reclaims
	ProbeRecall, ProbeFPR, ProbeNoise float64
	ProbeMiBsByte, ProbeMiBsFormat    float64
	Seed                              int64
	FailMigrate                       float64 // probability an attempted migration aborts after writing
	PUTCPUMS                          int64   // synchronous analysis budget per PUT (0 = unlimited)
	QueueCap, QueueTTL                int
	HotAmp64Cap                       float64 // hot-tier constraint on 64 KiB range amplification (0 = off)
	MemFactor                         float64 // modelled analysis RSS as multiple of object size
	SkipValidate                      bool    // oracle enumeration only: skip per-event store audit
	SpecFactor                        float64 // >1: migrations stage this multiple of the final bytes transiently (B2 measured 3-4x)
	ValidateEvery                     int     // audit the store every N events (0/1 = every event); always audited at Finish
	Prices                            PriceBook
}

// PriceBook converts ledger quantities to dollars (explicit, configurable).
type PriceBook struct {
	StorageUSDGBMonth, HorizonMonths, ReadIOUSDGB, CPUUSDCoreHour, MigrateIOUSDGB float64
}

func DefaultConfig() Config {
	return Config{Overhead: 0, DepthCap: 4, FanoutCap: 8, GCEfficiency: 1, ProbeRecall: 0.94, ProbeFPR: 0.05, ProbeNoise: 0.08,
		ProbeMiBsByte: 400, ProbeMiBsFormat: 40, Seed: 1, QueueCap: 256, QueueTTL: 120, MemFactor: 0.5,
		Prices: PriceBook{0.023, 12, 0.01, 0.04, 0.02}}
}

// World is the dataset plus the chronological stream. Lineages are runner-side
// ground truth; policies see them only through Env methods.
type World struct {
	Name      string
	Lineages  []Lineage
	Events    []Event
	Simulated []string // labels of simulated event classes
}

// DecisionRec is one replayable decision with the information time it was made.
type DecisionRec struct {
	Seq, Day, Obj        int
	Kind                 string
	Scheme, Form, Reason string
	Marginal             int64
	Sidecar              bool
}

type Ledger struct {
	Policy, World                                    string
	FinalPhys, FinalLive, PreGCPhys, Peak, Pinned    int64
	AnalysisMSSync, AnalysisMSBg, ProbeMS, MigrateMS float64
	AnalysisCalls, ProbeCalls                        int
	PeakAnalysisMem                                  int64
	SourceReadBytes, WriteBytes                      int64 // analysis reads, bytes written
	MigrateReadBytes, MigrateWriteBytes              int64
	ServedBytes, ReadReqs, WholeReads                int64
	LogicalReadBytes                                 int64
	DecodeSeconds                                    float64
	Migrations, FailedMigrations, Reverts, Rebases   int
	Reopts, RejectedSpace, RejectedBudget            int
	RejectedUnanalyzed, RejectedInvalid              int
	PutRejected                                      int // PUTs refused because even CDC no longer fits
	Enqueued, Completed, Cancelled, Expired          int
	MaxDepth, MaxFanout, MaxBlast                    int
	Objects, RepObjects, Sidecars                    int
	StatePeakBytes                                   int
	HotViolations                                    int // reads of hot objects served from a form above the 64 KiB amplification cap
	ByteDays                                         float64
	ReadsByTier                                      map[string]int64
	WallNS                                           int64
	Decisions                                        []DecisionRec
	Digest                                           string
	PerLineageBytes                                  map[string]int64
	FirstScheme                                      map[string]string // lineage -> scheme of its first stored object
	FinalSchemes                                     map[string]int
}

var (
	ErrFuture       = errors.New("planner2: object has not arrived")
	ErrBudget       = errors.New("planner2: analysis budget exhausted")
	ErrNoAnalysis   = errors.New("planner2: candidate was not analysed")
	ErrNeedAnalysis = errors.New("planner2: cheap probe unavailable for this scheme; analysis required")
)

type taskRec struct {
	Obj      int
	Scheme   string
	Priority float64
	Day      int
	Reason   string
}

type runner struct {
	w           *World
	cfg         Config
	st          *Store
	pol         Policy
	led         *Ledger
	objLin      map[int]int // object -> lineage index (runner truth)
	objStage    map[int]int
	arrived     map[int]bool
	live        map[int][]int // lineage -> stage -> obj  (stage index)
	stageObj    map[[2]int]int
	analyzed    map[[2]string]bool // (obj,scheme) charged once
	anaCache    map[[2]string]bool
	cdcSketch   []sk
	day, seq    int
	win         Budget // remaining window budget
	putLeft     int64
	inTick      bool
	queue       []taskRec
	migTry      int
	sketches    map[int]map[string][]string
	lastProbe   map[int]bool
	h           hash.Hash
	rejectedObj map[int]bool
}

type sk struct {
	obj    int
	sample []string
}

// Env is the only handle a policy has on the world.
type Env struct{ r *runner }

type view struct{ r *runner }

func (v view) StoredPrimary(l, k int) (Cand, int, bool) {
	o, ok := v.r.stageObj[[2]int{l, k}]
	if !ok {
		return Cand{}, 0, false
	}
	ob, ok := v.r.st.Obj(o)
	if !ok || !ob.Live || !ob.Attached {
		return Cand{}, 0, false
	}
	return ob.Primary, o, true
}
func (v view) Fanout(o int) int { return v.r.st.Fanout(o) }
func (v view) DepthCap() int    { return v.r.cfg.DepthCap }
func (v view) FanoutCap() int   { return v.r.cfg.FanoutCap }

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func hash01(seed int64, parts ...uint64) float64 {
	x := uint64(seed)
	for _, p := range parts {
		x = splitmix(x ^ p)
	}
	return float64(x>>11) / float64(1<<53)
}

func strh(s string) uint64 { h := fnv.New64a(); h.Write([]byte(s)); return h.Sum64() }

// Policy is the decision interface. All methods receive only the Env.
type Policy interface {
	Name() string
	Arrive(env *Env, a Arrival) Decision
	Event(env *Env, ev Event) []Action
	// Completed is called when a deferred analysis finished (background worker).
	Completed(env *Env, t Task) []Action
	Snapshot() ([]byte, error)
	Restore([]byte) error
}

// Task is a finished/unfinished deferred analysis handed back to the policy.
type Task struct {
	Obj      int
	Scheme   string
	Priority float64
	Day      int
}

// ObjView is the policy-visible state of a stored object.
type ObjView struct {
	ID, Day, Depth, Base, Fanout, Reads, LastRead int
	Key, Scheme, Form, Tier                       string
	Size, OwnBytes, Marginal                      int64
	Live, HasSide                                 bool
	RangeBytes                                    int64
}

func (e *Env) Day() int       { return e.r.day }
func (e *Env) Config() Config { return e.r.cfg }
func (e *Env) Bytes() (phys, live, dead, peak int64) {
	s := e.r.st
	return s.Phys(), s.Live(), s.Dead(), s.Peak
}
func (e *Env) Budget() Budget { return e.r.win }

// Caps returns static scheme knowledge for the object's format (cheap, format sniff).
func (e *Env) Caps(obj int) ([]SchemeInfo, error) {
	l, _, err := e.r.locate(obj)
	if err != nil {
		return nil, err
	}
	return l.Schemes(), nil
}

func (r *runner) locate(obj int) (Lineage, int, error) {
	if !r.arrived[obj] {
		return nil, 0, ErrFuture
	}
	return r.w.Lineages[r.objLin[obj]], r.objStage[obj], nil
}

// Objects lists stored objects in arrival order.
func (e *Env) Objects() []int { return e.r.st.ObjIDs() }

func (e *Env) Obj(id int) (ObjView, bool) {
	o, ok := e.r.st.Obj(id)
	if !ok {
		return ObjView{}, false
	}
	return ObjView{ID: o.ID, Day: o.Day, Depth: o.Primary.Depth, Base: o.Primary.Base, Fanout: e.r.st.Fanout(o.ID), Reads: int(o.Reads),
		LastRead: o.LastRead, Key: o.Key, Scheme: o.Primary.Scheme, Form: o.Primary.Form, Tier: o.Tier, Size: o.Size,
		OwnBytes: o.Primary.OwnBytes(), Live: o.Live, HasSide: o.Side != nil, RangeBytes: o.RangeBytes}, true
}

// Standalone is the full cost of c in an empty store (component bytes plus overhead).
func (e *Env) Standalone(c Cand) int64 {
	var n int64
	for _, x := range c.Comps {
		n += x.Bytes + e.r.st.Overhead
	}
	return n
}

// Releasable is the physical bytes that would become unreferenced if obj's primary
// closure were detached now (components referenced only by this object).
func (e *Env) Releasable(obj int) int64 {
	o, ok := e.r.st.Obj(obj)
	if !ok || !o.Attached {
		return 0
	}
	var n int64
	for _, c := range o.Primary.Comps {
		if p := e.r.st.comp[c.ID]; p != nil && p.refs == 1 {
			n += c.Bytes + e.r.st.Overhead
		}
	}
	return n
}

// Dependents lists live objects whose primary directly depends on obj.
func (e *Env) Dependents(obj int) []int {
	var out []int
	for _, id := range e.r.st.ObjIDs() {
		if o, _ := e.r.st.Obj(id); o.Live && o.Attached && o.Primary.Base == obj {
			out = append(out, id)
		}
	}
	return out
}

// Marginal is the exact bytes needed to add c to the current store.
func (e *Env) Marginal(c Cand) int64 { return e.r.st.Marginal(c) }

func (e *Env) putBudgetLeft() int64 { return e.r.putLeft }

// Probe is the cheap similarity probe. Layer A: real CDC-sketch overlap against
// earlier arrivals. Layer B: a calibrated, noisy, charged simulation.
type Probe struct {
	Scheme  string
	Related bool
	Rho     float64
	Base    int // earlier object id, -1 if none
	Real    bool
	CostMS  float64
}

func (e *Env) Probe(obj int, scheme string) (Probe, error) {
	r := e.r
	l, t, err := r.locate(obj)
	if err != nil {
		return Probe{}, err
	}
	known := false
	for _, s := range l.Schemes() {
		known = known || s.Name == scheme
	}
	if !known {
		return Probe{}, fmt.Errorf("planner2: scheme %q unavailable for object", scheme)
	}
	p := Probe{Scheme: scheme, Base: -1}
	miBs := r.cfg.ProbeMiBsFormat
	if scheme == CDC {
		miBs = r.cfg.ProbeMiBsByte
	}
	p.CostMS = float64(l.Logical(t)) / (1 << 20) / miBs * 1000
	if l.Layer() == "A" {
		if scheme != CDC {
			return p, ErrNeedAnalysis
		}
		p.Real = true
		smp := l.Sketch(t, CDC)
		best := 0.0
		start := 0
		if len(r.cdcSketch) > 256 {
			start = len(r.cdcSketch) - 256
		}
		for _, o := range r.cdcSketch[start:] {
			if o.obj == obj {
				continue
			}
			if ov := overlap(o.sample, smp); ov > best || (ov == best && best > 0 && o.obj > p.Base) {
				best, p.Base = ov, o.obj
			}
		}
		if best > 0 {
			p.Related, p.Rho = true, best
		}
	} else {
		rho := l.Reuse(t, scheme)
		prev := -1
		for k := t - 1; k >= 0; k-- {
			if o, ok := r.stageObj[[2]int{r.objLin[obj], k}]; ok {
				prev = o
				break
			}
		}
		u1 := hash01(r.cfg.Seed, uint64(obj), strh(scheme), 1)
		u2 := hash01(r.cfg.Seed, uint64(obj), strh(scheme), 2)
		u3 := hash01(r.cfg.Seed, uint64(obj), strh(scheme), 3)
		if rho > 0.05 && prev >= 0 {
			if u1 < r.cfg.ProbeRecall {
				n := (u2 + u3 - 1) * 2 * r.cfg.ProbeNoise // triangular noise, sd ~ noise*0.8
				p.Related, p.Rho, p.Base = true, math.Max(0, math.Min(1, rho+n)), prev
			}
		} else if prev >= 0 && u1 < r.cfg.ProbeFPR {
			p.Related, p.Rho, p.Base = true, 0.2+0.6*u2, prev
		}
	}
	r.led.ProbeCalls++
	r.led.ProbeMS += p.CostMS
	return p, nil
}

func overlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	have := map[string]bool{}
	for _, x := range a {
		have[x] = true
	}
	hits := 0
	for _, x := range b {
		if have[x] {
			hits++
		}
	}
	return float64(hits) / float64(len(b))
}

// Overlap is exported for policies that compare real sketches.
func Overlap(a, b []string) float64 { return overlap(a, b) }

// Analyze runs the full exact candidate analysis for (obj, scheme). It is charged
// once per pair; later calls are free re-enumerations against the current store.
func (e *Env) Analyze(obj int, scheme string) ([]Cand, error) {
	r := e.r
	l, t, err := r.locate(obj)
	if err != nil {
		return nil, err
	}
	forms := l.Forms(r.objLin[obj], t, scheme, view{r})
	if forms == nil {
		return nil, fmt.Errorf("planner2: scheme %q unavailable for object %d", scheme, obj)
	}
	key := [2]string{fmt.Sprint(obj), scheme}
	if !r.analyzed[key] {
		var ms float64
		var rd int64
		for _, c := range forms[:1] {
			ms, rd = c.AnalysisMS, c.ReadBytes
		}
		if scheme == CDC {
			ms, rd = 0, 0
		}
		if r.inTick {
			if r.win.CPUMS < int64(math.Ceil(ms)) || r.win.IOBytes < rd {
				return nil, ErrBudget
			}
			r.win.CPUMS -= int64(math.Ceil(ms))
			r.win.IOBytes -= rd
			r.led.AnalysisMSBg += ms
		} else {
			if r.cfg.PUTCPUMS > 0 && r.putLeft < int64(math.Ceil(ms)) {
				r.led.RejectedBudget++
				return nil, ErrBudget
			}
			r.putLeft -= int64(math.Ceil(ms))
			r.led.AnalysisMSSync += ms
		}
		r.analyzed[key] = true
		r.led.AnalysisCalls++
		if r.inTick {
			r.led.SourceReadBytes += rd // synchronous analysis consumes the PUT stream; background reads disk
		}
		if m := int64(float64(l.Logical(t)) * r.cfg.MemFactor); scheme != CDC && m > r.led.PeakAnalysisMem {
			r.led.PeakAnalysisMem = m
		}
	}
	for _, c := range forms {
		if err := c.Validate(); err != nil {
			return nil, err
		}
	}
	return forms, nil
}

// Analyzed reports whether the pair has been analysed (so its forms may be chosen).
func (e *Env) Analyzed(obj int, scheme string) bool {
	return e.r.analyzed[[2]string{fmt.Sprint(obj), scheme}] || scheme == CDC
}

// Forms re-enumerates forms of an already analysed pair against the current store.
func (e *Env) Forms(obj int, scheme string) ([]Cand, error) {
	if !e.Analyzed(obj, scheme) {
		return nil, ErrNoAnalysis
	}
	l, t, err := e.r.locate(obj)
	if err != nil {
		return nil, err
	}
	f := l.Forms(e.r.objLin[obj], t, scheme, view{e.r})
	if f == nil {
		return nil, fmt.Errorf("planner2: scheme %q unavailable", scheme)
	}
	return f, nil
}

// SameKey lists earlier stored objects with the same key hint (hints may lie).
func (e *Env) SameKey(key string) []int {
	var out []int
	if key == "" {
		return nil
	}
	for _, id := range e.r.st.ObjIDs() {
		if o, _ := e.r.st.Obj(id); o.Key == key {
			out = append(out, id)
		}
	}
	return out
}

func (e *Env) GroupActions(obj int) []GroupAction {
	l, _, err := e.r.locate(obj)
	if err != nil {
		return nil
	}
	return l.GroupActions()
}

// ------------------------------------------------------------------- replay

// Runner is a steppable replay (used for restart tests); Run is the one-shot form.
type Runner struct {
	r                *runner
	env              *Env
	lastSeq, lastDay int
	prevDay          int
}

func NewRunner(w *World, p Policy, cfg Config) *Runner {
	r := &runner{w: w, cfg: cfg, pol: p, st: NewStore(cfg.Overhead, cfg.Capacity), objLin: map[int]int{}, objStage: map[int]int{},
		arrived: map[int]bool{}, stageObj: map[[2]int]int{}, analyzed: map[[2]string]bool{},
		led: &Ledger{Policy: p.Name(), World: w.Name, PerLineageBytes: map[string]int64{}, FirstScheme: map[string]string{}, FinalSchemes: map[string]int{}, ReadsByTier: map[string]int64{}}}
	r.h = sha256.New()
	r.rejectedObj = map[int]bool{}
	return &Runner{r: r, env: &Env{r}, lastSeq: -1, lastDay: -1}
}

// SetPolicy swaps the policy object (after Restore) without touching physical state.
func (x *Runner) SetPolicy(p Policy) { x.r.pol = p }

// Ledger returns the in-progress ledger (do not retain across Step calls).
func (x *Runner) Ledger() *Ledger { return x.r.led }

func (x *Runner) Step(ev Event) error {
	r := x.r
	if ev.Seq <= x.lastSeq || ev.Day < x.lastDay {
		return fmt.Errorf("planner2: events not strictly chronological at seq %d", ev.Seq)
	}
	x.lastSeq, x.lastDay = ev.Seq, ev.Day
	r.led.ByteDays += float64(r.st.Phys()) * float64(ev.Day-x.prevDay)
	x.prevDay = ev.Day
	r.day, r.seq = ev.Day, ev.Seq
	env, p := x.env, r.pol
	switch ev.Kind {
	case Arrive:
		if err := r.arrive(env, ev); err != nil {
			return err
		}
	case Delete:
		if err := r.del(env, ev); err != nil {
			return err
		}
	case Read:
		if err := r.read(env, ev); err != nil {
			return err
		}
	case Tier:
		if o, ok := r.st.Obj(ev.Obj); ok {
			o.Tier = ev.TierHint
		}
		r.apply(env, p.Event(env, ev))
	case Tick:
		r.tick(env, ev)
	default:
		return fmt.Errorf("planner2: unknown event kind %d", ev.Kind)
	}
	if !r.cfg.SkipValidate && (r.cfg.ValidateEvery <= 1 || ev.Seq%r.cfg.ValidateEvery == 0) {
		if err := r.st.Validate(); err != nil {
			return err
		}
	}
	if sn, _ := p.Snapshot(); len(sn) > r.led.StatePeakBytes {
		r.led.StatePeakBytes = len(sn)
	}
	return nil
}

// Store exposes the physical model to tests and reports.
func (x *Runner) Store() *Store { return x.r.st }

func (x *Runner) Finish() *Ledger {
	if !x.r.cfg.SkipValidate {
		if err := x.r.st.Validate(); err != nil {
			panic(err)
		}
	}
	x.r.finish()
	return x.r.led
}

func Run(w *World, p Policy, cfg Config) (*Ledger, error) {
	start := time.Now()
	x := NewRunner(w, p, cfg)
	for _, ev := range w.Events {
		if err := x.Step(ev); err != nil {
			return nil, err
		}
	}
	led := x.Finish()
	led.WallNS = time.Since(start).Nanoseconds()
	return led, nil
}

func (r *runner) arrive(env *Env, ev Event) error {
	if r.arrived[ev.Obj] {
		return fmt.Errorf("planner2: duplicate arrival of object %d", ev.Obj)
	}
	li, st := ev.Lineage, ev.Stage
	if li < 0 || li >= len(r.w.Lineages) || st < 0 || st >= r.w.Lineages[li].Len() {
		return fmt.Errorf("planner2: arrival references unknown stage")
	}
	if _, dup := r.stageObj[[2]int{li, st}]; dup {
		return fmt.Errorf("planner2: duplicate lineage stage")
	}
	l := r.w.Lineages[li]
	r.objLin[ev.Obj], r.objStage[ev.Obj], r.arrived[ev.Obj] = li, st, true
	r.stageObj[[2]int{li, st}] = ev.Obj
	if l.Layer() == "A" {
		r.cdcSketch = append(r.cdcSketch, sk{ev.Obj, l.Sketch(st, CDC)})
	}
	r.putLeft = r.cfg.PUTCPUMS
	if r.cfg.PUTCPUMS == 0 {
		r.putLeft = math.MaxInt64 / 4
	}
	r.inTick = false
	d := r.pol.Arrive(env, Arrival{Seq: ev.Seq, Day: ev.Day, Obj: ev.Obj, Key: ev.Key, Size: ev.Size, TierHint: ev.TierHint})
	scheme, form, reason := d.Scheme, d.Form, d.Reason
	if scheme == "" {
		scheme = CDC
	}
	cand, err := r.pick(env, ev.Obj, scheme, form)
	if err != nil || (d.Sidecar && scheme == CDC) {
		if err != nil {
			r.countReject(err)
		}
		cand, _ = r.pick(env, ev.Obj, CDC, "")
		scheme, form = CDC, cand.Form
		d.Sidecar = false
	}
	// Admission feasibility: capacity. A capacity failure falls back to CDC too.
	obj := &Obj{ID: ev.Obj, Lineage: li, Stage: st, Key: ev.Key, Size: ev.Size, Day: ev.Day, Live: true, Tier: ev.TierHint}
	primary, side := cand, (*Cand)(nil)
	if d.Sidecar && scheme != CDC {
		primary, side = r.mustCDC(env, ev.Obj), &cand
	}
	marg := r.st.Marginal(primary)
	if side != nil {
		marg += r.st.Marginal(*side)
	}
	if err := r.attachAll(ev.Obj, primary, side); err != nil {
		r.led.RejectedSpace++
		primary, side = r.mustCDC(env, ev.Obj), nil
		marg = r.st.Marginal(primary)
		if err2 := r.attachAll(ev.Obj, primary, nil); err2 != nil {
			// out of space even for CDC: the PUT is refused (507); counted as a service failure
			r.led.PutRejected++
			r.rejectedObj[ev.Obj] = true
			r.log(DecisionRec{Seq: ev.Seq, Day: ev.Day, Obj: ev.Obj, Kind: "put-rejected", Reason: err2.Error()})
			return nil
		}
		scheme, form, d.Sidecar = CDC, primary.Form, false
	}
	obj.Primary, obj.Side, obj.Attached = primary, side, true
	r.led.WriteBytes += marg
	if side != nil {
		r.led.Sidecars++
	}
	if err := r.st.AddObj(obj); err != nil {
		return err
	}
	r.led.Objects++
	if st == 0 {
		r.led.FirstScheme[l.Name()] = obj.Primary.Scheme
	}
	rec := DecisionRec{Seq: ev.Seq, Day: ev.Day, Obj: ev.Obj, Kind: "store", Scheme: scheme, Form: form, Marginal: marg, Reason: reason, Sidecar: d.Sidecar}
	r.log(rec)
	for _, a := range d.Defer {
		a.Obj = ev.Obj
		r.enqueue(a)
	}
	r.track()
	return nil
}

func (r *runner) countReject(err error) {
	switch {
	case errors.Is(err, ErrNoAnalysis):
		r.led.RejectedUnanalyzed++
	case errors.Is(err, ErrBudget):
		r.led.RejectedBudget++
	default:
		r.led.RejectedInvalid++
	}
}

// pick finds an analysed candidate form; "" form means the cheapest marginal form
// of the scheme.
func (r *runner) pick(env *Env, obj int, scheme, form string) (Cand, error) {
	forms, err := env.Forms(obj, scheme)
	if err != nil {
		return Cand{}, err
	}
	best := -1
	var bm int64
	for i, c := range forms {
		if form != "" && c.Form != form {
			continue
		}
		if err := r.st.Check(c); err != nil {
			continue
		}
		if m := r.st.Marginal(c); best < 0 || m < bm {
			best, bm = i, m
		}
	}
	if best < 0 {
		return Cand{}, fmt.Errorf("planner2: no valid form %q/%q", scheme, form)
	}
	return forms[best], nil
}

func (r *runner) log(rec DecisionRec) {
	h := r.h
	r.led.Decisions = append(r.led.Decisions, rec)
	fmt.Fprintf(h, "%d|%d|%d|%s|%s|%s|%d|%v\n", rec.Seq, rec.Day, rec.Obj, rec.Kind, rec.Scheme, rec.Form, rec.Marginal, rec.Sidecar)
}

func (r *runner) track() {
	if r.st.Phys() > r.led.Peak {
		r.led.Peak = r.st.Phys()
	}
}

func (r *runner) del(env *Env, ev Event) error {
	o, ok := r.st.Obj(ev.Obj)
	if !ok && r.rejectedObj[ev.Obj] {
		return nil
	}
	if !ok || !o.Live {
		return fmt.Errorf("planner2: delete of absent object %d", ev.Obj)
	}
	o.Live = false
	if o.Attached {
		if err := r.st.Detach(o.Primary); err != nil {
			return err
		}
		if o.Side != nil {
			if err := r.st.Detach(*o.Side); err != nil {
				return err
			}
		}
		o.Attached = false
	}
	// cancel queued tasks of the deleted object
	q := r.queue[:0]
	for _, t := range r.queue {
		if t.Obj == ev.Obj {
			r.led.Cancelled++
			continue
		}
		q = append(q, t)
	}
	r.queue = q
	r.apply(env, r.pol.Event(env, ev))
	return nil
}

func (r *runner) read(env *Env, ev Event) error {
	o, ok := r.st.Obj(ev.Obj)
	if !ok || !o.Live {
		return nil // reads of deleted objects are ignored (404)
	}
	rd := ev.Range
	amp := o.Primary.Amp1M
	minAmp := func(c Cand) float64 {
		switch {
		case rd == 0:
			return 1.0 + 0*c.Amp1M
		case rd <= 8<<10:
			return c.Amp4K
		case rd <= 256<<10:
			return c.Amp64K
		default:
			return c.Amp1M
		}
	}
	amp = minAmp(o.Primary)
	dec := o.Primary.DecodeMiBs
	if o.Side != nil {
		if a := minAmp(*o.Side); a < amp {
			amp, dec = a, o.Side.DecodeMiBs
		}
	}
	logical := rd
	if rd == 0 || rd > o.Size {
		logical = o.Size
	}
	if o.Tier == "hot" && r.cfg.HotAmp64Cap > 0 && rd > 8<<10 && rd <= 256<<10 && amp > r.cfg.HotAmp64Cap && o.Primary.Scheme != CDC {
		r.led.HotViolations++
	}
	served := int64(float64(logical) * amp)
	if rd == 0 {
		// whole-object GET reads the stored closure once: approximate by logical size
		served = logical
	}
	r.led.ServedBytes += served
	r.led.LogicalReadBytes += logical
	r.led.ReadReqs++
	if rd == 0 {
		r.led.WholeReads++
	}
	if dec > 0 {
		r.led.DecodeSeconds += float64(served) / (1 << 20) / dec
	}
	o.Reads++
	o.RangeBytes += logical
	o.LastRead = r.day
	r.apply(env, r.pol.Event(env, ev))
	return nil
}

func (r *runner) tick(env *Env, ev Event) {
	r.inTick = ev.Window
	r.win = ev.Budget
	if !ev.Window {
		r.win = Budget{}
	}
	// age out queued tasks
	q := r.queue[:0]
	for _, t := range r.queue {
		if r.cfg.QueueTTL > 0 && ev.Day-t.Day > r.cfg.QueueTTL {
			r.led.Expired++
			continue
		}
		q = append(q, t)
	}
	r.queue = q
	r.apply(env, r.pol.Event(env, ev))
	if ev.Window {
		r.runQueue(env)
	}
	r.inTick = false
	if ev.GC {
		if r.led.PreGCPhys < r.st.Phys() {
			r.led.PreGCPhys = r.st.Phys()
		}
		eff := r.cfg.GCEfficiency
		if eff <= 0 || eff > 1 {
			eff = 1
		}
		r.st.Collect(eff)
	}
	r.track()
}

func (r *runner) enqueue(a Action) {
	o, ok := r.st.Obj(a.Obj)
	if !ok || !o.Live || a.Scheme == "" || a.Scheme == CDC {
		return
	}
	for _, t := range r.queue {
		if t.Obj == a.Obj && t.Scheme == a.Scheme {
			return
		}
	}
	if len(r.queue) >= r.cfg.QueueCap && r.cfg.QueueCap > 0 {
		r.led.RejectedBudget++
		return
	}
	r.queue = append(r.queue, taskRec{Obj: a.Obj, Scheme: a.Scheme, Priority: a.Priority, Day: r.day, Reason: a.Reason})
	r.led.Enqueued++
	r.log(DecisionRec{Seq: r.seq, Day: r.day, Obj: a.Obj, Kind: "enqueue", Scheme: a.Scheme, Reason: a.Reason})
}

func (r *runner) runQueue(env *Env) {
	sort.SliceStable(r.queue, func(i, j int) bool {
		if r.queue[i].Priority != r.queue[j].Priority {
			return r.queue[i].Priority > r.queue[j].Priority
		}
		return r.queue[i].Obj < r.queue[j].Obj
	})
	var keep []taskRec
	exhausted := false
	for _, t := range r.queue {
		if exhausted {
			keep = append(keep, t)
			continue
		}
		o, ok := r.st.Obj(t.Obj)
		if !ok || !o.Live {
			r.led.Cancelled++
			continue
		}
		if _, err := env.Analyze(t.Obj, t.Scheme); err != nil {
			if errors.Is(err, ErrBudget) {
				r.led.RejectedBudget++
				exhausted = true
				keep = append(keep, t)
				continue
			}
			r.led.RejectedInvalid++
			continue
		}
		r.led.Completed++
		r.apply(env, r.pol.Completed(env, Task{t.Obj, t.Scheme, t.Priority, t.Day}))
	}
	r.queue = keep
}

func (r *runner) finish() {
	if r.led.PreGCPhys < r.st.Phys() {
		r.led.PreGCPhys = r.st.Phys()
	}
	r.led.FinalPhys, r.led.FinalLive, r.led.Peak = r.st.Phys(), r.st.Live(), maxI(r.led.Peak, r.st.Peak)
	r.led.Pinned = r.st.PinnedBytes()
	for _, id := range r.st.ObjIDs() {
		o, _ := r.st.Obj(id)
		if !o.Live {
			continue
		}
		r.led.FinalSchemes[o.Primary.Scheme]++
		if o.Primary.Scheme != CDC {
			r.led.RepObjects++
		}
		if o.Primary.Depth > r.led.MaxDepth {
			r.led.MaxDepth = o.Primary.Depth
		}
		if f := r.st.Fanout(id); f > r.led.MaxFanout {
			r.led.MaxFanout = f
		}
		if b := r.st.BlastRadius(id); b > r.led.MaxBlast {
			r.led.MaxBlast = b
		}
	}
	for li, l := range r.w.Lineages {
		seen := map[CompID]bool{}
		var n int64
		for t := 0; t < l.Len(); t++ {
			if id, ok := r.stageObj[[2]int{li, t}]; ok {
				if o, ok := r.st.Obj(id); ok && o.Attached {
					for _, c := range o.Primary.Comps {
						if !seen[c.ID] {
							seen[c.ID] = true
							n += c.Bytes + r.st.Overhead
						}
					}
				}
			}
		}
		r.led.PerLineageBytes[l.Name()] = n
	}
	r.led.Digest = fmt.Sprintf("%x", r.h.Sum(nil))
}

func maxI(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (r *runner) mustCDC(env *Env, obj int) Cand {
	c, err := r.pick(env, obj, CDC, "")
	if err != nil {
		panic("planner2: validated cdc fallback missing: " + err.Error())
	}
	return c
}

// attachAll attaches primary then an optional sidecar atomically (rollback on failure).
func (r *runner) attachAll(obj int, primary Cand, side *Cand) error {
	if _, _, err := r.st.Attach(obj, primary); err != nil {
		return err
	}
	if side != nil {
		if _, _, err := r.st.Attach(obj, *side); err != nil {
			_ = r.st.Detach(primary)
			return err
		}
	}
	return nil
}

// Simulate applies actions to a copy of the store (no charges, no side effects) and
// returns the resulting live-byte change and the transient peak above the current
// physical size. Only already analysed (object, scheme) pairs can be migrated.
func (e *Env) Simulate(acts []Action) (liveDelta, peakExtra int64) {
	r := e.r
	c := *r
	c.st = r.st.Clone()
	c.st.Peak = c.st.Phys()
	c.led = &Ledger{PerLineageBytes: map[string]int64{}, FinalSchemes: map[string]int{}, ReadsByTier: map[string]int64{}}
	c.h = sha256.New()
	c.inTick = false
	c.cfg.FailMigrate = 0
	c.queue = nil
	ce := &Env{&c}
	before := c.st.Live()
	c.apply(ce, acts)
	return c.st.Live() - before, c.st.Peak - r.st.Phys()
}

func (e *Env) sizeOf(acts []Action) int64 {
	var n int64
	for _, a := range acts {
		if o, ok := e.r.st.Obj(a.Obj); ok {
			n += o.Size
		}
	}
	return n
}

// ReleasableSet is the physical bytes freed if every object in ids were detached:
// components whose references all come from the set.
func (e *Env) ReleasableSet(ids []int) int64 {
	in := map[int]bool{}
	for _, id := range ids {
		in[id] = true
	}
	cnt := map[CompID]int{}
	for _, id := range ids {
		if o, ok := e.r.st.Obj(id); ok && o.Attached {
			for _, c := range o.Primary.Comps {
				cnt[c.ID]++
			}
		}
	}
	var n int64
	for id, k := range cnt {
		if p := e.r.st.comp[id]; p != nil && p.refs == k {
			n += p.bytes + e.r.st.Overhead
		}
	}
	return n
}
