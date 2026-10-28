package admission

import (
	"fmt"
	"sort"
	"time"
)

type bits []uint64

func makeBits(n int) bits     { return make(bits, (n+63)/64) }
func (b bits) has(i int) bool { return b[i/64]>>(i%64)&1 != 0 }
func (b bits) add(i int)      { b[i/64] |= 1 << (i % 64) }
func (b bits) clone() bits    { return append(bits(nil), b...) }
func (c *Corpus) cost(p Candidate, have bits) int64 {
	var n int64
	for _, id := range p.Objects {
		if !have.has(id) {
			n += c.Objects[id].Packed + c.ObjectOverhead
		}
	}
	return n
}
func apply(p Candidate, have bits) {
	for _, id := range p.Objects {
		have.add(id)
	}
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
func cdc(s Stage) int {
	for i, p := range s.Candidates {
		if p.Name == "cdc" {
			return i
		}
	}
	panic("validated fallback missing")
}
func (c *Corpus) greedy(s Stage, have bits) int {
	best := cdc(s)
	for i, p := range s.Candidates {
		if c.cost(p, have) < c.cost(s.Candidates[best], have) {
			best = i
		}
	}
	return best
}

type Result struct {
	LogicalReadBytes, ExtraReadBytes, SketchBytes, SampleStateBytes, MembershipBytes int64
	BootstrapRedoReadUpper, BootstrapRedoAnalysisNS                                  int64
	BootstrapRedoCalls, OracleStates                                                 int
	Policy, Case                                                                     string
	Total, CDC, Oracle, Regret                                                       int64
	FirstPremium                                                                     float64
	BadAdmissions, MissedLineages, States, AnalysisCalls                             int
	AnalysisNS, FeatureReads, EarlyReadBytes, BufferedBytes, WallNS                  int64
	Choices                                                                          []string
	PerStage                                                                         []int64
	OracleExact                                                                      bool
}
type oracleResult struct {
	cost    int64
	choices []int
	states  int
	exact   bool
}

// Oracle performs bounded exact branch-and-bound only on small cases. A reached
// state cap remains an explicit non-exact upper bound, never a claimed oracle.
func (c *Corpus) oracle(f Case, cap int) oracleResult {
	have := makeBits(len(c.Objects))
	upper := int64(0)
	for _, s := range f.Stages {
		i := c.greedy(s, have)
		upper += c.cost(s.Candidates[i], have)
		apply(s.Candidates[i], have)
	}
	out := oracleResult{cost: upper, exact: true}
	var search func(int, int64, bits, []int)
	search = func(at int, cost int64, have bits, path []int) {
		out.states++
		if out.states > cap {
			out.exact = false
			return
		}
		if cost > out.cost {
			return
		}
		if at == len(f.Stages) {
			if cost <= out.cost {
				out.cost = cost
				out.choices = append([]int(nil), path...)
			}
			return
		}
		s := f.Stages[at]
		order := make([]int, len(s.Candidates))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool {
			return c.cost(s.Candidates[order[i]], have) < c.cost(s.Candidates[order[j]], have)
		})
		for _, i := range order {
			p := s.Candidates[i]
			n := c.cost(p, have)
			next := have.clone()
			apply(p, next)
			search(at+1, cost+n, next, append(path, i))
			if !out.exact {
				return
			}
		}
	}
	search(0, 0, makeBits(len(c.Objects)), nil)
	return out
}

type beamState struct {
	have  bits
	cost  int64
	first int
}

func (c *Corpus) look(f Case, at, depth, width int, have bits) (int, int) {
	states := []beamState{{have: have, cost: 0, first: -1}}
	expanded := 0
	for step := at; step < len(f.Stages) && step <= at+depth; step++ {
		var next []beamState
		for _, s := range states {
			for i, p := range f.Stages[step].Candidates {
				b := s.have.clone()
				n := s.cost + c.cost(p, b)
				apply(p, b)
				first := s.first
				if first < 0 {
					first = i
				}
				next = append(next, beamState{b, n, first})
				expanded++
			}
		}
		sort.SliceStable(next, func(i, j int) bool { return next[i].cost < next[j].cost })
		if len(next) > width {
			next = next[:width]
		}
		states = next
	}
	return states[0].first, expanded
}

