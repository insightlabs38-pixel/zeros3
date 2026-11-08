package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	pl "zeros3-z3/planner2"
)

type expEnv struct {
	proto  *pl.Protocol
	folds  map[string][]pl.Lineage
	base   pl.Config
	tuned  map[string]map[string]pl.AParams
	result map[string]interface{}
	book   string // price book the policies plan under (A_storage unless an experiment says otherwise)
}

func newExp(lins []pl.Lineage) *expEnv {
	proto, err := pl.LoadProtocol(*protoP)
	die(err)
	folds, err := pl.SplitFolds(proto, lins)
	die(err)
	e := &expEnv{proto: proto, folds: folds, base: proto.BaseConfig(), result: map[string]interface{}{}, book: "A_storage"}
	e.base.Prices = proto.Books["A_storage"]
	if _, err := os.Stat(*out); err == nil {
		e.tuned = loadRows().Tuned
	}
	return e
}

func (e *expEnv) foldOf(l pl.Lineage) string { return pl.FoldOf(e.proto.Folds, l) }

func (e *expEnv) tunedFor(fold, name string) *pl.AParams {
	if e.tuned == nil {
		return nil
	}
	if t, ok := e.tuned[e.book+"/"+fold][name]; ok {
		return &t
	}
	return nil
}

func (e *expEnv) prior(fold string) pl.Prior { return pl.Calibrate(pl.Others(e.folds, fold)) }

func (e *expEnv) save() {
	fh, err := os.Create("../z4/b7/experiments.json")
	die(err)
	enc := json.NewEncoder(fh)
	enc.SetIndent("", " ")
	die(enc.Encode(e.result))
	die(fh.Close())
}

func totalDollars(l *pl.Ledger, b pl.PriceBook) float64 { return l.Price(b).Total }

// runOne evaluates a policy on a single lineage in a given spec with the held-out prior.
func (e *expEnv) runWorld(w *pl.World, fold, name string, cfg pl.Config) *pl.Ledger {
	cfg.Prices = e.proto.Books[e.book]
	p, err := pl.PresetWith(name, e.prior(fold), e.tunedFor(fold, name))
	die(err)
	led, err := pl.Run(w, p, cfg)
	die(err)
	return led
}

func expAll(lins []pl.Lineage) {
	e := newExp(lins)
	e.expRepro()
	e.expB2()
	e.expGroup()
	e.expCalib()
	e.expSched()
	e.expReads()
	e.expSens()
	e.expOneOff()
	e.expReopt()
	e.save()
}

// ---------------------------------------------------------------- A: reproduction + architecture
func (e *expEnv) expRepro() {
	fmt.Println("\n=== A. Z3-36 policies under old vs new candidate architecture (arrivals only, storage bytes, exact oracle)")
	type cell struct {
		tot, cdc, orc float64
		n, bad, miss  int
	}
	policies := []string{"always-cdc", "greedy", "premium-cap", "late-bootstrap", "expected-value", "lineage-prior", "combined"}
	res := map[string]map[string]*cell{"A:Z3-36 frontier (additive-era reps)": {}, "B:Z4 captured series (B1-B6)": {}}
	for _, f := range e.proto.FoldNames() {
		for _, l := range e.folds[f] {
			layer := "A:Z3-36 frontier (additive-era reps)"
			if l.Layer() == "B" {
				layer = "B:Z4 captured series (B1-B6)"
			}
			w := pl.ArrivalsOnly(l)
			cfg := e.base
			cfg.ValidateEvery = 0
			orc, err := pl.Oracle(pl.SubWorld(w, 0), cfg, func(x *pl.Ledger) float64 { return float64(x.FinalPhys) }, 200000)
			die(err)
			cdc := e.runWorld(w, f, "always-cdc", cfg)
			for _, p := range policies {
				led := e.runWorld(w, f, p, cfg)
				c := res[layer][p]
				if c == nil {
					c = &cell{}
					res[layer][p] = c
				}
				c.tot += float64(led.FinalPhys)
				c.cdc += float64(cdc.FinalPhys)
				c.orc += float64(orc.FinalBytes)
				c.n++
				if led.FinalPhys > cdc.FinalPhys+cdc.FinalPhys/200 {
					c.bad++
				}
				if float64(orc.FinalBytes) <= 0.95*float64(cdc.FinalPhys) && float64(led.FinalPhys) >= 0.99*float64(cdc.FinalPhys) {
					c.miss++
				}
			}
		}
	}
	out := map[string]interface{}{}
	for layer, m := range res {
		fmt.Printf("%s\n%-16s %6s %10s %10s %8s %6s %6s\n", layer, "policy", "n", "total/cdc", "total/orcl", "regret", "bad", "missed")
		for _, p := range policies {
			c := m[p]
			fmt.Printf("%-16s %6d %10.4f %10.4f %7.2f%% %6d %6d\n", p, c.n, c.tot/c.cdc, c.tot/c.orc, 100*(c.tot/c.orc-1), c.bad, c.miss)
			out[layer+"|"+p] = map[string]float64{"n": float64(c.n), "total_over_cdc": c.tot / c.cdc, "regret_pct": 100 * (c.tot/c.orc - 1), "bad": float64(c.bad), "missed": float64(c.miss), "oracle_over_cdc": c.orc / c.cdc}
		}
	}
	e.result["A_repro"] = out
}

