package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// GF1 explores fixed pack placements. A resource is the exact set of records
// for one immutable identity. Alternate physical placements are separate
// resources. Each program declares its entire required record closure.
type gf1Resource struct {
	pack  int
	bytes int64
}
type gf1Option struct {
	name                               string
	own, closure                       []int
	depth, parents                     int
	rangeBytes, wholeBytes, writeBytes int64
}
type gf1Problem struct {
	resources                                  []gf1Resource
	options                                    [][]gf1Option
	packCount                                  int
	quantum, packHeader, packLimit, writeLimit int64
	rangeLimits, wholeLimits                   []int64
	maxDepth                                   int
}
type gf1Plan struct {
	choices []int
	bytes   int64
}
type gf1Solution struct {
	plan    gf1Plan
	visited int
}

func (p gf1Problem) allowed(root int, o gf1Option) bool {
	return o.depth <= p.maxDepth && o.rangeBytes <= p.rangeLimits[root] && o.wholeBytes <= p.wholeLimits[root]
}

func (p gf1Problem) cost(choices []int, isolated bool) (int64, bool) {
	packs := make([]int64, p.packCount)
	seen := make([]bool, len(p.resources))
	var write int64
	for root, selection := range choices {
		if selection < 0 {
			continue
		}
		o := p.options[root][selection]
		if !p.allowed(root, o) {
			return 0, false
		}
		write += o.writeBytes
		// The isolated comparator does not credit records shared across roots.
		if isolated {
			clear(seen)
		}
		for _, id := range o.closure {
			if !seen[id] {
				seen[id] = true
				r := p.resources[id]
				packs[r.pack] += r.bytes
			}
		}
	}
	if write > p.writeLimit {
		return 0, false
	}
	var bytes int64
	for _, size := range packs {
		if size == 0 {
			continue
		}
		size += p.packHeader
		if size > p.packLimit {
			return 0, false
		}
		bytes += (size + p.quantum - 1) / p.quantum * p.quantum
	}
	return bytes, true
}

func gf1Blank(n int) []int {
	choices := make([]int, n)
	for i := range choices {
		choices[i] = -1
	}
	return choices
}

func (p gf1Problem) direct() gf1Plan {
	choices := make([]int, len(p.options))
	bytes, ok := p.cost(choices, false)
	if !ok {
		panic("GF1 input lacks a feasible direct plan")
	}
	return gf1Plan{choices, bytes}
}

// Independent chooses each stand-alone optimum and then admits it into the
// joint plan only if the full plan still respects the modeled constraints.
func (p gf1Problem) independent(single bool) gf1Plan {
	plan := p.direct()
	for root, options := range p.options {
		alone := gf1Blank(len(p.options))
		alone[root] = 0
		bestCost, _ := p.cost(alone, true)
		best := 0
		for index, opt := range options {
			if single && opt.parents > 1 {
				continue
			}
			alone[root] = index
			if size, ok := p.cost(alone, true); ok && size < bestCost {
				best, bestCost = index, size
			}
		}
		choices := slices.Clone(plan.choices)
		choices[root] = best
		if size, ok := p.cost(choices, false); ok {
			plan = gf1Plan{choices, size}
		}
	}
	return plan
}

// Oracle uses the cost of the current partial assignment as an admissible
// lower bound. Nonnegative resources and upward rounding make it monotone.
func (p gf1Problem) oracle(limit int) (gf1Solution, bool) {
	out := gf1Solution{plan: p.direct()}
	choices := gf1Blank(len(p.options))
	var visit func(int) bool
	visit = func(root int) bool {
		if out.visited == limit {
			return false
		}
		out.visited++
		lower, ok := p.cost(choices, false)
		if !ok || lower >= out.plan.bytes {
			return true
		}
		if root == len(p.options) {
			out.plan = gf1Plan{slices.Clone(choices), lower}
			return true
		}
		for i := range p.options[root] {
			choices[root] = i
			if !visit(root + 1) {
				return false
			}
		}
		choices[root] = -1
		return true
	}
	complete := visit(0)
	return out, complete
}

