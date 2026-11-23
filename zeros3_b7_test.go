package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io/fs"
	"math/bits"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
)

// ---- B7 region-aware similarity discovery -----------------------------------

func b7Rand(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e37))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// rsRef is the reference extraction: one sequential scan, level fixed by size, no parallel slices.
func rsRef(p rsParams, b []byte) []rsFeat {
	lvl := p.level(int64(len(b)))
	var out []rsFeat
	for i := 0; i+rsWindow <= len(b); i++ {
		h := dwDcore_rollInit(b[i : i+rsWindow])
		if z := uint8(bits.LeadingZeros64(h * rsSampleK)); z >= lvl {
			out = append(out, rsFeat{dwDcore_mix64(h), uint32(i >> rsPosShift), z})
		}
	}
	return rsNorm(out, lvl)
}

// TestB7FeaturesDeterministic: parallel slices and the streaming scan equal the direct reference.
func TestB7FeaturesDeterministic(t *testing.T) {
	p := rsDefaultParams()
	for _, n := range []int{0, 31, 32, 5000, 300 << 10, 9<<20 + 77} {
		b := append(b5Text(uint64(n), n/2), b7Rand(uint64(n), n-n/2)...)
		got, lvl := p.features(b)
		want := rsRef(p, b)
		if lvl != p.level(int64(len(b))) || !slices.Equal(got, want) {
			t.Fatalf("n=%d: %d features at level %d, reference %d", n, len(got), lvl, len(want))
		}
		again, _ := p.features(b)
		if !slices.Equal(got, again) {
			t.Fatalf("n=%d: not deterministic", n)
		}
		sk, fs, l2, err := p.sketchFeaturesAt(bytes.NewReader(b), int64(len(b)), 16)
		if err != nil || l2 != lvl || !slices.Equal(fs, want) || !slices.Equal(sk, authFeatures(b, 16)) {
			t.Fatalf("n=%d: streamed %d features level %d (err %v), sketch equal %v", n, len(fs), l2, err, slices.Equal(sk, authFeatures(b, 16)))
		}
	}
	// level nesting: a content's fingerprints at a higher level are a subset of its lower-level ones
	b := b5Text(3, 1<<20)
	lo, _ := rsParams{MinLevel: 5, MaxFeat: 1 << 20}.features(b)
	hi, _ := rsParams{MinLevel: 9, MaxFeat: 1 << 20}.features(b)
	for _, f := range hi {
		if _, ok := slices.BinarySearchFunc(lo, f.h, func(a rsFeat, h uint64) int { return cmpU64(a.h, h) }); !ok {
			t.Fatal("level-9 fingerprint missing at level 5")
		}
	}
}

func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestB7FeaturesBounded: dense sampling raises the level instead of growing without bound.
func TestB7FeaturesBounded(t *testing.T) {
	p := rsParams{MinLevel: 1, MaxFeat: 64}
	b := b7Rand(9, 8<<20)
	fs, lvl := p.features(b)
	if len(fs) > 4*p.MaxFeat || lvl <= p.MinLevel {
		t.Fatalf("%d features at level %d", len(fs), lvl)
	}
	_, fs2, _, err := p.sketchFeaturesAt(bytes.NewReader(b), int64(len(b)), 16)
	if err != nil || len(fs2) > 4*p.MaxFeat {
		t.Fatalf("streamed %d features: %v", len(fs2), err)
	}
	// a constant run is one fingerprint, not one per window
	z, _ := rsParams{MinLevel: 0, MaxFeat: 1 << 20}.features(make([]byte, 1<<20))
	if len(z) > 1 {
		t.Fatalf("zero run: %d fingerprints", len(z))
	}
}

