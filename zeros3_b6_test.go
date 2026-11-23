package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// ---- B6 global representation-graph optimization ----------------------------

func b6Policy(p OptimizePolicy) OptimizePolicy {
	if p.Order == "" {
		p.Order = "oldest"
	}
	p.AllowDelta = true
	if p.Profile == "" {
		p.Profile = "archive"
	}
	return p
}

func (l *authLab) optimize(p OptimizePolicy) OptimizeReport {
	l.t.Helper()
	rep, err := l.s.AuthorityOptimize(context.Background(), b6Policy(p))
	if err != nil {
		l.t.Fatal(err)
	}
	return rep
}

// b6Graph builds the model of the lab store with candidates generated.
func b6Graph(t *testing.T, l *authLab, p OptimizePolicy) *rgGraph {
	t.Helper()
	pol := b6Policy(p).norm()
	g, err := l.s.rgBuild(context.Background(), pol)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.generate(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.partition()
	return g
}

// b6Shared is the joint-selection case: a and b hold the same 768 KiB section s and a small unique tail;
// c (older) holds a lightly edited s. Each of a, b alone frees only its tail.
func b6Shared(t *testing.T) (*authLab, map[string][]byte) {
	l := newAuthLab(t)
	s := b5Text(61, 768<<10)
	c := b5Mutate(s, 7, 150)
	objs := map[string][]byte{"c": c, "a": append(slices.Clone(s), b5Text(62, 24<<10)...), "b": append(slices.Clone(s), b5Text(63, 24<<10)...)}
	for _, k := range []string{"c", "a", "b"} {
		l.put(k, objs[k])
	}
	l.reclaim()
	return l, objs
}

// TestB6ClosureIncremental: the incremental evaluator equals a from-scratch evaluation after every
// random move, shared components are paid once, and record files are charged only while required.
func TestB6ClosureIncremental(t *testing.T) {
	l, _ := b6Shared(t)
	v := b5Text(71, 256<<10)
	for i := range 4 {
		l.put("h", v)
		v = b5Mutate(v, uint64(90+i), 8)
	}
	l.reclaim()
	g := b6Graph(t, l, OptimizePolicy{ReadWeight: 3})
	r := rand.New(rand.NewPCG(1, 2))
	e := newRGEval(g, g.currentSel())
	moves := 0
	for range 400 {
		vtx := int32(r.IntN(len(g.nodes)))
		o := int32(r.IntN(len(g.nodes[vtx].opts)))
		e.set(vtx, o)
		if !e.feasible(vtx) {
			continue
		}
		moves++
		f := newRGEval(g, e.sel)
		if f.bytes != e.bytes || f.meta != e.meta || !slices.Equal(f.ref, e.ref) || !slices.Equal(f.req, e.req) {
			t.Fatalf("incremental %d/%d bytes, %d/%d meta differ from scratch", e.bytes, f.bytes, e.meta, f.meta)
		}
		if a, b := e.readSum(), f.readSum(); math.Abs(a-b) > 1e-6*max(1, b) {
			t.Fatalf("incremental read cost %f, scratch %f", a, b)
		}
		// independent union: every component a required selection names, once
		var want int64
		seen := map[int32]bool{}
		for i := range g.nodes {
			if f.req[i] == 0 {
				continue
			}
			for _, c := range f.opt(int32(i)).comps {
				if !seen[c] {
					seen[c] = true
					want += g.cphys[c]
				}
			}
		}
		if want+f.meta != f.bytes {
			t.Fatalf("union %d + meta %d != %d", want, f.meta, f.bytes)
		}
	}
	if moves < 20 {
		t.Fatalf("only %d feasible moves", moves)
	}
}

// TestB6JointMoveOnly: with a and b unable to serve each other (their mutual options removed), no single
// move from all-CDC pays (each frees only its tail), and the joint move that frees the shared section does.
func TestB6JointMoveOnly(t *testing.T) {
	l, _ := b6Shared(t)
	g := b6Graph(t, l, OptimizePolicy{})
	ka := map[string]int32{}
	for i, nd := range g.nodes {
		ka[nd.obj.key] = int32(i)
	}
	for _, k := range []string{"a", "b"} {
		nd := g.nodes[ka[k]]
		nd.opts = slices.DeleteFunc(nd.opts, func(o *rgOpt) bool { return o.kind == rgDelta && o.bases[0] != ka["c"] })
	}
	g.partition()
	e := newRGEval(g, g.currentSel())
	for _, k := range []string{"a", "b"} {
		v := ka[k]
		for o := range int32(len(g.nodes[v].opts)) {
			if o != e.sel[v] {
				if d := e.try(v, o); d < 0 {
					t.Fatalf("%s alone improves by %v", k, d)
				}
			}
		}
	}
	j0 := e.objective()
	part := g.parts[g.partOf[ka["a"]]]
	if !e.jointMoves(part, 1<<20) {
		t.Fatal("no joint move")
	}
	if e.opt(ka["a"]).kind != rgDelta || e.opt(ka["b"]).kind != rgDelta || e.objective() >= j0 {
		t.Fatalf("joint move %v -> %v", j0, e.objective())
	}
}

// TestB6EdmondsBruteForce: the arborescence matches exhaustive search on random small graphs.
func TestB6EdmondsBruteForce(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for trial := range 300 {
		n := 2 + r.IntN(5)
		root := n - 1
		var es []rgEdge
		for v := 0; v < n-1; v++ {
			es = append(es, rgEdge{root, v, float64(10 + r.IntN(90))})
			for u := 0; u < n-1; u++ {
				if u != v && r.IntN(2) == 0 {
					es = append(es, rgEdge{u, v, float64(r.IntN(60))})
				}
			}
		}
		got := rgEdmonds(n, root, es)
		cost := func(ids []int) float64 {
			var w float64
			for _, k := range ids {
				w += es[k].w
			}
			return w
		}
		// brute force: choose one in-edge per non-root vertex, keep acyclic choices
		in := make([][]int, n)
		for k, e := range es {
			in[e.to] = append(in[e.to], k)
		}
		best := math.Inf(1)
		pick := make([]int, n-1)
		var rec func(v int)
		rec = func(v int) {
			if v == n-1 {
				for s := 0; s < n-1; s++ {
					x, steps := s, 0
					for x != root && steps <= n {
						x = es[pick[x]].from
						steps++
					}
					if x != root {
						return
					}
				}
				best = min(best, cost(pick))
				return
			}
			for _, k := range in[v] {
				pick[v] = k
				rec(v + 1)
			}
		}
		rec(0)
		if got == nil || len(got) != n-1 || math.Abs(cost(got)-best) > 1e-9 {
			t.Fatalf("trial %d: edmonds %v (%v) brute %v", trial, got, cost(got), best)
		}
		has := make([]bool, n)
		for _, k := range got {
			if has[es[k].to] {
				t.Fatalf("trial %d: two in-edges", trial)
			}
			has[es[k].to] = true
		}
	}
}

// TestB6JointSelection: the sequential rule keeps the shared section twice-held and refuses; the global
// search moves a and b together, the measured store after reclamation matches the closure estimate, and
// the result is smaller than what the existing planner reaches on an identical store.
func TestB6JointSelection(t *testing.T) {
	l, objs := b6Shared(t)
	plan := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{MinSaving: 0.10}, Oracle: true})
	if plan.Local.Bytes <= plan.Planned.Bytes*12/10 {
		t.Fatalf("global %d is not clearly below the local rule %d", plan.Planned.Bytes, plan.Local.Bytes)
	}
	if plan.Oracle == nil || plan.Planned.Objective > plan.Oracle.Objective*1.001+1 {
		t.Fatalf("global objective %v, oracle %+v", plan.Planned.Objective, plan.Oracle)
	}
	// The existing planner on a copy of the same store.
	twin := newAuthLab(t)
	for _, k := range []string{"c", "a", "b"} {
		twin.put(k, objs[k])
	}
	twin.reclaim()
	twin.migrate(AuthPolicy{AllowDelta: true, MinSaving: 0.10, Order: "oldest"})
	twin.reclaim()
	twinAlloc, _ := allocStore(twin.dir)
	l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{MinSaving: 0.10, Apply: true}})
	l.reclaim()
	l.exact("optimized")
	l.verifyOK("optimized")
	alloc, _ := allocStore(l.dir)
	if alloc >= twinAlloc {
		t.Fatalf("global store %d bytes is not below the local planner's %d", alloc, twinAlloc)
	}
	// Exact post-materialization check of the estimate: the reclaimed packs and bodies against the plan.
	meas := b6Measured(t, l.s)
	if d := math.Abs(float64(meas-plan.Planned.Bytes)) / float64(plan.Planned.Bytes); d > 0.06 {
		t.Fatalf("measured %d retained bytes, planned %d (%.1f%%)", meas, plan.Planned.Bytes, 100*d)
	}
	again := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{MinSaving: 0.10}})
	if len(again.Changes) != 0 {
		t.Fatalf("re-run plans %d changes: %+v", len(again.Changes), again.Changes)
	}
}

