package main

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"testing"
)

func TestC12FastReencodeMatchesAndPublishes(t *testing.T) {
	base := genRandomBytes(1207, 624<<10)
	insert := genRandomBytes(1209, 8<<10)
	target := slices.Concat(base[:256<<10], insert, base[256<<10:])
	src := []xdSrc{{sha256.Sum256(base), base}}
	fast, _, err := xdEncodeOps(src, xdSingleChange(base, target), target, 64<<10)
	mustOK(t, err)
	native, _, err := xdEncode(src, target, 64<<10)
	mustOK(t, err)
	if !bytes.Equal(fast, native) {
		t.Fatal("small insertion did not reproduce the planned XD")
	}
	for _, mode := range []string{"fast", "retained"} {
		t.Run(mode, func(t *testing.T) {
			lab := newAuthLab(t)
			lab.put("0/volume.raw", base)
			lab.put("1/volume.raw", target)
			report := lab.optimize(OptimizePolicy{
				AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", MaxDepth: 2, MaxBases: 1, MinSaving: .10},
				Profile:    "archive", Locality: []int{0}, FastReencode: mode == "fast", RetainDeltaPlans: mode == "retained",
			})
			if report.Failed != 0 || mode == "fast" && report.FastXDHits == 0 || mode == "retained" && report.RetainedHits == 0 {
				t.Fatalf("planned program did not publish through %s: %+v", mode, report.Changes)
			}
			lab.reclaim()
			lab.close()
			lab.open()
			lab.exact("XD after reclamation and reopen")
			lab.verifyOK("XD")
		})
	}
}

func TestC12FastReencodeRejectsLargeGap(t *testing.T) {
	base := bytes.Repeat([]byte{1}, 512<<10)
	target := slices.Clone(base)
	copy(target[128<<10:], bytes.Repeat([]byte{7}, 48<<10))
	if xdSingleChange(base, target) != nil {
		t.Fatal("candidate exceeded the bounded edit")
	}
}

func TestC12RetainedPlanBudget(t *testing.T) {
	var g rgGraph
	for i := byte(0); i < 20; i++ {
		g.retainDeltaPlan([32]byte{i}, [32]byte{i, 1}, make([]byte, rgDeltaPlanMax))
	}
	if g.planBytes != rgDeltaPlanBudget || len(g.plans) != rgDeltaPlanBudget/rgDeltaPlanMax {
		t.Fatalf("budget held %d bytes and %d entries", g.planBytes, len(g.plans))
	}
	g.retainDeltaPlan([32]byte{111}, [32]byte{111}, make([]byte, rgDeltaPlanMax+1))
	if _, retained := g.plans[[32]byte{111}]; retained {
		t.Fatal("oversized envelope was retained")
	}
}

func TestC12RetainedPlanPrunesAlternates(t *testing.T) {
	var g rgGraph
	first, second := [32]byte{1}, [32]byte{2}
	blob := make([]byte, rgDeltaPlanMax)
	for i := byte(1); i <= 16; i++ {
		g.retainDeltaPlan(first, [32]byte{i, 1}, blob)
	}
	g.retainDeltaPlan(second, [32]byte{1, 2}, blob)
	if len(g.plans) != 2 || !g.planTight {
		t.Fatalf("pressure retained %d plans", len(g.plans))
	}
	g.retainDeltaPlan(first, [32]byte{1, 3}, blob[:500])
	if len(g.plans) != 2 || len(g.plans[g.bestPlan[first]]) != 500 || g.planBytes != rgDeltaPlanMax+500 {
		t.Fatalf("replacement retained %d plans and %d bytes", len(g.plans), g.planBytes)
	}
}
