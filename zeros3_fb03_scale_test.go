package main

// FB-03 M-A8 G-SCALE harness on the real locator. Every test here is gated by
// an environment variable and skipped by default; results are JSON lines
// appended to $FB03_OUT (and logged). Digests are z206Sum(i), so record i of
// a generated store is known without keeping a copy.
//
//	FB03_LOOKUP=1000000,4000000,16000000          X2 on the real locator
//	FB03_OPEN_DIR=d FB03_N=n [FB03_PER=1024]      open memory, one process per run
//	    [FB03_LOADER=lean|ref] [FB03_GOGC=400]
//	FB03_GROWTH=n [FB03_M=n FB03_K=2 FB03_SEG=8192]  segment commits on a Store
//	FB03_RETIRE=n [FB03_CYCLES=10 FB03_FRAC=0.1]     online retirement on a Store
//
// testing-harnesses/foundational/FB-03/ma8_scale.sh runs the set.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func fb03Emit(t *testing.T, v map[string]any) {
	t.Helper()
	v["go"] = runtime.Version()
	v["gomaxprocs"] = runtime.GOMAXPROCS(0)
	v["max_levels_cfg"] = locMaxLevels
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
	if p := os.Getenv("FB03_OUT"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func fb03Env(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func fb03EnvF(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

// fb03Status returns a /proc/self/status field in bytes.
func fb03Status(field string) int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, field+":") {
			if f := strings.Fields(line[len(field)+1:]); len(f) > 0 {
				kb, _ := strconv.ParseInt(f[0], 10, 64)
				return kb << 10
			}
		}
	}
	return -1
}

// fb03Peak samples heap-object bytes every millisecond without stopping the world.
type fb03Peak struct {
	stop, done chan struct{}
	heap       uint64
}

func fb03StartPeak() *fb03Peak {
	p := &fb03Peak{stop: make(chan struct{}), done: make(chan struct{})}
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	go func() {
		defer close(p.done)
		tk := time.NewTicker(time.Millisecond)
		defer tk.Stop()
		for {
			metrics.Read(s)
			p.heap = max(p.heap, s[0].Value.Uint64())
			select {
			case <-p.stop:
				return
			case <-tk.C:
			}
		}
	}()
	return p
}

func (p *fb03Peak) end() { close(p.stop); <-p.done }

func fb03Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

// fb03Pct is the q quantile of d in microseconds.
func fb03Pct(d []time.Duration, q float64) float64 {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	return float64(s[int(q*float64(len(s)-1))].Nanoseconds()) / 1000
}

// fb03Entries are records [lo, hi) as one pack holds them: 1-byte payloads.
func fb03Entries(lo, hi int) []packEntry {
	es := make([]packEntry, 0, hi-lo)
	off := uint64(packHeaderSize + packRecordHeaderSize)
	for i := lo; i < hi; i++ {
		es = append(es, packEntry{sha: z206Sum(uint64(i)), off: off, stored: 1, logical: 1})
		off += 1 + packRecordHeaderSize
	}
	return es
}

// fb03Synth is n records in packs of per records (record i in pack i/per).
func fb03Synth(n, per int) ([]packInfo, []locRec) {
	var packs []packInfo
	recs := make([]locRec, 0, n)
	for p := 0; p*per < n; p++ {
		lo, hi := p*per, min((p+1)*per, n)
		packs = append(packs, packInfo{id: locTestPackID(p), tier: tierHot, records: hi - lo})
		for _, e := range fb03Entries(lo, hi) {
			recs = append(recs, mkLocRec(&e, uint32(p), tierHot))
		}
	}
	return packs, recs
}

func fb03NoClash(prim, rep *locRec) { panic("fb03: unexpected clash") }

// fb03Bytes is the resident size of st's levels and their share per record.
func fb03Bytes(st *packState) (structBytes, filterBytes int) {
	for _, ix := range st.levels {
		structBytes += (len(ix.recs)+len(ix.dups))*int(unsafe.Sizeof(locRec{})) + len(ix.tbl)*4
		filterBytes += ix.filter.bytes()
	}
	return structBytes + filterBytes, filterBytes
}

