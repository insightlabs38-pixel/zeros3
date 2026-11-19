package main

// Z4-B2 research harness (not a regression test): measures real physical
// storage economics, read cost and resource use of representation authority
// on pinned corpora. It runs only when ZEROS3_B2_CORPUS points at the corpus
// directory; each family is meant to run in its own fresh process:
//
//	ZEROS3_B2_CORPUS=DIR ZEROS3_B2_OUT=out.jsonl go test -run 'TestB2Economics/<family>' -count=1 .

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type econFamily struct {
	name  string
	files []string
	note  string
}

func econFamilies(root string) []econFamily {
	g := func(pat string) []string {
		m, _ := filepath.Glob(filepath.Join(root, pat))
		sort.Strings(m)
		return m
	}
	clr := func(vs ...string) []string {
		var o []string
		for _, v := range vs {
			o = append(o, filepath.Join(root, "clr", v+".dll"))
		}
		return o
	}
	var unrelated []string
	for i := 0; i < 4; i++ {
		p := filepath.Join(root, fmt.Sprintf("unrelated-rand-%d.bin", i))
		if _, err := os.Stat(p); err != nil {
			os.WriteFile(p, genRandomBytes(int64(500+i), 3<<20), 0o644)
		}
		unrelated = append(unrelated, p)
	}
	unrelated = append(unrelated, g("dex/1019000/classes2.dex")...)
	unrelated = append(unrelated, clr("13.0.4")...)
	unrelated = append(unrelated, g("sqlite/v0.db")...)
	return []econFamily{
		{"src-tar-short", g("src/coreutils-*.tar"), "3 real coreutils releases (tar)"},
		{"clr-short", clr("12.0.3", "13.0.1", "13.0.2", "13.0.3", "13.0.4"), "5 real Newtonsoft.Json releases"},
		{"clr-long", clr("10.0.1", "10.0.2", "10.0.3", "11.0.1", "11.0.2", "12.0.1", "12.0.2", "12.0.3", "13.0.1", "13.0.2", "13.0.3", "13.0.4"), "12 real Newtonsoft.Json releases"},
		{"dex-b8", g("dex-b8/*/classes.dex"), "B1/B8 cohort: F-Droid 2.0.0/2.0.1/2.0.2 classes.dex (versionCodes 2000050-52), 3 files"},
		{"dex", append(g("dex/*/classes.dex"), g("dex/*/classes2.dex")...), "expanded cohort: 3 real F-Droid client releases (1019000/1020050/1021000) x 2 dex files"},
		{"parquet", g("parquet/snappy/c*.parquet"), "4 pyarrow Snappy producer states (synthetic workload)"},
		{"parquet-zstd", g("parquet/zstd/c*.parquet"), "4 pyarrow Zstd producer states"},
		{"sqlite", g("sqlite/v*.db"), "4 states of one SQLite database (synthetic edits)"},
		{"git", g("git/h*.pack"), "3 git pack exports of this repository's history"},
		{"parquet-z331-8", g("parquet-z331/c*.parquet"), "Z3-31 generator, pyarrow 26.0.1, 8-revision Snappy chain"},
		{"parquet-z331-8p", g("parquet-z331p/c*.parquet"), "Z3-31 generator, pinned pyarrow 18.1.0, 8-revision Snappy chain"},
		{"sst-snappy", g("sst/snappy/c*.sst"), "4 RocksDB SST states (Snappy)"},
		{"sst-lz4", g("sst/lz4/c*.sst"), "4 RocksDB SST states (LZ4, replayed as opaque bytes)"},
		{"unrelated", unrelated, "one-off unrelated inputs (random, dex, dll, sqlite)"},
	}
}

func rssPeakMiB() float64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			f := strings.Fields(l)
			kb, _ := strconv.ParseFloat(f[1], 64)
			return kb / 1024
		}
	}
	return 0
}

func resetRSSPeak() { os.WriteFile("/proc/self/clear_refs", []byte("5"), 0o200) }

func dropCaches() bool {
	os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
	return true
}

type econRow map[string]any