// Run requires Corpus.Validate to have succeeded. It keeps only current physical membership and a 64-hash sample per candidate
// name. Lookahead policies explicitly buffer future objects; streaming policies
// never inspect future rows. Priors/caps are fixed, not fitted to test outcomes.
func (c *Corpus) Run(f Case, policy string) (Result, error) {
	r := Result{Policy: policy, Case: f.Name}
	oracle := c.oracle(f, 100000)
	r.Oracle, r.OracleExact = oracle.cost, oracle.exact
	r.OracleStates = oracle.states
	start := time.Now()
	have := makeBits(len(c.Objects))
	empty := makeBits(len(c.Objects))
	baseline := makeBits(len(c.Objects))
	r.MembershipBytes = int64(len(have) * 8)
	var baselineRead int64
	samples := map[string][]string{}
	analyzed := map[int]bool{}
	for at, s := range f.Stages {
		for _, p := range s.Candidates {
			if !analyzed[at] {
				r.AnalysisCalls++
				r.AnalysisNS += p.AnalysisNS
			}
		}
		analyzed[at] = true
		dc := cdc(s)
		cc := c.cost(s.Candidates[dc], have)
		base := c.cost(s.Candidates[dc], baseline)
		r.CDC += base
		baselineRead += s.Candidates[dc].ReadBytes
		apply(s.Candidates[dc], baseline)
		chosen := c.greedy(s, have)
		switch policy {
		case "greedy":
		case "premium-cap":
			best := -1
			for i, p := range s.Candidates {
				if i == dc {
					continue
				}
				if best < 0 || c.cost(p, have) < c.cost(s.Candidates[best], have) {
					best = i
				}
			}
			if best >= 0 && float64(c.cost(s.Candidates[best], have)) <= 1.10*float64(cc) {
				chosen = best
			}
		case "late-bootstrap":
			if at > 0 {
				for i, p := range s.Candidates {
					if i == dc {
						continue
					}
					r.FeatureReads += int64(len(p.Sample))
					if overlap(samples[p.Name], p.Sample) >= 0.25 && float64(c.cost(p, have)) <= 1.25*float64(cc) {
						chosen = i
						break
					}
				}
			}
		case "expected-value":
			chosen = dc
			score := float64(cc)
			cdcOverlap := 0.10
			related := at > 0 && f.KeyHint != ""
			q := 0.25
			if related {
				q = 0.65
			}
			if a := samples["cdc"]; len(a) > 0 {
				cdcOverlap = overlap(a, s.Candidates[dc].Sample)
			}
			for i, p := range s.Candidates {
				r.FeatureReads += int64(len(p.Sample))
				if i == dc {
					continue
				}
				reuse := 0.50
				if a := samples[p.Name]; len(a) > 0 {
					reuse = overlap(a, p.Sample)
				}
				nextRep := float64(c.cost(p, empty)) * (1 - reuse)
				nextCDC := float64(c.cost(s.Candidates[dc], empty)) * (1 - cdcOverlap)
				candidateScore := float64(c.cost(p, have)) + q*(nextRep-nextCDC)
				if candidateScore < score {
					score = candidateScore
					chosen = i
				}
			}
		case "one-step", "beam":
			depth, width := 1, 64
			if policy == "beam" {
				depth, width = 2, 8
			}
			var expanded int
			chosen, expanded = c.look(f, at, depth, width, have)
			r.States += expanded
			var buffered int64
			for j := at + 1; j <= at+depth && j < len(f.Stages); j++ {
				buffered += f.Stages[j].Size
				if !analyzed[j] {
					r.EarlyReadBytes += f.Stages[j].Size
					for _, p := range f.Stages[j].Candidates {
						r.AnalysisCalls++
						r.AnalysisNS += p.AnalysisNS
					}
					analyzed[j] = true
				}
			}
			if buffered > r.BufferedBytes {
				r.BufferedBytes = buffered
			}
		case "oracle":
			if !oracle.exact {
				return r, fmt.Errorf("admission: oracle cap reached")
			}
			chosen = oracle.choices[at]
		default:
			return r, fmt.Errorf("admission: unknown policy")
		}
		p := s.Candidates[chosen]
		cost := c.cost(p, have)
		if at == 0 && base > 0 {
			r.FirstPremium = float64(cost)/float64(base) - 1
		}
		r.Total += cost
		r.LogicalReadBytes += p.ReadBytes
		// If a declined layout must be reconstructed, bound the extra source
		// materialization by replaying every earlier chosen object. Full prior
		// dry-run cost is reported separately, rather than treating a base as free.
		// This is deliberately a conservative redo scenario, not measured I/O.
		if chosen != dc && at > 0 && r.Choices[len(r.Choices)-1] == "cdc" {
			for j := 0; j < at; j++ {
				old := f.Stages[j]
				for _, candidate := range old.Candidates {
					r.BootstrapRedoAnalysisNS += candidate.AnalysisNS
					r.BootstrapRedoCalls++
					if candidate.Name == r.Choices[j] {
						r.BootstrapRedoReadUpper += candidate.ReadBytes
					}
				}
			}
		}

		r.Choices = append(r.Choices, p.Name)
		r.PerStage = append(r.PerStage, cost)
		apply(p, have)
		for _, p := range s.Candidates {
			samples[p.Name] = append([]string(nil), p.Sample...)
		}
	}
	r.ExtraReadBytes = r.LogicalReadBytes - baselineRead
	r.SketchBytes = r.FeatureReads * 32
	for _, sample := range samples {
		r.SampleStateBytes += int64(len(sample) * 32)
	}
	r.Regret = r.Total - r.Oracle
	if r.Regret < 0 && r.OracleExact {
		return r, fmt.Errorf("admission: negative regret")
	}
	rep := false
	for _, p := range r.Choices {
		rep = rep || p != "cdc"
	}
	if rep && r.Total > r.CDC {
		r.BadAdmissions = 1
	}
	if !rep && r.Oracle < r.CDC {
		r.MissedLineages = 1
	}
	r.WallNS = time.Since(start).Nanoseconds()
	return r, nil
}

var Policies = []string{"greedy", "premium-cap", "late-bootstrap", "one-step", "expected-value", "beam", "oracle"}