// Cohort search opens a facility for multiple clients in a single proposal.
// Also test isolated moves, so artificial pack alignment can be removed
// without relying on an individual predictor's profitability.
func (p gf1Problem) cohort(rounds int) gf1Solution {
	out := gf1Solution{plan: p.direct()}
	for range rounds {
		best := out.plan
		submit := func(choices []int) {
			out.visited++
			if bytes, ok := p.cost(choices, false); ok && bytes < best.bytes {
				best = gf1Plan{slices.Clone(choices), bytes}
			}
		}
		for root, options := range p.options {
			trial := slices.Clone(out.plan.choices)
			for index := range options {
				trial[root] = index
				submit(trial)
			}
		}
		for facility := range p.resources {
			trial := slices.Clone(out.plan.choices)
			for root, options := range p.options {
				bestIndex, bestOwn := -1, int64(1<<62)
				for index, opt := range options {
					if !p.allowed(root, opt) || slices.Contains(opt.own, facility) || !slices.Contains(opt.closure, facility) {
						continue
					}
					var own int64
					for _, id := range opt.own {
						own += p.resources[id].bytes
					}
					if own < bestOwn {
						bestIndex, bestOwn = index, own
					}
				}
				if bestIndex >= 0 {
					trial[root] = bestIndex
				}
			}
			submit(trial)
		}
		if best.bytes == out.plan.bytes {
			break
		}
		out.plan = best
	}
	return out
}

// Beam search enumerates feasible prefixes, even when an individual choice
// looks unattractive beside the incumbent. Its width is a hard memory cap
// on surviving prefixes; it is not a certificate of optimality.
func (p gf1Problem) beam(width int) gf1Solution {
	out := gf1Solution{plan: p.direct()}
	beam := []gf1Plan{{choices: gf1Blank(len(p.options))}}
	for root, options := range p.options {
		next := make([]gf1Plan, 0, width*len(options))
		for _, prefix := range beam {
			for index := range options {
				choices := slices.Clone(prefix.choices)
				choices[root] = index
				out.visited++
				if bytes, ok := p.cost(choices, false); ok && bytes < out.plan.bytes {
					next = append(next, gf1Plan{choices, bytes})
				}
			}
		}
		slices.SortFunc(next, func(a, b gf1Plan) int {
			if order := cmp.Compare(a.bytes, b.bytes); order != 0 {
				return order
			}
			return slices.Compare(a.choices, b.choices)
		})
		beam = next[:min(len(next), width)]
	}
	if len(beam) > 0 {
		out.plan = beam[0]
	}
	return out
}

func TestGF1SharedClosureOracle(t *testing.T) {
	p := gf1Problem{
		resources: []gf1Resource{{0, 8}, {1, 8}, {2, 8}, {2, 2}, {2, 2}},
		options: [][]gf1Option{
			{{name: "direct", own: []int{0}, closure: []int{0}}, {name: "shared", own: []int{3}, closure: []int{2, 3}, depth: 1, rangeBytes: 7, writeBytes: 2}},
			{{name: "direct", own: []int{1}, closure: []int{1}}, {name: "shared", own: []int{4}, closure: []int{2, 4}, depth: 1, rangeBytes: 7, writeBytes: 2}},
		},
		packCount: 3, quantum: 4, packLimit: 32, writeLimit: 4,
		rangeLimits: []int64{7, 7}, wholeLimits: []int64{0, 0}, maxDepth: 1,
	}
	oracle, complete := p.oracle(40)
	if !complete || oracle.plan.bytes != 12 || p.independent(false).bytes != 16 ||
		p.cohort(4).plan.bytes != oracle.plan.bytes || p.beam(4).plan.bytes != oracle.plan.bytes {
		t.Fatalf("wrong shared-cost result: oracle=%+v complete=%t", oracle, complete)
	}
	if _, complete := p.oracle(1); complete {
		t.Fatal("bounded oracle reported an incomplete proof as exact")
	}
	for _, limit := range []string{"read", "write", "pack", "depth"} {
		q := p
		switch limit {
		case "read":
			q.rangeLimits = []int64{6, 7}
		case "write":
			q.writeLimit = 3
		case "pack":
			q.packLimit = 11
		case "depth":
			q.maxDepth = 0
		}
		oracle, complete := q.oracle(40)
		if !complete || oracle.plan.bytes != 16 || q.cohort(4).plan.bytes != 16 || q.beam(4).plan.bytes != 16 {
			t.Fatalf("%s constraint bypassed: oracle=%+v complete=%t", limit, oracle, complete)
		}
	}
	// Once both clients disappear, nobody pays the shared base.
	if size, ok := p.cost([]int{-1, -1}, false); !ok || size != 0 {
		t.Fatalf("unreachable: %d %t", size, ok)
	}
}

