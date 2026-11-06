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
	"sort"
	"strconv"
	"strings"
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
		{"dex", append(g("dex/*/classes.dex"), g("dex/*/classes2.dex")...), "3 real F-Droid client releases x 2 dex files"},
		{"parquet", g("parquet/c*.parquet"), "4 pyarrow producer states (synthetic workload)"},
		{"sqlite", g("sqlite/v*.db"), "4 states of one SQLite database (synthetic edits)"},
		{"git", g("git/h*.pack"), "3 git pack exports of this repository's history"},
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

func econCompact(t *testing.T, dir string) {
	t.Helper()
	if _, err := compactStore(dir, authCompactOpts()); err != nil {
		t.Fatal(err)
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
	if _, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err != nil {
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
				{"auth-hot-hybrid", AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, MaxReadAmp: 2, AllowRep: true, AllowDelta: true}},
			} {
				only := os.Getenv("ZEROS3_B2_VARIANTS")
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
				for _, p := range rep.Plans {
					forms[strings.TrimSuffix(p.Form, "?")]++
				}
				gcSec, repackSec := econReclaim(t, dir)
				row(v.name, dir, logical, econRow{"migrate_s": migSec, "migrate_rss_mib": rss, "peak_during_migration": peak, "forms": forms, "gc_s": gcSec, "repack_s": repackSec,
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