// ---------------------------------------------------------------- F: named group cases
func (e *expEnv) expGroup() {
	fmt.Println("\n=== F. Group cases: final bytes / always-cdc (S1 arrivals+ticks, retain-all); oracle = static clairvoyant")
	names := []string{"sst-lz4-rocksdb", "b3-smollm2-forward", "b3-smollm2-reverse", "b4-llvm-o2", "b4-pdb", "b5-pythia70m", "b5-smollm2", "b1-rocksdb-sst", "parquet-lz4-chain", "b1-dex-fdroid", "b1-clr-newtonsoft"}
	pols := []string{"greedy", "premium-cap", "expected-value", "class-prior", "cons", "cons-group", "combined-no-defer", "combined", "ORACLE"}
	fmt.Printf("%-22s", "case")
	for _, p := range pols {
		fmt.Printf(" %9.9s", p)
	}
	fmt.Printf(" %8s %8s\n", "mig", "peak/cdc")
	out := map[string]interface{}{}
	for _, f := range e.proto.FoldNames() {
		for _, l := range e.folds[f] {
			ok := false
			for _, n := range names {
				ok = ok || n == l.Name()
			}
			if !ok {
				continue
			}
			sp := pl.DefaultSpec("g")
			sp.Seed = 11
			w := pl.Build(sp, []pl.Lineage{l})
			cfg := e.base
			cdc := e.runWorld(w, f, "always-cdc", cfg)
			fmt.Printf("%-22.22s", l.Name())
			row := map[string]float64{}
			var mig int
			var peak float64
			for _, p := range pols {
				var v float64
				if p == "ORACLE" {
					o, err := pl.Oracle(pl.SubWorld(w, 0), cfg, func(x *pl.Ledger) float64 { return float64(x.FinalPhys) }, 150000)
					die(err)
					v = float64(o.FinalBytes) / float64(cdc.FinalPhys)
				} else {
					led := e.runWorld(w, f, p, cfg)
					v = float64(led.FinalPhys) / float64(cdc.FinalPhys)
					if p == "cons-group" {
						mig, peak = led.Migrations, float64(led.Peak)/float64(cdc.FinalPhys)
					}
				}
				row[p] = v
				fmt.Printf(" %9.3f", v)
			}
			fmt.Printf(" %8d %8.2f\n", mig, peak)
			out[l.Name()] = row
		}
	}
	e.result["F_group"] = out
}