// Adapt real ZeroS3 options to a catalog of concrete depth-one programs.
// Every earlier referenced root is constrained to retain its direct form.
// This restricted catalog avoids assuming closure costs independent of
// the representation subsequently chosen for a mutable parent.
func gf1NativeCatalog(g *rgGraph, anchor [32]byte, capFactor int64) gf1Problem {
	n := len(g.nodes)
	p := gf1Problem{
		options: make([][]gf1Option, n), rangeLimits: make([]int64, n), wholeLimits: make([]int64, n),
		packCount: 2, quantum: g.block, packHeader: packHeaderSize + packFooterSize,
		packLimit: defaultPackTargetBytes, writeLimit: 64 << 20, maxDepth: 1,
	}
	for _, stored := range g.cphys {
		p.resources = append(p.resources, gf1Resource{0, stored})
	}
	direct := g.currentSel()
	addComponents := func(dst []int, src []int32) []int {
		for _, id := range src {
			if !slices.Contains(dst, int(id)) {
				dst = append(dst, int(id))
			}
		}
		return dst
	}
	pin := -1
	parent := g.idx[anchor]
	for k, node := range g.nodes {
		for i, opt := range node.opts {
			if i != 0 && (opt.kind != rgDelta || len(opt.bases) != 1 || opt.bases[0] != parent || int32(k) == parent) {
				continue
			}
			if i == 0 && opt.kind != rgCDC {
				panic("GF1 catalog requires CDC seed at each root")
			}
			var own []int
			own = addComponents(own, opt.comps)
			if i != 0 {
				own = append(own, len(p.resources))
				p.resources = append(p.resources, gf1Resource{1, opt.meta})
			}
			closure := slices.Clone(own)
			if i != 0 {
				base := g.nodes[parent].opts[direct[parent]]
				if pin == -1 {
					pin = len(p.resources)
					p.resources = append(p.resources, gf1Resource{1, base.meta})
				}
				closure = append(addComponents(closure, base.comps), pin)
			}
			sel := slices.Clone(direct)
			sel[k] = int32(i)
			e := newRGEval(g, sel)
			var worst int64
			for _, window := range rgProbes(node.size)[:8] {
				worst = max(worst, g.readStats(e, int32(k), window[0], window[1]).Decoded)
			}
			whole := g.readStats(e, int32(k), 0, node.size).Decoded
			if i == 0 {
				p.rangeLimits[k], p.wholeLimits[k] = worst*capFactor, whole*capFactor
			}
			p.options[k] = append(p.options[k], gf1Option{
				name: fmt.Sprintf("%s:%d", opt.label, i), own: own, closure: closure, depth: e.depth(int32(k)),
				parents: len(opt.bases), rangeBytes: worst, wholeBytes: whole, writeBytes: opt.write,
			})
		}
	}
	return p
}