func econEmit(t *testing.T, r econRow) {
	b, _ := json.Marshal(r)
	t.Log(string(b))
	if out := os.Getenv("ZEROS3_B2_OUT"); out != "" {
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

func econBuild(t *testing.T, dir string, files []string, mode repMode) (logical int64, putSec, rssMiB float64) {
	t.Helper()
	resetRSSPeak()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.setRepMode(mode)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		logical += int64(len(data))
		key := strings.ReplaceAll(strings.TrimPrefix(f, filepath.Dir(filepath.Dir(f))+"/"), "/", "_")
		if _, err := s.PutObject("b", key, data, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	return logical, time.Since(t0).Seconds(), rssPeakMiB()
}

// econRecompress reports whether the reclamation pipeline also compresses fully live raw
// packs (repack -recompress), the cold-tier end state B8 measured for CDC and authority
// alike. ZEROS3_ECON_RAW=1 keeps direct-ingest packs raw (the as-ingested physical state).
func econRecompress() bool { return os.Getenv("ZEROS3_ECON_RAW") != "1" }

func econCompact(t *testing.T, dir string) {
	t.Helper()
	if _, err := compactStore(dir, authCompactOpts()); err != nil {
		t.Fatal(err)
	}
	if econRecompress() {
		if _, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: true}); err != nil {
			t.Fatal(err)
		}
	}
}

func econReclaim(t *testing.T, dir string) (gcSec, repackSec float64) {
	t.Helper()
	t0 := time.Now()
	if _, err := gcCollect(dir, true); err != nil {
		t.Fatal(err)
	}
	gcSec = time.Since(t0).Seconds()
	t0 = time.Now()
	if _, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: econRecompress()}); err != nil {
		t.Fatal(err)
	}
	if _, err := compactStore(dir, authCompactOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := gcCollect(dir, true); err != nil {
		t.Fatal(err)
	}
	return gcSec, time.Since(t0).Seconds()
}

func econMigrate(t *testing.T, dir string, pol AuthPolicy) (AuthReport, float64, int64) {
	t.Helper()
	resetRSSPeak()
	lock, err := acquireStoreLock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pol.Apply = true
	rep, err := s.AuthorityMigrate(pol)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	peak := dirBytes(t, dir) // double storage: record bodies and CDC coexist until gc
	return rep, rssPeakMiB(), peak
}

func econBreakdown(t *testing.T, dir string) map[string]int64 {
	out := map[string]int64{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if fi, e := d.Info(); e == nil && top != "tmp" {
			out[top] += fi.Size()
		}
		return nil
	})
	return out
}

// econBlast measures reliability blast radius: how many live contents become
// unreadable if one physical chunk is lost (max and mean over the chunks any
// live content needs), compared with CDC where a chunk is lost only for the
// contents that list it.
func econBlast(s *Store) (maxN int, mean float64, chunks int) {
	deps := map[string]int{}
	seen := map[[32]byte]bool{}
	for _, lm := range s.liveManifests() {
		sha, err := decodeHexSHA256(lm.man.ObjectSHA256)
		if err != nil || seen[sha] {
			continue
		}
		seen[sha] = true
		var cs []chunkRef
		var res reachabilityResult
		a := s.newAuthReachCtx(&res)
		if rec, _ := s.authLoad(sha); rec != nil {
			a.mark(sha, func(c []chunkRef) { cs = append(cs, c...) }, map[[32]byte]bool{}, 1)
		} else {
			cs = lm.man.Chunks
		}
		uniq := map[string]bool{}
		for _, c := range cs {
			if !uniq[c.SHA256] {
				uniq[c.SHA256] = true
				deps[c.SHA256]++
			}
		}
	}
	var tot int
	for _, n := range deps {
		maxN = max(maxN, n)
		tot += n
	}
	if len(deps) > 0 {
		mean = float64(tot) / float64(len(deps))
	}
	return maxN, mean, len(deps)
}

func econCDCAmp(man manifestV1, off, n int64) int64 {
	var pos, fetched int64
	for _, c := range man.Chunks {
		if pos+c.Length > off && pos < off+n {
			fetched += c.Length
		}
		pos += c.Length
	}
	return fetched
}