// TestB7IndexReference: postings and measured coverage equal brute force over every content.
func TestB7IndexReference(t *testing.T) {
	p := rsDefaultParams()
	p.MinLevel = 4
	shared := b5Text(11, 40<<10)
	var feats [][]rsFeat
	var lvls []uint8
	for i := range 40 {
		b := b7Rand(uint64(i), 20<<10)
		if i%3 == 0 {
			copy(b[i*100:], shared[:10<<10])
		}
		f, l := p.features(append(b, shared[i*512:i*512+2048]...))
		feats, lvls = append(feats, f), append(lvls, l)
	}
	x := newRSIndex(p, feats, lvls)
	for v := range feats {
		for _, f := range feats[v] {
			var want []int32
			for u := range feats {
				if _, ok := slices.BinarySearchFunc(feats[u], f.h, func(a rsFeat, h uint64) int { return cmpU64(a.h, h) }); ok {
					want = append(want, int32(u))
				}
			}
			var got []int32
			for _, q := range x.postings(f.h) {
				got = append(got, q.node)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("postings of %x: %v, want %v", f.h, got, want)
			}
		}
		for _, c := range x.rank(int32(v), 1<<30, func(int32) bool { return true }, nil, nil) {
			var n int64
			for _, f := range feats[v] {
				if f.lvl < c.lvl {
					continue
				}
				if _, ok := slices.BinarySearchFunc(feats[c.u], f.h, func(a rsFeat, h uint64) int { return cmpU64(a.h, h) }); ok {
					n++
				}
			}
			if c.cov != n*c.w {
				t.Fatalf("coverage of %d by %d: %d, brute force %d", v, c.u, c.cov, n*c.w)
			}
		}
	}
}

// b7Mosaic: target T = lib part of A + src part of B (shifted), 30 distractors that share only the
// boilerplate header every content carries, and five near-copies of A (redundant with it).
func b7Mosaic() (a, b, tgt []byte, others [][]byte) {
	hdr := b5Text(1, 6<<10)
	a = slices.Concat(hdr, b5Text(2, 96<<10))
	b = slices.Concat(hdr, b7Rand(3, 8<<10), b5Text(4, 64<<10))
	tgt = slices.Concat(hdr, a[len(hdr):len(hdr)+80<<10], b5Text(5, 2<<10), b[len(hdr):len(hdr)+8<<10])
	for i := range 30 {
		others = append(others, slices.Concat(hdr, b5Text(uint64(100+i), 40<<10)))
	}
	for i := range 5 {
		c := slices.Clone(a)
		copy(c[len(c)-4096:], b7Rand(uint64(50+i), 4096))
		others = append(others, c)
	}
	return
}

// TestB7ComplementaryRetrieval: the region ranking finds both sources of a mosaic target despite the
// boilerplate and the near-copies, and greedy marginal coverage pairs A (or a copy) with B.
func TestB7ComplementaryRetrieval(t *testing.T) {
	p := rsDefaultParams()
	a, b, tgt, others := b7Mosaic()
	all := append([][]byte{tgt, a, b}, others...)
	var feats [][]rsFeat
	var lvls []uint8
	for _, c := range all {
		f, l := p.features(c)
		feats, lvls = append(feats, f), append(lvls, l)
	}
	x := newRSIndex(p, feats, lvls)
	cands := x.rank(0, int64(len(tgt)), func(u int32) bool { return true }, nil, nil)
	if len(cands) < 2 {
		t.Fatalf("%d candidates", len(cands))
	}
	isA := func(u int32) bool { return u == 1 || u >= int32(len(all)-5) }
	if !isA(cands[0].u) {
		t.Fatalf("top candidate %d", cands[0].u)
	}
	set := rsGreedy(cands, 0, 2, 1<<10)
	if len(set) != 2 || cands[set[1]].u != 2 {
		t.Fatalf("greedy set %v (%d)", set, cands[set[len(set)-1]].u)
	}
	// boilerplate holders gain nothing at the margin
	if m := cands[set[0]].marginal(make([]uint64, len(cands[0].bits))); m < int64(len(tgt))/3 {
		t.Fatalf("A covers %d of %d", m, len(tgt))
	}
}

