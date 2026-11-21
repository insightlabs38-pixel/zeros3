//go:build b1rangeexperiment

package main

// B1 nested range execution experiment.
//
//	ZEROS3_B2_CORPUS  corpus root (src/coreutils-9.{5,6,7}.tar, testing-harnesses/z4/p8/CORPUS.md)
//	ZEROS3_B1_INPUTS  optional comma-separated history (oldest first) replacing the Coreutils tars
//	ZEROS3_B1_DIR     where built fixtures persist between runs (one directory per family and layout)
//	ZEROS3_B1_OUT     JSONL rows (appended)
//	ZEROS3_B1_COLD=1  drop the page cache before every measured read (needs root)

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type b1Layout struct {
	name     string
	w1, w2   int64 // ZW1 frame bytes of the depth-1 and depth-2 objects; 0 keeps them in xd envelopes
	maxBases int
}

var b1Layouts = []b1Layout{
	{"cdc", 0, 0, 0},
	{"zw-mixed", 1 << 20, 256 << 10, 1},
	{"zw-1m", 1 << 20, 1 << 20, 1},
	{"xd-multi", 0, 0, 2},
	{"xd-merge", 0, 0, 2}, // the last revision replaced by the first half of the oldest and the second half of the next
}

// b1Merged writes the xd-merge object (two bases, one of them the other's base) next to the fixtures.
func b1Merged(t *testing.T, root string, inputs []string) string {
	out := filepath.Join(root, "merged.tar")
	if _, err := os.Stat(out); err == nil {
		return out
	}
	a, err := os.ReadFile(inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(inputs[1])
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(root, 0o755)
	if err := os.WriteFile(out, append(a[:len(a)/2:len(a)/2], b[len(b)/2:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func b1Inputs(t *testing.T) (string, []string) {
	if v := os.Getenv("ZEROS3_B1_INPUTS"); v != "" {
		in := strings.Split(v, ",")
		return "custom-" + hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte(v)); return h[:4] }()), in
	}
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("set ZEROS3_B2_CORPUS or ZEROS3_B1_INPUTS")
	}
	in, _ := filepath.Glob(filepath.Join(root, "src", "coreutils-*.tar"))
	sort.Strings(in)
	if len(in) != 3 {
		t.Fatalf("expected three Coreutils tars, found %v", in)
	}
	return "coreutils", in
}

func b1Dir(t *testing.T) string {
	d := os.Getenv("ZEROS3_B1_DIR")
	if d == "" {
		t.Skip("set ZEROS3_B1_DIR")
	}
	return d
}

func b1Put(t *testing.T, dir string, files []string) {
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.HeadBucket("b"); err != nil {
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutObject("b", filepath.Base(f), data, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
}

// b1Migrate migrates at the given frame size; the later revision also uses the persistent similarity
// index (the c2 research fixture), which is what finds the depth-2 base.
func b1Migrate(t *testing.T, dir string, lay b1Layout, window int64, indexed bool) AuthReport {
	defer func(w, m int64) { authWindowSize, authWindowMin = w, m }(authWindowSize, authWindowMin)
	if window > 0 {
		authWindowSize = window
	} else {
		authWindowMin = xdMaxObject
	}
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
	pol := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: lay.maxBases, AllowDelta: true, Apply: true}
	if indexed {
		report, err := s.buildAuthSimilarityStage()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.publishAuthSimilarityStage(filepath.Base(report.Directory)); err != nil {
			t.Fatal(err)
		}
		if pol.similarity, err = s.openCurrentAuthSimilarityQuery(); err != nil {
			t.Fatal(err)
		}
		defer pol.similarity.close()
	}
	rep, err := s.AuthorityMigrate(pol)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func allocatedBytes(dir string) (alloc, apparent int64) {
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

// TestB1Fixtures builds every missing layout: the first two revisions migrate at the depth-1 frame
// size, the third is added and migrated at the depth-2 frame size, then gc, repack, compact and gc.
func TestB1Fixtures(t *testing.T) {
	family, inputs := b1Inputs(t)
	root := filepath.Join(b1Dir(t), family)
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, lay := range b1Layouts {
		dir := filepath.Join(root, lay.name)
		if _, err := os.Stat(filepath.Join(dir, ".b1-ready")); err == nil {
			continue
		}
		os.RemoveAll(dir)
		t0, cpu := time.Now(), procCPUSeconds()
		var reports []AuthReport
		inputs := inputs
		if lay.name == "xd-merge" {
			inputs = []string{inputs[0], inputs[1], b1Merged(t, root, inputs)}
		}
		if lay.name == "cdc" {
			b1Put(t, dir, inputs)
		} else {
			b1Put(t, dir, inputs[:len(inputs)-1])
			reports = append(reports, b1Migrate(t, dir, lay, lay.w1, false))
			b1Put(t, dir, inputs[len(inputs)-1:])
			reports = append(reports, b1Migrate(t, dir, lay, lay.w2, true))
		}
		econReclaim(t, dir)
		if lay.name != "cdc" && (len(reports) != 2 || reports[1].Admitted != 1) {
			t.Fatalf("%s: the last revision did not become a delta: %+v", lay.name, reports)
		}
		alloc, apparent := allocatedBytes(dir)
		meta := map[string]any{"layout": lay.name, "w1": lay.w1, "w2": lay.w2, "max_bases": lay.maxBases,
			"allocated": alloc, "apparent": apparent, "build_s": time.Since(t0).Seconds(), "build_cpu_s": procCPUSeconds() - cpu, "reports": reports}
		b, _ := json.MarshalIndent(meta, "", " ")
		if err := os.WriteFile(filepath.Join(dir, ".b1-ready"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s/%s allocated=%d apparent=%d", family, lay.name, alloc, apparent)
	}
}

type b1Row struct {
	Family, Layout, Mode string
	Key                  string
	Depth                int
	Off, Len             int64
	Fetched              int64            // logical CAS bytes (the production pricing unit)
	Distinct             int64            // distinct CAS bytes
	PieceReads           int64            // chunk reads that missed the request cache
	SharedHits           int64            // chunks served by the store chunk cache (filled by this read only)
	Windows, WindowBytes int64            // whole frames reconstructed and digest-checked
	Progs, ProgBytes     int64            // delta program frames parsed
	ByRecord             map[string]int64 `json:",omitempty"` // fetched CAS bytes per owning record (depth:sha)
	LeafAt               map[int64]int64  `json:",omitempty"` // pinned-base bytes a fixed piece size would fetch
	WallUS, CPUUS        int64
	AllocBytes, Mallocs  uint64
	DiskRead             int64 // read_bytes from /proc/self/io (physical, page cache dependent)
	GetUS                int64 // the signed-GET path (GetObjectRange) on the same range
}

func procIO() (rchar, readBytes int64) {
	b, _ := os.ReadFile("/proc/self/io")
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(l, ":")
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		switch k {
		case "rchar":
			rchar = n
		case "read_bytes":
			readBytes = n
		}
	}
	return
}

// b1LeafBlocks are the hypothetical fixed leaf piece sizes the pinned-interval trace is priced at.
var b1LeafBlocks = []int64{8 << 10, 16 << 10, 32 << 10, 64 << 10}

// b1TraceOut, when ZEROS3_B1_TRACE names a file, receives every chunk request of whole reads.
var b1TraceOut *os.File

func b1DropCaches() {
	if os.Getenv("ZEROS3_B1_COLD") == "1" {
		syscall.Sync()
		os.WriteFile("/proc/sys/vm/drop_caches", []byte("1"), 0o200)
	}
}

// b1Owner maps every chunk of every record to "depth:sha" for the per-level breakdown.
func b1Owner(t *testing.T, s *Store) map[[32]byte]string {
	out := map[[32]byte]string{}
	shas, err := s.authFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range shas {
		rec, err := s.authLoad(sha)
		if err != nil || rec == nil {
			continue
		}
		for _, p := range rec.Pieces {
			out[p.SHA] = strconv.Itoa(int(rec.Depth)) + ":" + hex.EncodeToString(sha[:4])
		}
	}
	return out
}

// b1Read reads [off, off+n) as authOpenReader does, on a fresh context, and reports its work.
// b1Warm keeps the store chunk cache and the page cache between reads (repeated-read mode).
var b1Warm bool

func b1Read(t *testing.T, s *Store, man manifestV1, off, n int64, owner map[[32]byte]string, want []byte) b1Row {
	sha, _ := decodeHexSHA256(man.ObjectSHA256)
	var mu sync.Mutex
	distinct := map[[32]byte]int64{}
	by := map[string]int64{}
	if !b1Warm {
		s.ccache.purge()
		b1DropCaches()
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	_, rd0 := procIO()
	cpu0 := procCPUSeconds()
	t0 := time.Now()
	ctx := s.newAuthCtx()
	leaf := map[[2]int64]bool{}
	ctx.trace = func(kind byte, sum [32]byte, off, size int64) {
		if kind == 'i' {
			mu.Lock()
			for _, bs := range b1LeafBlocks {
				for b := off / bs; b <= (off+size-1)/bs; b++ {
					leaf[[2]int64{bs, b}] = true
				}
			}
			mu.Unlock()
			return
		}
		if kind == 'r' && b1TraceOut != nil {
			mu.Lock()
			b1TraceOut.WriteString(owner[sum] + " " + hex.EncodeToString(sum[:8]) + " " + strconv.FormatInt(size, 10) + "\n")
			mu.Unlock()
		}
		if kind != 'c' {
			return
		}
		mu.Lock()
		distinct[sum] = size
		by[owner[sum]] += size
		mu.Unlock()
	}
	var got []byte
	body, _, err := ctx.body(sha, 0, 1)
	if wb, ok := body.(*authWinBody); ok {
		wb.reqEnd = off + n
	}
	if err == nil {
		got = make([]byte, n)
		for done := int64(0); done < n && err == nil; {
			k := min(int64(authReadPiece), n-done)
			var m int
			m, err = body.ReadAt(got[done:done+k], off+done)
			if int64(m) != k && err == nil {
				err = io.ErrUnexpectedEOF
			}
			done += k
		}
	}
	wall := time.Since(t0)
	cpu := procCPUSeconds() - cpu0
	_, rd1 := procIO()
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatalf("read %d+%d: %v", off, n, err)
	}
	if !bytes.Equal(got, want[off:off+n]) {
		t.Fatalf("inexact read at %d+%d", off, n)
	}
	row := b1Row{Off: off, Len: n, Fetched: ctx.fetched, PieceReads: ctx.st.pieceReads.Load(), SharedHits: ctx.st.sharedHits.Load(),
		Windows: ctx.st.windows.Load(), WindowBytes: ctx.st.windowBytes.Load(), Progs: ctx.st.progs.Load(), ProgBytes: ctx.st.progBytes.Load(),
		ByRecord: by, WallUS: wall.Microseconds(), CPUUS: int64(cpu * 1e6),
		AllocBytes: m1.TotalAlloc - m0.TotalAlloc, Mallocs: m1.Mallocs - m0.Mallocs, DiskRead: rd1 - rd0}
	for _, v := range distinct {
		row.Distinct += v
	}
	row.LeafAt = map[int64]int64{}
	for k := range leaf {
		row.LeafAt[k[0]] += k[0]
	}
	if !b1Warm {
		s.ccache.purge()
		b1DropCaches()
	}
	g0 := time.Now()
	_, _, data, gerr := s.GetObjectRange("b", filepath.Base(man.ManifestUUID), byteRange{off, off + n - 1})
	_ = data
	_ = gerr
	row.GetUS = time.Since(g0).Microseconds()
	return row
}

func b1CDCCost(man manifestV1, off, n int64) (fetched int64) {
	var pos int64
	for _, c := range man.Chunks {
		if pos < off+n && pos+c.Length > off {
			fetched += c.Length
		}
		pos += c.Length
	}
	return
}

// TestB1RangeBench measures every built layout with the reader of the current tree.
func TestB1RangeBench(t *testing.T) {
	family, inputs := b1Inputs(t)
	root := filepath.Join(b1Dir(t), family)
	label := os.Getenv("ZEROS3_B1_LABEL")
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", cmp.Or(os.Getenv("ZEROS3_B1_CC_MIB"), "0")) // emptied before every read
	corpus := map[string][]byte{}
	for _, in := range inputs {
		data, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		corpus[filepath.Base(in)] = data
	}
	if data, err := os.ReadFile(filepath.Join(root, "merged.tar")); err == nil {
		corpus["merged.tar"] = data
	}
	var rows []b1Row
	emit := func(r b1Row) {
		rows = append(rows, r)
		if out := os.Getenv("ZEROS3_B1_OUT"); out != "" {
			f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				b, _ := json.Marshal(map[string]any{"label": label, "row": r})
				f.Write(append(b, '\n'))
				f.Close()
			}
		}
	}
	if v, err := strconv.ParseInt(os.Getenv("ZEROS3_B1_PROG_MIB"), 10, 64); err == nil {
		authWinProgBytes = v << 20
	}
	only := os.Getenv("ZEROS3_B1_LAYOUTS")
	for _, lay := range b1Layouts {
		if only != "" && !slices.Contains(strings.Split(only, ","), lay.name) {
			continue
		}
		src := filepath.Join(root, lay.name)
		if _, err := os.Stat(filepath.Join(src, ".b1-ready")); err != nil {
			t.Logf("skip %s: not built", lay.name)
			continue
		}
		dir := filepath.Join(t.TempDir(), lay.name)
		if err := econCopyTree(src, dir); err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.SetAuthorityCache(0)
		owner := b1Owner(t, s)
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range corpus {
				if !yield(k) {
					return
				}
			}
		})
		for _, key := range keys {
			_, man, err := s.HeadObject("b", key)
			if err != nil {
				continue // the layout does not hold this key
			}
			man.ManifestUUID = key // b1Read addresses the S3 key through this field
			sha, _ := decodeHexSHA256(man.ObjectSHA256)
			rec, _ := s.authLoad(sha)
			depth := 0
			if rec != nil {
				depth = int(rec.Depth)
			}
			want := corpus[key]
			size := int64(len(want))
			rng := rand.New(rand.NewPCG(81, 201))
			var offs []int64
			for range 16 {
				offs = append(offs, rng.Int64N(size-4096))
			}
			base := b1Row{Family: family, Layout: lay.name, Key: key, Depth: depth}
			if rec == nil {
				if lay.name != "cdc" {
					continue
				}
				for _, off := range offs[:8] {
					r := base
					r.Mode, r.Off, r.Len, r.Fetched = "cold-4k", off, 4096, b1CDCCost(man, off, 4096)
					b1DropCaches()
					g0 := time.Now()
					_, _, got, err := s.GetObjectRange("b", key, byteRange{off, off + 4095})
					r.GetUS = time.Since(g0).Microseconds()
					if err != nil || !bytes.Equal(got, want[off:off+4096]) {
						t.Fatalf("cdc range %s %d: %v", key, off, err)
					}
					emit(r)
				}
				continue
			}
			measure := func(mode string, off, n int64) {
				r := b1Read(t, s, man, off, n, owner, want)
				r.Family, r.Layout, r.Key, r.Depth, r.Mode = family, lay.name, key, depth, mode
				emit(r)
			}
			for _, off := range offs {
				measure("cold-4k", off, 4096)
			}
			if s.ccache.enabled() {
				for _, off := range offs[:4] { // the third read of the same range, caches kept
					b1Read(t, s, man, off, 4096, owner, want)
					b1Warm = true
					b1Read(t, s, man, off, 4096, owner, want)
					measure("repeat-4k", off, 4096)
					b1Warm = false
				}
			}
			for _, off := range offs[:2] {
				measure("seq-1m", off, min(1<<20, size-off))
			}
			measure("seq-16m", size/4, 16<<20)
			if tr := os.Getenv("ZEROS3_B1_TRACE"); tr != "" {
				b1TraceOut, _ = os.Create(tr + "." + lay.name + "." + strconv.Itoa(depth))
			}
			measure("whole", 0, size)
			if b1TraceOut != nil {
				b1TraceOut.Close()
				b1TraceOut = nil
			}
		}
		s.Close()
	}
	summarize(t, rows)
}

func summarize(t *testing.T, rows []b1Row) {
	type agg struct {
		n                                  int
		fetched, windowB, progB, wall, cpu []int64
	}
	m := map[string]*agg{}
	var order []string
	for _, r := range rows {
		k := r.Layout + " d" + strconv.Itoa(r.Depth) + " " + r.Mode
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
			order = append(order, k)
		}
		a.n++
		a.fetched = append(a.fetched, r.Fetched)
		a.windowB = append(a.windowB, r.WindowBytes)
		a.progB = append(a.progB, r.ProgBytes)
		a.wall = append(a.wall, r.WallUS)
		a.cpu = append(a.cpu, r.GetUS)
	}
	mean := func(v []int64) int64 {
		var s int64
		for _, x := range v {
			s += x
		}
		return s / int64(len(v))
	}
	med := func(v []int64) int64 { v = slices.Clone(v); slices.Sort(v); return v[len(v)/2] }
	for _, k := range order {
		a := m[k]
		t.Logf("%-28s n=%2d fetched mean=%11d med=%11d window=%11d prog=%9d wall med=%8dus get med=%8dus", k, a.n, mean(a.fetched), med(a.fetched), mean(a.windowB), mean(a.progB), med(a.wall), med(a.cpu))
	}
}

// TestB1Anatomy reports the structure the range executor works against: program payload sizes per
// frame, COPY length distribution and the leaf chunk sizes of the pinned base.
func TestB1Anatomy(t *testing.T) {
	family, _ := b1Inputs(t)
	root := filepath.Join(b1Dir(t), family)
	for _, lay := range b1Layouts[1:] {
		s, err := OpenStore(filepath.Join(root, lay.name))
		if err != nil {
			t.Fatal(err)
		}
		shas, _ := s.authFiles()
		for _, sha := range shas {
			rec, _ := s.authLoad(sha)
			if rec == nil {
				continue
			}
			ctx := s.newAuthCtx()
			if rec.Form == authFormPin {
				var tot int64
				for _, p := range rec.Pieces {
					tot += p.Len
				}
				t.Logf("%s pin %x: %d chunks, mean %d bytes", lay.name, sha[:4], len(rec.Pieces), tot/int64(len(rec.Pieces)))
				continue
			}
			blob := newPieceSeq(ctx, rec.Pieces)
			var hist [8]int64 // copies by length: <64, <256, <1K, <4K, <16K, <64K, <256K, >=
			var copies, lits, litBytes, payload int64
			nframes := 0
			bucket := func(n int64) {
				i := 0
				for lim := int64(64); i < 7 && n >= lim; lim *= 4 {
					i++
				}
				hist[i]++
			}
			if wr, err := dwDcoreOpenWRecord(blob, blob.size); err == nil {
				nframes = len(wr.frames)
				for i := range wr.frames {
					payload += wr.frames[i].plen
					ops, err := wr.program(i)
					if err != nil {
						t.Fatal(err)
					}
					for _, o := range ops {
						if o.base < 0 {
							lits++
							litBytes += o.n
						} else {
							copies++
							bucket(o.n)
						}
					}
				}
			} else {
				b, err := ctx.newDelta(rec, 1)
				if err != nil {
					t.Fatal(err)
				}
				d := b.(*authDeltaBody)
				nframes = len(d.hdr.frames)
				for i := range d.hdr.frames {
					payload += d.hdr.frames[i].plen
					ops, err := d.loadFrame(i)
					if err != nil {
						t.Fatal(err)
					}
					for _, o := range ops {
						if o.base < 0 {
							lits++
							litBytes += o.n
						} else {
							copies++
							bucket(o.n)
						}
					}
				}
			}
			t.Logf("%s depth %d %x: %d frames, payload %d (mean %d), %d copies, %d literals (%d bytes); copy lengths <64,<256,<1K,<4K,<16K,<64K,<256K,more: %v",
				lay.name, rec.Depth, sha[:4], nframes, payload, payload/int64(max(1, nframes)), copies, lits, litBytes, hist)
		}
		s.Close()
	}
}

// TestB1SegmentCost re-encodes every unsegmented frame program as ZW2 segments of several spans (the
// writer's own segmentFrame) and reports the payload growth against the stored frames.
func TestB1SegmentCost(t *testing.T) {
	family, _ := b1Inputs(t)
	root := filepath.Join(b1Dir(t), family)
	spans := []int64{16 << 10, 64 << 10, 128 << 10}
	for _, lay := range []string{"zw-mixed", "zw-1m"} {
		s, err := OpenStore(filepath.Join(root, lay))
		if err != nil {
			t.Fatal(err)
		}
		shas, _ := s.authFiles()
		for _, sha := range shas {
			rec, _ := s.authLoad(sha)
			if rec == nil || rec.Form != authFormDelta {
				continue
			}
			blob := newPieceSeq(s.newAuthCtx(), rec.Pieces)
			wr, err := dwDcoreOpenWRecord(blob, blob.size)
			if err != nil || wr.segmented {
				continue
			}
			var stored int64
			seg := map[int64]int64{}
			for i := range wr.frames {
				stored += wr.frames[i].plen
				ops, err := wr.program(i)
				if err != nil {
					t.Fatal(err)
				}
				dops := make([]dwDcoreOp, len(ops))
				for k, o := range ops {
					dops[k] = dwDcoreOp{TPos: o.tpos - wr.tstart[i], N: o.n, Base: o.base, Off: o.off, Lit: o.lit}
				}
				for _, span := range spans {
					out, _ := dwDcore_segmentFrame(dops, wr.frames[i].tlen, span)
					seg[span] += int64(len(out))
				}
			}
			for _, span := range spans {
				t.Logf("%s depth %d frame %d KiB: stored programs %d, %d KiB segments %d (%+.1f%%)", lay, rec.Depth, wr.maxWindow>>10, stored, span>>10, seg[span], 100*float64(seg[span]-stored)/float64(stored))
			}
		}
		s.Close()
	}
}
