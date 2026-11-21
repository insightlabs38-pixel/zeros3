//go:build cxrangeexperiment && b1rangeexperiment

package main

// B2 seekable codec replay experiment (testing-harnesses/next/b2).
//
//	ZEROS3_B2_CORPUS  corpus root holding gz/coreutils-9.7.l{6,9}.tar.gz and zstd/coreutils-9.7.l{3,19}.tar.zst
//	ZEROS3_CX_INPUTS  optional comma-separated compressed inputs replacing the Coreutils set
//	ZEROS3_CX_DIR     where built fixtures persist (one directory per family and lane)
//	ZEROS3_CX_OUT     JSONL rows (appended); ZEROS3_CX_LABEL tags them
//	ZEROS3_B1_COLD=1  drop the page cache before every measured read (needs root)

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var cxLanes = []string{"cdc", "replay", "forced"}

func cxInputs(t *testing.T) (string, []string) {
	if v := os.Getenv("ZEROS3_CX_INPUTS"); v != "" {
		h := sha256.Sum256([]byte(v))
		return "custom-" + hex.EncodeToString(h[:4]), strings.Split(v, ",")
	}
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("set ZEROS3_B2_CORPUS or ZEROS3_CX_INPUTS")
	}
	var in []string
	for _, f := range []string{"gz/coreutils-9.7.l6.tar.gz", "gz/coreutils-9.7.l9.tar.gz", "zstd/coreutils-9.7.l3.tar.zst", "zstd/coreutils-9.7.l19.tar.zst"} {
		in = append(in, filepath.Join(root, f))
	}
	return "coreutils", in
}

func cxDir(t *testing.T) string {
	d := os.Getenv("ZEROS3_CX_DIR")
	if d == "" {
		t.Skip("set ZEROS3_CX_DIR")
	}
	return d
}

func cxMigrate(t *testing.T, dir string, forced bool) AuthReport {
	lock, err := acquireStoreLock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// authority migrate -archive -codec-replay -no-delta, as the C5 replay lane
	pol := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, AllowRep: true, CodecReplay: true, Workers: 2, Apply: true}
	if forced { // admit every exact representation regardless of its storage saving (read-cost measurement only)
		pol.Strategy, pol.MinSaving = "forced", -10
	}
	rep, err := s.AuthorityMigrate(pol)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestCXFixtures builds every missing lane: put, (migrate), gc, repack, compact, gc.
func TestCXFixtures(t *testing.T) {
	family, inputs := cxInputs(t)
	root := filepath.Join(cxDir(t), family)
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, lane := range cxLanes {
		dir := filepath.Join(root, lane)
		if _, err := os.Stat(filepath.Join(dir, ".cx-ready")); err == nil || lane == "forced" && family == "coreutils" {
			continue
		}
		os.RemoveAll(dir)
		t0, cpu := time.Now(), procCPUSeconds()
		b1Put(t, dir, inputs)
		var rep *AuthReport
		if lane != "cdc" {
			r := cxMigrate(t, dir, lane == "forced")
			rep = &r
		}
		econReclaim(t, dir)
		alloc, apparent := allocatedBytes(dir)
		meta := map[string]any{"family": family, "lane": lane, "allocated": alloc, "apparent": apparent,
			"build_s": time.Since(t0).Seconds(), "build_cpu_s": procCPUSeconds() - cpu, "report": rep}
		b, _ := json.MarshalIndent(meta, "", " ")
		if err := os.WriteFile(filepath.Join(dir, ".cx-ready"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s/%s allocated=%d apparent=%d", family, lane, alloc, apparent)
	}
}

type cxRow struct {
	Family, Lane, Key, Mode, Label string
	Form                           string // auth record form: cdc, rep
	Off, Len                       int64
	WallUS, CPUUS                  int64
	CASBytes, Chunks               int64 // logical CAS bytes and chunks the representation sources read
	Codec                          repCodecTrace
	AllocBytes                     uint64
	HeapPeak                       uint64
}

// cxGet reads [off, off+n) through the production GET path and accounts for the representation sources it used.
func cxGet(t *testing.T, s *Store, key string, off, n int64, want []byte, cold bool) cxRow {
	var mu sync.Mutex
	var row cxRow
	repSourceObserver = func(st repReadStats, cw repCodecTrace) {
		mu.Lock()
		row.CASBytes += st.LogicalBytes
		row.Chunks += st.Chunks
		row.Codec.Restarts += cw.Restarts
		row.Codec.Units += cw.Units
		row.Codec.UnitBytes += cw.UnitBytes
		row.Codec.PlainBytes += cw.PlainBytes
		row.Codec.OutBytes += cw.OutBytes
		mu.Unlock()
	}
	defer func() { repSourceObserver = nil }()
	if cold {
		s.ccache.purge()
		s.repCache().purge()
		b1DropCaches()
	}
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	stop := make(chan struct{})
	var peak uint64
	var pw sync.WaitGroup
	pw.Add(1)
	go func() {
		defer pw.Done()
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&ms)
				peak = max(peak, ms.HeapInuse)
			}
		}
	}()
	cpu0 := procCPUSeconds()
	t0 := time.Now()
	_, _, got, err := s.GetObjectRange("b", key, byteRange{off, off + n - 1})
	row.WallUS = time.Since(t0).Microseconds()
	row.CPUUS = int64((procCPUSeconds() - cpu0) * 1e6)
	close(stop)
	pw.Wait()
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatalf("%s %d+%d: %v", key, off, n, err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want[off:off+n]) {
		t.Fatalf("%s %d+%d: digest differs from the original bytes", key, off, n)
	}
	mu.Lock()
	defer mu.Unlock()
	row.Off, row.Len, row.AllocBytes, row.HeapPeak = off, n, m1.TotalAlloc-m0.TotalAlloc, max(peak, m1.HeapInuse)
	return row
}