// TestB7OptimizeMultiBase: through the optimizer the mosaic target becomes a 2-base delta only with
// region discovery; the plan applies exactly, deep-verifies after reclamation and reopen, and converges.
func TestB7OptimizeMultiBase(t *testing.T) {
	a, b, tgt, others := b7Mosaic()
	l := newAuthLab(t)
	for i, o := range others {
		l.put("other/"+string(rune('a'+i%26))+string(rune('a'+i/26)), o)
	}
	l.put("lib/a", a)
	l.put("src/b", b)
	l.put("app/t", tgt)
	l.reclaim()
	pol := OptimizePolicy{Profile: "archive", Locality: []int{0}, AuthPolicy: AuthPolicy{Order: "oldest", MaxBases: 2, MaxDepth: 1}}
	sk := pol
	sk.Discovery = "sketch"
	r6 := l.optimize(sk)
	pol.Discovery = "region"
	pol.Apply = true
	r7 := l.optimize(pol)
	multi := false
	for _, c := range r7.Changes {
		if c.Key == "app/t" && len(c.Bases) == 2 {
			multi = true
		}
		if c.Status != "done" {
			t.Fatalf("%+v", c)
		}
	}
	if !multi || r7.Discovery == nil || r7.Discovery.Multi == 0 {
		t.Fatalf("no multi-base plan for the mosaic: %+v %+v", r7.Changes, r7.Discovery)
	}
	if r7.Planned.Bytes >= r6.Planned.Bytes {
		t.Fatalf("region discovery %d bytes, sketch %d", r7.Planned.Bytes, r6.Planned.Bytes)
	}
	l.exact("applied")
	l.reclaim()
	l.offline(func() {})
	l.exact("reopened")
	l.verifyOK("multi")
	pol.Apply = false
	if again := l.optimize(pol); len(again.Changes) != 0 {
		t.Fatalf("re-run plans %+v", again.Changes)
	}
}

// TestB7NegativeControls: random and unrelated contents get no region candidates and no changes.
func TestB7NegativeControls(t *testing.T) {
	l := newAuthLab(t)
	for i := range 6 {
		l.put("r"+string(rune('0'+i)), b7Rand(uint64(70+i), 96<<10))
	}
	l.reclaim()
	rep := l.optimize(OptimizePolicy{Discovery: "region", Profile: "archive", AuthPolicy: AuthPolicy{Order: "oldest"}})
	if len(rep.Changes) != 0 || rep.Discovery.Singles != 0 || rep.Encodes != 0 {
		t.Fatalf("random: %d changes, %d singles, %d encodes", len(rep.Changes), rep.Discovery.Singles, rep.Encodes)
	}
}

// TestB7DiscoveryDeterministic: two planning passes over one store produce identical candidate sets.
func TestB7DiscoveryDeterministic(t *testing.T) {
	a, b, tgt, others := b7Mosaic()
	l := newAuthLab(t)
	for i, o := range others[:8] {
		l.put("o"+string(rune('a'+i)), o)
	}
	l.put("a", a)
	l.put("b", b)
	l.put("t", tgt)
	var prev []string
	for range 2 {
		g := b6Graph(t, l, OptimizePolicy{Discovery: "region", Profile: "archive"})
		var labels []string
		for _, nd := range g.nodes {
			for _, o := range nd.opts {
				labels = append(labels, o.label+string(o.sum[:4]))
			}
		}
		if prev != nil && !slices.Equal(prev, labels) {
			t.Fatal("candidate sets differ between passes")
		}
		prev = labels
	}
	_ = context.Background
}

// b7Files counts the regular files under dir (staged components are files or pack records in files).
func b7Files(dir string) (n int, size int64) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n++
				size += fi.Size()
			}
		}
		return nil
	})
	return
}