// ---------------------------------------------------------------- E: calibration
func (e *expEnv) expCalib() {
	fmt.Println("\n=== E. Calibration of the lifetime-weighted EV policy (best simple adaptive) (predicted vs realized), held-out priors, S1 worlds, seeds 11,23")
	type bin struct {
		n          int
		pred, real float64
	}
	rho := make([]bin, 5)
	qb := make([]bin, 5)
	var admitted, profitableAdm, profitable, caught int
	for _, f := range e.proto.FoldNames() {
		for _, sd := range []int64{11, 23} {
			sp := pl.DefaultSpec("c")
			sp.Seed = sd
			w := pl.Build(sp, e.folds[f])
			pol, err := pl.PresetWith("lifetime", e.prior(f), e.tunedFor(f, "lifetime"))
			die(err)
			ad := pol.(*pl.Adaptive)
			led, err := pl.Run(w, ad, e.base)
			die(err)
			cdc := e.runWorld(w, f, "always-cdc", e.base)
			stage := map[int][2]int{}
			for _, ev := range w.Events {
				if ev.Kind == pl.Arrive {
					stage[ev.Obj] = [2]int{ev.Lineage, ev.Stage}
				}
			}
			for _, t := range ad.Trace {
				ls := stage[t.Obj]
				l := w.Lineages[ls[0]]
				hasNext := ls[1]+1 < l.Len()
				bq := int(math.Min(4, t.Q*5))
				qb[bq].n++
				qb[bq].pred += t.Q
				if hasNext {
					qb[bq].real++
					r := 0.0
					if l.Layer() == "B" {
						r = l.Reuse(ls[1]+1, t.Scheme)
					} else {
						r = pl.Overlap(l.Sketch(ls[1], t.Scheme), l.Sketch(ls[1]+1, t.Scheme))
					}
					br := int(math.Min(4, t.Rho*5))
					rho[br].n++
					rho[br].pred += t.Rho
					rho[br].real += r
				}
			}
			for _, l := range w.Lineages {
				cb := float64(cdc.PerLineageBytes[l.Name()])
				pb := float64(led.PerLineageBytes[l.Name()])
				first := led.FirstScheme[l.Name()]
				if pb <= 0.95*cb {
					profitable++
				}
				if first != pl.CDC && first != "" {
					admitted++
					if pb <= 0.995*cb {
						profitableAdm++
					}
				}
				if pb <= 0.95*cb {
					caught++
				}
			}
		}
	}
	fmt.Println("reuse prediction (rep scheme, next arrival): bin  n  mean-pred  mean-realized")
	rr := []map[string]float64{}
	for i, b := range rho {
		if b.n > 0 {
			fmt.Printf("  [%.1f,%.1f) %5d %8.3f %8.3f\n", float64(i)/5, float64(i+1)/5, b.n, b.pred/float64(b.n), b.real/float64(b.n))
			rr = append(rr, map[string]float64{"bin": float64(i), "n": float64(b.n), "pred": b.pred / float64(b.n), "real": b.real / float64(b.n)})
		}
	}
	fmt.Println("continuation q: bin n mean-pred realized-rate")
	qq := []map[string]float64{}
	for i, b := range qb {
		if b.n > 0 {
			fmt.Printf("  [%.1f,%.1f) %5d %8.3f %8.3f\n", float64(i)/5, float64(i+1)/5, b.n, b.pred/float64(b.n), b.real/float64(b.n))
			qq = append(qq, map[string]float64{"bin": float64(i), "n": float64(b.n), "pred": b.pred / float64(b.n), "real": b.real / float64(b.n)})
		}
	}
	prec := 0.0
	if admitted > 0 {
		prec = float64(profitableAdm) / float64(admitted)
	}
	fmt.Printf("first-sight investments %d, paid off (>=0.5%% saving) %d => precision %.2f; lineages saving>=5%% total: %d\n", admitted, profitableAdm, prec, profitable)
	e.result["E_calibration"] = map[string]interface{}{"rho": rr, "q": qq, "first_sight_investments": admitted, "paid_off": profitableAdm, "precision": prec}
}