// b6Measured is the store's retained bytes in the units the closure model prices: live pack records
// (header and index entry included), loose live chunks at their pack price and body files' blocks.
func b6Measured(t *testing.T, s *Store) int64 {
	t.Helper()
	rr, err := s.computeReachability(false)
	if err != nil || !rr.OK() {
		t.Fatalf("reachability: %v %v", err, rr.Issues)
	}
	live := rr.liveSet()
	var n int64
	for _, sum := range live.chunks.keys {
		if loc, ok := s.packLookup(sum); ok {
			n += int64(loc.stored) + packRecordBytes
		} else if b, err := s.casRead(sum); err == nil {
			n += packPriceLen(b) + packRecordBytes
		}
	}
	files, _ := s.authFiles()
	for _, f := range files {
		if st, err := os.Stat(s.authPath(f)); err == nil {
			n += (st.Size() + 4095) / 4096 * 4096
		}
	}
	return n
}

// allocStore is the store's allocated and apparent bytes (st_blocks of every file).
func allocStore(dir string) (alloc, apparent int64) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		var st syscall.Stat_t
		if syscall.Stat(p, &st) == nil {
			alloc += st.Blocks * 512
			apparent += st.Size
		}
		return nil
	})
	return
}

// TestB6HistoryApplyConverges: a version history plus negative controls; the plan applies exactly,
// survives reclamation and reopen, deep-verifies, and a second run changes nothing.
func TestB6HistoryApplyConverges(t *testing.T) {
	l := newAuthLab(t)
	v := b5Text(81, 384<<10)
	for i := range 5 {
		l.put("doc", v)
		v = b5Mutate(v, uint64(100+i), 10)
	}
	rnd := rand.New(rand.NewPCG(5, 6))
	for i := range 3 {
		b := make([]byte, 200<<10)
		for k := range b {
			b[k] = byte(rnd.Uint32())
		}
		l.put("rand"+string(rune('0'+i)), b)
	}
	l.reclaim()
	for _, prof := range []string{"archive", "hot"} {
		rep := l.optimize(OptimizePolicy{Profile: prof, AuthPolicy: AuthPolicy{Order: "newest", Apply: true}})
		for _, c := range rep.Changes {
			if c.Status != "done" {
				t.Fatalf("%s: %+v", prof, c)
			}
		}
		if prof == "archive" && rep.Applied < 3 {
			t.Fatalf("archive applied %d: %+v", rep.Applied, rep.Changes)
		}
		if prof == "hot" && rep.Planned.MaxDepth > 1 {
			t.Fatalf("hot profile kept depth %d", rep.Planned.MaxDepth)
		}
		l.exact(prof)
		l.reclaim()
		l.offline(func() {})
		l.exact(prof + " reopened")
		l.verifyOK(prof)
		again := l.optimize(OptimizePolicy{Profile: prof, AuthPolicy: AuthPolicy{Order: "newest"}})
		if len(again.Changes) != 0 {
			t.Fatalf("%s re-run plans %+v", prof, again.Changes)
		}
		for _, c := range rep.Changes {
			if len(c.Key) > 3 && c.Key[:4] == "rand" {
				t.Fatalf("random control changed: %+v", c)
			}
		}
	}
}

