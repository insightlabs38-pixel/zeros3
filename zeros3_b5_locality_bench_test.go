//go:build b5locality && b1rangeexperiment

package main

// B5 physical-locality benchmark (not a regression test): go test -tags b5locality,b1rangeexperiment.
//
//	ZEROS3_B5_STORES  comma-separated name=dir fixture stores (opened read-only in spirit: reads only)
//	ZEROS3_B5_OUT     JSONL rows (appended)
//	ZEROS3_B5_LABEL   layout label written into every row
//	ZEROS3_B5_COLD=1  drop the page cache before every cold read (needs root)
//	ZEROS3_B5_N       cold reads per object and size (default 24)
//	ZEROS3_B5_TRACE   file receiving every physical record of every cold read (pack off stored codec)

import (
	"bytes"
	"cmp"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type b5Store struct{ name, dir string }

func b5Stores(t *testing.T) []b5Store {
	v := os.Getenv("ZEROS3_B5_STORES")
	if v == "" {
		t.Skip("set ZEROS3_B5_STORES")
	}
	var out []b5Store
	for _, kv := range strings.Split(v, ",") {
		k, d, _ := strings.Cut(kv, "=")
		out = append(out, b5Store{k, d})
	}
	return out
}

func b5Cold() bool { return os.Getenv("ZEROS3_B5_COLD") == "1" }

func b5Drop() {
	if b5Cold() {
		syscall.Sync()
		os.WriteFile("/proc/sys/vm/drop_caches", []byte("1"), 0o200)
	}
}

// b5DiskReads is the completed read requests and sectors of every whole block device.
func b5DiskReads() (ios, sectors int64) {
	ents, _ := os.ReadDir("/sys/block")
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/sys/block", e.Name(), "stat"))
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) < 3 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		s, _ := strconv.ParseInt(f[2], 10, 64)
		ios, sectors = ios+n, sectors+s
	}
	return
}

type b5Rec struct {
	sum              [32]byte
	pack             int32
	off              int64
	stored, logical  uint32
	codec            byte
	readNS, decodeNS int64
}

// b5Row is one measured read.
type b5Row struct {
	Label, Store, Key, Mode string
	Off, Len                int64
	Records                 int   // single-record pack reads (casRead)
	Runs, RunRecords        int64 // coalesced run reads (CDC stream path)
	WinReads, WinBytes      int64 // pack windows fetched by sparse authority reads
	StoredBytes             int64 // pack bytes issued by single-record reads
	LogicalBytes            int64 // decoded bytes of those records
	Packs                   int   // distinct packs touched
	Extents                 int   // maximal physically contiguous groups of the records read
	Windows128K             int   // distinct 128 KiB-aligned pack windows touched (readahead units)
	ReadNS, DecodeNS        int64 // summed pread / inflate+hash time of the single-record reads
	DiskReads, DiskBytes    int64 // block-device read requests and bytes (whole machine)
	ProcReadBytes           int64 // /proc/self/io read_bytes
	WallUS, CPUUS           int64
	AllocBytes              uint64
	ByClass                 map[string][3]int64 `json:",omitempty"` // records, stored, decoded bytes per record class
}

