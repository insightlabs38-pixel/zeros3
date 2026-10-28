package admission

import (
	"crypto/sha256"
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
		prefix := f
		prefix.Stages = prefix.Stages[:1]
		first, e := c.Run(prefix, policy)
		if e != nil {
			t.Fatal(e)
		}
		if first.Choices[0] != r.Choices[0] || r.BufferedBytes != 0 || r.EarlyReadBytes != 0 {
			t.Fatalf("future leakage %s", policy)
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