func fb03Sizes(st *packState) (sizes []int, filtered int) {
	for _, ix := range st.levels {
		sizes = append(sizes, ix.size())
		if ix.filter != nil {
			filtered++
		}
	}
	return
}

// ---------------------------------------------------------------------------
// X2 on the real locator

// fb03Queries is X2's workload: count lookups, half present and half absent,
// in random order; want[i] is the expected pack index or -1.
func fb03Queries(n, per, count int, seed int64) (sums [][32]byte, want []int32) {
	r := rand.New(rand.NewSource(seed))
	sums, want = make([][32]byte, count), make([]int32, count)
	for i := range sums {
		if i%2 == 0 {
			k := r.Intn(n)
			sums[i], want[i] = z206Sum(uint64(k)), int32(k/per)
		} else {
			sums[i], want[i] = z206Sum(uint64(n)+uint64(r.Int63n(1<<40))), -1
		}
	}
	r.Shuffle(count, func(i, j int) { sums[i], sums[j] = sums[j], sums[i]; want[i], want[j] = want[j], want[i] })
	return
}

// fb03Forged are digests of stored records changed only past byte 16: they
// land in the stored record's table bucket and pass every filter it passes,
// so only the exact comparison can reject them. Half flip one bit of the last
// byte (the sorted neighbour of the stored record), half replace bytes 16..32.
func fb03Forged(n, count int, seed int64) [][32]byte {
	r := rand.New(rand.NewSource(seed))
	out := make([][32]byte, count)
	for i := range out {
		s := z206Sum(uint64(r.Intn(n)))
		if i%2 == 0 {
			s[31] ^= 1 << uint(r.Intn(8))
		} else {
			r.Read(s[16:])
		}
		out[i] = s
	}
	return out
}

func fb03TimeLookup(st *packState, sums [][32]byte, want []int32) (float64, int) {
	bad := 0
	t0 := time.Now()
	for i := range sums {
		loc, ok := st.lookup(sums[i])
		if ok != (want[i] >= 0) || ok && loc.pack != want[i] {
			bad++
		}
	}
	return float64(time.Since(t0).Nanoseconds()) / float64(len(sums)), bad
}

func fb03TimeBest(st *packState, sums [][32]byte, want []int32) (float64, int) {
	bad := 0
	t0 := time.Now()
	for i := range sums {
		r := st.best(&sums[i])
		if (r != nil) != (want[i] >= 0) || r != nil && int32(r.packIdx()) != want[i] {
			bad++
		}
	}
	return float64(time.Since(t0).Nanoseconds()) / float64(len(sums)), bad
}

// fb03TimeMany looks sums up batch digests at a time, as segment ingest does.
func fb03TimeMany(st *packState, sums [][32]byte, want []int32, batch int) (float64, int) {
	bad := 0
	out := make([]*locRec, batch)
	t0 := time.Now()
	for i := 0; i < len(sums); i += batch {
		j := min(i+batch, len(sums))
		st.bestMany(sums[i:j], out)
		for k := i; k < j; k++ {
			r := out[k-i]
			if (r != nil) != (want[k] >= 0) || r != nil && int32(r.packIdx()) != want[k] {
				bad++
			}
		}
	}
	return float64(time.Since(t0).Nanoseconds()) / float64(len(sums)), bad
}

type fb03Cfg struct {
	name string
	st   *packState
}