// b6Raised is TestB6RebaseRaisesBase's store before optimization.
func b6Raised(t *testing.T) *authLab {
	l := newAuthLab(t)
	v0 := b5Text(91, 512<<10)
	v1 := b5Mutate(v0, 1, 6)
	v2 := b5Mutate(v1, 2, 6)
	l.put("x", v0)
	l.put("x", v1)
	l.put("x", v2)
	l.reclaim()
	l.migrate(AuthPolicy{AllowDelta: true, Order: "newest", MaxBases: 1, MaxDepth: 1})
	l.reclaim()
	l.put("x", b5Mutate(v2, 9, 40))
	l.reclaim()
	return l
}

// TestB6BridgedTransition: with dependents bridged to pins instead of moved first, the transition still
// keeps a valid graph at every publication, reads exactly and leaves only collectible garbage.
func TestB6BridgedTransition(t *testing.T) {
	l := b6Raised(t)
	defer func() { rgBridgeFirst, testHook = false, nil }()
	rgBridgeFirst = true
	testHook = func(p string) {
		if p == hookOptimizeBeforePublish {
			if rr, err := l.s.computeReachability(false); err != nil || !rr.OK() {
				t.Errorf("intermediate graph invalid: %v %v", err, rr.Issues)
			}
		}
	}
	rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 1, Apply: true}})
	rgBridgeFirst, testHook = false, nil
	if rep.Bridges < 1 || rep.Failed != 0 {
		t.Fatalf("bridges %d failed %d: %+v", rep.Bridges, rep.Failed, rep.Changes)
	}
	l.exact("bridged")
	l.reclaim()
	l.exact("bridged reclaimed")
	l.verifyOK("bridged")
	if again := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 1}}); len(again.Changes) != 0 {
		t.Fatalf("not converged: %+v", again.Changes)
	}
}

