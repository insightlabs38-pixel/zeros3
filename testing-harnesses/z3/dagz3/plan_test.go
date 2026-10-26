package dagz3

import (
	"errors"
	"math"
	"testing"

	"zeros3-z3/repz3"
)

func solve(t *testing.T, p *Problem) (ind, mar, ref, ex *Plan) {
	t.Helper()
	var err error
	if ind, err = p.Independent(); err != nil {
		t.Fatal(err)
	}
	if mar, err = p.Marginal(); err != nil {
		t.Fatal(err)
	}
	if ref, err = p.Refine(); err != nil {
		t.Fatal(err)
	}
	if ex, err = p.Exact(0); err != nil {
		t.Fatal(err)
	}
	if !ex.Complete {
		t.Fatal("exact search incomplete")
	}
	return
}

// brute is the specification: enumerate every assignment and cost the union directly.
func brute(t *testing.T, p *Problem) int64 {
	t.Helper()
	by := map[string][]int{}
	for i, c := range p.Cands {
		by[c.Object] = append(by[c.Object], i)
	}
	best := int64(math.MaxInt64)
	var rec func(i int, chosen []int)
	rec = func(i int, chosen []int) {
		if i == len(p.Objects) {
			seen := map[repz3.SHA]bool{}
			var total int64
			for _, ci := range chosen {
				c := &p.Cands[ci]
				total += c.Read * p.ReadPer1000 / 1000
				for id, n := range c.Objs {
					if !seen[id] && !p.Base[id] {
						total += n + p.PerObject
					}
					seen[id] = true
				}
			}
			best = min(best, total)
			return
		}
		for _, ci := range by[p.Objects[i].Name] {
			rec(i+1, append(chosen, ci))
		}
	}
	rec(0, nil)
	return best
}

func TestPlannerFixtures(t *testing.T) {
	want := map[string]struct{ ind, mar, opt int64 }{
		"independent-optimal":             {330, 330, 330},
		"shared-child-pays":               {2 * 105, 2 * 105, 100 + 2*10},
		"expensive-child-unlocks-parents": {4 * 120, 4 * 120, 300 + 4*5},
		"fake-sharing":                    {2 * 105, 2 * 105, 2 * 105},
		"store-holds-child":               {2 * 10, 2 * 10, 2 * 10},
		"overhead-flips":                  {0, 0, 0}, // checked below
	}
	for name, p := range Fixtures() {
		ind, mar, ref, ex := solve(t, p)
		if b := brute(t, p); ex.Total != b {
			t.Errorf("%s: exact %d, brute force %d", name, ex.Total, b)
		}
		t.Logf("%-32s independent %4d marginal %4d marginal+swap %4d exact %4d  (shared objs %d, shared reps %d)", name, ind.Total, mar.Total, ref.Total, ex.Total, ex.Shared, ex.SharedRe)
		if ex.Total > ind.Total || ex.Total > mar.Total || ex.Total > ref.Total {
			t.Errorf("%s: a heuristic beat the optimum", name)
		}
		if w, ok := want[name]; ok && name != "overhead-flips" {
			if ind.Total != w.ind || mar.Total != w.mar || ex.Total != w.opt {
				t.Errorf("%s: got %d/%d/%d want %d/%d/%d", name, ind.Total, mar.Total, ex.Total, w.ind, w.mar, w.opt)
			}
		}
		// accounting sums exactly
		for _, pl := range []*Plan{ind, mar, ref, ex} {
			if pl.Payload+pl.Recipe+pl.Index+pl.Table+pl.Overhead+pl.Read != pl.Total {
				t.Errorf("%s/%s: accounting does not sum", name, pl.Method)
			}
		}
	}
}

func TestPlannerObjectiveVariants(t *testing.T) {
	p := Fixtures()["overhead-flips"]
	// without overhead the many small objects win; with 40 B per object the fat one does
	p.PerObject = 0
	_, _, _, ex0 := solve(t, p)
	p.PerObject = 40
	_, _, _, ex1 := solve(t, p)
	if ex0.Choice["A"] != "many" || ex1.Choice["A"] != "one" {
		t.Fatalf("overhead 0 -> %s, overhead 40 -> %s", ex0.Choice["A"], ex1.Choice["A"])
	}
	// the read term can flip a storage-optimal choice
	q := NewFB(0).Object("A")
	q.Cand("A", "small", "small", 10, nil, map[string]int64{"s": 90})
	q.Cand("A", "fast", "fast", 10, nil, map[string]int64{"f": 120})
	q.P.Cands[0].Read, q.P.Cands[1].Read = 100000, 100
	q.P.ReadPer1000 = 0
	ex, _ := q.P.Exact(0)
	if ex.Choice["A"] != "small" {
		t.Fatal("storage-only should pick the smaller candidate")
	}
	q.P.ReadPer1000 = 1000 // one byte of storage per byte fetched
	ex, _ = q.P.Exact(0)
	if ex.Choice["A"] != "fast" || ex.Read != 100 {
		t.Fatalf("read-aware: %+v", ex)
	}
}

