package admission

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func digest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func trap(base int64, revs int) (Corpus, Case) {
	c := Corpus{Version: 1}
	add := func(name string, n int64) int {
		c.Objects = append(c.Objects, Object{SHA: digest(name), Raw: n, Packed: n, Role: "payload"})
		return len(c.Objects) - 1
	}
	f := Case{Name: "lock-in", Class: "fixture", Evidence: "SYNTHETIC_FIXTURE", KeyHint: "object"}
	repBase := add("base", base)
	repIDs := []int{repBase}
	for i := 0; i < revs; i++ {
		raw := add(fmt.Sprint("cdc", i), 100)
		if i > 0 {
			repIDs = append(repIDs, add(fmt.Sprint("delta", i), 5))
		}
		ids := append([]int(nil), repIDs...)
		input := digest(fmt.Sprint("input", i))
		samples := []string{}
		for _, id := range ids {
			samples = append(samples, c.Objects[id].SHA)
		}
		f.Stages = append(f.Stages, Stage{Label: fmt.Sprint(i), InputSHA: input, Size: 100, Candidates: []Candidate{{Name: "cdc", Root: c.Objects[raw].SHA, ProofSHA: input, Objects: []int{raw}, Sample: []string{c.Objects[raw].SHA}}, {Name: "rep", Root: c.Objects[repBase].SHA, ProofSHA: input, Objects: ids, Sample: Sample(samples)}}})
	}
	c.Cases = []Case{f}
	return c, f
}
func TestExactOracleAndFirstSightLockIn(t *testing.T) {
	c, f := trap(130, 2)
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, policy := range Policies {
		r, e := c.Run(f, policy)
		if e != nil {
			t.Fatal(e)
		}
		if !r.OracleExact || r.Oracle != 135 {
			t.Fatalf("oracle %s: %+v", policy, r)
		}
		if policy == "greedy" && (r.Total != 200 || r.Regret != 65 || r.MissedLineages != 1) {
			t.Fatalf("greedy lock-in: %+v", r)
		}
		if policy == "one-step" && (r.Total != 135 || r.BufferedBytes != 100) {
			t.Fatalf("charged lookahead: %+v", r)
		}
	}
}
func TestLateBootstrapPaysMissingBase(t *testing.T) {
	c, f := trap(110, 4)
	r, e := c.Run(f, "late-bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	if r.Choices[0] != "cdc" || r.Choices[1] != "rep" || r.PerStage[1] != 115 || r.Total != 225 {
		t.Fatalf("bootstrap skipped base cost: %+v", r)
	}
}
func TestStreamingPoliciesDoNotReadFuture(t *testing.T) {
	c, f := trap(110, 4)
	for _, policy := range []string{"greedy", "premium-cap", "late-bootstrap", "expected-value"} {
		r, e := c.Run(f, policy)
		if e != nil {
			t.Fatal(e)
		}
		for n := 1; n <= len(f.Stages); n++ {
			prefix := f
			prefix.Stages = prefix.Stages[:n]
			first, e := c.Run(prefix, policy)
			if e != nil {
				t.Fatal(e)
			}
			for i := range first.Choices {
				if first.Choices[i] != r.Choices[i] {
					t.Fatalf("future leakage %s at %d", policy, i)
				}
			}
			if r.BufferedBytes != 0 || r.EarlyReadBytes != 0 {
				t.Fatalf("future prefetch %s", policy)
			}
		}

	}
}
func TestHiddenRootAndForeignSketchRejected(t *testing.T) {
	c, f := trap(110, 2)
	c.Cases[0].Stages[0].Candidates[1].Objects = nil
	if e := c.Validate(); e == nil {
		t.Fatal("hidden root uncharged")
	}
	c, f = trap(110, 2)
	c.Cases[0].Stages[0].Candidates[0].Sample = []string{digest("invented")}
	if e := c.Validate(); e == nil {
		t.Fatal("invented component overlap")
	}
	_ = f
}
func TestBoundedOracleDoesNotClaimExact(t *testing.T) {
	c, f := trap(110, 4)
	if c.oracle(f, 1).exact {
		t.Fatal("capped search claimed exact")
	}
}

func TestBootstrapRedoBudgetIsVisible(t *testing.T) {
	c, f := trap(110, 4)
	for i := range f.Stages {
		for j := range f.Stages[i].Candidates {
			f.Stages[i].Candidates[j].AnalysisNS = 10
			f.Stages[i].Candidates[j].ReadBytes = 100
		}
	}
	r, e := c.Run(f, "late-bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	if r.BootstrapRedoCalls != 2 || r.BootstrapRedoAnalysisNS != 20 || r.BootstrapRedoReadUpper != 100 {
		t.Fatalf("free base recomputation: %+v", r)
	}
}
func FuzzFrontierValidation(f *testing.F) {
	c, _ := trap(110, 4)
	b, e := json.Marshal(c)
	if e != nil {
		f.Fatal(e)
	}
	f.Add(b)
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<16 {
			return
		}
		var c Corpus
		if json.Unmarshal(b, &c) != nil || c.Validate() != nil {
			return
		}
		for _, f := range c.Cases {
			if len(f.Stages) > 6 {
				return
			}
			r, e := c.Run(f, "greedy")
			if e == nil && r.OracleExact && r.Regret < 0 {
				t.Fatal("invalid regret")
			}
		}
	})
}

func TestOracleMatchesBruteForce(t *testing.T) {
	seed := uint64(36)
	next := func() uint64 { seed ^= seed << 13; seed ^= seed >> 7; seed ^= seed << 17; return seed }
	for trial := 0; trial < 50; trial++ {
		c := Corpus{Version: 1}
		for i := 0; i < 12; i++ {
			n := int64(next()%99 + 1)
			c.Objects = append(c.Objects, Object{SHA: digest(fmt.Sprint(trial, "/", i)), Raw: n, Packed: n})
		}
		f := Case{Name: "oracle-control", Class: "control", Evidence: "SYNTHETIC_FIXTURE"}
		for stage := 0; stage < 5; stage++ {
			s := Stage{Label: fmt.Sprint(stage), Size: 100, InputSHA: digest(fmt.Sprint("input", stage))}
			for alt := 0; alt < 3; alt++ {
				p := Candidate{Name: fmt.Sprint("rep", alt), ProofSHA: s.InputSHA}
				if alt == 0 {
					p.Name = "cdc"
				}
				for id := range c.Objects {
					if next()%5 == 0 {
						p.Objects = append(p.Objects, id)
					}
				}
				if len(p.Objects) == 0 {
					p.Objects = []int{0}
				}
				p.Root = c.Objects[p.Objects[0]].SHA
				s.Candidates = append(s.Candidates, p)
			}
			f.Stages = append(f.Stages, s)
		}
		c.Cases = []Case{f}
		if e := c.Validate(); e != nil {
			t.Fatal(e)
		}
		best := int64(1 << 62)
		var walk func(int, int64, bits)
		walk = func(at int, cost int64, have bits) {
			if at == len(f.Stages) {
				if cost < best {
					best = cost
				}
				return
			}
			for _, p := range f.Stages[at].Candidates {
				n := c.cost(p, have)
				b := have.clone()
				apply(p, b)
				walk(at+1, cost+n, b)
			}
		}
		walk(0, 0, makeBits(len(c.Objects)))
		got := c.oracle(f, 100000)
		if !got.exact || got.cost != best {
			t.Fatalf("trial%d B&B%d brute%d", trial, got.cost, best)
		}
	}
}
