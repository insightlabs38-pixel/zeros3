//go:build b7bench && b6bench && b5locality && b1rangeexperiment

package main

// B7 similarity-discovery campaign (not a regression test):
// go test -tags b7bench,b6bench,b5locality,b1rangeexperiment. Environment as B6 (ZEROS3_B6_CORPUS, ZEROS3_B6_FX,
// ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES), plus ZEROS3_B7_ABLATE for the discovery ablations.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// b7Families are the pre-registered families (testing-harnesses/next/b7/PREREGISTRATION.md).
func b7Families(c string) []b6Family {
	fams := b6Families(c)
	dir := func(name, root string, vers ...string) b6Family {
		f := b6Family{name: name}
		for _, v := range vers {
			r := filepath.Join(c, root, v)
			var files []string
			filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() != ".done" {
					files = append(files, p)
				}
				return nil
			})
			slices.Sort(files)
			for _, p := range files {
				rel, _ := filepath.Rel(r, p)
				f.puts = append(f.puts, b6Put{rel, p})
			}
		}
		return f
	}
	fams = append(fams, dir("go-files", "gof", "1.22.0", "1.22.6", "1.23.0", "1.23.4"))
	v := b6Family{name: "vendor"}
	for _, r := range []string{"1", "2"} {
		for k := range 6 {
			n := "p" + string(rune('0'+k)) + "-r" + r + ".tar"
			v.puts = append(v.puts, b6Put{"products/" + n, filepath.Join(c, "vendor", n)})
		}
	}
	fams = append(fams, v)
	t := b6Family{name: "tensors"}
	for _, n := range []string{"t0", "t1", "t2"} {
		t.puts = append(t.puts, b6Put{"model.bin", filepath.Join(c, "tensors", n+".bin")})
	}
	fams = append(fams, t)
	u := b6Family{name: "unrelated"}
	for _, n := range []string{"coreutils.tar", "compile.tar", "app.db", "ts.tgz", "h.pack", "r.bin"} {
		u.puts = append(u.puts, b6Put{"misc/" + n, filepath.Join(c, "unrelated", n)})
	}
	return append(fams, u)
}