type econReadStats struct {
	WholeMBps         float64
	RangeUs, RangeAmp map[string]float64
	ConcurrentMBps    float64
	Exact             bool
}

func econReads(t *testing.T, dir string, cacheMiB int64, cold bool) econReadStats {
	t.Helper()
	if cold {
		dropCaches()
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetAuthorityCache(cacheMiB << 20)
	var mans []manifestV1
	for _, lm := range s.liveManifests() {
		mans = append(mans, lm.man)
	}
	sort.Slice(mans, func(i, j int) bool { return mans[i].ManifestUUID < mans[j].ManifestUUID })
	st := econReadStats{RangeUs: map[string]float64{}, RangeAmp: map[string]float64{}, Exact: true}
	var total int64
	t0 := time.Now()
	for _, m := range mans {
		rd := s.openObjectReader(m, byteRange{0, m.TotalLength - 1})
		h := sha256.New()
		for {
			b, err := rd.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("whole read: %v", err)
			}
			h.Write(b)
		}
		rd.close()
		total += m.TotalLength
		if fmt.Sprintf("%x", h.Sum(nil)) != m.ObjectSHA256 {
			st.Exact = false
		}
	}
	st.WholeMBps = float64(total) / (1 << 20) / time.Since(t0).Seconds()
	r := rand.New(rand.NewPCG(9, 9))
	for _, sz := range []int64{4 << 10, 64 << 10, 1 << 20} {
		const N = 80
		var dur time.Duration
		var amp float64
		for i := 0; i < N; i++ {
			m := mans[r.IntN(len(mans))]
			if m.TotalLength <= sz {
				continue
			}
			off := r.Int64N(m.TotalLength - sz)
			t1 := time.Now()
			rd := s.openObjectReader(m, byteRange{off, off + sz - 1})
			var got int64
			for {
				b, err := rd.next()
				if err != nil {
					break
				}
				got += int64(len(b))
			}
			rd.close()
			dur += time.Since(t1)
			if got != sz {
				st.Exact = false
			}
			if f, ok, _ := s.AuthorityReadCost(m, off, sz); ok {
				amp += float64(f) / float64(sz)
			} else {
				amp += float64(econCDCAmp(m, off, sz)) / float64(sz)
			}
		}
		name := fmt.Sprintf("%dK", sz>>10)
		st.RangeUs[name] = float64(dur.Microseconds()) / N
		st.RangeAmp[name] = amp / N
	}
	// four concurrent whole-object readers
	done := make(chan int64, 4)
	t0 = time.Now()
	for g := 0; g < 4; g++ {
		go func(g int) {
			var n int64
			for i := range mans {
				m := mans[(i+g)%len(mans)]
				rd := s.openObjectReader(m, byteRange{0, m.TotalLength - 1})
				for {
					b, err := rd.next()
					if err != nil {
						break
					}
					n += int64(len(b))
				}
				rd.close()
			}
			done <- n
		}(g)
	}
	var cn int64
	for g := 0; g < 4; g++ {
		cn += <-done
	}
	st.ConcurrentMBps = float64(cn) / (1 << 20) / time.Since(t0).Seconds()
	return st
}

