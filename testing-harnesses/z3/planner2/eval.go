package planner2

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
)

// Protocol is the preregistered evaluation protocol (z4/b7/protocol.json).
type Protocol struct {
	Folds map[string][]string  `json:"folds"`
	Grids map[string][]float64 `json:"grids"`
	Seeds []int64              `json:"seeds"`
	Books map[string]PriceBook `json:"-"`
	Raw   struct {
		PriceBooks map[string]struct {
			Storage float64 `json:"storage_usd_gb_month"`
			Horizon float64 `json:"horizon_months"`
			ReadIO  float64 `json:"read_io_usd_gb"`
			CPU     float64 `json:"cpu_usd_core_hour"`
			Migrate float64 `json:"migrate_io_usd_gb"`
		} `json:"price_books"`
		Constraints struct {
			Depth  int     `json:"depth_cap"`
			Fanout int     `json:"fanout_cap"`
			Hot    float64 `json:"hot_range64k_amp_cap"`
		} `json:"constraints"`
	} `json:"-"`
}

func LoadProtocol(path string) (*Protocol, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Protocol{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &p.Raw); err != nil {
		return nil, err
	}
	p.Books = map[string]PriceBook{}
	for k, v := range p.Raw.PriceBooks {
		p.Books[k] = PriceBook{StorageUSDGBMonth: v.Storage, HorizonMonths: v.Horizon, ReadIOUSDGB: v.ReadIO, CPUUSDCoreHour: v.CPU, MigrateIOUSDGB: v.Migrate}
	}
	if len(p.Books) < 2 || len(p.Folds) < 2 || len(p.Seeds) == 0 {
		return nil, fmt.Errorf("planner2: incomplete protocol")
	}
	return p, nil
}