func TestPlannerRejectsMalformed(t *testing.T) {
	base := func() *FB {
		f := NewFB(0).Object("A")
		f.Cand("A", "a1", "a1", 5, nil, map[string]int64{"x": 10})
		return f
	}
	cases := map[string]func(f *FB){
		"unknown child": func(f *FB) {
			c := &f.P.Cands[0]
			c.Reps[c.Root] = []repz3.SHA{fake("rep:ghost")}
		},
		"output identity mismatch": func(f *FB) { f.P.Cands[0].Out = fake("other") },
		"duplicate id with different content": func(f *FB) {
			f.Cand("A", "a1", "a1", 5, nil, map[string]int64{"y": 99})
		},
		"dependency cycle": func(f *FB) {
			c := &f.P.Cands[0]
			k := fake("rep:k")
			c.Objs[k] = 1
			c.Reps[c.Root] = []repz3.SHA{k}
			c.Reps[k] = []repz3.SHA{c.Root}
		},
		"no valid candidate": func(f *FB) { f.Object("B") },
		"cost overflow": func(f *FB) {
			f.P.Cands[0].Objs[fake("part:big")] = math.MaxInt64 - 3
			f.P.Cands[0].Objs[fake("part:big2")] = 10
		},
		"negative size":       func(f *FB) { f.P.Cands[0].Objs[fake("part:x")] = -1 },
		"unreachable rep":     func(f *FB) { c := &f.P.Cands[0]; c.Objs[fake("rep:orphan")] = 1; c.Reps[fake("rep:orphan")] = nil },
		"unknown object":      func(f *FB) { f.P.Cands[0].Object = "Z" },
		"read overflow":       func(f *FB) { f.P.Cands[0].Read = math.MaxInt64; f.P.ReadPer1000 = 1000 },
		"overhead overflow":   func(f *FB) { f.P.PerObject = math.MaxInt64 / 2 },
		"too deep (6 levels)": func(f *FB) { deepen(&f.P.Cands[0], 5) },
	}
	for name, mutate := range cases {
		f := base()
		mutate(f)
		for _, run := range []func() error{
			func() error { _, err := f.P.Independent(); return err },
			func() error { _, err := f.P.Marginal(); return err },
			func() error { _, err := f.P.Exact(0); return err },
		} {
			if err := run(); !errors.Is(err, ErrPlan) {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	if err := func() error { f := base(); deepen(&f.P.Cands[0], 5); _, err := f.P.Exact(0); return err }(); !errors.Is(err, repz3.ErrDepth) {
		t.Errorf("depth: %v", err)
	}
	// a valid depth-4 chain is accepted
	f := base()
	deepen(&f.P.Cands[0], 3)
	if _, err := f.P.Exact(0); err != nil {
		t.Errorf("depth 4: %v", err)
	}
}

// deepen makes the candidate a chain of n extra representation levels above its root.
func deepen(c *Cand, n int) {
	cur := c.Root
	for i := 0; i < n; i++ {
		up := fake("rep:up" + string(rune('a'+i)))
		c.Objs[up] = 1
		c.Reps[up] = []repz3.SHA{cur}
		cur = up
	}
	c.Root = cur
}

// TestExactMatchesBruteForceOnRandomInstances checks the pruned search against enumeration.
func TestExactMatchesBruteForceOnRandomInstances(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		p := Synthetic(6, seed)
		ex, err := p.Exact(0)
		if err != nil || !ex.Complete {
			t.Fatal(err)
		}
		if b := brute(t, p); ex.Total != b {
			t.Fatalf("seed %d: exact %d brute %d", seed, ex.Total, b)
		}
		p.PerObject = 17
		if ex, _ = p.Exact(0); ex.Total != brute(t, p) {
			t.Fatalf("seed %d with overhead", seed)
		}
	}
}