// TestB6RebaseRaisesBase: a graph the local planner left at depth 1 is re-planned so a former base
// becomes a delta itself; its dependents are republished deeper first (no bridge), every step stays a
// valid published graph, and the result reads exactly.
func TestB6RebaseRaisesBase(t *testing.T) {
	l := b6Raised(t)
	var steps int
	defer func() { testHook = nil }()
	testHook = func(p string) {
		if p == hookOptimizeBeforePublish {
			steps++
			rr, err := l.s.computeReachability(false)
			if err != nil || !rr.OK() {
				t.Errorf("intermediate graph invalid: %v %v", err, rr.Issues)
			}
		}
	}
	rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 1, Apply: true}})
	testHook = nil
	if rep.Failed != 0 {
		t.Fatalf("failed: %+v", rep.Changes)
	}
	l.exact("rebased")
	l.reclaim()
	l.exact("rebased reclaimed")
	l.verifyOK("rebased")
	if rep.Planned.Bytes >= rep.Current.Bytes || rep.Applied < 2 || steps != rep.Applied {
		t.Fatalf("plan %d current %d applied %d steps %d: %+v", rep.Planned.Bytes, rep.Current.Bytes, rep.Applied, steps, rep.Changes)
	}
	raised := false
	for _, c := range rep.Changes {
		if c.From == "keep-pin" && c.Depth >= 1 {
			raised = true
		}
	}
	if !raised {
		t.Fatalf("no former base was raised: %+v", rep.Changes)
	}
	t.Logf("steps %d bridges %d changes %+v", steps, rep.Bridges, rep.Changes)
}

// TestB6InterruptedRecovers: cancellation before the k-th publication leaves a store that verifies and
// reads exactly; a rerun completes the plan and then converges.
func TestB6InterruptedRecovers(t *testing.T) {
	for _, cut := range []int{1, 2, 3} {
		l := newAuthLab(t)
		v := b5Text(111, 300<<10)
		for i := range 5 {
			l.put("k", v)
			v = b5Mutate(v, uint64(i+30), 8)
		}
		l.reclaim()
		ctx, cancel := context.WithCancel(context.Background())
		n := 0
		testHook = func(p string) {
			if p == hookOptimizeBeforePublish {
				if n++; n == cut {
					cancel()
				}
			}
		}
		_, err := l.s.AuthorityOptimize(ctx, b6Policy(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true}}))
		testHook = nil
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cut %d: %v", cut, err)
		}
		l.offline(func() {})
		l.exact("interrupted")
		l.verifyOK("interrupted")
		l.reclaim()
		l.exact("interrupted reclaimed")
		rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true}})
		if rep.Failed != 0 {
			t.Fatalf("cut %d: rerun failed %+v", cut, rep.Changes)
		}
		l.reclaim()
		l.exact("completed")
		l.verifyOK("completed")
		if again := l.optimize(OptimizePolicy{}); len(again.Changes) != 0 {
			t.Fatalf("cut %d: not converged: %+v", cut, again.Changes)
		}
	}
}