// TestB7Build writes the CDC store of every B7 family that B6 did not build.
func TestB7Build(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, f := range b7Families(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		dir := filepath.Join(fx, f.name, "cdc")
		if _, err := os.Stat(filepath.Join(dir, ".b6-ready")); err == nil {
			continue
		}
		os.RemoveAll(dir)
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
		var logical int64
		for _, p := range f.puts {
			data, err := os.ReadFile(p.file)
			if err != nil {
				t.Fatal(err)
			}
			logical += int64(len(data))
			if _, err := s.PutObject("b", p.key, data, "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
		econReclaim(t, dir)
		alloc, apparent := allocatedBytes(dir)
		meta, _ := json.Marshal(map[string]any{"family": f.name, "objects": len(f.puts), "logical": logical, "allocated": alloc, "apparent": apparent})
		os.WriteFile(filepath.Join(dir, ".b6-ready"), meta, 0o644)
		t.Logf("%s", meta)
	}
}

// b7Oracle is the exhaustive single-base frontier of one family: every ordered pair matched exactly.
type b7Oracle struct {
	g      *rgGraph
	tg     []int32           // in-memory targets
	bases  []int32           // every live content
	data   map[int32][]byte  // content bytes
	est0   map[int32]int64   // standalone estimate (one literal program)
	copied map[int32][]int64 // target -> copied bytes per base (bases order)
	est    map[[2]int32]int64
	mu     sync.Mutex
}

func (o *b7Oracle) estimate(v, u int32) int64 {
	o.mu.Lock()
	e, ok := o.est[[2]int32{v, u}]
	o.mu.Unlock()
	if ok {
		return e
	}
	x, err := dwDcoreBuildIndex([][]byte{o.data[u]}, dwDcoreDefaultParams())
	if err != nil {
		return math.MaxInt64
	}
	e = dwDcoreEstimateProgram(x.Match(o.data[v]), dwDcoreEstimateOpts{}).Est
	o.mu.Lock()
	o.est[[2]int32{v, u}] = e
	o.mu.Unlock()
	return e
}

func (o *b7Oracle) union(v int32, us []int32) int64 {
	var xs [][]byte
	for _, u := range us {
		xs = append(xs, o.data[u])
	}
	x, err := dwDcoreBuildIndex(xs, dwDcoreDefaultParams())
	if err != nil {
		return math.MaxInt64
	}
	return dwDcoreEstimateProgram(x.Match(o.data[v]), dwDcoreEstimateOpts{}).Est
}

// ranked is the oracle order of bases for v: the 16 bases copying the most bytes, by exact estimate.
func (o *b7Oracle) ranked(v int32) []int32 {
	cp := o.copied[v]
	idx := make([]int, len(o.bases))
	for k := range idx {
		idx[k] = k
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(cp[b], cp[a]) })
	var out []int32
	for _, k := range idx[:min(16, len(idx))] {
		if cp[k] > 0 {
			out = append(out, o.bases[k])
		}
	}
	slices.SortStableFunc(out, func(a, b int32) int { return cmp.Compare(o.estimate(v, a), o.estimate(v, b)) })
	return out
}

func newB7Oracle(t *testing.T, s *Store, pol OptimizePolicy) *b7Oracle {
	g, err := s.rgBuild(context.Background(), pol)
	if err != nil {
		t.Fatal(err)
	}
	o := &b7Oracle{g: g, data: map[int32][]byte{}, est0: map[int32]int64{}, copied: map[int32][]int64{}, est: map[[2]int32]int64{}}
	for i, nd := range g.nodes {
		v := int32(i)
		if !nd.live || nd.obj == nil || nd.size > authWindowMin {
			continue
		}
		b, err := s.manifestBytes(nd.obj.man)
		if err != nil {
			t.Fatal(err)
		}
		o.data[v] = b
		o.bases = append(o.bases, v)
		if !nd.fixed && nd.size >= pol.MinObject {
			o.tg = append(o.tg, v)
			o.est0[v] = dwDcoreEstimateProgram([]dwDcoreOp{{N: int64(len(b)), Base: -1, Lit: b}}, dwDcoreEstimateOpts{}).Est
		}
	}
	for _, v := range o.tg {
		o.copied[v] = make([]int64, len(o.bases))
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for k, u := range o.bases {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			x, err := dwDcoreBuildIndex([][]byte{o.data[u]}, dwDcoreDefaultParams())
			if err != nil {
				return
			}
			for _, v := range o.tg {
				if v == u {
					continue
				}
				var cp int64
				for _, op := range x.Match(o.data[v]) {
					if op.Base >= 0 {
						cp += op.N
					}
				}
				o.copied[v][k] = cp
			}
		}()
	}
	wg.Wait()
	return o
}

// TestB7Oracle measures the discovery frontier: B6's sketch shortlist and B7's region shortlist against
// exhaustive pairwise matching, at the estimate level and through the B6 optimizer (model only).
func TestB7Oracle(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	base := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, AllowDelta: true, Candidates: 8}
	for _, f := range b7Families(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		dir := filepath.Join(fx, f.name, "cdc")
		s, done := b6Open(t, dir)
		pol := OptimizePolicy{AuthPolicy: base, Profile: "archive", Locality: []int{0}}.norm()
		t0 := time.Now()
		o := newB7Oracle(t, s, pol)
		oracleS := time.Since(t0).Seconds()
		if len(o.tg) == 0 {
			done()
			continue
		}
		// shortlists: B6 sketch ranking and B7 region ranking (8 deep), from one generate pass each
		ranks := map[string]map[int32][]int32{}
		multis := map[string]map[int32][][]int32{}
		stats := map[string]*DiscoveryStats{}
		variants := []string{"sketch", "region"}
		if v := os.Getenv("ZEROS3_B7_ABLATE"); v != "" {
			variants = append(variants, strings.Split(v, ",")...)
		}
		for _, d := range variants {
			p := pol
			p.Discovery = d
			if d != "sketch" {
				p.Discovery = "region"
				p.Region = b7Variant(d)
			}
			rk, mu := map[int32][]int32{}, map[int32][][]int32{}
			g, err := s.rgBuild(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			p.AllowDelta = false // rank only: no encodes
			g.pol = p
			if err := g.generate(context.Background()); err != nil {
				t.Fatal(err)
			}
			m := &authMigrator{ctx: context.Background(), s: s, pol: p.AuthPolicy, wc: g.wc}
			m.idx = &authIdxCache{budget: authIdxCacheBytes, m: map[[32]byte]*authIdxEnt{}}
			inv, byKey := map[uint64][]int32{}, map[string][]int32{}
			for i, nd := range g.nodes {
				if nd.sk != nil {
					for _, f := range nd.sk {
						inv[f] = append(inv[f], int32(i))
					}
					byKey[nd.obj.bucket+"\x00"+nd.obj.key] = append(byKey[nd.obj.bucket+"\x00"+nd.obj.key], int32(i))
				}
			}
			t1 := time.Now()
			for _, v := range o.tg {
				var cs rgCandSet
				if p.Discovery == "sketch" {
					cs.singles = g.rgCandidates(v, inv, byKey)
				} else {
					g.pol.AllowDelta = true
					cs = g.regionCands(m, v, byKey)
				}
				rk[v], mu[v] = cs.singles, cs.multi
			}
			g.disc.RankSeconds = time.Since(t1).Seconds()
			ranks[d], multis[d], stats[d] = rk, mu, g.disc
		}
		// estimate-level frontier
		row := map[string]any{"kind": "frontier", "family": f.name, "targets": len(o.tg), "bases": len(o.bases), "oracle_s": oracleS}
		var useful int
		var sum0, sumO, sumO2 float64
		hits, got := map[string][]int{}, map[string][]float64{}
		for d := range ranks {
			hits[d], got[d] = make([]int, 9), make([]float64, 9)
		}
		fp := map[string]int{}
		shortN := map[string]int{}
		var multiB6, multiB7 float64
		for _, v := range o.tg {
			e0 := o.est0[v]
			or := o.ranked(v)
			best := e0
			if len(or) > 0 {
				best = min(e0, o.estimate(v, or[0]))
			}
			sum0 += float64(e0)
			sumO += float64(e0 - best)
			if float64(best) <= 0.9*float64(e0) {
				useful++
			}
			// oracle pair (exhaustive over the 6 best singles)
			b2 := best
			for i := 0; i < min(6, len(or)); i++ {
				for j := i + 1; j < min(6, len(or)); j++ {
					b2 = min(b2, o.union(v, []int32{or[i], or[j]}))
				}
			}
			sumO2 += float64(best - b2)
			for d, rk := range ranks {
				for _, u := range rk[v][:min(4, len(rk[v]))] {
					shortN[d]++
					if e, ok := o.data[u]; ok && e != nil && float64(o.estimate(v, u)) >= 0.98*float64(e0) {
						fp[d]++
					}
				}
				for _, K := range []int{1, 2, 4, 8} {
					bk := e0
					for _, u := range rk[v][:min(K, len(rk[v]))] {
						if _, ok := o.data[u]; ok {
							bk = min(bk, o.estimate(v, u))
						}
					}
					if float64(bk) <= 1.02*float64(best) {
						hits[d][K]++
					}
					got[d][K] += float64(e0 - bk)
				}
			}
			// B6 refinement over its top-4 vs B7's complementary sets (estimated)
			if rk := ranks["sketch"][v]; len(rk) > 1 {
				s1 := slices.Clone(rk[:min(4, len(rk))])
				slices.SortStableFunc(s1, func(a, b int32) int { return cmp.Compare(o.estimate(v, a), o.estimate(v, b)) })
				x0, _ := dwDcoreBuildIndex([][]byte{o.data[s1[0]]}, dwDcoreDefaultParams())
				ops := x0.Match(o.data[v])
				lit := dwDcoreEstimateProgram(ops, dwDcoreEstimateOpts{}).Stats.LitBytes
				e1 := o.estimate(v, s1[0])
				for _, u := range s1[1:] {
					x1, _ := dwDcoreBuildIndex([][]byte{o.data[u]}, dwDcoreDefaultParams())
					if r, saved := authRefine(ops, x1, 1); saved > 0 && saved*50 >= lit {
						multiB6 += float64(e1 - min(e1, dwDcoreEstimateProgram(r, dwDcoreEstimateOpts{}).Est))
						break
					}
				}
			}
			if ms := multis["region"][v]; len(ms) > 0 && len(ranks["region"][v]) > 0 {
				e1 := o.estimate(v, ranks["region"][v][0])
				multiB7 += float64(e1 - min(e1, o.union(v, ms[len(ms)-1])))
			}
		}
		row["useful_targets"], row["standalone_est"], row["oracle_single_saving"], row["oracle_pair_extra"] = useful, sum0, sumO, sumO2
		row["b6_refine_extra"], row["b7_multi_extra"] = multiB6, multiB7
		for d := range ranks {
			r := map[string]any{"stats": stats[d], "false_pos_top4": float64(fp[d]) / float64(max(1, shortN[d]))}
			for _, K := range []int{1, 2, 4, 8} {
				r["recall@"+string(rune('0'+K))] = float64(hits[d][K]) / float64(len(o.tg))
				r["benefit@"+string(rune('0'+K))] = got[d][K] / math.Max(1, sumO)
			}
			row[d] = r
		}
		b6Emit(row)
		b, _ := json.Marshal(row)
		t.Logf("%s", b)
		if os.Getenv("ZEROS3_B7_GRAPH") == "0" {
			done()
			continue
		}
		// graph level: the B6 optimizer (archive, model only) on each candidate source
		bySHA := map[[32]byte]int32{}
		for _, w := range o.tg {
			bySHA[o.g.nodes[w].sha] = w
		}
		hook := func(g *rgGraph, v int32) rgCandSet {
			ov, ok := bySHA[g.nodes[v].sha]
			if !ok {
				return rgCandSet{}
			}
			var cs rgCandSet
			or := o.ranked(ov)
			for _, u := range or[:min(4, len(or))] {
				cs.singles = append(cs.singles, g.idx[o.g.nodes[u].sha])
			}
			if len(or) > 1 {
				b1, bp := o.estimate(ov, or[0]), []int32(nil)
				for i := 0; i < min(6, len(or)); i++ {
					for j := i + 1; j < min(6, len(or)); j++ {
						if e := o.union(ov, []int32{or[i], or[j]}); e < b1-dwDcoreBaseRefBytes-64 {
							b1, bp = e, []int32{or[i], or[j]}
						}
					}
				}
				if bp != nil {
					cs.multi = [][]int32{{g.idx[o.g.nodes[bp[0]].sha], g.idx[o.g.nodes[bp[1]].sha]}}
				}
			}
			return cs
		}
		for _, cfg := range []struct {
			name string
			p    func(*OptimizePolicy)
		}{
			{"b6", func(p *OptimizePolicy) { p.Discovery = "sketch"; p.noRestrictedStart = true }},
			{"b6-search", func(p *OptimizePolicy) { p.Discovery = "sketch" }},
			{"b7-single", func(p *OptimizePolicy) { p.Discovery = "region"; p.Region.Multi = false }},
			{"b7", func(p *OptimizePolicy) { p.Discovery = "region" }},
			{"oracle", func(p *OptimizePolicy) { p.discover = hook }},
			{"b6-exact-records", func(p *OptimizePolicy) { p.Discovery = "sketch"; p.recordBlock = 1 }},
			{"oracle-exact-records", func(p *OptimizePolicy) { p.discover = hook; p.recordBlock = 1 }},
		} {
			p := OptimizePolicy{AuthPolicy: base, Profile: "archive", Locality: []int{0}}
			p.Candidates = 4
			p.Region = rsDefaultParams()
			cfg.p(&p)
			resetRSSPeak()
			cpu0 := procCPUSeconds()
			rep, err := s.AuthorityOptimize(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			gr := map[string]any{"kind": "graph", "family": f.name, "config": cfg.name, "cdc_bytes": rep.CDCOnly.Bytes, "planned_bytes": rep.Planned.Bytes,
				"planned": rep.Planned, "discovery": rep.Discovery, "encodes": rep.Encodes, "options": rep.Options, "cpu_s": procCPUSeconds() - cpu0,
				"candidate_s": rep.CandidateSeconds, "rss_mib": rssPeakMiB(), "seconds": rep.Seconds}
			var multi int
			for _, ch := range rep.Changes {
				if len(ch.Bases) > 1 {
					multi++
				}
			}
			gr["multi_selected"] = multi
			b6Emit(gr)
			t.Logf("%s %s: planned %d (cdc %d) deltas %d multi %d encodes %d cand %.1fs", f.name, cfg.name, rep.Planned.Bytes, rep.CDCOnly.Bytes, rep.Planned.Deltas, multi, rep.Encodes, rep.CandidateSeconds)
		}
		done()
	}
}

var _ = hex.EncodeToString
var _ = strings.Contains

// b7Phys is the packed size of an envelope's pieces (the pack writer's compressor and record header).
func b7Phys(blob []byte, ends []int) int64 {
	comp := newCompressor(true)
	var n int64
	start := 0
	for _, e := range ends {
		d, _ := comp.encode(blob[start:e])
		n += int64(len(d)) + packRecordBytes
		start = e
	}
	return n
}

// TestB7Density measures COPY-anchor density on large bases: index build, memory, matching and the
// envelope bytes of consecutive versions, in memory (<= 32 MiB) and windowed (coreutils).
func TestB7Density(t *testing.T) {
	c := os.Getenv("ZEROS3_B6_CORPUS")
	if c == "" {
		t.Skip("set ZEROS3_B6_CORPUS")
	}
	ap := AuthPolicy{}.norm()
	for _, f := range b7Families(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) || !slices.Contains([]string{"go", "sqlite", "tensors", "coreutils"}, f.name) {
			continue
		}
		for i := 1; i < len(f.puts); i++ {
			base, _ := os.ReadFile(f.puts[i-1].file)
			tgt, _ := os.ReadFile(f.puts[i].file)
			bsha, tsha := sha256.Sum256(base), sha256.Sum256(tgt)
			for _, anchors := range []int{0, 1 << 20, 1 << 22, 1 << 24, 1 << 26} {
				row := map[string]any{"kind": "density", "family": f.name, "pair": i, "base_bytes": len(base), "target_bytes": len(tgt)}
				runtime.GC()
				if int64(len(base)) <= authWindowMin {
					prm := dwDcoreDefaultParams()
					if anchors == 0 {
						continue
					}
					prm.MaxAnchors = anchors
					t0 := time.Now()
					x, err := dwDcoreBuildIndex([][]byte{base}, prm)
					if err != nil {
						t.Fatal(err)
					}
					row["anchors"], row["build_s"], row["index_bytes"], row["sample_bits"] = anchors, time.Since(t0).Seconds(), len(x.table)*8, x.nbits
					for _, loc := range []int{0, 256} {
						t1 := time.Now()
						ops := x.MatchLocal(tgt, loc)
						ms := time.Since(t1).Seconds()
						blob, _, err := xdEncodeOpsSum([]xdSrc{{bsha, base}}, xdFromDw(ops), tgt, tsha, ap.FrameSpan)
						if err != nil {
							t.Fatal(err)
						}
						st := dwDcoreStats(ops)
						row[fmt.Sprintf("loc%d", loc)] = map[string]any{"match_s": ms, "copied": st.CopyBytes, "copies": st.Copies, "phys": b7Phys(blob, authPieceEnds(blob))}
					}
				} else {
					wp := authWParams(4, int64(len(base)))
					if anchors > 0 {
						wp.Anchor.MaxAnchors = anchors
					}
					row["anchors"] = wp.Anchor.MaxAnchors
					t0 := time.Now()
					idx, err := dwDcoreBuildWIndex(bytes.NewReader(base), int64(len(base)), wp.Anchor, 4, &bsha)
					if err != nil {
						t.Fatal(err)
					}
					row["build_s"] = time.Since(t0).Seconds()
					for _, loc := range []int{0, 256} {
						wp.Locality = loc
						w := &rgBlob{}
						t1 := time.Now()
						st, err := dwDcoreWEncode(w, bytes.NewReader(tgt), int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, wp)
						if err != nil {
							t.Fatal(err)
						}
						row[fmt.Sprintf("loc%d", loc)] = map[string]any{"match_s": time.Since(t1).Seconds(), "copied": st.CopyBytes, "phys": b7Phys(w.b, w.finish())}
					}
				}
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				row["heap_mib"] = ms.HeapAlloc >> 20
				b6Emit(row)
				b, _ := json.Marshal(row)
				t.Logf("%s", b)
			}
		}
	}
}

// b7Variant is a discovery ablation: region parameters or an alternative fingerprint generator.
func b7Variant(name string) rsParams {
	p := rsDefaultParams()
	switch name {
	case "region":
	case "lvl5", "lvl6", "lvl8", "lvl9":
		p.MinLevel = name[3] - '0'
	case "rare4":
		p.Rare = 4
	case "rare64":
		p.Rare = 64
	case "short8":
		p.Short = 8
	case "noverify":
		p.Verify = 0
	case "fastcdc", "gear", "cdcv1":
		ch := b7Chunkers()[name]
		p.gen = func(b []byte) ([]rsFeat, uint8) {
			var fs []rsFeat
			off := 0
			for _, n := range ch(b) {
				h := dwDcore_mix64(b7Hash(b[off : off+n]))
				fs = append(fs, rsFeat{h, uint32(off >> rsPosShift), 63})
				off += n
			}
			return rsNorm(fs, 0), 10 // every chunk; one fingerprint stands for ~1 KiB
		}
	default:
		panic("unknown discovery variant " + name)
	}
	return p
}

func b7Hash(b []byte) uint64 {
	s := sha256.Sum256(b)
	return binary.LittleEndian.Uint64(s[:8])
}

// b7Chunkers are the boundary generators compared in B7-F; each returns chunk lengths.
func b7Chunkers() map[string]func([]byte) []int {
	gearCut := func(b []byte, minN, maxN, avgBits, sBits, lBits int) []int {
		var out []int
		for len(b) > 0 {
			n := len(b)
			var h uint64
			lim := min(len(b), maxN)
			for i := 0; i < lim; i++ {
				h = h<<1 + gearTable[b[i]]
				if i+1 < minN {
					continue
				}
				bitsN := sBits
				if i+1 >= 1<<avgBits {
					bitsN = lBits
				}
				if h>>(64-bitsN) == 0 {
					n = i + 1
					break
				}
				n = lim
			}
			out = append(out, n)
			b = b[n:]
		}
		return out
	}
	return map[string]func([]byte) []int{
		"cdcv1": func(b []byte) []int {
			var out []int
			for len(b) > 0 {
				n := findCDCBoundary(b, true)
				out = append(out, n)
				b = b[n:]
			}
			return out
		},
		"gear":    func(b []byte) []int { return gearCut(b, 256, 8192, 10, 10, 10) },
		"fastcdc": func(b []byte) []int { return gearCut(b, 256, 8192, 10, 12, 8) }, // normalized chunking, level 2
	}
}

// TestB7Chunking compares chunk boundaries as similarity features (B7-F): stability of consecutive
// versions at each granularity against the matcher's copied bytes, size distribution and cost.
func TestB7Chunking(t *testing.T) {
	c := os.Getenv("ZEROS3_B6_CORPUS")
	if c == "" {
		t.Skip("set ZEROS3_B6_CORPUS")
	}
	for _, f := range b7Families(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) || !slices.Contains([]string{"coreutils", "go", "sqlite", "tensors", "git", "vendor"}, f.name) {
			continue
		}
		for i := 1; i < len(f.puts); i++ {
			base, _ := os.ReadFile(f.puts[i-1].file)
			tgt, _ := os.ReadFile(f.puts[i].file)
			row := map[string]any{"kind": "chunking", "family": f.name, "pair": i, "target_bytes": len(tgt)}
			prm := dwDcoreDefaultParams()
			prm.MaxAnchors = 1 << 27
			x, _ := dwDcoreBuildIndex([][]byte{base}, prm)
			row["matcher_copied"] = dwDcoreStats(x.Match(tgt)).CopyBytes
			x = nil
			for name, ch := range b7Chunkers() {
				t0 := time.Now()
				tc := ch(tgt)
				ns := float64(time.Since(t0).Nanoseconds()) / float64(len(tgt))
				have := map[uint64]bool{}
				off := 0
				for _, n := range ch(base) {
					have[b7Hash(base[off:off+n])] = true
					off += n
				}
				var stable int64
				var sum, sq float64
				off = 0
				for _, n := range tc {
					if have[b7Hash(tgt[off:off+n])] {
						stable += int64(n)
					}
					sum += float64(n)
					sq += float64(n) * float64(n)
					off += n
				}
				mean := sum / float64(len(tc))
				row[name] = map[string]any{"chunks": len(tc), "mean": mean, "cv": math.Sqrt(sq/float64(len(tc))-mean*mean) / mean, "ns_per_byte": ns, "stable": stable}
			}
			// B7 sampled windows: the share of target fingerprints the base holds (densest level)
			p := rsParams{MinLevel: 7, MaxFeat: 1 << 30}
			t0 := time.Now()
			tf, _ := p.features(tgt)
			ns := float64(time.Since(t0).Nanoseconds()) / float64(len(tgt))
			bf, _ := p.features(base)
			have := map[uint64]bool{}
			for _, f := range bf {
				have[f.h] = true
			}
			n := 0
			for _, f := range tf {
				if have[f.h] {
					n++
				}
			}
			row["windows"] = map[string]any{"features": len(tf), "ns_per_byte": ns, "stable_est": float64(n) / float64(max(1, len(tf))) * float64(len(tgt))}
			b6Emit(row)
			b, _ := json.Marshal(row)
			t.Logf("%s", b)
		}
	}
}

