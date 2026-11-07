package planner2

import (
	"testing"

	"zeros3-z3/admission"
)

const frontierPath = "../../testdata/q6-admission/frontier.json.gz"

// Track A: the Z3-36 policies, re-expressed on the new interface, must reproduce
// the published per-case totals exactly (and the published primary aggregates).
func TestZ3PoliciesReproduce(t *testing.T) {
	corp, lins, err := LoadFrontier(frontierPath)
	if err != nil {
		t.Skipf("frontier unavailable: %v", err)
	}
	cfg := DefaultConfig()
	cfg.Overhead = corp.ObjectOverhead
	primary := map[string]int64{}
	want := map[string]int64{"greedy": 131470043, "premium-cap": 129524441, "late-bootstrap": 133901577, "expected-value": 128456102}
	isPrimary := func(n string) bool { return len(n) < 3 || (n[:3] != "no-" && n[:4] != "one-") }
	for i, l := range lins {
		f := corp.Cases[i]
		for _, pol := range []string{"greedy", "premium-cap", "late-bootstrap", "expected-value"} {
			led, err := Run(ArrivalsOnly(l), NewZ3(pol), cfg)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := corp.Run(f, pol)
			if err != nil {
				t.Fatal(err)
			}
			if led.FinalPhys != ref.Total {
				t.Fatalf("%s/%s: planner2 %d != Z3-36 %d", f.Name, pol, led.FinalPhys, ref.Total)
			}
			if isPrimary(f.Name) {
				primary[pol] += led.FinalPhys
			}
		}
	}
	for k, v := range want {
		if primary[k] != v {
			t.Errorf("%s primary total %d, published %d", k, primary[k], v)
		}
	}
	_ = admission.Policies
}
