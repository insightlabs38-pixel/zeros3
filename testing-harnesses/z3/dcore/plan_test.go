package dcore

import (
	"math/rand/v2"
	"testing"
)

// randModel builds a version-chain-like model: contents share chunks with
// neighbours; deltas against earlier versions; optional rep with shared components.
func randModel(seed uint64, n int, withRep bool) *Model {
	r := rand.New(rand.NewPCG(seed, 17))
	m := &Model{RecordOverhead: 96, MaxDepth: 3, MaxBases: 2}
	newChunk := func(exists bool) int {
		m.Chunks = append(m.Chunks, Chunk{Phys: int64(2000 + r.IntN(60000)), Pack: r.IntN(6) - 1, Exists: exists})
		return len(m.Chunks) - 1
	}
	var prev []int
	for i := 0; i < n; i++ {
		var cs []int
		for _, c := range prev {
			if r.IntN(100) < 85 {
				cs = append(cs, c)
			}
		}
		for k := 0; k < 3+r.IntN(5); k++ {
			cs = append(cs, newChunk(true))
		}
		prev = cs
		c := Content{Name: "v", Refs: 1 + r.IntN(2), Chunks: cs}
		var total int64
		for _, ch := range cs {
			total += m.Chunks[ch].Phys
		}
		for b := 0; b < i; b++ {
			if i-b <= 3 {
				c.Delta = append(c.Delta, DeltaOpt{Bases: []int{b}, Bytes: total/int64(5+r.IntN(20)) + 500, ReadAmp: 1 + r.Float64()*2})
			}
		}
		if i >= 2 {
			c.Delta = append(c.Delta, DeltaOpt{Bases: []int{i - 1, i - 2}, Bytes: total / 30, ReadAmp: 3})
		}
		if withRep && r.IntN(3) > 0 {
			var comps []int
			for _, ch := range cs[:len(cs)/2+1] {
				comps = append(comps, ch) // components reuse existing chunks
			}
			for k := 0; k < 2; k++ {
				comps = append(comps, newChunk(false))
			}
			c.Rep = &RepOpt{Comps: comps, RootBytes: 400, ReadAmp: 4}
		}
		m.Contents = append(m.Contents, c)
	}
	return m
}

// referenceBytes recomputes totals with an explicit manifest-level reference
// simulation, independent of Model.Eval's code path.
func referenceLogical(m *Model, a []Form) int64 {
	chunkRefs := map[int]int{}
	liveC := map[int]bool{}
	var visit func(i int)
	visit = func(i int) {
		if liveC[i] {
			return
		}
		liveC[i] = true
		if a[i].Kind == FormDelta {
			for _, b := range m.Contents[i].Delta[a[i].Opt].Bases {
				visit(b)
			}
		}
	}
	for i, c := range m.Contents {
		if c.Refs > 0 {
			visit(i)
		}
	}
	var total int64
	for i := range m.Contents {
		if !liveC[i] {
			continue
		}
		switch a[i].Kind {
		case FormCDC:
			for _, c := range m.Contents[i].Chunks {
				chunkRefs[c]++
			}
		case FormRep:
			for _, c := range m.Contents[i].Rep.Comps {
				chunkRefs[c]++
			}
			total += m.Contents[i].Rep.RootBytes + m.RecordOverhead
		case FormDelta:
			total += m.Contents[i].Delta[a[i].Opt].Bytes + m.RecordOverhead
		}
	}
	for c := range chunkRefs {
		total += m.Chunks[c].Phys
	}
	// pins
	for i := range m.Contents {
		if liveC[i] && a[i].Kind == FormCDC {
			for j := range m.Contents {
				if liveC[j] && a[j].Kind == FormDelta {
					for _, b := range m.Contents[j].Delta[a[j].Opt].Bases {
						if b == i {
							total += m.RecordOverhead
							goto next
						}
					}
				}
			}
		}
	next:
	}
	return total
}

func randAssign(m *Model, r *rand.Rand) []Form {
	a := make([]Form, len(m.Contents))
	for i, c := range m.Contents {
		switch r.IntN(3) {
		case 1:
			if len(c.Delta) > 0 {
				a[i] = Form{FormDelta, r.IntN(len(c.Delta))}
			}
		case 2:
			if c.Rep != nil {
				a[i] = Form{FormRep, 0}
			}
		}
	}
	return a
}