func b5ProcRead() int64 {
	b, _ := os.ReadFile("/proc/self/io")
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && k == "read_bytes" {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

type b5Tracer struct {
	class map[[32]byte]string // piece -> "pin", "d1", "d2", ...; unlisted records are "other" (manifests)
	mu    sync.Mutex
	recs  []b5Rec
	tr    *packTrace
}

func newB5Tracer(s *Store) *b5Tracer {
	x := &b5Tracer{tr: &packTrace{}}
	x.tr.OnRecord = func(sum *[32]byte, pack int32, off int64, stored, logical uint32, codec byte, rn, dn int64) {
		x.mu.Lock()
		x.recs = append(x.recs, b5Rec{*sum, pack, off, stored, logical, codec, rn, dn})
		x.mu.Unlock()
	}
	s.packTrace = x.tr
	x.class = map[[32]byte]string{}
	shas, _ := s.authFiles()
	for _, sha := range shas {
		rec, err := s.authLoad(sha)
		if err != nil || rec == nil {
			continue
		}
		k := "pin"
		if rec.Form == authFormDelta {
			k = "d" + strconv.Itoa(int(rec.Depth))
		} else if rec.Form == authFormRep {
			k = "rep"
		}
		for _, p := range rec.Pieces {
			x.class[p.SHA] = k
		}
	}
	return x
}

func (x *b5Tracer) reset() {
	x.mu.Lock()
	x.recs = x.recs[:0]
	x.mu.Unlock()
	x.tr.Runs.Store(0)
	x.tr.Chunks.Store(0)
	x.tr.StoredBytes.Store(0)
	x.tr.ReadAts.Store(0)
	x.tr.WinReads.Store(0)
	x.tr.WinBytes.Store(0)
}

// summarize folds the traced records into r.
func (x *b5Tracer) summarize(r *b5Row) {
	x.mu.Lock()
	recs := slices.Clone(x.recs)
	x.mu.Unlock()
	r.Records = len(recs)
	r.Runs, r.RunRecords = x.tr.ReadAts.Load(), x.tr.Chunks.Load()
	r.WinReads, r.WinBytes = x.tr.WinReads.Load(), x.tr.WinBytes.Load()
	packs := map[int32]bool{}
	wins := map[[2]int64]bool{}
	for _, c := range recs {
		r.StoredBytes += int64(c.stored) + packRecordHeaderSize
		r.LogicalBytes += int64(c.logical)
		r.ReadNS += c.readNS
		r.DecodeNS += c.decodeNS
		packs[c.pack] = true
		lo := c.off - packRecordHeaderSize
		for w := lo >> 17; w <= (c.off+int64(c.stored)-1)>>17; w++ {
			wins[[2]int64{int64(c.pack), w}] = true
		}
	}
	r.Packs, r.Windows128K = len(packs), len(wins)
	r.ByClass = map[string][3]int64{}
	for _, c := range recs {
		k := cmp.Or(x.class[c.sum], "other")
		v := r.ByClass[k]
		r.ByClass[k] = [3]int64{v[0] + 1, v[1] + int64(c.stored), v[2] + int64(c.logical)}
	}
	slices.SortFunc(recs, func(a, b b5Rec) int { return cmp.Or(cmp.Compare(a.pack, b.pack), cmp.Compare(a.off, b.off)) })
	for i, c := range recs {
		if i > 0 {
			p := recs[i-1]
			if p.pack == c.pack && c.off-packRecordHeaderSize <= p.off+int64(p.stored) {
				continue
			}
		}
		r.Extents++
	}
}

// b5Purge empties every in-process read cache so the next read is cold above the page cache.
func b5Purge(s *Store) {
	s.purgeReadCaches()
	s.authInvalidate()
	if rc := s.repCache(); rc != nil {
		rc.purge()
	}
}

type b5Object struct {
	key  string
	data []byte
}

func b5Objects(t *testing.T, s *Store) []b5Object {
	page, err := s.ListObjectsV2("b", "", "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []b5Object
	for _, o := range page.contents {
		_, man, err := s.HeadObject("b", o.key)
		if err != nil {
			t.Fatal(err)
		}
		_, data, err := s.GetObject("b", o.key)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(func() []byte { h := sha256.Sum256(data); return h[:] }()) != man.ObjectSHA256 {
			t.Fatalf("%s: reference read is not exact", o.key)
		}
		out = append(out, b5Object{o.key, data})
	}
	return out
}

func b5Emit(r b5Row) {
	out := os.Getenv("ZEROS3_B5_OUT")
	if out == "" {
		return
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(r)
	f.Write(append(b, '\n'))
}

// b5Measure performs one ranged GET through the production path and records its physical work.
func b5Measure(t *testing.T, s *Store, x *b5Tracer, st b5Store, o b5Object, mode string, off, n int64, cold bool) b5Row {
	if cold {
		b5Purge(s)
		b5Drop()
	}
	x.reset()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	io0, sec0 := b5DiskReads()
	pr0 := b5ProcRead()
	cpu0 := procCPUSeconds()
	t0 := time.Now()
	_, _, got, err := s.GetObjectRange("b", o.key, byteRange{off, off + n - 1})
	wall := time.Since(t0)
	cpu := procCPUSeconds() - cpu0
	io1, sec1 := b5DiskReads()
	pr1 := b5ProcRead()
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatalf("%s %s %d+%d: %v", st.name, o.key, off, n, err)
	}
	if !bytes.Equal(got, o.data[off:off+n]) {
		t.Fatalf("%s %s %d+%d: inexact", st.name, o.key, off, n)
	}
	r := b5Row{Label: os.Getenv("ZEROS3_B5_LABEL"), Store: st.name, Key: o.key, Mode: mode, Off: off, Len: n,
		DiskReads: io1 - io0, DiskBytes: (sec1 - sec0) * 512, ProcReadBytes: pr1 - pr0,
		WallUS: wall.Microseconds(), CPUUS: int64(cpu * 1e6), AllocBytes: m1.TotalAlloc - m0.TotalAlloc}
	x.summarize(&r)
	return r
}

func b5N() int {
	if n, err := strconv.Atoi(os.Getenv("ZEROS3_B5_N")); err == nil && n > 0 {
		return n
	}
	return 24
}

// b5Config is one store read with one reader setting.
type b5Config struct {
	b5Store
	prefetch bool
	s        *Store
	x        *b5Tracer
}

// b5Configs parses ZEROS3_B5_CONFIGS (name=dir[:p0], p0 turning prefetch off) or falls back to ZEROS3_B5_STORES.
func b5Configs(t *testing.T) []*b5Config {
	v := os.Getenv("ZEROS3_B5_CONFIGS")
	if v == "" {
		var out []*b5Config
		for _, st := range b5Stores(t) {
			out = append(out, &b5Config{b5Store: st, prefetch: os.Getenv("ZEROS3_B5_PREFETCH") != "0"})
		}
		return out
	}
	var out []*b5Config
	for _, kv := range strings.Split(v, ",") {
		k, d, _ := strings.Cut(kv, "=")
		d, opt, _ := strings.Cut(d, ":")
		out = append(out, &b5Config{b5Store: b5Store{k, d}, prefetch: opt != "p0"})
	}
	return out
}

// TestB5ReadBench measures cold sparse, repeated, warm and whole reads of every object. Every read is
// made on every configuration in rotating order (paired design), with caches dropped before each cold one.
func TestB5ReadBench(t *testing.T) {
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", cmp.Or(os.Getenv("ZEROS3_B5_CC_MIB"), "32"))
	defer func(w, p bool) { authPackWindows, authPrefetch = w, p }(authPackWindows, authPrefetch)
	authPackWindows = os.Getenv("ZEROS3_B5_WINDOWS") == "1"
	cfgs := b5Configs(t)
	var objs []b5Object
	for i, c := range cfgs {
		s, err := OpenStore(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c.s = s
		if i == 0 {
			objs = b5Objects(t, s)
		}
		c.x = newB5Tracer(s)
	}
	turn := 0
	each := func(fn func(c *b5Config)) {
		for k := range cfgs {
			c := cfgs[(turn+k)%len(cfgs)]
			authPrefetch = c.prefetch
			fn(c)
		}
		turn++
	}
	reads := strings.Split(cmp.Or(os.Getenv("ZEROS3_B5_MODES"), "cold,near,warm,whole"), ",")
	for oi, o := range objs {
		size := int64(len(o.data))
		rng := rand.New(rand.NewPCG(uint64(oi)+1, 0xb5))
		for _, n := range []int64{4 << 10, 64 << 10} {
			if n > size {
				continue
			}
			var offs []int64
			for range b5N() {
				offs = append(offs, rng.Int64N(size-n+1))
			}
			tag := strconv.FormatInt(n>>10, 10) + "k"
			if slices.Contains(reads, "cold") {
				for _, off := range offs {
					each(func(c *b5Config) { b5Emit(b5Measure(t, c.s, c.x, c.b5Store, o, "cold"+tag, off, n, true)) })
				}
			}
			// Repeated small ranges near each other share dependencies: one cold start, then no purge.
			if slices.Contains(reads, "near") {
				each(func(c *b5Config) {
					b5Purge(c.s)
					b5Drop()
					for k, off := range offs[:min(8, len(offs))] {
						near := min(size-n, off/(1<<20)*(1<<20)+int64(k)*n*3)
						b5Emit(b5Measure(t, c.s, c.x, c.b5Store, o, "near"+tag, near, n, false))
					}
				})
			}
			if slices.Contains(reads, "warm") {
				each(func(c *b5Config) {
					for pass := range 3 {
						for _, off := range offs[:min(8, len(offs))] {
							if r := b5Measure(t, c.s, c.x, c.b5Store, o, "warm"+tag, off, n, false); pass == 2 {
								b5Emit(r)
							}
						}
					}
				})
			}
		}
		if slices.Contains(reads, "whole") {
			for range 3 {
				each(func(c *b5Config) { b5Emit(b5Measure(t, c.s, c.x, c.b5Store, o, "whole", 0, size, true)) })
			}
		}
	}
	for _, c := range cfgs {
		c.s.packTrace = nil
	}
}

// TestB5MigrateProfile migrates a copy of a CDC fixture (ZEROS3_B5_MIGRATE) as the B1 fixtures were
// built and counts the pack records it decodes; -cpuprofile shows where migration CPU goes.
func TestB5MigrateProfile(t *testing.T) {
	src := os.Getenv("ZEROS3_B5_MIGRATE")
	if src == "" {
		t.Skip("set ZEROS3_B5_MIGRATE")
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "LOCK"))
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mu sync.Mutex
	distinct := map[[32]byte]int64{}
	var single, singleBytes, runLogical, runs int64
	s.packTrace = &packTrace{
		OnRecord: func(sum *[32]byte, _ int32, _ int64, _, logical uint32, _ byte, _, _ int64) {
			mu.Lock()
			single++
			singleBytes += int64(logical)
			distinct[*sum] = int64(logical)
			mu.Unlock()
		},
		OnRun: func(records, stored, logical int) {
			mu.Lock()
			runs++
			runLogical += int64(logical)
			mu.Unlock()
		},
	}
	defer func(w int64) { authWindowSize = w }(authWindowSize)
	authWindowSize = 1 << 20
	resetRSSPeak()
	cpu0, t0 := procCPUSeconds(), time.Now()
	rep, err := s.AuthorityMigrate(AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 1, AllowDelta: true, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	var dbytes int64
	for _, v := range distinct {
		dbytes += v
	}
	t.Logf("peak RSS %.0f MiB", rssPeakMiB())
	if k := os.Getenv("ZEROS3_B5_KEEP"); k != "" {
		s.Close()
		os.CopyFS(k, os.DirFS(dir))
	}
	t.Logf("migrate: %.1fs wall %.1fs cpu admitted=%d; single-record decodes %d (%.1f MB, %d distinct %.1f MB), run decodes %d (%.1f MB)",
		time.Since(t0).Seconds(), procCPUSeconds()-cpu0, rep.Admitted, single, float64(singleBytes)/1e6, len(distinct), float64(dbytes)/1e6, runs, float64(runLogical)/1e6)
}

// TestB5BlockCost prices restartable sub-record blocks: every compressed record of ZEROS3_B5_BLOCKSTORE
// re-encoded whole and as independent DEFLATE blocks of several sizes (payload bytes only).
func TestB5BlockCost(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_BLOCKSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_BLOCKSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sizes := []int{8 << 10, 16 << 10, 32 << 10, 64 << 10}
	var whole, stored, logical int64
	blocked := make([]int64, len(sizes))
	comp := newPackCompressor()
	for sum, loc := range s.packSnap().primaries() {
		data, err := s.readPacked(*sum, loc)
		if err != nil {
			t.Fatal(err)
		}
		logical += int64(len(data))
		stored += int64(loc.stored)
		w, _ := comp.encode(data)
		whole += int64(len(w))
		for i, bs := range sizes {
			for o := 0; o < len(data); o += bs {
				b, _ := comp.encode(data[o:min(len(data), o+bs)])
				blocked[i] += int64(len(b)) + 4
			}
		}
	}
	t.Logf("logical %d stored %d re-encoded whole %d", logical, stored, whole)
	for i, bs := range sizes {
		t.Logf("blocks %d KiB: %d (%+.2f%% vs whole)", bs>>10, blocked[i], 100*float64(blocked[i]-whole)/float64(whole))
	}
}

// TestB5BlockGain is the size-biased expected bytes decoded for a 4 KiB interval of a random object offset
// with whole-record decode versus restartable blocks (chunks of the manifests of ZEROS3_B5_BLOCKSTORE).
func TestB5BlockGain(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_BLOCKSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_BLOCKSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, _ := s.ListObjectsV2("b", "", "", "", 1000)
	for _, o := range page.contents {
		_, man, _ := s.HeadObject("b", o.key)
		var tot, whole float64
		blk := map[int64]float64{}
		for _, c := range man.Chunks {
			L := float64(c.Length)
			tot += L
			whole += L * L
			for _, bs := range []int64{16 << 10, 32 << 10, 64 << 10} {
				// a 4 KiB interval starting uniformly in the chunk touches one block, two when it straddles
				b := float64(min(bs, c.Length))
				blk[bs] += L * b * (1 + 4096/b)
			}
		}
		t.Logf("%s: whole %.0f B, 16K %.0f, 32K %.0f, 64K %.0f", o.key, whole/tot, blk[16<<10]/tot, blk[32<<10]/tot, blk[64<<10]/tot)
	}
}

// TestB5DictCost re-encodes every compressed record as independent blocks primed with a preset dictionary
// sampled from the records themselves (1 KiB from every k-th record, 32 KiB in all).
func TestB5DictCost(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_BLOCKSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_BLOCKSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var recs [][]byte
	var whole int64
	comp := newPackCompressor()
	for sum, loc := range s.packSnap().primaries() {
		if loc.codec != packCodecDeflate {
			continue
		}
		data, err := s.readPacked(*sum, loc)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, data)
		w, _ := comp.encode(data)
		whole += int64(len(w))
	}
	for _, dsz := range []int{8 << 10, 16 << 10, 32 << 10} {
		sample := dsz / 256
		var dict []byte
		step := max(1, len(recs)*sample/dsz)
		for i := 0; i < len(recs) && len(dict) < dsz; i += step {
			r := recs[i]
			o := len(r) / 2
			dict = append(dict, r[o:min(len(r), o+sample)]...)
		}
		for _, bs := range []int{32 << 10} {
			var n int64
			var buf bytes.Buffer
			for _, r := range recs {
				for o := 0; o < len(r); o += bs {
					buf.Reset()
					fw, _ := flate.NewWriterDict(&buf, 6, dict)
					fw.Write(r[o:min(len(r), o+bs)])
					fw.Close()
					n += int64(buf.Len()) + 4
				}
			}
			t.Logf("dict %d KiB, blocks %d KiB: %+.2f%% vs whole (%d records)", dsz>>10, bs>>10, 100*float64(n-whole)/float64(whole), len(recs))
		}
	}
}

// TestB5DictPrefix: per pack, a 32 KiB dictionary from records of the first ZEROS3_B5_DICTMIB MiB only.
func TestB5DictPrefix(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_BLOCKSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_BLOCKSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pre, _ := strconv.Atoi(cmp.Or(os.Getenv("ZEROS3_B5_DICTMIB"), "4"))
	byPack := map[int32][][]byte{}
	var whole, n int64
	comp := newPackCompressor()
	type pr struct {
		sum [32]byte
		loc packLoc
	}
	var all []pr
	for sum, loc := range s.packSnap().primaries() {
		all = append(all, pr{*sum, loc})
	}
	slices.SortFunc(all, func(a, b pr) int {
		return cmp.Or(cmp.Compare(a.loc.pack, b.loc.pack), cmp.Compare(a.loc.off, b.loc.off))
	})
	for _, r := range all {
		if r.loc.codec != packCodecDeflate {
			continue
		}
		data, _ := s.readPacked(r.sum, r.loc)
		byPack[r.loc.pack] = append(byPack[r.loc.pack], data)
		w, _ := comp.encode(data)
		whole += int64(len(w))
	}
	var buf bytes.Buffer
	for _, recs := range byPack {
		var head [][]byte
		var hb int
		for _, r := range recs {
			if hb >= pre<<20 {
				break
			}
			head, hb = append(head, r), hb+len(r)
		}
		dict := b5TrainDict(head, 32<<10)
		if k, _ := strconv.Atoi(os.Getenv("ZEROS3_B5_DICTRECS")); k > 0 {
			var pick [][]byte
			for i := range k {
				pick = append(pick, recs[i*len(recs)/k])
			}
			dict = b5TrainDict(pick, 32<<10)
		}
		for _, r := range recs {
			for o := 0; o < len(r); o += 32 << 10 {
				buf.Reset()
				fw, _ := flate.NewWriterDict(&buf, 6, dict)
				fw.Write(r[o:min(len(r), o+32<<10)])
				fw.Close()
				n += int64(buf.Len()) + 36
			}
		}
	}
	t.Logf("prefix %d MiB: %+.2f%% vs whole (%d packs)", pre, 100*float64(n-whole)/float64(whole), len(byPack))
}

func b5TrainDict(recs [][]byte, size int) []byte {
	var tot int
	for _, r := range recs {
		tot += len(r)
	}
	const sample = 128
	nsamp := size / sample
	var dict []byte
	stride := max(1, tot/nsamp)
	pos := 0
	for _, r := range recs {
		for o := (stride - pos%stride) % stride; o+sample <= len(r) && len(dict) < size; o += stride {
			dict = append(dict, r[o:o+sample]...)
		}
		pos += len(r)
	}
	return dict
}

// TestB5ReconDump prints the co-access of a few probes of ZEROS3_B5_RECONSTORE: what each reads, where it is
// now and its anchor.
func TestB5ReconDump(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_RECONSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_RECONSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := s.authFiles()
	var fam []*authRecord
	for _, sha := range files {
		r, _ := s.authLoad(sha)
		fam = append(fam, r)
	}
	f, err := s.probeReconFamily(fam, rr.liveSet(), 1<<14)
	if err != nil || f == nil {
		t.Fatal(err)
	}
	st := s.packSnap()
	hist := map[string]int{}
	for i, got := range f.probes {
		var line []string
		for _, sha := range got {
			p := f.pieces[sha]
			loc, _ := st.lookup(sha)
			kind := "pin"
			if p.coord < 0 {
				kind = "d" + strconv.Itoa(p.depth)
				if f.isHeader(sha) {
					kind += "H"
				}
			}
			hist[kind]++
			if i%97 == 0 {
				line = append(line, kind+"@"+strconv.Itoa(int(loc.pack))+":"+strconv.FormatInt(int64(loc.off)>>10, 10)+"K/a"+strconv.FormatInt(p.anchorOf()>>10, 10)+"K/"+strconv.FormatInt(p.size>>10, 10)+"K")
			}
		}
		if line != nil {
			t.Logf("probe %d: %s", i, strings.Join(line, " "))
		}
	}
	t.Logf("requests by kind: %v over %d probes", hist, len(f.probes))
}

// TestB5ReconVariants prices layout variants of ZEROS3_B5_RECONSTORE's family with the planner's estimator.
func TestB5ReconVariants(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_RECONSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_RECONSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := s.authFiles()
	var fam []*authRecord
	for _, sha := range files {
		r, _ := s.authLoad(sha)
		fam = append(fam, r)
	}
	f, err := s.probeReconFamily(fam, rr.liveSet(), 1<<14)
	if err != nil || f == nil {
		t.Fatal(err)
	}
	now, _ := s.reconEstimate(f, f.order)
	_, blob := s.reconEstimate(f, f.order)
	t.Logf("current %.2f windows/probe; blob order rewritten %.2f", now, blob)
	for _, c := range []int64{64 << 10, 256 << 10, 1 << 20, 4 << 20} {
		for name, order := range map[string][][32]byte{
			"interleave": f.layoutAs(reconInterleave, c), "pins-apart": f.layoutAs(reconPinsApart, c),
			"mode-anchor": b5VariantOrder(f, c, false), "refined": b5VariantOrder(f, c, true)} {
			_, pl := s.reconEstimate(f, order)
			t.Logf("%-11s cluster %4d KiB: %.2f", name, c>>10, pl)
		}
	}
}

// b5VariantOrder lays the family out interleaved by cluster with two rejected refinements: refine runs
// coordinate descent on the probe hypergraph (each dependent moves to the cluster most of its probes touch
// through their other pieces, two sweeps); otherwise each dependent goes to the base cluster its probes read
// most (mode anchor).
func b5VariantOrder(f *reconFamily, cluster int64, refine bool) [][32]byte {
	clusterOf := map[[32]byte]int64{}
	hdr := func(sha [32]byte) bool { return f.isHeader(sha) }
	hits := map[[32]byte]map[int64]int{}
	for _, got := range f.probes {
		var cs []int64
		for _, sha := range got {
			if p := f.pieces[sha]; p != nil && p.coord >= 0 {
				cs = append(cs, p.coord/cluster)
			}
		}
		for _, sha := range got {
			if p := f.pieces[sha]; p != nil && p.coord < 0 {
				if hits[sha] == nil {
					hits[sha] = map[int64]int{}
				}
				for _, c := range cs {
					hits[sha][c]++
				}
			}
		}
	}
	for _, sha := range f.order {
		p := f.pieces[sha]
		a := p.anchorOf()
		if p.coord >= 0 {
			a = p.coord
		}
		c := max(a, 0) / cluster
		if !refine && len(hits[sha]) > 0 {
			best, bn := int64(0), -1
			for k, n := range hits[sha] {
				if n > bn || n == bn && k < best {
					best, bn = k, n
				}
			}
			c = best
		}
		clusterOf[sha] = c
	}
	if refine {
		byPiece := map[[32]byte][]int{}
		for i, got := range f.probes {
			for _, sha := range got {
				if l := byPiece[sha]; len(l) == 0 || l[len(l)-1] != i {
					byPiece[sha] = append(l, i)
				}
			}
		}
		for range 2 {
			for _, sha := range f.order {
				if f.pieces[sha].coord >= 0 || hdr(sha) {
					continue
				}
				tally := map[int64]int{}
				for _, i := range byPiece[sha] {
					seen := map[int64]bool{}
					for _, o := range f.probes[i] {
						if c := clusterOf[o]; o != sha && !hdr(o) && !seen[c] {
							seen[c] = true
							tally[c]++
						}
					}
				}
				best, bn := clusterOf[sha], tally[clusterOf[sha]]
				for c, n := range tally {
					if n > bn || n == bn && c < best {
						best, bn = c, n
					}
				}
				clusterOf[sha] = best
			}
		}
	}
	seq := map[[32]byte]int{}
	for i, sha := range f.order {
		seq[sha] = i
	}
	out := slices.Clone(f.order)
	slices.SortStableFunc(out, func(a, b [32]byte) int {
		ha, hb := hdr(a), hdr(b)
		if ha != hb {
			if ha {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(clusterOf[a], clusterOf[b]), cmp.Compare(f.pieces[a].depth, f.pieces[b].depth), cmp.Compare(seq[a], seq[b]))
	})
	return out
}

// b5Corpus is one benchmark corpus family: files ingested oldest first, then migrated and reclaimed.
type b5Corpus struct {
	name  string
	files []string
	pol   AuthPolicy
}

func b5Corpora(corpus string) []b5Corpus {
	g := func(p string) []string { m, _ := filepath.Glob(filepath.Join(corpus, p)); slices.Sort(m); return m }
	arch := AuthPolicy{Order: "oldest", MaxDepth: 2, AllowRep: true, AllowDelta: true, MinSaving: 0.05}
	return []b5Corpus{
		{"sqlite", g("sqlite/v*.db"), arch},
		{"git", g("git/h*.pack"), arch},
		{"ts", g("ts/typescript-*.tgz"), arch},
		{"gz", g("gz/coreutils-9.7.l6.tar.gz"), AuthPolicy{AllowRep: true, MinSaving: 0.05}},
		{"random", g("random/r*.bin"), arch},
	}
}

// TestB5BuildFixtures builds ZEROS3_B5_FX/<family>/{base,blk,rb} from ZEROS3_B2_CORPUS (make_inputs.py and
// fetch_corpus.sh) and copies the B1 Coreutils stores from ZEROS3_B1_DIR; derived variants are rewritten by
// repack (-recompress -record-blocks 32 [-layout recon]) and their cost is appended to ZEROS3_B5_OUT.
func TestB5BuildFixtures(t *testing.T) {
	fx, corpus := os.Getenv("ZEROS3_B5_FX"), os.Getenv("ZEROS3_B2_CORPUS")
	if fx == "" || corpus == "" {
		t.Skip("set ZEROS3_B5_FX and ZEROS3_B2_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, f := range b5Corpora(corpus) {
		base := filepath.Join(fx, f.name, "base")
		if _, err := os.Stat(filepath.Join(base, ".b5-ready")); err == nil {
			continue
		}
		if len(f.files) == 0 {
			t.Fatalf("%s: no inputs", f.name)
		}
		os.RemoveAll(base)
		b1Put(t, base, f.files)
		econReclaim(t, base)
		lock, err := acquireStoreLock(base, true)
		if err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(base)
		if err != nil {
			t.Fatal(err)
		}
		pol := f.pol
		pol.Apply = true
		rep, err := s.AuthorityMigrate(pol)
		s.Close()
		lock.release()
		if err != nil {
			t.Fatal(err)
		}
		econReclaim(t, base)
		alloc, apparent := allocatedBytes(base)
		meta, _ := json.Marshal(map[string]any{"family": f.name, "allocated": alloc, "apparent": apparent, "admitted": rep.Admitted, "objects": len(f.files)})
		os.WriteFile(filepath.Join(base, ".b5-ready"), meta, 0o644)
		t.Logf("%s: %s", f.name, meta)
	}
	if b1 := os.Getenv("ZEROS3_B1_DIR"); b1 != "" {
		for _, lay := range []string{"zw-mixed", "xd-multi", "cdc"} {
			dst := filepath.Join(fx, "coreutils-"+lay, "base")
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			if err := os.CopyFS(dst, os.DirFS(filepath.Join(b1, "coreutils", lay))); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(dst, "LOCK"))
		}
	}
	ents, _ := os.ReadDir(fx)
	for _, e := range ents {
		base := filepath.Join(fx, e.Name(), "base")
		for _, v := range []struct {
			name string
			opt  repackOptions
		}{{"blk", repackOptions{BlockLog: 15}}, {"rb", repackOptions{BlockLog: 15, Layout: layoutRecon}}} {
			dst := filepath.Join(fx, e.Name(), v.name)
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			if err := os.CopyFS(dst, os.DirFS(base)); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(dst, "LOCK"))
			opt := v.opt
			opt.TargetBytes, opt.MaxLivePercent, opt.Compress, opt.Recompress = defaultPackTargetBytes, 100, true, true
			resetRSSPeak()
			cpu0, t0 := procCPUSeconds(), time.Now()
			res, err := repackStore(dst, opt)
			if err != nil {
				t.Fatal(err)
			}
			wall, cpu := time.Since(t0).Seconds(), procCPUSeconds()-cpu0
			a0, _ := allocatedBytes(base)
			a1, _ := allocatedBytes(dst)
			row := map[string]any{"kind": "repack", "family": e.Name(), "variant": v.name, "wall_s": wall, "cpu_s": cpu, "peak_rss_mib": rssPeakMiB(),
				"bytes_read": res.BytesRead, "bytes_written": res.BytesWritten, "records_copied": res.RecordsCopied, "packs_rewritten": res.PacksRewritten,
				"allocated_base": a0, "allocated": a1, "recon": res.Recon}
			b, _ := json.Marshal(row)
			t.Logf("%s", b)
			if out := os.Getenv("ZEROS3_B5_OUT"); out != "" {
				f, _ := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				f.Write(append(b, '\n'))
				f.Close()
			}
		}
	}
}

// TestB5ConcurrentBench runs ZEROS3_B5_READERS closed-loop readers of random 4 KiB / 64 KiB ranges over
// every object of each configuration for ZEROS3_B5_SECONDS, starting from a dropped page cache; reads are
// checked against the reference bytes.
func TestB5ConcurrentBench(t *testing.T) {
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	defer func(p bool) { authPrefetch = p }(authPrefetch)
	cfgs := b5Configs(t)
	readers, _ := strconv.Atoi(cmp.Or(os.Getenv("ZEROS3_B5_READERS"), "8"))
	secs, _ := strconv.Atoi(cmp.Or(os.Getenv("ZEROS3_B5_SECONDS"), "15"))
	var objs []b5Object
	for i, c := range cfgs {
		s, err := OpenStore(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			objs = b5Objects(t, s)
		}
		s.Close()
	}
	for _, c := range cfgs {
		s, err := OpenStore(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		authPrefetch = c.prefetch
		b5Purge(s)
		b5Drop()
		io0, sec0 := b5DiskReads()
		cpu0 := procCPUSeconds()
		var mu sync.Mutex
		var lat []int64
		var bytesRead int64
		deadline := time.Now().Add(time.Duration(secs) * time.Second)
		var wg sync.WaitGroup
		for g := range readers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(g), 0xc0))
				for time.Now().Before(deadline) {
					o := objs[r.IntN(len(objs))]
					n := int64(4 << 10)
					if r.IntN(4) == 0 {
						n = 64 << 10
					}
					n = min(n, int64(len(o.data)))
					off := r.Int64N(int64(len(o.data)) - n + 1)
					t0 := time.Now()
					_, _, got, err := s.GetObjectRange("b", o.key, byteRange{off, off + n - 1})
					d := time.Since(t0).Microseconds()
					if err != nil || !bytes.Equal(got, o.data[off:off+n]) {
						t.Errorf("%s %s %d+%d: %v", c.name, o.key, off, n, err)
						return
					}
					mu.Lock()
					lat, bytesRead = append(lat, d), bytesRead+n
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		cpu := procCPUSeconds() - cpu0
		io1, sec1 := b5DiskReads()
		s.Close()
		slices.Sort(lat)
		q := func(p float64) int64 { return lat[min(len(lat)-1, int(p*float64(len(lat))))] }
		row := map[string]any{"kind": "concurrent", "label": os.Getenv("ZEROS3_B5_LABEL"), "config": c.name, "readers": readers, "seconds": secs,
			"reads": len(lat), "reads_per_s": float64(len(lat)) / float64(secs), "p50_us": q(.5), "p95_us": q(.95), "p99_us": q(.99),
			"cpu_s": cpu, "disk_reads": io1 - io0, "disk_bytes": (sec1 - sec0) * 512, "payload_bytes": bytesRead}
		b, _ := json.Marshal(row)
		t.Logf("%s", b)
		if out := os.Getenv("ZEROS3_B5_OUT"); out != "" {
			f, _ := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

// TestB5MaintUnderLoad copies ZEROS3_B5_MAINTSTORE, adds dead data, and measures 4 KiB reader latency over
// three phases: idle, during back-to-back online passes with -maintenance-layout recon -maintenance-record-blocks 32,
// and after. Each pass is reported (time, CPU, bytes rewritten, relayout).
func TestB5MaintUnderLoad(t *testing.T) {
	src := os.Getenv("ZEROS3_B5_MAINTSTORE")
	if src == "" {
		t.Skip("set ZEROS3_B5_MAINTSTORE")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "LOCK"))
	s := b4OpenMaint(t, dir)
	defer s.Close()
	objs := b5Objects(t, s)
	for i := range 24 { // dead data for the passes to reclaim beside the relayout
		k := fmt.Sprintf("dead%02d", i)
		if _, err := s.PutObject("b", k, b5Text(uint64(900+i), 1<<20), "text/plain", nil); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteObject("b", k); err != nil {
			t.Fatal(err)
		}
	}
	b4Prune(t, s)
	emit := func(row map[string]any) {
		b, _ := json.Marshal(row)
		t.Logf("%s", b)
		if out := os.Getenv("ZEROS3_B5_OUT"); out != "" {
			f, _ := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	phase := func(name string, d time.Duration, during func(stop <-chan struct{})) {
		stop := make(chan struct{})
		var mu sync.Mutex
		var lat []int64
		var wg sync.WaitGroup
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(g), 0xd0))
				for {
					select {
					case <-stop:
						return
					default:
					}
					o := objs[r.IntN(len(objs))]
					off := r.Int64N(int64(len(o.data)) - 4096)
					t0 := time.Now()
					_, _, got, err := s.GetObjectRange("b", o.key, byteRange{off, off + 4095})
					if err != nil || !bytes.Equal(got, o.data[off:off+4096]) {
						t.Errorf("%s: %s@%d: %v", name, o.key, off, err)
						return
					}
					mu.Lock()
					lat = append(lat, time.Since(t0).Microseconds())
					mu.Unlock()
				}
			}()
		}
		done := make(chan struct{})
		go func() { during(stop); close(done) }()
		time.Sleep(d)
		close(stop)
		wg.Wait()
		<-done
		slices.Sort(lat)
		q := func(p float64) int64 { return lat[min(len(lat)-1, int(p*float64(len(lat))))] }
		emit(map[string]any{"kind": "maint-phase", "phase": name, "reads": len(lat), "p50_us": q(.5), "p95_us": q(.95), "p99_us": q(.99), "max_us": lat[len(lat)-1]})
	}
	opt := b4Opts()
	opt.MaxLivePercent, opt.Layout, opt.BlockLog, opt.TargetBytes = defaultRepackMaxLivePercent, layoutRecon, 15, defaultPackTargetBytes
	a0, _ := allocatedBytes(dir)
	phase("idle", 10*time.Second, func(<-chan struct{}) {})
	phase("passes", 10*time.Second, func(stop <-chan struct{}) {
		idle := 0
		defer func() { emit(map[string]any{"kind": "maint-idle-passes", "passes": idle}) }()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			cpu0, t0 := procCPUSeconds(), time.Now()
			rep, err := s.maintainOnline(context.Background(), opt)
			if err != nil {
				t.Error(err)
				return
			}
			if rep.PacksWritten == 0 && rep.PacksRetired == 0 && rep.Recon == nil {
				idle++
				continue
			}
			emit(map[string]any{"kind": "maint-pass", "pass": i, "wall_ms": time.Since(t0).Milliseconds(), "process_cpu_s": procCPUSeconds() - cpu0,
				"packs_written": rep.PacksWritten, "bytes_written": rep.BytesWritten, "bytes_reclaimed": rep.BytesReclaimed, "packs_retired": rep.PacksRetired,
				"scan_ms": rep.ScanMS, "recon": rep.Recon})
		}
	})
	phase("after", 10*time.Second, func(<-chan struct{}) {})
	a1, _ := allocatedBytes(dir)
	emit(map[string]any{"kind": "maint-store", "allocated_before": a0, "allocated_after": a1})
	for _, o := range objs {
		_, got, err := s.GetObject("b", o.key)
		if err != nil || !bytes.Equal(got, o.data) {
			t.Fatalf("%s inexact after maintenance", o.key)
		}
	}
	res, err := s.Verify(true)
	if err != nil || !res.OK() {
		t.Fatalf("verify: %v %+v", err, res.Issues)
	}
}

// TestB5DupEconomics prices selective duplication on the planned (interleaved) layout of ZEROS3_B5_RECONSTORE's
// family: a probe pays one window per distinct cluster its pieces fall in; copying a base piece into a cluster
// removes that window from every probe anchored there that reads the piece and nothing else of its cluster.
// Copies are chosen greedily by windows saved per stored byte; the curve is appended to ZEROS3_B5_OUT.
func TestB5DupEconomics(t *testing.T) {
	dir := os.Getenv("ZEROS3_B5_RECONSTORE")
	if dir == "" {
		t.Skip("set ZEROS3_B5_RECONSTORE")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := s.authFiles()
	var fam []*authRecord
	for _, sha := range files {
		r, _ := s.authLoad(sha)
		fam = append(fam, r)
	}
	f, err := s.probeReconFamily(fam, rr.liveSet(), 1<<14)
	if err != nil || f == nil {
		t.Fatal(err)
	}
	st := s.packSnap()
	clusterOf := func(sha [32]byte) int64 {
		p := f.pieces[sha]
		if f.isHeader(sha) {
			return -1
		}
		a := p.anchorOf()
		if p.coord >= 0 {
			a = p.coord
		}
		return max(a, 0) / reconCluster
	}
	type probe struct {
		home   int64
		pieces map[int64][][32]byte // cluster -> pieces read there
	}
	var ps []probe
	var total int
	for _, got := range f.probes {
		pr := probe{pieces: map[int64][][32]byte{}}
		var cs []int64
		for _, sha := range got {
			c := clusterOf(sha)
			if !slices.Contains(pr.pieces[c], sha) {
				pr.pieces[c] = append(pr.pieces[c], sha)
			}
			if f.pieces[sha].coord < 0 && c >= 0 {
				cs = append(cs, c)
			}
		}
		if len(cs) == 0 {
			continue
		}
		slices.Sort(cs)
		pr.home = cs[len(cs)/2]
		ps = append(ps, pr)
		total += len(pr.pieces)
	}
	// Candidate copy (piece, home cluster): saves one window for each probe of that home whose only piece in
	// the piece's cluster is that piece.
	type cand struct {
		sha  [32]byte
		home int64
	}
	gain := map[cand]int{}
	for _, pr := range ps {
		for c, l := range pr.pieces {
			if c != pr.home && c >= 0 && len(l) == 1 && f.pieces[l[0]].coord >= 0 {
				gain[cand{l[0], pr.home}]++
			}
		}
	}
	type scored struct {
		cand
		saved int
		bytes int64
	}
	var all []scored
	for k, n := range gain {
		loc, _ := st.lookup(k.sha)
		all = append(all, scored{k, n, int64(loc.stored) + packRecordHeaderSize})
	}
	slices.SortFunc(all, func(a, b scored) int {
		return cmp.Compare(float64(b.saved)/float64(b.bytes), float64(a.saved)/float64(a.bytes))
	})
	var famBytes int64
	for sha := range f.pieces {
		loc, _ := st.lookup(sha)
		famBytes += int64(loc.stored) + packRecordHeaderSize
	}
	var saved int
	var extra int64
	next := []float64{0.05, 0.1, 0.2, 0.3}
	var curve []map[string]any
	for _, c := range all {
		saved += c.saved
		extra += c.bytes
		for len(next) > 0 && float64(saved)/float64(total) >= next[0] {
			curve = append(curve, map[string]any{"window_reduction": next[0], "extra_bytes": extra, "extra_share": float64(extra) / float64(famBytes), "copies": 0})
			next = next[1:]
		}
	}
	row := map[string]any{"kind": "dup-economics", "probes": len(ps), "windows": total, "family_bytes": famBytes, "max_window_reduction": float64(saved) / float64(total), "all_copies_bytes": extra, "curve": curve}
	b, _ := json.Marshal(row)
	t.Logf("%s", b)
	if out := os.Getenv("ZEROS3_B5_OUT"); out != "" {
		fo, _ := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fo.Write(append(b, '\n'))
		fo.Close()
	}
}