// fb03LookupStates builds X2's designs on the real locator over the same n
// records: one level without a filter (X2's production baseline) and with
// one (what an open now builds); four unmerged levels without filters and
// with them; and the state the level policy itself builds from packs of per
// records, with every planned background merge applied.
func fb03LookupStates(t *testing.T, n, per int) ([]fb03Cfg, map[string]any) {
	info := map[string]any{}
	packs, recs := fb03Synth(n, per)
	t0 := time.Now()
	one := &packState{packs: packs, levels: []*locIndex{buildLocIndexF(slices.Clone(recs), fb03NoClash, false)}}
	info["one_build_ms"] = time.Since(t0).Seconds() * 1000
	oneF := &packState{packs: packs, levels: []*locIndex{finishLocIndex(&locIndex{recs: one.levels[0].recs, dups: one.levels[0].dups}, true)}}
	four := &packState{packs: packs}
	fourF := &packState{packs: packs}
	q := (len(packs) + 3) / 4
	for k := 0; k < 4; k++ {
		lo, hi := min(k*q*per, n), min((k+1)*q*per, n)
		ix := buildLocIndexF(slices.Clone(recs[lo:hi]), fb03NoClash, false)
		four.levels = append(four.levels, ix)
		fourF.levels = append(fourF.levels, finishLocIndex(&locIndex{recs: ix.recs, dups: ix.dups}, true))
	}
	recs = nil
	t0 = time.Now()
	pol := &packState{}
	settles := 0
	for p := range packs {
		var err error
		if pol, err = pol.withPack(packs[p], fb03Entries(p*per, min((p+1)*per, n))); err != nil {
			t.Fatal(err)
		}
		if pol.needsMerge(nil) {
			pol = pol.settled()
			settles++
		}
	}
	info["policy_build_ms"] = time.Since(t0).Seconds() * 1000
	info["policy_settles"] = settles
	if len(pol.clash) != 0 || len(pol.bad) != 0 || pol.distinct() != n {
		t.Fatalf("policy state: clash=%d bad=%d distinct=%d", len(pol.clash), len(pol.bad), pol.distinct())
	}
	return []fb03Cfg{{"one-level", one}, {"one-filtered", oneF}, {"four-unfiltered", four}, {"four-filtered", fourF}, {"policy", pol}}, info
}