// TestB6NoBenefitKeepsCDC: unrelated incompressible contents and a lone document plan nothing.
func TestB6NoBenefitKeepsCDC(t *testing.T) {
	l := newAuthLab(t)
	rnd := rand.New(rand.NewPCG(8, 9))
	for i := range 4 {
		b := make([]byte, 128<<10)
		for k := range b {
			b[k] = byte(rnd.Uint32())
		}
		l.put("r"+string(rune('0'+i)), b)
	}
	l.put("doc", b5Text(5, 100<<10))
	l.reclaim()
	rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true}})
	if len(rep.Changes) != 0 || rep.Applied != 0 || rep.Planned.Bytes != rep.Current.Bytes {
		t.Fatalf("changes on unrelated data: %+v", rep.Changes)
	}
}

// TestB6DeterministicPlan: equivalent inputs plan the same graph.
func TestB6DeterministicPlan(t *testing.T) {
	plan := func() []OptimizeChange {
		l := newAuthLab(t)
		v := b5Text(121, 200<<10)
		for i := range 4 {
			l.put("k", v)
			v = b5Mutate(v, uint64(i+50), 7)
		}
		l.put("other", b5Mutate(v, 99, 30))
		return l.optimize(OptimizePolicy{ReadWeight: 5}).Changes
	}
	a, b := plan(), plan()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("plans %d / %d changes", len(a), len(b))
	}
	for i := range a {
		if a[i].SHA != b[i].SHA || a[i].To != b[i].To || !slices.Equal(a[i].Bases, b[i].Bases) {
			t.Fatalf("plans differ at %d: %+v / %+v", i, a[i], b[i])
		}
	}
}

// TestB6OracleGap: on small partitions the scalable search reaches the exhaustive optimum across read weights.
func TestB6OracleGap(t *testing.T) {
	l, _ := b6Shared(t)
	v := b5Text(131, 160<<10)
	for i := range 4 {
		l.put("h", v)
		v = b5Mutate(v, uint64(i+60), 9)
	}
	l.reclaim()
	for _, w := range []float64{0, 2, 20, 200} {
		rep := l.optimize(OptimizePolicy{ReadWeight: w, Oracle: true, Profile: "mixed"})
		if rep.Oracle == nil || rep.OracleParts == 0 {
			t.Fatalf("w=%v: no oracle", w)
		}
		if gap := rep.Planned.Objective/rep.Oracle.Objective - 1; gap > 0.005 {
			t.Fatalf("w=%v: global %v oracle %v (gap %.3f)", w, rep.Planned.Objective, rep.Oracle.Objective, gap)
		}
	}
}

// TestB6LocalityMatcherExact: locality-aware programs reconstruct exactly (in-memory and windowed codecs,
// whole and at frame boundaries), and on boilerplate-heavy input they keep COPY sources nearer.
func TestB6LocalityMatcherExact(t *testing.T) {
	// boilerplate: every "file" carries the same header, files are edited between versions
	hdr := b5Text(141, 3000)
	var base []byte
	for i := range 160 {
		base = append(base, hdr...)
		base = append(base, b5Text(uint64(200+i), 6000)...)
	}
	target := b5Mutate(base, 3, 40)
	x, err := dwDcoreBuildIndex([][]byte{base}, dwDcoreDefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	scatter := func(ops []dwDcoreOp) (far int) {
		var prev int64 = -1
		for _, o := range ops {
			if o.Base < 0 {
				continue
			}
			if prev >= 0 && (o.Off-prev > dwDcoreNearRadius || prev-o.Off > dwDcoreNearRadius) {
				far++
			}
			prev = o.Off + o.N
		}
		return
	}
	var farPlain int
	for _, loc := range []int{0, 32, 256, 4096} {
		ops := x.MatchLocal(target, loc)
		if loc == 0 {
			if !slices.EqualFunc(ops, x.Match(target), func(a, b dwDcoreOp) bool { return a.TPos == b.TPos && a.N == b.N && a.Off == b.Off && a.Base == b.Base }) {
				t.Fatal("MatchLocal(0) differs from Match")
			}
			farPlain = scatter(ops)
		} else if f := scatter(ops); f >= farPlain {
			t.Fatalf("loc %d: %d far jumps, plain %d", loc, f, farPlain)
		}
		blob, _, err := xdEncodeOps([]xdSrc{{sha256.Sum256(base), base}}, xdFromDw(ops), target, 16<<10)
		if err != nil {
			t.Fatal(err)
		}
		got, err := xdApplyForTest(blob, base)
		if err != nil || !bytes.Equal(got, target) {
			t.Fatalf("loc %d: in-memory program inexact: %v", loc, err)
		}
	}
	// windowed codec through the store: a 2 MiB history with authWindowMin lowered
	defer func(a, b int64) { authWindowMin, authWindowSize = a, b }(authWindowMin, authWindowSize)
	authWindowMin, authWindowSize = 1<<20, 256<<10
	l := newAuthLab(t)
	big := slices.Repeat(base, 3)
	l.put("w", big)
	l.put("w", b5Mutate(big, 11, 30))
	l.reclaim()
	rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true, Order: "oldest"}, Locality: []int{256}})
	if rep.Applied == 0 {
		t.Fatalf("no windowed delta applied: %+v", rep.Changes)
	}
	l.reclaim()
	l.exact("windowed locality")
	l.verifyOK("windowed locality")
}