func TestGF1NativeCatalog(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, family := range families {
		if !slices.Contains([]string{"typescript-tar", "sqlite", "parquet", "sparse-volume", "entropy"}, family.Name) {
			continue
		}
		t.Run(family.Name, func(t *testing.T) {
			l := newAuthLab(t)
			ctx := context.Background()
			var anchor [32]byte
			for i := range family.Paths {
				raw, _ := gf0ReadFixture(t, root, family, i)
				if i == 0 {
					anchor = sha256.Sum256(raw)
				}
				l.put(fmt.Sprintf("series/%02d", i), raw)
			}
			_, err := l.s.finishAuthorityArchive(ctx, 0)
			mustOK(t, err)
			mustOK(t, l.s.ensureFeature(agFeature))
			pol := (OptimizePolicy{AuthPolicy: AuthPolicy{Order: "oldest", AllowDelta: true, MaxDepth: 3},
				Profile: "archive", SparseFiles: true}).norm()
			t0 := time.Now()
			g, err := l.s.rgBuild(ctx, pol)
			mustOK(t, err)
			defer g.close()
			mustOK(t, g.generate(ctx))
			g.partition()
			generation := time.Since(t0).Nanoseconds()
			incumbent, _ := g.solve(pol.Algorithm, pol.ReadWeight)
			for _, factor := range []int64{1, 2, 8} {
				p := gf1NativeCatalog(g, anchor, factor)
				cpu := fb13CPU()
				oracle, complete := p.oracle(20000)
				oracleCPU := int64(fb13CPU() - cpu)
				if !complete {
					t.Fatal("restricted oracle should fit node budget")
				}
				type measured struct {
					Bytes   int64    `json:"bytes"`
					Options []string `json:"options"`
					Visits  int      `json:"visits"`
					CPU     int64    `json:"cpu_ns"`
				}
				vals := map[string]measured{}
				record := func(label string, f func() gf1Solution) {
					cpu := fb13CPU()
					sol := f()
					m := measured{Bytes: sol.plan.bytes, Visits: sol.visited, CPU: int64(fb13CPU() - cpu)}
					for k, option := range sol.plan.choices {
						m.Options = append(m.Options, p.options[k][option].name)
					}
					if size, ok := p.cost(sol.plan.choices, false); !ok || size != m.Bytes || size < oracle.plan.bytes {
						t.Fatalf("%s returned infeasible or out-of-bound result: %+v", label, m)
					}
					vals[label] = m
				}
				record("oracle", func() gf1Solution { return oracle })
				oracleRow := vals["oracle"]
				oracleRow.CPU = oracleCPU
				vals["oracle"] = oracleRow
				record("direct", func() gf1Solution { return gf1Solution{plan: p.direct()} })
				record("independent", func() gf1Solution { return gf1Solution{plan: p.independent(false)} })
				record("single_base", func() gf1Solution { return gf1Solution{plan: p.independent(true)} })
				record("cohort", func() gf1Solution { return p.cohort(5) })
				record("beam", func() gf1Solution { return p.beam(16) })
				// Reprice the incumbent only when its selected programs belong to
				// the same catalog and satisfy these same explicit read caps.
				var included bool
				incumbentPlan := make([]int, len(p.options))
				for k, picked := range incumbent.sel {
					suffix := fmt.Sprintf(":%d", picked)
					at := slices.IndexFunc(p.options[k], func(o gf1Option) bool { return strings.HasSuffix(o.name, suffix) })
					if at == -1 {
						break
					}
					incumbentPlan[k] = at
					included = k == len(incumbent.sel)-1
				}
				if included {
					if size, ok := p.cost(incumbentPlan, false); ok {
						record("same_catalog_native_global", func() gf1Solution { return gf1Solution{plan: gf1Plan{incumbentPlan, size}} })
					}
				}
				counts := make([]int, len(p.options))
				for i, v := range p.options {
					counts[i] = len(v)
				}
				row, err := json.Marshal(map[string]any{
					"family": family.Name, "range_whole_cap_factor": factor, "options_per_root": counts,
					"generation_ns": generation, "restricted_results": vals, "incumbent_native": incumbent.state(pol.Algorithm, false),
				})
				mustOK(t, err)
				fmt.Printf("GF1 %s\n", row)
			}
		})
	}
}