// TestFB03Scale_Lookup is X2 extended to the real locator: per design, the
// median over 5 interleaved rounds of 2M lookups (50/50 present and absent,
// random order) on one core, every answer checked, plus 1,000 forged
// same-prefix digests that must all be rejected.
func TestFB03Scale_Lookup(t *testing.T) {
	sizes := strings.Split(os.Getenv("FB03_LOOKUP"), ",")
	if sizes[0] == "" {
		t.Skip("set FB03_LOOKUP to a comma-separated list of record counts")
	}
	per := fb03Env("FB03_PER", 1024)
	queries := fb03Env("FB03_QUERIES", 2_000_000)
	rounds := fb03Env("FB03_ROUNDS", 5)
	for _, field := range sizes {
		n, err := strconv.Atoi(field)
		if err != nil || n <= 0 {
			t.Fatalf("bad size %q", field)
		}
		cfgs, info := fb03LookupStates(t, n, per)
		mix, mixWant := fb03Queries(n, per, queries, int64(n))
		var pos, neg [][32]byte
		var posWant, negWant []int32
		for i := range mix {
			if mixWant[i] >= 0 {
				pos, posWant = append(pos, mix[i]), append(posWant, mixWant[i])
			} else {
				neg, negWant = append(neg, mix[i]), append(negWant, -1)
			}
		}
		forged := fb03Forged(n, 1000, int64(n)+1)
		forgedWant := make([]int32, len(forged))
		for i := range forgedWant {
			forgedWant[i] = -1
		}
		type acc struct{ mix, neg, pos, best, many8, many64 []float64 }
		res := make([]acc, len(cfgs))
		bad := make([]int, len(cfgs))
		procs := runtime.GOMAXPROCS(1)
		for round := 0; round < rounds; round++ {
			for c := range cfgs {
				cfg := cfgs[(c+round)%len(cfgs)]
				a := &res[(c+round)%len(cfgs)]
				b := &bad[(c+round)%len(cfgs)]
				runtime.GC()
				for _, m := range []struct {
					dst *[]float64
					f   func() (float64, int)
				}{
					{&a.mix, func() (float64, int) { return fb03TimeLookup(cfg.st, mix, mixWant) }},
					{&a.neg, func() (float64, int) { return fb03TimeLookup(cfg.st, neg, negWant) }},
					{&a.pos, func() (float64, int) { return fb03TimeLookup(cfg.st, pos, posWant) }},
					{&a.best, func() (float64, int) { return fb03TimeBest(cfg.st, mix, mixWant) }},
					{&a.many8, func() (float64, int) { return fb03TimeMany(cfg.st, mix, mixWant, locBatch) }},
					{&a.many64, func() (float64, int) { return fb03TimeMany(cfg.st, mix, mixWant, 64) }},
				} {
					ns, nb := m.f()
					*m.dst = append(*m.dst, ns)
					*b += nb
				}
			}
		}
		runtime.GOMAXPROCS(procs)
		oneNeg, oneMix := fb03Median(res[0].neg), fb03Median(res[0].mix)
		for c, cfg := range cfgs {
			accepted := 0
			if _, nb := fb03TimeLookup(cfg.st, forged, forgedWant); nb != 0 {
				accepted += nb
			}
			if _, nb := fb03TimeMany(cfg.st, forged, forgedWant, locBatch); nb != 0 {
				accepted += nb
			}
			passedFilter := 0
			for i := range forged {
				for _, ix := range cfg.st.levels {
					if ix.filter != nil && ix.filter.has(&forged[i]) {
						passedFilter++
						break
					}
				}
			}
			sizesL, filtered := fb03Sizes(cfg.st)
			sb, fb := fb03Bytes(cfg.st)
			a := res[c]
			row := map[string]any{"test": "x2-real-locator", "n": n, "per": per, "design": cfg.name, "queries": queries, "rounds": rounds,
				"levels": len(cfg.st.levels), "level_sizes": sizesL, "filtered_levels": filtered,
				"struct_bytes_per_rec": float64(sb) / float64(n), "filter_bytes_per_rec": float64(fb) / float64(n),
				"mix_ns": fb03Median(a.mix), "neg_ns": fb03Median(a.neg), "pos_ns": fb03Median(a.pos),
				"best_ns": fb03Median(a.best), "bestmany8_ns": fb03Median(a.many8), "bestmany64_ns": fb03Median(a.many64),
				"mix_ns_rounds": a.mix, "neg_ns_rounds": a.neg,
				"neg_over_one_level": fb03Median(a.neg) / oneNeg, "mix_over_one_level": fb03Median(a.mix) / oneMix,
				"bestmany8_speedup": fb03Median(a.best) / fb03Median(a.many8),
				"mismatches":        bad[c], "forged": len(forged), "forged_accepted": accepted, "forged_past_filters": passedFilter}
			if cfg.name == "policy" {
				for k, v := range info {
					row[k] = v
				}
			}
			fb03Emit(t, row)
			if bad[c] != 0 || accepted != 0 {
				t.Errorf("%s at %d: %d wrong answers, %d forged digests accepted", cfg.name, n, bad[c], accepted)
			}
		}
		cfgs = nil
		runtime.GC()
	}
}

// ---------------------------------------------------------------------------
// open memory on generated pack files

// fb03WritePack writes a valid pack v1 file holding records [lo, hi), each
// with a 1-byte raw payload, named by its body hash.
func fb03WritePack(dir string, lo, hi int) error {
	n := hi - lo
	body := make([]byte, 0, packHeaderSize+n*(packRecordHeaderSize+1)+n*packIndexEntrySize+packFooterSize)
	var hdr [packHeaderSize]byte
	putPackHeader(hdr[:])
	body = append(body, hdr[:]...)
	idx := make([]byte, 0, n*packIndexEntrySize)
	var rh [packRecordHeaderSize]byte
	var ie [packIndexEntrySize]byte
	for i := lo; i < hi; i++ {
		e := packEntry{sha: z206Sum(uint64(i)), off: uint64(len(body) + packRecordHeaderSize), stored: 1, logical: 1, codec: packCodecRaw}
		putPackRecordHeader(rh[:], e)
		body = append(body, rh[:]...)
		body = append(body, byte(i))
		putPackIndexEntry(ie[:], e)
		idx = append(idx, ie[:]...)
	}
	indexOff := uint64(len(body))
	body = append(body, idx...)
	bodySHA := sha256.Sum256(body)
	var ft [packFooterSize]byte
	putPackFooter(ft[:], uint64(n), indexOff, bodySHA, crc32.Checksum(idx, packCRC))
	body = append(body, ft[:]...)
	return os.WriteFile(filepath.Join(dir, hex.EncodeToString(bodySHA[:])+packFileSuffix), body, 0o644)
}

// fb03Gen generates (once per root, n and per) a hot packs directory of n
// records in packs of per records.
func fb03Gen(t *testing.T, root string, n, per int) {
	mark := filepath.Join(root, ".fb03gen")
	want := fmt.Sprintf("%d %d", n, per)
	if b, err := os.ReadFile(mark); err == nil && string(b) == want {
		return
	}
	dir := tierPackDir(root, tierHot)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	next := make(chan int, 64)
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range next {
				if err := fb03WritePack(dir, p*per, min((p+1)*per, n)); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}
		}()
	}
	for p := 0; p*per < n; p++ {
		next <- p
	}
	close(next)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if err := os.WriteFile(mark, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFB03Scale_Open opens a generated store's packs with the lean loader
// (or, with FB03_LOADER=ref, the pre-FB-03 loader) under the collector
// setting OpenStore uses, and reports peak memory against the steady locator.
// Run one loader per process: VmHWM is a process high-water mark.
func TestFB03Scale_Open(t *testing.T) {
	root, n := os.Getenv("FB03_OPEN_DIR"), fb03Env("FB03_N", 0)
	if root == "" || n == 0 {
		t.Skip("set FB03_OPEN_DIR and FB03_N")
	}
	per := fb03Env("FB03_PER", 1024)
	fb03Gen(t, root, n, per)
	loader, load := "lean", loadPackState
	if os.Getenv("FB03_LOADER") == "ref" {
		loader, load = "ref", loadPackStateRef
	}
	gogc := fb03Env("FB03_GOGC", openGCPercent)
	base := z206HeapAfterGC()
	debug.FreeOSMemory()
	rss0 := fb03Status("VmRSS")
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	old := debug.SetGCPercent(gogc)
	os.WriteFile("/proc/self/clear_refs", []byte("5"), 0) // reset VmHWM to the current RSS
	pk := fb03StartPeak()
	t0 := time.Now()
	st, err := load(root, storeFormatVersionPacked)
	open := time.Since(t0)
	pk.end()
	hwm := fb03Status("VmHWM")
	debug.SetGCPercent(old)
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.bad) != 0 || len(st.clash) != 0 || st.distinct() != n {
		t.Fatalf("bad=%d clash=%d distinct=%d", len(st.bad), len(st.clash), st.distinct())
	}
	steady := int64(z206HeapAfterGC() - base)
	sizes, _ := fb03Sizes(st)
	sb, _ := fb03Bytes(st)
	row := map[string]any{"test": "locator-open", "loader": loader, "n": n, "per": per, "packs": len(st.packs), "gogc": gogc,
		"workers": maintWorkers(), "open_ms": open.Seconds() * 1000, "ns_per_record": float64(open.Nanoseconds()) / float64(n),
		"alloc_per_rec": float64(m1.TotalAlloc-m0.TotalAlloc) / float64(n), "mallocs": m1.Mallocs - m0.Mallocs, "num_gc": m1.NumGC - m0.NumGC,
		"peak_heap_per_rec": float64(pk.heap-min(pk.heap, base)) / float64(n), "vm_hwm": hwm, "rss_before": rss0,
		"vm_hwm_per_rec": float64(hwm-rss0) / float64(n), "steady_per_rec": float64(steady) / float64(n),
		"hwm_over_steady": float64(hwm-rss0) / float64(steady), "peak_heap_over_steady": float64(pk.heap-min(pk.heap, base)) / float64(steady),
		"levels": sizes, "locator_bytes_per_rec": float64(sb) / float64(n),
		"packinfo_bytes_per_rec": float64(len(st.packs)*int(unsafe.Sizeof(packInfo{}))) / float64(n)}
	if os.Getenv("FB03_NOLOOKUP") != "1" {
		// Packs are numbered in name order at open, so check presence only
		// (TestFB03_LocOpenEquivalence checks every location).
		mix, want := fb03Queries(n, per, 1<<20, 7)
		nb := 0
		t0 := time.Now()
		for i := range mix {
			if _, ok := st.lookup(mix[i]); ok != (want[i] >= 0) {
				nb++
			}
		}
		ns := float64(time.Since(t0).Nanoseconds()) / float64(len(mix))
		row["mix_ns_1t"], row["mismatches"] = ns, nb
		if nb != 0 {
			t.Errorf("%d wrong answers after open", nb)
		}
	}
	fb03Emit(t, row)
	runtime.KeepAlive(st)
}

// ---------------------------------------------------------------------------
// growth and retirement on a Store (publish-time merges plus the background merger)

// fb03LocStore opens an empty store and installs a locator of n records in
// packs of 1024 as one level, which is what an open yields. The returned func
// puts the store's own locator back before it closes.
func fb03LocStore(t *testing.T, n int) (*Store, func()) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orig := s.packSnap()
	packs, recs := fb03Synth(n, 1024)
	st := &packState{packs: packs}
	st.index(recs)
	s.packMu.Lock()
	s.packSt.Store(st)
	s.packMu.Unlock()
	return s, func() {
		s.locMergeWait()
		s.packMu.Lock()
		s.packSt.Store(orig)
		s.packMu.Unlock()
		s.Close()
	}
}

func fb03NegNs(st *packState, total, count int, seed int64) float64 {
	runtime.GC()
	r := rand.New(rand.NewSource(seed))
	sums, want := make([][32]byte, count), make([]int32, count)
	for i := range sums {
		sums[i], want[i] = z206Sum(uint64(total)+uint64(r.Int63n(1<<40))), -1
	}
	ns, bad := fb03TimeLookup(st, sums, want)
	if bad != 0 {
		panic("fb03: an absent digest was found")
	}
	return ns
}

func fb03LocStats(s *Store) map[string]any {
	m := &s.loc.stats
	return map[string]any{"merges": m.Merges.Load(), "discarded": m.Discarded.Load(), "purges": m.Purges.Load(),
		"merge_ms_total": float64(m.MergeNS.Load()) / 1e6, "merge_ms_max": float64(m.MaxMergeNS.Load()) / 1e6,
		"swap_us_max": float64(m.MaxSwapNS.Load()) / 1e3, "max_levels_published": m.MaxLevels.Load(),
		"records_merged_in": m.RecordsIn.Load(), "records_merged_out": m.RecordsOut.Load()}
}

// TestFB03Scale_Growth commits m new records k at a time into segments of
// segRecs records through segPublish (the production publish) on top of an
// n-record locator, with the background merger running.
func TestFB03Scale_Growth(t *testing.T) {
	n := fb03Env("FB03_GROWTH", 0)
	if n == 0 {
		t.Skip("set FB03_GROWTH")
	}
	m, k, segRecs := fb03Env("FB03_M", n), fb03Env("FB03_K", 2), fb03Env("FB03_SEG", 8192)
	base0 := z206HeapAfterGC()
	s, done := fb03LocStore(t, n)
	defer done()
	baseHeap := int64(z206HeapAfterGC() - base0)
	negBase := fb03NegNs(s.packSnap(), n+m, 1<<18, 1)
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	pk := fb03StartPeak()
	var durs []time.Duration
	var slow []map[string]any
	maxSeen, segs, inSeg := 0, 0, segRecs
	var seg *segment
	t0 := time.Now()
	for c := 0; c*k < m; c++ {
		if inSeg >= segRecs {
			seg = &segment{seq: uint64(segs), id: segID(uint64(segs)), path: filepath.Join(t.TempDir(), segID(uint64(segs))+".seg")}
			inSeg, segs = 0, segs+1
		}
		es := make([]packEntry, 0, k)
		for j := 0; j < k && c*k+j < m; j++ {
			off := uint64(packHeaderSize + (inSeg+j)*(packRecordHeaderSize+1) + packRecordHeaderSize)
			es = append(es, packEntry{sha: z206Sum(uint64(n + c*k + j)), off: off, stored: 1, logical: 1})
		}
		before := len(s.packSnap().levels)
		ts := time.Now()
		if err := s.segPublish(seg, es); err != nil {
			t.Fatal(err)
		}
		d := time.Since(ts)
		inSeg += len(es)
		durs = append(durs, d)
		after := len(s.packSnap().levels)
		maxSeen = max(maxSeen, after)
		if d > 20*time.Millisecond {
			slow = append(slow, map[string]any{"commit": c, "ms": d.Seconds() * 1000, "levels_before": before, "levels_after": after})
		}
	}
	total := time.Since(t0)
	negLive := fb03NegNs(s.packSnap(), n+m, 1<<18, 2) // whatever the background had done by then
	s.locMergeWait()
	pk.end()
	runtime.ReadMemStats(&m1)
	st := s.packSnap()
	negEnd := fb03NegNs(st, n+m, 1<<18, 3)
	steady := int64(z206HeapAfterGC() - base0)
	if st.distinct() != n+m {
		t.Fatalf("distinct %d, want %d", st.distinct(), n+m)
	}
	r := rand.New(rand.NewSource(4))
	for q := 0; q < 1<<16; q++ {
		i := r.Intn(n + m)
		if _, ok := st.lookup(z206Sum(uint64(i))); !ok {
			t.Fatalf("record %d not found after growth", i)
		}
	}
	sizes, filtered := fb03Sizes(st)
	_, fb := fb03Bytes(st)
	var sum time.Duration
	for _, d := range durs {
		sum += d
	}
	row := map[string]any{"test": "locator-growth", "n_base": n, "k": k, "m": m, "seg_records": segRecs, "commits": len(durs),
		"total_ms": total.Seconds() * 1000, "ns_per_new_record": float64(sum.Nanoseconds()) / float64(m),
		"commit_us_p50": fb03Pct(durs, 0.5), "commit_us_p90": fb03Pct(durs, 0.9), "commit_us_p99": fb03Pct(durs, 0.99),
		"commit_us_p999": fb03Pct(durs, 0.999), "commit_us_max": fb03Pct(durs, 1), "slow_commits_over_20ms": slow,
		"max_levels_seen": maxSeen, "final_level_sizes": sizes, "final_filtered_levels": filtered,
		"filter_bytes_per_rec": float64(fb) / float64(n+m),
		"alloc_per_new_record": float64(m1.TotalAlloc-m0.TotalAlloc) / float64(m), "num_gc": m1.NumGC - m0.NumGC,
		"base_heap": baseHeap, "peak_heap_objects": pk.heap, "steady_heap": steady, "steady_per_rec": float64(steady) / float64(n+m),
		"neg_ns_base": negBase, "neg_ns_at_end_of_ingest": negLive, "neg_ns_settled": negEnd}
	for k, v := range fb03LocStats(s) {
		row[k] = v
	}
	fb03Emit(t, row)
}