// ---------------------------------------------------------------- H: scheduling / budgets / probe quality
func (e *expEnv) expSched() {
	fmt.Println("\n=== H. Analysis scheduling: S2-style world (last3snap, mixed reads), all folds pooled, seed 11; cost ratio vs always-cdc (book A)")
	book := e.proto.Books["A_storage"]
	type v struct {
		name string
		mod  func(*pl.Config, *pl.AParams)
		pol  string
	}
	sweeps := []v{
		{"analyze-on-PUT (unbounded)", func(c *pl.Config, a *pl.AParams) { a.Defer, a.PUTAnalyzeMS = false, 1e12 }, "combined"},
		{"PUT<=1000ms else defer", func(c *pl.Config, a *pl.AParams) { a.PUTAnalyzeMS = 1000 }, "combined"},
		{"PUT<=200ms else defer", func(c *pl.Config, a *pl.AParams) { a.PUTAnalyzeMS = 200 }, "combined"},
		{"PUT<=20ms else defer", func(c *pl.Config, a *pl.AParams) { a.PUTAnalyzeMS = 20 }, "combined"},
		{"never analyze on PUT (all deferred)", func(c *pl.Config, a *pl.AParams) { a.PUTAnalyzeMS = 0 }, "combined"},
		{"deferred, window CPU 10s", func(c *pl.Config, a *pl.AParams) { a.PUTAnalyzeMS = 0 }, "combined"},
		{"no probe (conf off)", func(c *pl.Config, a *pl.AParams) { a.Conf = false }, "combined"},
		{"probe recall .5/fpr .2", func(c *pl.Config, a *pl.AParams) { c.ProbeRecall, c.ProbeFPR = .5, .2 }, "combined"},
		{"probe recall .75/fpr .1", func(c *pl.Config, a *pl.AParams) { c.ProbeRecall, c.ProbeFPR = .75, .1 }, "combined"},
		{"probe perfect", func(c *pl.Config, a *pl.AParams) { c.ProbeRecall, c.ProbeFPR, c.ProbeNoise = 1, 0, 0 }, "combined"},
		{"PUT budget 50ms hard (no defer)", func(c *pl.Config, a *pl.AParams) { c.PUTCPUMS = 50; a.Defer, a.PUTAnalyzeMS = false, 1e12 }, "combined"},
	}
	windows := map[string]pl.Budget{"deferred, window CPU 10s": {CPUMS: 10_000, IOBytes: 1 << 30}}
	out := []map[string]interface{}{}
	fmt.Printf("%-38s %8s %9s %9s %8s %8s %7s %7s\n", "variant", "cost/cdc", "syncMS", "bgMS", "migr", "expired", "missed", "bad")
	for _, s := range sweeps {
		var cost, cdcCost float64
		var sync, bg float64
		var mig, exp, missed, bad int
		for _, f := range e.proto.FoldNames() {
			sp := pl.DefaultSpec("h")
			sp.Seed, sp.Retention, sp.Reads = 11, "last3snap", "mixed"
			if b, ok := windows[s.name]; ok {
				sp.Window = b
			}
			w := pl.Build(sp, e.folds[f])
			cfg := e.base
			var tune pl.AParams
			if t := e.tunedFor(f, s.pol); t != nil {
				tune = *t
			} else {
				pp, _ := pl.Preset(s.pol, pl.DefaultPrior())
				tune = pp.(*pl.Adaptive).P
			}
			pp, _ := pl.PresetWith(s.pol, e.prior(f), &tune)
			ad := pp.(*pl.Adaptive)
			ad.P.PremiumCap, ad.P.K, ad.P.MinConf, ad.P.Horizon = tune.PremiumCap, tune.K, tune.MinConf, tune.Horizon
			s.mod(&cfg, &ad.P)
			led, err := pl.Run(w, ad, cfg)
			die(err)
			cdc := e.runWorld(w, f, "always-cdc", cfg)
			cost += led.Price(book).Total
			cdcCost += cdc.Price(book).Total
			sync += led.AnalysisMSSync
			bg += led.AnalysisMSBg
			mig += led.Migrations
			exp += led.Expired
			for _, l := range w.Lineages {
				cb, pb := float64(cdc.PerLineageBytes[l.Name()]), float64(led.PerLineageBytes[l.Name()])
				if pb > 1.005*cb {
					bad++
				}
				if pb >= 0.99*cb {
					missed++ // not improved (includes truly unprofitable lineages; compare across rows)
				}
			}
		}
		fmt.Printf("%-38s %8.3f %9.0f %9.0f %8d %8d %7d %7d\n", s.name, cost/cdcCost, sync, bg, mig, exp, missed, bad)
		out = append(out, map[string]interface{}{"variant": s.name, "cost_over_cdc": cost / cdcCost, "sync_ms": sync, "bg_ms": bg, "migrations": mig, "expired": exp, "not_improved": missed, "bad": bad})
	}
	e.result["H_sched"] = out
}

