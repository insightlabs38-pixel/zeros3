package gitz3

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(GitBin()); err != nil {
		t.Skip("git not installed (optional research producer)")
	}
}

// Everything the lab reports as exact really is: loose -> pack -> reordered pack
// through the whole representation set, SHA-1 and SHA-256, with the index derived.
func TestProducerPipelineExact(t *testing.T) {
	needGit(t)
	for _, a := range algos {
		h := LinearHistory(11, 1, 4)
		w := t.TempDir()
		lo, err := Build(w, "lo", a, h, SpecUnpack)
		if err != nil {
			t.Fatal(err)
		}
		pk, err := Build(w, "pk", a, h, SpecDefault)
		if err != nil {
			t.Fatal(err)
		}
		rv, err := Build(w, "rv", a, h, SpecReverse)
		if err != nil {
			t.Fatal(err)
		}
		var states []*State
		for _, x := range []struct {
			r *Repo
			l string
		}{{lo, "loose"}, {pk, "pack"}, {rv, "reversed"}} {
			st, err := StateOf(x.r, x.l)
			if err != nil {
				t.Fatal(err)
			}
			states = append(states, st)
		}
		l := NewLab(96, true)
		rows := l.RunStates(states, append(BaselineReps(), ExactReps()...))
		idxDerived := int64(0)
		for _, r := range rows {
			if !r.Oracle && !r.Exact {
				t.Errorf("%v %s/%s not exact: %s", a, r.State, r.Rep, r.Err)
			}
			if strings.HasPrefix(r.Rep, "gp-set") {
				idxDerived += r.Detail["idx_derived"]
			}
		}
		if idxDerived == 0 {
			t.Errorf("%v: no .idx was derived", a)
		}
		// a reordered pack of the same object set must be far cheaper than raw CDC under the exact representation
		var cdc, set int64
		for _, r := range rows {
			if r.State == "reversed" && r.Rep == "cdc" {
				cdc = r.CostPacked
			}
			if r.State == "reversed" && r.Rep == "gp-set-plain" {
				set = r.CostPacked
			}
		}
		if set*2 > cdc {
			t.Errorf("%v: reorder cost %d (exact) vs %d (cdc)", a, set, cdc)
		}
	}
}

func TestProducerReproducible(t *testing.T) {
	needGit(t)
	h := LinearHistory(5, 1, 3)
	st, err := Determinism(t.TempDir(), SHA1, h, SpecDefault)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Repeat || !st.RepeatIdx {
		t.Errorf("identical command did not reproduce the pack: %+v", st)
	}
}

// With the git version the fixtures were built with, regenerating them gives the
// same bytes (the committed fixtures are the test inputs; this records that the
// producer is deterministic for them).
func TestFixturesRegenerate(t *testing.T) {
	needGit(t)
	want, err := os.ReadFile("testdata/GIT_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != GitVersion() {
		t.Skipf("fixtures built with %q, installed %q", strings.TrimSpace(string(want)), GitVersion())
	}
	fx2, err := BuildFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range fx2 {
		old, err := os.ReadFile("testdata/" + name)
		if err != nil || !bytes.Equal(old, b) {
			t.Errorf("%s differs from the committed fixture", name)
		}
	}
}