// TestCXRangeBench measures every built lane with the reader of the current tree.
func TestCXRangeBench(t *testing.T) {
	family, inputs := cxInputs(t)
	root := filepath.Join(cxDir(t), family)
	label := os.Getenv("ZEROS3_CX_LABEL")
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	var rows []cxRow
	emit := func(r cxRow) {
		r.Label = label
		rows = append(rows, r)
		if out := os.Getenv("ZEROS3_CX_OUT"); out != "" {
			if f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				b, _ := json.Marshal(r)
				f.Write(append(b, '\n'))
				f.Close()
			}
		}
	}
	only := os.Getenv("ZEROS3_CX_LANES")
	for _, lane := range cxLanes {
		if only != "" && !slices.Contains(strings.Split(only, ","), lane) {
			continue
		}
		src := filepath.Join(root, lane)
		if _, err := os.Stat(filepath.Join(src, ".cx-ready")); err != nil {
			t.Logf("skip %s: not built", lane)
			continue
		}
		dir := filepath.Join(t.TempDir(), lane)
		if err := econCopyTree(src, dir); err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.SetAuthorityCache(0)
		for _, in := range inputs {
			key := filepath.Base(in)
			want, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			size := int64(len(want))
			_, man, err := s.HeadObject("b", key)
			if err != nil {
				t.Fatal(err)
			}
			form := "cdc"
			if sha, _ := decodeHexSHA256(man.ObjectSHA256); sha != [32]byte{} {
				if rec, _ := s.authLoad(sha); rec != nil {
					form = map[byte]string{authFormPin: "pin", authFormRep: "rep", authFormDelta: "delta"}[rec.Form]
				}
			}
			if k := os.Getenv("ZEROS3_CX_KEYS"); k != "" && !slices.Contains(strings.Split(k, ","), key) {
				continue
			}
			measure := func(mode string, off, n int64, cold bool) {
				if m := os.Getenv("ZEROS3_CX_MODES"); m != "" && !slices.Contains(strings.Split(m, ","), mode) {
					return
				}
				r := cxGet(t, s, key, off, n, want, cold)
				r.Family, r.Lane, r.Key, r.Mode, r.Form = family, lane, key, mode, form
				emit(r)
			}
			rng := rand.New(rand.NewPCG(82, 202))
			offs := []int64{size / 2}
			for range 7 {
				offs = append(offs, rng.Int64N(size-4096))
			}
			for _, off := range offs {
				measure("cold-4k", off, 4096, true)
			}
			measure("cold-1b-end", size-1, 1, true)
			measure("cold-4k-head", 0, 4096, true)
			for _, off := range offs[:2] {
				measure("repeat-4k", off, 4096, false)
			}
			measure("cold-1m", offs[1]/2, min(1<<20, size-offs[1]/2), true)
			measure("cold-16m", size/8, min(16<<20, size-size/8), true)
			measure("whole", 0, size, true)
		}
		s.Close()
	}
	cxSummarize(t, rows)
}

func cxSummarize(t *testing.T, rows []cxRow) {
	type agg struct{ wall, cpu, cas, plain, units []int64 }
	m := map[string]*agg{}
	var order []string
	for _, r := range rows {
		k := r.Lane + " " + r.Key + " " + r.Form + " " + r.Mode
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
			order = append(order, k)
		}
		a.wall = append(a.wall, r.WallUS)
		a.cpu = append(a.cpu, r.CPUUS)
		a.cas = append(a.cas, r.CASBytes)
		a.plain = append(a.plain, r.Codec.PlainBytes)
		a.units = append(a.units, r.Codec.Units)
	}
	med := func(v []int64) int64 {
		v = slices.Clone(v)
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		return v[len(v)/2]
	}
	for _, k := range order {
		a := m[k]
		t.Logf("%-52s n=%d wall med=%9dus cpu med=%9dus cas med=%10d plain med=%10d units med=%5d", k, len(a.wall), med(a.wall), med(a.cpu), med(a.cas), med(a.plain), med(a.units))
	}
}

var _ = bytes.Equal