// ---------------------------------------------------------------- G: reads, tiers, Pareto
func (e *expEnv) expReads() {
	fmt.Println("\n=== G. Read/tier trade-off: S3 hot-reads and S4 cold-archive worlds pooled over folds (seed 11); each book plans AND prices under itself")
	pols := []string{"always-cdc", "greedy", "expected-value", "lineage-prior", "class-prior", "read-ev", "deferred", "combined", "cons-group"}
	out := []map[string]interface{}{}
	for _, bn := range []string{"A_storage", "B_io_heavy"} {
		e.book = bn
		pb := e.proto.Books[bn]
		for _, scn := range []string{"S3", "S4"} {
			type pt struct {
				b, sv float64
				name  string
			}
			var pts []pt
			fmt.Printf("%s %s: %-16s %10s %12s %10s %8s %8s\n", bn, scn, "policy", "bytes/cdc", "served/cdc", "cost/cdc", "hotViol", "maxBlast")
			for _, p := range pols {
				var b, bc, sv, svc, cb, cbc float64
				var viol, blast int
				for _, f := range e.proto.FoldNames() {
					for _, sc := range pl.Scenarios() {
						if sc.Name[:2] != scn {
							continue
						}
						w := pl.Build(sc.Spec(11), e.folds[f])
						led := e.runWorld(w, f, p, e.base)
						cdc := e.runWorld(w, f, "always-cdc", e.base)
						b += float64(led.FinalPhys)
						bc += float64(cdc.FinalPhys)
						sv += float64(led.ServedBytes)
						svc += float64(cdc.ServedBytes)
						cb += led.Price(pb).Total
						cbc += cdc.Price(pb).Total
						viol += led.HotViolations
						if led.MaxBlast > blast {
							blast = led.MaxBlast
						}
					}
				}
				pts = append(pts, pt{b / bc, sv / svc, p})
				fmt.Printf("%s %s: %-16s %10.3f %12.3f %10.3f %8d %8d\n", bn, scn, p, b/bc, sv/svc, cb/cbc, viol, blast)
				out = append(out, map[string]interface{}{"book": bn, "scenario": scn, "policy": p, "bytes": b / bc, "served": sv / svc, "cost": cb / cbc, "hot_violations": viol, "max_blast": blast})
			}
			fmt.Printf("%s %s Pareto-optimal on (bytes, served read bytes):", bn, scn)
			for i, p := range pts {
				dom := false
				for j, q := range pts {
					if j != i && q.b <= p.b+1e-9 && q.sv <= p.sv+1e-9 && (q.b < p.b-1e-9 || q.sv < p.sv-1e-9) {
						dom = true
					}
				}
				if !dom {
					fmt.Printf(" %s", p.name)
				}
			}
			fmt.Println()
		}
	}
	e.book = "A_storage"
	e.result["G_reads"] = out
}

// ---------------------------------------------------------------- sensitivity / worst case
func (e *expEnv) expSens() {
	fmt.Println("\n=== Sensitivity: adaptive policies vs best simple baseline (cost/cdc, book A), S1+S2 pooled, seed 11")
	book := e.proto.Books["A_storage"]
	simple := []string{"greedy", "premium-cap", "expected-value", "late-bootstrap"}
	type mod struct {
		name  string
		cfg   func(*pl.Config)
		spec  func(*pl.Spec)
		trunc int
	}
	mods := []mod{
		{"baseline", func(c *pl.Config) {}, func(s *pl.Spec) {}, 0},
		{"overhead 0 B/object", func(c *pl.Config) { c.Overhead = 0 }, func(s *pl.Spec) {}, 0},
		{"overhead 256 B/object", func(c *pl.Config) { c.Overhead = 256 }, func(s *pl.Spec) {}, 0},
		{"GC reclaims 70%/collection", func(c *pl.Config) { c.GCEfficiency = 0.7 }, func(s *pl.Spec) {}, 0},
		{"20% migration failures", func(c *pl.Config) { c.FailMigrate = 0.2 }, func(s *pl.Spec) {}, 0},
		{"no key hints", func(c *pl.Config) {}, func(s *pl.Spec) { s.KeyMode = "none" }, 0},
		{"misleading key hints", func(c *pl.Config) {}, func(s *pl.Spec) { s.KeyMode = "misleading" }, 0},
		{"histories truncated to 2 stages", func(c *pl.Config) {}, func(s *pl.Spec) {}, 2},
		{"histories truncated to 3 stages", func(c *pl.Config) {}, func(s *pl.Spec) {}, 3},
		{"depth cap 1", func(c *pl.Config) { c.DepthCap = 1 }, func(s *pl.Spec) {}, 0},
		{"fanout cap 1", func(c *pl.Config) { c.FanoutCap = 1 }, func(s *pl.Spec) {}, 0},
		{"speculative staging x3.6 (B2 peak)", func(c *pl.Config) { c.SpecFactor = 3.6 }, func(s *pl.Spec) {}, 0},
		{"tiny maintenance windows", func(c *pl.Config) {}, func(s *pl.Spec) { s.Window = pl.Budget{CPUMS: 500, IOBytes: 1 << 20} }, 0},
	}
	out := []map[string]interface{}{}
	fmt.Printf("%-34s %9s %9s %9s %9s %s\n", "condition", "combined", "lifetime", "classprior", "bestSimple", "best simple")
	for _, m := range mods {
		sum := map[string]float64{}
		var cdcSum float64
		for _, f := range e.proto.FoldNames() {
			lins := e.folds[f]
			if m.trunc > 0 {
				lins = nil
				for _, l := range e.folds[f] {
					lins = append(lins, pl.Truncate(l, m.trunc))
				}
			}
			for _, ret := range []string{"all", "last3snap"} {
				sp := pl.DefaultSpec("s")
				sp.Seed, sp.Retention = 11, ret
				if ret != "all" {
					sp.Reads = "mixed"
				}
				m.spec(&sp)
				w := pl.Build(sp, lins)
				cfg := e.base
				m.cfg(&cfg)
				cdcSum += e.runWorld(w, f, "always-cdc", cfg).Price(book).Total
				for _, p := range append([]string{"combined", "lifetime", "class-prior"}, simple...) {
					sum[p] += e.runWorld(w, f, p, cfg).Price(book).Total
				}
			}
		}
		best, bn := math.Inf(1), ""
		for _, p := range simple {
			if sum[p] < best {
				best, bn = sum[p], p
			}
		}
		fmt.Printf("%-34s %9.3f %9.3f %9.3f %9.3f %s\n", m.name, sum["combined"]/cdcSum, sum["lifetime"]/cdcSum, sum["class-prior"]/cdcSum, best/cdcSum, bn)
		out = append(out, map[string]interface{}{"condition": m.name, "combined": sum["combined"] / cdcSum, "lifetime": sum["lifetime"] / cdcSum, "class_prior": sum["class-prior"] / cdcSum, "best_simple": best / cdcSum, "best_simple_name": bn})
	}
	e.result["Sens"] = out
	_ = sort.Strings
}