// TestB7LazyRepCandidates: representation candidates are priced without staging anything, at the
// prices the staged build gives, and a selected lazy candidate is constructed and proven at apply.
func TestB7LazyRepCandidates(t *testing.T) {
	l := newAuthLab(t)
	for _, f := range repFixtures(t) {
		l.put(f.name, f.data)
	}
	l.reclaim()
	price := func(stage bool) (map[[32]byte]int64, *rgGraph) {
		g := b6Graph(t, l, OptimizePolicy{Profile: "archive", AuthPolicy: AuthPolicy{Order: "oldest", AllowRep: true}, StageReps: stage})
		out := map[[32]byte]int64{}
		for _, nd := range g.nodes {
			for _, o := range nd.opts {
				if o.kind == rgRep {
					var c int64
					for _, id := range o.comps {
						c += g.cphys[id]
					}
					out[nd.sha] = c + o.meta
				}
			}
		}
		return out, g
	}
	n0, s0 := b7Files(l.dir)
	lazy, g := price(false)
	if n1, s1 := b7Files(l.dir); n1 != n0 || s1 != s0 {
		t.Fatalf("lazy planning wrote: %d files / %d bytes -> %d / %d", n0, s0, n1, s1)
	}
	if len(lazy) < 3 {
		t.Fatalf("%d representation candidates", len(lazy))
	}
	staged, _ := price(true)
	for sha, c := range lazy {
		if staged[sha] != c {
			t.Fatalf("%x: lazy price %d, staged %d", sha[:6], c, staged[sha])
		}
	}
	// force every lazy representation into the plan; one planned root is corrupted (a re-construction that
	// differs from the plan): that node must fail and keep its current record, the others publish
	e := newRGEval(g, g.currentSel())
	forced := 0
	var bad *rgNode
	for v, nd := range g.nodes {
		for o, opt := range nd.opts {
			if opt.kind == rgRep && opt.rep.lazy {
				e.set(int32(v), int32(o))
				forced++
				if bad == nil {
					bad = nd
					r2 := *opt.rep.root
					r2.Rep[0] ^= 1
					opt.rep = &authRepCand{kind: opt.rep.kind, root: &r2, chunks: opt.rep.chunks, lazy: true}
				}
			}
		}
	}
	var rep OptimizeReport
	if err := g.apply(context.Background(), e, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 1 {
		t.Fatalf("corrupted plan: %d failed", rep.Failed)
	}
	if r, _ := l.s.authLoad(bad.sha); r != nil && r.Form == authFormRep {
		t.Fatal("a representation that differs from its plan was published")
	}
	forced--
	t.Logf("forced %d lazy representations, %d candidates", forced, len(lazy))
	if rep.Applied != forced {
		t.Fatalf("applied %d of %d lazy representations (failed %d)", rep.Applied, forced, rep.Failed)
	}
	st, _ := l.s.AuthorityStatus()
	if st.ByForm["rep"] != forced {
		t.Fatalf("rep records %v, want %d", st.ByForm, forced)
	}
	l.exact("lazy reps")
	l.reclaim()
	l.exact("lazy reps reclaimed")
	l.verifyOK("lazy reps")
}

// TestB7UnionProgramsExact: programs matched over the union of 2-4 bases (shifted, reordered and
// repeated regions, each locality) reconstruct the target exactly from the bases the envelope names.
func TestB7UnionProgramsExact(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 4))
	for trial := range 24 {
		nb := 2 + trial%3
		var bases [][]byte
		var srcs []xdSrc
		for k := range nb {
			b := b5Text(uint64(trial*10+k), 20<<10+r.IntN(60<<10))
			bases = append(bases, b)
			srcs = append(srcs, xdSrc{sha256Of(b), b})
		}
		var tgt []byte
		for range 6 + r.IntN(10) {
			b := bases[r.IntN(nb)]
			off := r.IntN(len(b) - 4096)
			tgt = append(tgt, b[off:off+512+r.IntN(3500)]...)
			if r.IntN(3) == 0 {
				tgt = append(tgt, b7Rand(uint64(trial), 1+r.IntN(300))...)
			}
		}
		x, err := dwDcoreBuildIndex(bases, dwDcoreDefaultParams())
		if err != nil {
			t.Fatal(err)
		}
		for _, loc := range []int{0, 32, 256} {
			blob, refs, err := xdEncodeOpsSum(srcs, xdFromDw(x.MatchLocal(tgt, loc)), tgt, sha256Of(tgt), 128<<10)
			if err != nil || len(refs) < 2 {
				t.Fatalf("trial %d loc %d: %d refs, %v", trial, loc, len(refs), err)
			}
			h, more, err := xdParseHeader(blob)
			if err != nil || more {
				t.Fatal(err)
			}
			by := map[[32]byte][]byte{}
			for _, s := range srcs {
				by[s.sha] = s.data
			}
			out := make([]byte, h.size)
			for i, f := range h.frames {
				ops, err := xdDecodeFrame(&h, i, blob[f.poff:f.poff+f.plen])
				if err != nil {
					t.Fatal(err)
				}
				for _, o := range ops {
					if o.base < 0 {
						copy(out[o.tpos:], o.lit)
					} else {
						copy(out[o.tpos:o.tpos+o.n], by[h.bases[o.base].SHA][o.off:o.off+o.n])
					}
				}
			}
			if !bytes.Equal(out, tgt) {
				t.Fatalf("trial %d loc %d: reconstruction differs", trial, loc)
			}
		}
	}
}

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }
