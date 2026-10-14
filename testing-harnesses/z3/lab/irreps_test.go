package lab

import (
	"bytes"
	"testing"

	"zeros3-z3/ir"
)

// Every IR representation rebuilds every revision of DEFLATE, gzip and ZIP
// families bit for bit from skeleton + units + semantic plaintext only.
func TestIRRepsExact(t *testing.T) {
	dir := tinyCorpus(t, "gzip-L6-insert-mid", "deflate-L1-insert-mid", "deflate-fixed-insert-mid", "deflate-L6-append", "zip-deflate-change", "deflate-L0-insert-mid", "deflate-huffonly-insert-mid")
	cfg := testCfg(dir)
	for _, sp := range IRSpecs() {
		cfg.Reps = append(cfg.Reps, sp.Name)
	}
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rep.Rows {
		if r.Status != "ok" || r.RoundtripExact != "yes" {
			t.Errorf("%s %s %s: %s %s %s", r.Family, r.Revision, r.Rep, r.Status, r.RoundtripExact, r.Error)
		}
		if r.Detail["streams"] != r.Detail["streams_ok"] {
			t.Errorf("%s %s %s: streams %v ok %v", r.Family, r.Revision, r.Rep, r.Detail["streams"], r.Detail["streams_ok"])
		}
		n++
	}
	if n < 5*len(IRSpecs()) {
		t.Fatalf("only %d rows", n)
	}
}

// Unit-aligned plaintext variants must also reconstruct, and a revision that
// reuses everything but a tail (append) must cost far less than a fresh store.
func TestIRAppendReusesUnits(t *testing.T) {
	dir := tinyCorpus(t, "deflate-L6-append")
	cfg := testCfg(dir)
	cfg.Reps = []string{"cdc", "ir-tok2k-e2", "irp-tok2k"}
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Rows {
		if r.Rep == "cdc" || r.Index == 0 {
			continue
		}
		if r.ReusePct < 60 {
			t.Errorf("%s %s: reuse %.1f%%, want most bytes shared with the previous revision", r.Rep, r.Revision, r.ReusePct)
		}
	}
}

func TestIRSpecsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, sp := range IRSpecs() {
		if seen[sp.Name] {
			t.Errorf("duplicate %s", sp.Name)
		}
		seen[sp.Name] = true
		if sp.Codec == ir.Pred && sp.Profile != ir.AutoProfile {
			t.Errorf("%s: predictive spec without auto profile", sp.Name)
		}
	}
	_ = bytes.Equal
}