// ---------------------------------------------------------------- D: B2 isolated physical reference
func (e *expEnv) expB2() {
	fmt.Println("\n=== D. Model vs B2 measured physical store (draft #97 economics rows; different inputs than B1, so ratios are compared, not bytes)")
	raw, err := os.ReadFile("../z4/b7/b2_reference.json")
	die(err)
	var ref struct {
		Families map[string]map[string]map[string]float64 `json:"families"`
	}
	die(json.Unmarshal(raw, &ref))
	pairs := [][2]string{{"b1-clr-newtonsoft", "clr-short"}, {"b1-dex-fdroid", "dex"}, {"b1-parquet-chain", "parquet"}, {"b1-source-archives", "src-tar-short"}, {"b1-git-history", "git"}}
	fmt.Printf("%-20s | %-34s | %-34s | %-30s\n", "family", "additive sidecar (model | B2)", "archive all-rep (model | B2 best)", "hot-hybrid newest=CDC (model | B2)")
	out := []map[string]interface{}{}
	for _, f := range e.proto.FoldNames() {
		for _, l := range e.folds[f] {
			for _, pr := range pairs {
				if l.Name() != pr[0] {
					continue
				}
				b := ref.Families[pr[1]]
				cdc := b["cdc"]["phys"]
				n := l.Len()
				w := pl.ArrivalsOnly(l)
				cfg := e.base
				cfg.ValidateEvery = 0
				plan := func(last bool) float64 {
					m := map[int]pl.PlanChoice{}
					for t := 0; t < n; t++ {
						m[t] = pl.PlanChoice{Scheme: "b1-delta"}
						if last && t == n-1 {
							m[t] = pl.PlanChoice{Scheme: pl.CDC}
						}
					}
					led, err := pl.Run(w, &pl.PlanPolicy{Plan: m, Tag: "ref"}, cfg)
					die(err)
					return float64(led.FinalPhys)
				}
				cd, err := pl.Run(w, mustPolicy("always-cdc", e.prior(f)), cfg)
				die(err)
				base := float64(cd.FinalPhys)
				arch := plan(false) / base
				hyb := plan(true) / base
				bestB := math.Inf(1)
				for _, k := range []string{"auth-archive-oldest", "auth-archive-newest", "auth-archive-depth4"} {
					if v, ok := b[k]["phys"]; ok && v < bestB {
						bestB = v
					}
				}
				best := bestB / cdc
				fmt.Printf("%-20s | %6.2fx | %6.2fx (CDC+rep)      | %6.3fx | %6.3fx                    | %6.3fx | %6.3fx\n", pr[0], 1+arch, b["cdc+additive-rep"]["phys"]/cdc, arch, best, hyb, b["auth-hot-hybrid"]["phys"]/cdc)
				out = append(out, map[string]interface{}{"family": pr[0], "b2": pr[1], "model_additive": 1 + arch, "b2_additive": b["cdc+additive-rep"]["phys"] / cdc,
					"model_archive": arch, "b2_archive_best": best, "model_hybrid": hyb, "b2_hybrid": b["auth-hot-hybrid"]["phys"] / cdc,
					"b2_peak_over_cdc": b["auth-hot-hybrid"]["peak_during_migration"] / cdc, "b2_mixed_greedy": b["auth-mixed-oldest"]["phys"] / cdc})
			}
		}
	}
	e.result["D_b2_reference"] = out
}