// TestFB03Scale_Retire runs online-maintenance style cycles on a Store: each
// rewrites frac of the packs into new packs (Store.addPack) and retires the
// sources the way the carry swap does (withoutPacks, purge, kick). After the
// background settles, the locator must hold no more than the purge rule
// allows: a level is rewritten once a quarter of it is retired, and every
// merge drops what it copies.
func TestFB03Scale_Retire(t *testing.T) {
	n := fb03Env("FB03_RETIRE", 0)
	if n == 0 {
		t.Skip("set FB03_RETIRE")
	}
	per, cycles, frac := 1024, fb03Env("FB03_CYCLES", 10), fb03EnvF("FB03_FRAC", 0.1)
	base0 := z206HeapAfterGC()
	s, done := fb03LocStore(t, n)
	defer done()
	packs := len(s.packSnap().packs)
	live := make([]int32, packs) // live[g] = pack index holding record group g
	for i := range live {
		live[i] = int32(i)
	}
	var rows []map[string]any
	worst := 0.0
	measure := func(cycle int) {
		s.locMergeWait()
		st := s.packSnap()
		heap := int64(z206HeapAfterGC() - base0)
		recs, gone := 0, 0
		for _, ix := range st.levels {
			recs += ix.size()
			gone += st.goneIn(ix)
		}
		r := rand.New(rand.NewSource(int64(cycle)))
		sums, want := make([][32]byte, 1<<18), make([]int32, 1<<18)
		for i := range sums {
			if i%2 == 0 {
				g := r.Intn(n)
				sums[i], want[i] = z206Sum(uint64(g)), live[g/per]
			} else {
				sums[i], want[i] = z206Sum(uint64(n)+uint64(r.Int63n(1<<40))), -1
			}
		}
		ns, bad := fb03TimeLookup(st, sums, want)
		if bad != 0 {
			t.Fatalf("cycle %d: %d wrong answers", cycle, bad)
		}
		sizes, _ := fb03Sizes(st)
		worst = max(worst, float64(recs)/float64(n))
		rows = append(rows, map[string]any{"cycle": cycle, "heap_per_live": float64(heap) / float64(n), "locator_records": recs,
			"retired_records_left": gone, "records_over_live": float64(recs) / float64(n), "levels": sizes, "packs": len(st.packs), "mix_ns": ns})
	}
	measure(0)
	rewrite := int(frac * float64(packs))
	g := 0
	var cycleDur []time.Duration
	for c := 1; c <= cycles; c++ {
		ts := time.Now()
		var doomed []int32
		for j := 0; j < rewrite; j++ {
			grp := g % packs
			g++
			lo, hi := grp*per, min((grp+1)*per, n)
			if err := s.addPack(packInfo{id: fmt.Sprintf("%064x", 1<<40+c*packs+j), tier: tierHot, records: hi - lo}, fb03Entries(lo, hi)); err != nil {
				t.Fatal(err)
			}
			doomed = append(doomed, live[grp])
			live[grp] = int32(len(s.packSnap().packs) - 1)
		}
		s.packMu.Lock()
		s.packSt.Store(s.packSnap().withoutPacks(doomed))
		s.packMu.Unlock()
		s.loc.purge.Store(true)
		s.locMergeKick()
		cycleDur = append(cycleDur, time.Since(ts))
		measure(c)
	}
	row := map[string]any{"test": "locator-retire", "n": n, "cycles": cycles, "frac": frac, "rows": rows,
		"cycle_ms_max": fb03Pct(cycleDur, 1) / 1000, "worst_records_over_live": worst}
	for k, v := range fb03LocStats(s) {
		row[k] = v
	}
	fb03Emit(t, row)
	if worst > 4.0/3+0.02 {
		t.Errorf("the locator held %.3fx the live records after merges settled", worst)
	}
}

// TestFB03Scale_MergeCost times one background-sized merge: a level of n
// records with one of n/FB03_RATIO newer records (FB03_MERGE=n).
func TestFB03Scale_MergeCost(t *testing.T) {
	n := fb03Env("FB03_MERGE", 0)
	if n == 0 {
		t.Skip("set FB03_MERGE")
	}
	ratio := fb03Env("FB03_RATIO", 16)
	_, recs := fb03Synth(n+n/ratio, 1024)
	big := buildLocIndexF(slices.Clone(recs[:n]), fb03NoClash, false)
	small := buildLocIndexF(slices.Clone(recs[n:]), fb03NoClash, true)
	recs = nil
	runtime.GC()
	var ns []float64
	for rep := 0; rep < 3; rep++ {
		t0 := time.Now()
		m := mergeLevels([]*locIndex{big, small}, nil, false, fb03NoClash)
		ns = append(ns, float64(time.Since(t0).Nanoseconds())/float64(m.size()))
		m = nil
		runtime.GC()
	}
	fb03Emit(t, map[string]any{"test": "merge-cost", "n": n, "ratio": ratio, "ns_per_record": ns})
}