// BookNames returns price-book names in fixed order.
func (p *Protocol) BookNames() []string {
	var n []string
	for k := range p.Books {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (p *Protocol) FoldNames() []string {
	var n []string
	for k := range p.Folds {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Scenario is one labelled simulated chronology/configuration over the real lineages.
type Scenario struct {
	Name string
	Spec func(seed int64) Spec
	Cfg  func(c Config, w *World, cdcFinal int64) Config
}

func Scenarios() []Scenario {
	mk := func(ret, reads, keys string) func(int64) Spec {
		return func(seed int64) Spec {
			sp := DefaultSpec("w")
			sp.Seed, sp.Retention, sp.Reads, sp.KeyMode = seed, ret, reads, keys
			return sp
		}
	}
	id := func(c Config, _ *World, _ int64) Config { return c }
	return []Scenario{
		{"S1-retain-all", mk("all", "none", "good"), id},
		{"S2-last3snap-mixed", mk("last3snap", "mixed", "good"), id},
		{"S3-hot-reads", mk("last3", "hotlatest", "good"), id},
		{"S4-cold-archive", mk("all", "archive", "good"), id},
		{"S5-no-keys", mk("all", "none", "none"), id},
		{"S6-misleading-keys", mk("all", "none", "misleading"), id},
		{"S7-temp-oneoffs", mk("oneoff14", "none", "good"), id},
		{"S8-tight-capacity", mk("all", "none", "good"), func(c Config, _ *World, cdc int64) Config {
			c.Capacity = cdc + cdc/8 // 12.5% headroom over the all-CDC store
			return c
		}},
	}
}

// Row is one (fold, scenario, seed, policy) outcome over a fold world.
type Row struct {
	Fold, Scenario, Policy                                 string
	Seed                                                   int64
	Cost                                                   map[string]float64 // dollars per price book
	Storage                                                map[string]float64
	FinalPhys, Peak, Pinned                                int64
	ByteDays                                               float64
	Served, Logical                                        int64
	Decode                                                 float64
	AnalysisMSSync, AnalysisMSBg, ProbeMS                  float64
	MigrateMS                                              float64
	Migrations, Failed, Reverts, Rebases, Reopts           int
	Rejected                                               int
	Enqueued, Completed, Expired, Cancelled                int
	HotViol                                                int
	MaxDepth, MaxFanout, MaxBlast                          int
	RepObjects, Objects                                    int
	StateBytes                                             int
	WallMS                                                 float64
	BadLineages, MissedLineages, FalseFirstSight, Lineages int
	OracleCost                                             map[string]float64 // sum of per-lineage clairvoyant optima (S1 exact)
	OracleBytes                                            int64
	OracleExact                                            bool
}

// Job fully describes one world to evaluate.
type Job struct {
	Fold      string
	Scenario  Scenario
	Seed      int64
	Lins      []Lineage
	Prior     Prior
	Tuned     map[string]AParams
	Cfg       Config
	Books     map[string]PriceBook
	Policies  []string
	Oracle    bool
	OracleMax int
	Auto      string // validated policy choice; its rows are duplicated as policy \"auto\"
}

func (p *Protocol) BaseConfig() Config {
	c := DefaultConfig()
	c.Overhead = 64
	c.ValidateEvery = 64
	c.DepthCap, c.FanoutCap, c.HotAmp64Cap = p.Raw.Constraints.Depth, p.Raw.Constraints.Fanout, p.Raw.Constraints.Hot
	return c
}

// RunJob runs every policy over one world and returns rows (and nothing hidden).
func RunJob(j Job) ([]Row, error) {
	w := Build(j.Scenario.Spec(j.Seed), j.Lins)
	w.Name = fmt.Sprintf("%s/%s/%d", j.Fold, j.Scenario.Name, j.Seed)
	cfg := j.Cfg
	base, err := Run(w, mustP("always-cdc", j.Prior, nil), cfg)
	if err != nil {
		return nil, err
	}
	cfg = j.Scenario.Cfg(cfg, w, base.FinalPhys)
	cfg.Seed = j.Seed
	base, err = Run(w, mustP("always-cdc", j.Prior, nil), cfg)
	if err != nil {
		return nil, err
	}
	var or map[string]float64
	var orBytes int64
	oracleLin := map[string]int64{}
	exact := true
	if j.Oracle {
		or = map[string]float64{}
		for bn, pb := range j.Books {
			for li := range w.Lineages {
				sub := SubWorld(w, li)
				res, err := Oracle(sub, cfg, func(l *Ledger) float64 { return l.Price(pb).Total }, j.OracleMax)
				if err != nil {
					return nil, err
				}
				or[bn] += res.Cost
				if bn == sortedKey(j.Books) {
					orBytes += res.FinalBytes
					oracleLin[w.Lineages[li].Name()] = res.FinalBytes
				}
				exact = exact && res.Exact
			}
		}
	}
	leds := map[string]*Ledger{}
	for _, name := range j.Policies {
		var tune *AParams
		if t, ok := j.Tuned[name]; ok {
			tune = &t
		}
		p, err := PresetWith(name, j.Prior, tune)
		if err != nil {
			return nil, err
		}
		led := base
		if name != "always-cdc" {
			led, err = Run(w, p, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s on %s: %w", name, w.Name, err)
			}
		}
		leds[name] = led
	}
	// attainable per-lineage minimum: best of every evaluated policy and the oracle plan
	attain := map[string]int64{}
	for _, l := range w.Lineages {
		attain[l.Name()] = base.PerLineageBytes[l.Name()]
		for _, led := range leds {
			if b := led.PerLineageBytes[l.Name()]; b < attain[l.Name()] {
				attain[l.Name()] = b
			}
		}
		if ob, ok := oracleLin[l.Name()]; ok && ob < attain[l.Name()] {
			attain[l.Name()] = ob
		}
	}
	var rows []Row
	for _, name := range j.Policies {
		led := leds[name]
		r := Row{Fold: j.Fold, Scenario: j.Scenario.Name, Policy: name, Seed: j.Seed, Cost: map[string]float64{}, Storage: map[string]float64{},
			FinalPhys: led.FinalPhys, Peak: led.Peak, Pinned: led.Pinned, ByteDays: led.ByteDays, Served: led.ServedBytes, Logical: led.LogicalReadBytes,
			Decode: led.DecodeSeconds, AnalysisMSSync: led.AnalysisMSSync, AnalysisMSBg: led.AnalysisMSBg, ProbeMS: led.ProbeMS, MigrateMS: led.MigrateMS,
			Migrations: led.Migrations, Failed: led.FailedMigrations, Reverts: led.Reverts, Rebases: led.Rebases, Reopts: led.Reopts,
			Rejected: led.RejectedBudget + led.RejectedInvalid + led.RejectedSpace + led.RejectedUnanalyzed,
			Enqueued: led.Enqueued, Completed: led.Completed, Expired: led.Expired, Cancelled: led.Cancelled, HotViol: led.HotViolations,
			MaxDepth: led.MaxDepth, MaxFanout: led.MaxFanout, MaxBlast: led.MaxBlast, RepObjects: led.RepObjects, Objects: led.Objects,
			StateBytes: led.StatePeakBytes, WallMS: float64(led.WallNS) / 1e6, OracleCost: or, OracleBytes: orBytes, OracleExact: exact && j.Oracle}
		for bn, pb := range j.Books {
			d := led.Price(pb)
			r.Cost[bn], r.Storage[bn] = d.Total, d.Storage
		}
		for _, l := range w.Lineages {
			cdcB, pb, at := base.PerLineageBytes[l.Name()], led.PerLineageBytes[l.Name()], attain[l.Name()]
			r.Lineages++
			if name == "always-cdc" {
				continue
			}
			if float64(pb) > 1.005*float64(cdcB) {
				r.BadLineages++
			}
			if float64(at) <= 0.95*float64(cdcB) && float64(pb) >= 0.99*float64(cdcB) {
				r.MissedLineages++
			}
			if led.FirstScheme[l.Name()] != CDC && led.FirstScheme[l.Name()] != "" && float64(pb) > 1.005*float64(cdcB) {
				r.FalseFirstSight++
			}
		}
		rows = append(rows, r)
		if name == j.Auto && name != "" {
			r2 := r
			r2.Policy = "auto"
			rows = append(rows, r2)
		}
	}
	return rows, nil
}

func sortedKey(m map[string]PriceBook) string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k[0]
}

func mustP(name string, pr Prior, t *AParams) Policy {
	p, err := PresetWith(name, pr, t)
	if err != nil {
		panic(err)
	}
	return p
}

// Parallel runs jobs on a bounded worker pool, preserving job order in the output.
func Parallel(jobs []Job, workers int) ([][]Row, error) {
	out := make([][]Row, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	ch := make(chan int)
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				out[i], errs[i] = RunJob(jobs[i])
			}
		}()
	}
	for i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

// SplitFolds partitions lineages by protocol fold; unassigned lineages are an error.
func SplitFolds(p *Protocol, lins []Lineage) (map[string][]Lineage, error) {
	out := map[string][]Lineage{}
	for _, l := range lins {
		f := FoldOf(p.Folds, l)
		if f == "" {
			return nil, fmt.Errorf("planner2: lineage %q matches no protocol fold", l.Name())
		}
		out[f] = append(out[f], l)
	}
	return out, nil
}

func others(folds map[string][]Lineage, except string) []Lineage {
	var out []Lineage
	var names []string
	for k := range folds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if k != except {
			out = append(out, folds[k]...)
		}
	}
	return out
}

// Others exposes the training lineages for a held-out fold.
func Others(folds map[string][]Lineage, except string) []Lineage { return others(folds, except) }

// Tune selects the numeric parameters of each adaptive policy by coordinate search
// using ONLY the training folds (inner leave-one-fold-out priors). The objective is
// the mean cost ratio versus all-CDC averaged over the price books.
func Tune(p *Protocol, folds map[string][]Lineage, heldOut string, policies []string, base Config, workers int) (map[string]AParams, map[string]float64, error) {
	var train []string
	for _, f := range p.FoldNames() {
		if f != heldOut {
			train = append(train, f)
		}
	}
	scen := Scenarios()
	use := []Scenario{scen[0], scen[1]}
	seeds := p.Seeds[:2]
	score := func(name string, a AParams) (float64, error) {
		var jobs []Job
		for _, f := range train {
			pr := Calibrate(others(restrict(folds, train), f))
			for _, sc := range use {
				for _, sd := range seeds {
					jobs = append(jobs, Job{Fold: f, Scenario: sc, Seed: sd, Lins: folds[f], Prior: pr, Tuned: map[string]AParams{name: a},
						Cfg: base, Books: p.Books, Policies: []string{"always-cdc", name}})
				}
			}
		}
		res, err := Parallel(jobs, workers)
		if err != nil {
			return 0, err
		}
		var sum float64
		var n int
		for _, rows := range res {
			var cdc map[string]float64
			for _, r := range rows {
				if r.Policy == "always-cdc" {
					cdc = r.Cost
				}
			}
			for _, r := range rows {
				if r.Policy == name {
					for bn, c := range r.Cost {
						sum += c / cdc[bn]
						n++
					}
				}
			}
		}
		return sum / float64(n), nil
	}
	out := map[string]AParams{}
	scores := map[string]float64{}
	for _, name := range policies {
		pol, err := Preset(name, DefaultPrior())
		if err != nil {
			return nil, nil, err
		}
		ad, ok := pol.(*Adaptive)
		if !ok {
			continue
		}
		cur := ad.P
		best, err := score(name, cur)
		if err != nil {
			return nil, nil, err
		}
		type setter func(*AParams, float64)
		axes := []struct {
			key string
			set setter
		}{
			{"premium_cap", func(a *AParams, v float64) { a.PremiumCap = v }},
			{"prior_strength", func(a *AParams, v float64) { a.K = v }},
			{"min_conf", func(a *AParams, v float64) { a.MinConf = v }},
			{"invest_horizon", func(a *AParams, v float64) { a.Horizon = int(v) }},
		}
		for _, ax := range axes {
			for _, v := range p.Grids[ax.key] {
				cand := cur
				ax.set(&cand, v)
				s, err := score(name, cand)
				if err != nil {
					return nil, nil, err
				}
				if s < best-1e-9 {
					best, cur = s, cand
				}
			}
		}
		out[name] = cur
		scores[name] = best
	}
	return out, scores, nil
}

func restrict(folds map[string][]Lineage, keep []string) map[string][]Lineage {
	out := map[string][]Lineage{}
	for _, k := range keep {
		out[k] = folds[k]
	}
	return out
}

// Mean returns the arithmetic mean.
func Mean(x []float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}