func TestEvalMatchesReferenceAccounting(t *testing.T) {
	checked := 0
	for seed := uint64(1); seed <= 60; seed++ {
		m := randModel(seed, 4+int(seed%8), seed%2 == 0)
		r := rand.New(rand.NewPCG(seed, 3))
		for k := 0; k < 80; k++ {
			a := randAssign(m, r)
			tot, err := m.Eval(a, 1)
			if err != nil {
				continue // depth/cycle/fanout infeasible
			}
			checked++
			if want := referenceLogical(m, a); tot.Logical != want {
				t.Fatalf("seed %d: Eval %d vs reference %d", seed, tot.Logical, want)
			}
			if !(tot.Logical <= tot.AfterRepack && tot.AfterRepack <= tot.AfterGC) {
				t.Fatalf("stage order violated: %+v", tot)
			}
			if tot.Peak < tot.Logical && tot.NewBytes >= 0 {
				// peak (all existing + new) can never be below what is finally needed
				t.Fatalf("peak below logical: %+v", tot)
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d feasible assignments checked", checked)
	}
}

func TestAccountingEdgeCases(t *testing.T) {
	// Two manifests sharing one content count it once.
	m := &Model{RecordOverhead: 0, Chunks: []Chunk{{Phys: 100, Pack: -1, Exists: true}},
		Contents: []Content{{Refs: 2, Chunks: []int{0}}}}
	if tot, _ := m.Eval([]Form{{}}, 1); tot.Logical != 100 {
		t.Fatalf("shared content double counted: %d", tot.Logical)
	}
	// A component listed by a rejected rep candidate is not live or free.
	m = &Model{RecordOverhead: 10, Chunks: []Chunk{{100, -1, true}, {50, -1, false}},
		Contents: []Content{{Refs: 1, Chunks: []int{0}, Rep: &RepOpt{Comps: []int{1}, RootBytes: 5}}}}
	if tot, _ := m.Eval([]Form{{}}, 1); tot.Logical != 100 || tot.Peak != 100 {
		t.Fatalf("unchosen component charged: %+v", tot)
	}
	if tot, _ := m.Eval([]Form{{FormRep, 0}}, 1); tot.Logical != 50+5+10 || tot.Peak != 100+50+15 {
		t.Fatalf("rep accounting: %+v", tot)
	}
	// Partially live pack: dead chunk bytes persist after GC, vanish after repack.
	m = &Model{Chunks: []Chunk{{100, 0, true}, {300, 0, true}, {100, 1, true}},
		Contents: []Content{{Refs: 1, Chunks: []int{0}}, {Refs: 1, Chunks: []int{1, 2}, Delta: []DeltaOpt{{Bases: []int{0}, Bytes: 20}}}}}
	tot, err := m.Eval([]Form{{}, {FormDelta, 0}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// content1 -> delta: chunk 1 (pack0, shares with live chunk 0) dead, chunk 2 (pack1) fully dead pack.
	if tot.Logical != 100+20 || tot.AfterGC != 100+20+300 || tot.AfterRepack != 100+20 {
		t.Fatalf("pack stages: %+v", tot)
	}
	// Cycle and depth are infeasible.
	m = &Model{MaxDepth: 1, Chunks: []Chunk{{1, -1, true}}, Contents: []Content{
		{Refs: 1, Chunks: []int{0}, Delta: []DeltaOpt{{Bases: []int{1}, Bytes: 1}}},
		{Refs: 1, Chunks: []int{0}, Delta: []DeltaOpt{{Bases: []int{0}, Bytes: 1}}}}}
	if _, err := m.Eval([]Form{{FormDelta, 0}, {FormDelta, 0}}, 1); err == nil {
		t.Fatal("cycle accepted")
	}
}

// The B8 defect: members of a group share most CDC chunks. Per-object
// exclusive accounting sees almost nothing reclaimable and refuses a rep that the
// union rule correctly prices as a large win.
func TestExclusiveAccountingDefectAndFix(t *testing.T) {
	m := &Model{RecordOverhead: 96}
	var shared []int
	for i := 0; i < 80; i++ {
		m.Chunks = append(m.Chunks, Chunk{Phys: 10000, Pack: i / 20, Exists: true})
		shared = append(shared, i)
	}
	for v := 0; v < 3; v++ {
		cs := append([]int{}, shared...)
		for k := 0; k < 6; k++ { // exclusive tail
			m.Chunks = append(m.Chunks, Chunk{Phys: 10000, Pack: 4, Exists: true})
			cs = append(cs, len(m.Chunks)-1)
		}
		comps := []int{}
		for k := 0; k < 30; k++ { // rep components shared across versions
			if v == 0 || k >= 20 {
				m.Chunks = append(m.Chunks, Chunk{Phys: 10000, Pack: -1, Exists: false})
			}
			if v == 0 || k >= 20 {
				comps = append(comps, len(m.Chunks)-1)
			} else {
				comps = append(comps, m.Contents[0].Rep.Comps[k])
			}
		}
		m.Contents = append(m.Contents, Content{Refs: 1, Chunks: cs, Rep: &RepOpt{Comps: comps, RootBytes: 300}})
	}
	o := PlanOpts{AllowRep: true}
	base := PlanCDC(m, o)
	ex := PlanExclusiveGreedy(m, o)
	bo2 := PlanBestOfTwo(m, o)
	if ex.Total != base.Total {
		t.Fatalf("exclusive rule was expected to refuse every rep: ex=%d cdc=%d", ex.Total, base.Total)
	}
	if bo2.Total >= base.Total*7/10 {
		t.Fatalf("union accounting should find a large win: %d vs %d", bo2.Total, base.Total)
	}
	if g := m.GroupReclaimable([]int{0, 1, 2}); g < 80*10000 {
		t.Fatalf("group reclaimable %d should include the 80 shared chunks", g)
	}
	if one := m.ExclusiveSaving(0); one > 6*10000 {
		t.Fatalf("exclusive saving %d unexpectedly large", one)
	}
}

func TestPlannersAgainstExhaustiveOracle(t *testing.T) {
	var worstBO2, worstHC, worstBeam, worstRoots, sumRoots, sumBeam, sumBO2, sumHC float64
	cnt := 0
	for seed := uint64(1); seed <= 40; seed++ {
		m := randModel(seed, 5+int(seed%3), seed%2 == 0)
		o := PlanOpts{AllowDelta: true, AllowRep: seed%2 == 0, TopK: 3}
		or, ok := PlanExhaustive(m, o, 3000000)
		if !ok {
			t.Fatalf("seed %d: oracle space too large", seed)
		}
		cdc := PlanCDC(m, o)
		bo2 := PlanBestOfTwo(m, o)
		hc := PlanHillClimb(m, o, bo2.Assign)
		beam := PlanBeam(m, o, 8)
		roots := PlanRoots(m, o)
		for _, p := range []Plan{bo2, hc, beam, roots} {
			if p.Total < or.Total {
				t.Fatalf("seed %d: %s beats the exhaustive oracle (%d < %d): oracle not exhaustive?", seed, p.Name, p.Total, or.Total)
			}
			if p.Total > cdc.Total {
				t.Fatalf("seed %d: %s worse than CDC", seed, p.Name)
			}
		}
		reg := func(p Plan) float64 { return float64(p.Total)/float64(or.Total) - 1 }
		worstBO2, worstHC, worstBeam = max(worstBO2, reg(bo2)), max(worstHC, reg(hc)), max(worstBeam, reg(beam))
		worstRoots = max(worstRoots, reg(roots))
		sumRoots += reg(roots)
		sumBeam += reg(beam)
		sumBO2 += reg(bo2)
		sumHC += reg(hc)
		cnt++
		if hc.Total > bo2.Total {
			t.Fatalf("hill climb worsened its start")
		}
	}
	c := float64(cnt)
	t.Logf("regret vs exhaustive (worst/mean) over %d models: best-of-two %.3f/%.3f hill-climb %.3f/%.3f beam8 %.3f/%.3f roots %.3f/%.3f",
		cnt, worstBO2, sumBO2/c, worstHC, sumHC/c, worstBeam, sumBeam/c, worstRoots, sumRoots/c)
	if worstBeam > 0.15 {
		t.Fatalf("beam regret %.3f", worstBeam)
	}
}

func TestPlanConstraints(t *testing.T) {
	m := randModel(5, 8, true)
	o := PlanOpts{AllowDelta: true, AllowRep: true, KeepNewest: 2, MaxReadAmp: 2.5}
	p := PlanBeam(m, o, 4)
	for i := len(m.Contents) - 2; i < len(m.Contents); i++ {
		if p.Assign[i].Kind != FormCDC {
			t.Fatalf("newest content %d left CDC", i)
		}
	}
	for i, f := range p.Assign {
		switch f.Kind {
		case FormDelta:
			if m.Contents[i].Delta[f.Opt].ReadAmp > 2.5 {
				t.Fatal("read-amp bound violated")
			}
		case FormRep:
			t.Fatal("rep (amp 4) chosen despite bound")
		}
	}
}