func TestB2Economics(t *testing.T) {
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("research harness: set ZEROS3_B2_CORPUS")
	}
	for _, fam := range econFamilies(root) {
		t.Run(fam.name, func(t *testing.T) {
			if len(fam.files) == 0 {
				t.Skip("no inputs")
			}
			base := t.TempDir()
			row := func(scn string, dir string, logical int64, extra econRow) {
				r := econRow{"family": fam.name, "scenario": scn, "logical": logical, "phys": dirBytes(t, dir), "breakdown": econBreakdown(t, dir)}
				if st, err := OpenStoreShared(dir); err == nil {
					if sr, err := st.computeStats(statsScope{}); err == nil {
						r["packs"] = map[string]int64{"raw_recs": int64(sr.PackedRawRecords), "deflate_recs": int64(sr.PackedCompressedRecs), "logical": sr.PackedLogicalBytes, "stored": sr.PackedStoredBytes, "loose": int64(sr.LooseChunkCount)}
					}
					st.Close()
				}
				for k, v := range extra {
					r[k] = v
				}
				econEmit(t, r)
			}
			// 1. CDC authoritative only (baseline).
			cdcDir := filepath.Join(base, "cdc")
			logical, putSec, putRSS := econBuild(t, cdcDir, fam.files, repModeOff)
			econCompact(t, cdcDir)
			row("cdc", cdcDir, logical, econRow{"put_s": putSec, "put_rss_mib": putRSS, "note": fam.note})
			// 2. CDC plus additive representations (existing production behaviour).
			addDir := filepath.Join(base, "additive")
			_, aput, arss := econBuild(t, addDir, fam.files, repModePrefer)
			econCompact(t, addDir)
			row("cdc+additive-rep", addDir, logical, econRow{"put_s": aput, "put_rss_mib": arss})
			// 3..5. Authority variants, each migrated from a copy of the CDC baseline.
			type variant struct {
				name string
				pol  AuthPolicy
			}
			for _, v := range []variant{
				{"auth-archive-oldest", AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowRep: true, AllowDelta: true}},
				{"auth-mixed-oldest", AuthPolicy{Strategy: "mixed", Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowRep: true, AllowDelta: true}},
				{"auth-archive-newest", AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, AllowRep: true, AllowDelta: true}},
				{"auth-archive-depth4", AuthPolicy{Order: "oldest", MaxDepth: 4, MaxBases: 4, Candidates: 8, AllowRep: true, AllowDelta: true}},
				{"auth-delta-only", AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowDelta: true}},
				{"auth-delta-frame16k", AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowDelta: true, FrameSpan: 16 << 10}},
				{"auth-delta-frame1m", AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowDelta: true, FrameSpan: 1 << 20}},
				{"auth-rep-only", AuthPolicy{Order: "oldest", AllowRep: true}},
				{"auth-rep-forced", AuthPolicy{Order: "oldest", AllowRep: true, MinSaving: -100}}, // diagnostic: admit every representation the builder can make
				{"auth-archive-skip", AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowRep: true, AllowDelta: true, RepSkipIfDeltaAtMost: 0.6}},
				{"auth-hot-hybrid", AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, MaxReadAmp: 4, MinReadRatio: 0.5, AllowRep: true, AllowDelta: true}},
			} {
				only := os.Getenv("ZEROS3_B2_VARIANTS")
				if only != "" && !strings.Contains(","+only+",", ","+v.name+",") {
					continue
				}
				if strings.Contains(v.name, "frame") && !strings.Contains(","+only+",", ","+v.name+",") {
					continue // optional range-granularity experiments run only when named
				}
				if only != "" && !strings.Contains(","+only+",", ","+v.name+",") {
					continue
				}
				dir := filepath.Join(base, v.name)
				if err := econCopyTree(cdcDir, dir); err != nil {
					t.Fatal(err)
				}
				t0 := time.Now()
				rep, rss, peak := econMigrate(t, dir, v.pol)
				migSec := time.Since(t0).Seconds()
				forms := map[string]int{}
				reasons := map[string]int{}
				for _, p := range rep.Plans {
					forms[strings.TrimSuffix(p.Form, "?")]++
					if p.Reason != "" && p.Reason != "plan only" {
						reasons[p.Reason]++
					}
				}
				plans := []econRow{}
				for _, p := range rep.Plans {
					plans = append(plans, econRow{"sha": p.SHA[:8], "size": p.Size, "form": p.Form, "cdc_unshared": p.CDCBytes, "added": p.AddedBytes, "reason": p.Reason})
				}
				gcSec, repackSec := econReclaim(t, dir)
				row(v.name, dir, logical, econRow{"plans": plans, "migrate_s": migSec, "migrate_rss_mib": rss, "peak_during_migration": peak, "forms": forms, "reasons": reasons, "gc_s": gcSec, "repack_s": repackSec,
					"admitted": rep.Admitted, "objects": rep.Objects})
				// Recovery/rebuild: fresh process-equivalent reopen, reachability + deep verify.
				s, _ := OpenStore(dir)
				t1 := time.Now()
				rr, _ := s.computeReachability(false)
				reach := time.Since(t1).Seconds()
				t1 = time.Now()
				vr, _ := s.Verify(true)
				verify := time.Since(t1).Seconds()
				bmax, bmean, bch := econBlast(s)
				s.Close()
				econEmit(t, econRow{"family": fam.name, "scenario": v.name, "recovery": true, "reachability_s": reach, "reach_ok": rr.OK(), "deep_verify_s": verify, "verify_ok": vr.OK(), "bodies": rr.AuthPins + rr.AuthReps + rr.AuthDeltas, "blast_max": bmax, "blast_mean": bmean, "chunks": bch})
				cold := econReads(t, dir, 0, true)
				econEmit(t, econRow{"family": fam.name, "scenario": v.name, "reads": "cold", "stats": cold})
				warm := econReads(t, dir, 256, false)
				econEmit(t, econRow{"family": fam.name, "scenario": v.name, "reads": "warm-cache256MiB", "stats": warm})
				if !cold.Exact || !warm.Exact {
					t.Fatalf("%s: inexact reads", v.name)
				}
			}
			{
				s, _ := OpenStore(cdcDir)
				bmax, bmean, bch := econBlast(s)
				s.Close()
				econEmit(t, econRow{"family": fam.name, "scenario": "cdc", "recovery": true, "blast_max": bmax, "blast_mean": bmean, "chunks": bch})
			}
			econEmit(t, econRow{"family": fam.name, "scenario": "cdc", "reads": "cold", "stats": econReads(t, cdcDir, 0, true)})
			econEmit(t, econRow{"family": fam.name, "scenario": "cdc", "reads": "warm", "stats": econReads(t, cdcDir, 0, false)})
		})
	}
}

func econCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if filepath.Base(p) == "LOCK" {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(filepath.Join(dst, rel))
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// TestB2RepBreakdown is the component-by-component physical account of a representation-authority
// run (ZEROS3_B2_CORPUS, family ZEROS3_B2_FAMILY, default parquet-z331-8): per body the components it
// owns, how many are new to the store, what the packs hold for them (raw/deflate, stored vs logical,
// duplicated copies) and what the CDC side still keeps. One JSON object on stdout/ZEROS3_B2_OUT.
func TestB2RepBreakdown(t *testing.T) {
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("research harness: set ZEROS3_B2_CORPUS")
	}
	famName := os.Getenv("ZEROS3_B2_FAMILY")
	if famName == "" {
		famName = "parquet-z331-8"
	}
	var fam econFamily
	for _, f := range econFamilies(root) {
		if f.name == famName {
			fam = f
		}
	}
	if len(fam.files) == 0 {
		t.Skip("no inputs")
	}
	dir := t.TempDir()
	logical, _, _ := econBuild(t, dir, fam.files, repModeOff)
	econCompact(t, dir)
	cdcPhys := dirBytes(t, dir)
	econMigrate(t, dir, AuthPolicy{Order: "oldest", AllowRep: true})
	econReclaim(t, dir)
	// Independent fresh-process reconstruction: a separate server process serves every object and a
	// separate verify process deep-checks the store; each served body is hashed against its source file.
	{
		bin := buildZeros3Binary(t)
		g1MustCLI(t, bin, "verify", "-store", dir, "-deep")
		srv := g1Serve(t, bin, dir)
		for _, f := range fam.files {
			key := strings.ReplaceAll(strings.TrimPrefix(f, filepath.Dir(filepath.Dir(f))+"/"), "/", "_")
			want, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			code, got, _ := g1Get(t, srv.base, key, nil)
			if code != 200 || sha256.Sum256(got) != sha256.Sum256(want) {
				t.Fatalf("fresh process: %s status %d, exact=%v", key, code, sha256.Sum256(got) == sha256.Sum256(want))
			}
		}
		srv.stop()
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	type bodyRow struct {
		Key                           string
		Form                          string
		Components                    int
		LogicalBytes, NewLogicalBytes int64
		NewStoredBytes                int64
		RecordFileBytes               int64
		Reused                        int
	}
	var rows []bodyRow
	seen := map[[32]byte]bool{}
	var sumLogical, unionLogical, unionStored, dupRecords, rawRecs, deflRecs, framing int64
	mans := s.liveManifests()
	sort.Slice(mans, func(i, j int) bool { return mans[i].key < mans[j].key })
	for _, lm := range mans {
		sha, _ := decodeHexSHA256(lm.man.ObjectSHA256)
		rec, _ := s.authLoad(sha)
		if rec == nil {
			rows = append(rows, bodyRow{Key: lm.key, Form: "cdc"})
			continue
		}
		cs, err := s.authRecordChunks(rec)
		if err != nil {
			t.Fatal(err)
		}
		r := bodyRow{Key: lm.key, Form: map[byte]string{authFormPin: "pin", authFormRep: "rep", authFormDelta: "delta"}[rec.Form], Components: len(cs)}
		if fi, err := os.Stat(s.authPath(sha)); err == nil {
			r.RecordFileBytes = fi.Size()
		}
		for _, c := range cs {
			sum, _ := decodeHexSHA256(c.SHA256)
			r.LogicalBytes += c.Length
			sumLogical += c.Length
			if seen[sum] {
				r.Reused++
				continue
			}
			seen[sum] = true
			r.NewLogicalBytes += c.Length
			unionLogical += c.Length
			locs := s.packLocs(sum)
			if len(locs) > 1 {
				dupRecords += int64(len(locs) - 1)
			}
			if len(locs) > 0 {
				r.NewStoredBytes += int64(locs[0].stored)
				unionStored += int64(locs[0].stored)
				framing += packRecordBytes
				if locs[0].codec == packCodecRaw {
					rawRecs++
				} else {
					deflRecs++
				}
			}
		}
		rows = append(rows, r)
	}
	// CDC chunks still on disk that no body owns as a component: retained CDC chunks of the group.
	var stillCDC int64
	for _, lm := range mans {
		for _, c := range lm.man.Chunks {
			sum, _ := decodeHexSHA256(c.SHA256)
			if !seen[sum] && len(s.packLocs(sum)) > 0 {
				stillCDC += c.Length
				seen[sum] = true
			}
		}
	}
	econEmit(t, econRow{"family": fam.name, "breakdown_of": "rep-only", "logical": logical, "cdc_phys": cdcPhys, "final_phys": dirBytes(t, dir),
		"bodies": rows, "sum_component_logical": sumLogical, "union_component_logical": unionLogical, "reuse_factor": float64(sumLogical) / float64(max(unionLogical, 1)),
		"union_stored": unionStored, "stored_over_logical": float64(unionStored) / float64(max(unionLogical, 1)), "duplicate_records": dupRecords,
		"raw_records": rawRecs, "deflate_records": deflRecs, "record_framing_bytes": framing, "retained_cdc_logical": stillCDC, "pack_breakdown": econBreakdown(t, dir)})
}

// TestP8Migration measures authority migration only (the store is built once per
// family; each variant is migrated from a copy and read back by SHA-256).
//
//	ZEROS3_B2_CORPUS=DIR ZEROS3_P8_FAMILY=tar ZEROS3_P8_VARIANTS=auth-delta-only ZEROS3_P8_REPS=3 \
//	  ZEROS3_B2_OUT=out.jsonl go test -run TestP8Migration -count=1 -cpuprofile cpu.pprof .
func TestP8Migration(t *testing.T) {
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("research harness: set ZEROS3_B2_CORPUS")
	}
	reps, _ := strconv.Atoi(os.Getenv("ZEROS3_P8_REPS"))
	reps = max(reps, 1)
	variants := map[string]AuthPolicy{
		"delta":   {Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowDelta: true},
		"archive": {Order: "oldest", MaxDepth: 2, MaxBases: 2, AllowRep: true, AllowDelta: true},
		"rep":     {Order: "oldest", AllowRep: true},
	}
	for _, fam := range econFamilies(root) {
		if f := os.Getenv("ZEROS3_P8_FAMILY"); f != "" && !strings.Contains(","+f+",", ","+fam.name+",") {
			continue
		}
		t.Run(fam.name, func(t *testing.T) {
			base := t.TempDir()
			cdcDir := filepath.Join(base, "cdc")
			logical, _, _ := econBuild(t, cdcDir, fam.files, repModeOff)
			econCompact(t, cdcDir)
			cdcPhys := dirBytes(t, cdcDir)
			for _, vn := range strings.Split(os.Getenv("ZEROS3_P8_VARIANTS"), ",") {
				pol, ok := variants[vn]
				if !ok {
					continue
				}
				for r := 0; r < reps; r++ {
					dir := filepath.Join(base, fmt.Sprintf("%s-%d", vn, r))
					if err := econCopyTree(cdcDir, dir); err != nil {
						t.Fatal(err)
					}
					runtime.GC()
					var m0, m1 runtime.MemStats
					runtime.ReadMemStats(&m0)
					cpu0, io0 := procCPUSeconds(), procReadBytes()
					srcCalls0, srcBytes0 := authSrcStats()
					var pf *os.File
					if pd := os.Getenv("ZEROS3_P8_PROF"); pd != "" && r == 0 {
						pf, _ = os.Create(filepath.Join(pd, fmt.Sprintf("cpu-%s-%s.pprof", fam.name, vn)))
						pprof.StartCPUProfile(pf)
					}
					t0 := time.Now()
					rep, rss, _ := econMigrate(t, dir, pol)
					wall := time.Since(t0).Seconds()
					if pf != nil {
						pprof.StopCPUProfile()
						pf.Close()
					}
					cpu1, io1 := procCPUSeconds(), procReadBytes()
					srcCalls1, srcBytes1 := authSrcStats()
					runtime.ReadMemStats(&m1)
					forms := map[string]int{}
					for _, p := range rep.Plans {
						forms[strings.TrimSuffix(p.Form, "?")]++
					}
					gcSec, repackSec := econReclaim(t, dir)
					phys := dirBytes(t, dir)
					// independent read-back of every live object by SHA-256 in a fresh store handle
					s, err := OpenStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					exact := true
					for _, lm := range s.liveManifests() {
						rd := s.openObjectReader(lm.man, byteRange{0, lm.man.TotalLength - 1})
						h := sha256.New()
						for {
							b, err := rd.next()
							if err != nil {
								break
							}
							h.Write(b)
						}
						rd.close()
						if fmt.Sprintf("%x", h.Sum(nil)) != lm.man.ObjectSHA256 {
							exact = false
						}
					}
					s.Close()
					econEmit(t, econRow{"p8": "migrate", "family": fam.name, "variant": vn, "rep": r, "logical": logical, "cdc_phys": cdcPhys,
						"wall_s": wall, "cpu_s": cpu1 - cpu0, "rss_mib": rss, "alloc_mib": float64(m1.TotalAlloc-m0.TotalAlloc) / (1 << 20),
						"mallocs": m1.Mallocs - m0.Mallocs, "read_mib": float64(io1-io0) / (1 << 20), "src_reads": srcCalls1 - srcCalls0, "src_mib": float64(srcBytes1-srcBytes0) / (1 << 20),
						"mig_s": rep.Seconds, "cache_hits": rep.CacheHits, "cache_misses": rep.CacheMisses, "decoded_mib": float64(rep.DecodedBytes) / (1 << 20), "added": rep.AddedBytes, "admitted": rep.Admitted, "objects": rep.Objects, "forms": forms, "phys_after": phys, "gc_s": gcSec, "repack_s": repackSec, "exact": exact})
					if !exact {
						t.Fatalf("%s/%s: inexact read-back", fam.name, vn)
					}
					os.RemoveAll(dir)
				}
			}
		})
	}
}

func procCPUSeconds() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6
}

func procReadBytes() int64 {
	b, _ := os.ReadFile("/proc/self/io")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "rchar:") {
			n, _ := strconv.ParseInt(strings.TrimSpace(l[6:]), 10, 64)
			return n
		}
	}
	return 0
}