// TestB7Graph runs the optimizer model-only on one family's CDC store for discovery configurations
// (ZEROS3_B7_CONFIGS: b6, b7-single, b7) under one algorithm (ZEROS3_B7_ALG) and logs the plan.
func TestB7Graph(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	for _, f := range b7Families(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		s, done := b6Open(t, filepath.Join(fx, f.name, "cdc"))
		for _, cfg := range strings.Split(cmp.Or(os.Getenv("ZEROS3_B7_CONFIGS"), "b6,b7"), ",") {
			p := OptimizePolicy{AuthPolicy: AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowDelta: true}, Profile: cmp.Or(os.Getenv("ZEROS3_B7_PROFILE"), "archive"),
				Locality: []int{0}, Algorithm: cmp.Or(os.Getenv("ZEROS3_B7_ALG"), "global"), Oracle: os.Getenv("ZEROS3_B7_ORACLE") == "1"}
			p.Discovery = "sketch"
			p.noRestrictedStart = cfg == "b6"
			if cfg != "b6" && cfg != "b6-search" {
				p.Discovery = "region"
				p.Region = rsDefaultParams()
				p.Region.Multi = cfg != "b7-single" && cfg != "union-single"
				p.Region.Union = strings.HasPrefix(cfg, "union")
			}
			rep, err := s.AuthorityOptimize(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"kind": "graph-alg", "family": f.name, "config": cfg, "algorithm": p.Algorithm, "planned": rep.Planned, "oracle": rep.Oracle, "oracle_parts": rep.OracleParts, "local": rep.Local, "changes": rep.Changes, "options": rep.Options}
			b6Emit(row)
			o := int64(-1)
			if rep.Oracle != nil {
				o = rep.Oracle.Bytes
			}
			t.Logf("%s %s %s: planned %d oracle %d (parts %d) local %d options %d", f.name, cfg, p.Algorithm, rep.Planned.Bytes, o, rep.OracleParts, rep.Local.Bytes, rep.Options)
			for _, ch := range rep.Changes {
				t.Logf("   %s %s -> %s %v d%d", ch.Key, ch.From, ch.To, ch.Bases, ch.Depth)
			}
		}
		done()
	}
}