func mustPolicy(n string, p pl.Prior) pl.Policy {
	x, err := pl.Preset(n, p)
	die(err)
	return x
}

// ---------------------------------------------------------------- one-off / unrelated objects
func (e *expEnv) expOneOff() {
	fmt.Println("\n=== One-off and unrelated objects: bytes / always-cdc (arrival only, held-out priors)")
	pols := []string{"greedy", "premium-cap", "expected-value", "late-bootstrap", "class-prior", "lifetime", "cons-group", "combined"}
	type acc struct {
		b, c float64
		n    int
	}
	res := map[string]*acc{}
	for _, f := range e.proto.FoldNames() {
		_, zl, err := pl.LoadFrontier(*frontier)
		die(err)
		var singles []pl.Lineage
		for _, l := range e.folds[f] {
			if l.Len() == 1 {
				singles = append(singles, l)
			}
		}
		if f != "" {
			for _, z := range zl {
				n := z.Name()
				if len(n) > 8 && n[:8] == "one-off/" && e.foldOf(&oneOffName{z}) == f {
					singles = append(singles, z)
				}
			}
		}
		for _, l := range singles {
			w := pl.ArrivalsOnly(l)
			cdc := e.runWorld(w, f, "always-cdc", e.base)
			for _, p := range pols {
				led := e.runWorld(w, f, p, e.base)
				a := res[p]
				if a == nil {
					a = &acc{}
					res[p] = a
				}
				a.b += float64(led.FinalPhys)
				a.c += float64(cdc.FinalPhys)
				a.n++
			}
		}
	}
	out := map[string]interface{}{}
	for _, p := range pols {
		a := res[p]
		fmt.Printf("  %-16s n=%2d bytes/cdc %.4f (one-off penalty %+.2f%%)\n", p, a.n, a.b/a.c, 100*(a.b/a.c-1))
		out[p] = map[string]float64{"n": float64(a.n), "bytes_over_cdc": a.b / a.c}
	}
	e.result["OneOff"] = out
}

// oneOffName lets FoldOf see the base name of a "one-off/<case>" Z3 lineage.
type oneOffName struct{ pl.Lineage }

func (o *oneOffName) Name() string { return o.Lineage.Name()[len("one-off/"):] }

// ---------------------------------------------------------------- B3 optimize economics + executor
func (e *expEnv) expReopt() {
	fmt.Println("\n=== B3 group reoptimisation (measured: 438.0 MB -> 283.1 MB, 48.5 s CPU, ~1.0 GiB RSS): break-even horizon")
	book := e.proto.Books["A_storage"]
	freed := 437985558.0 - 283123663.0
	io := 2 * 1495370725.0 * book.MigrateIOUSDGB / 1e9 // read every file, write rebased components
	cpu := 48.5 / 3600 * book.CPUUSDCoreHour
	perMonth := freed * book.StorageUSDGBMonth / 1e9
	be := (io + cpu) / perMonth
	fmt.Printf("  freed %.0f MB; migration IO $%.4f + CPU $%.5f; saves $%.5f/month => break-even %.1f months (book A horizon %.0f)\n", freed/1e6, io, cpu, perMonth, be, book.HorizonMonths)
	e.result["B3_reopt_breakeven_months"] = be
}