// xdApplyForTest decodes an in-memory envelope against one base through the record resolver's codec.
func xdApplyForTest(blob, base []byte) ([]byte, error) {
	h, more, err := xdParseHeader(blob)
	if err != nil || more {
		return nil, errDelta
	}
	out := make([]byte, h.size)
	for i, f := range h.frames {
		ops, err := xdDecodeFrame(&h, i, blob[f.poff:f.poff+f.plen])
		if err != nil {
			return nil, err
		}
		for _, o := range ops {
			if o.base < 0 {
				copy(out[o.tpos:], o.lit)
			} else {
				copy(out[o.tpos:o.tpos+o.n], base[o.off:o.off+o.n])
			}
		}
	}
	if sha256.Sum256(out) != h.sha {
		return nil, errDelta
	}
	return out, nil
}

// TestB6MaintenanceAndBlocks: an optimized graph whose base loses its last live root keeps that base as a
// required intermediate through online maintenance passes (B4) and a blocked, reconstruction-ordered repack
// (B5); reads stay exact, deep verify passes, and re-optimization models the intermediate without moving it.
func TestB6MaintenanceAndBlocks(t *testing.T) {
	l := newAuthLab(t)
	v := b5Text(151, 400<<10)
	keys := []string{"k0", "k1", "k2", "k3"}
	for i, k := range keys {
		l.put(k, v)
		v = b5Mutate(v, uint64(i+70), 10)
	}
	l.reclaim()
	rep := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true, Order: "oldest"}})
	if rep.Applied < 2 {
		t.Fatalf("applied %d", rep.Applied)
	}
	// delete a content that some delta uses as a base
	var victim string
	for _, c := range rep.Changes {
		for _, b := range c.Bases {
			for _, k := range keys {
				if hexOf(l.want[k])[:12] == b && victim == "" {
					victim = k
				}
			}
		}
	}
	if victim == "" {
		t.Fatalf("no base among %+v", rep.Changes)
	}
	l.close()
	s := b4OpenMaint(t, l.dir)
	l.s = s
	mustOK(t, s.DeleteObject("b", victim))
	delete(l.want, victim)
	zero := 0
	res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
	mustOK(t, err)
	mustOK(t, s.ApplyHistoryPrune(&res))
	for range 3 {
		if _, err := s.maintainOnline(context.Background(), b4Opts()); err != nil {
			t.Fatal(err)
		}
		l.exact("after pass")
	}
	l.verifyOK("online")
	l.offline(func() {
		if _, err := repackStore(l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: true, BlockLog: 15, Layout: layoutRecon}); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("blocked recon")
	l.verifyOK("blocked recon")
	again := l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Order: "oldest"}})
	if again.Nodes != len(keys) {
		t.Fatalf("model has %d nodes, want the %d contents including the intermediate", again.Nodes, len(keys))
	}
	if again.Live != len(keys)-1 {
		t.Fatalf("live %d", again.Live)
	}
	l.optimize(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true, Order: "oldest"}})
	l.reclaim()
	l.exact("reoptimized")
	l.verifyOK("reoptimized")
}

func hexOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
