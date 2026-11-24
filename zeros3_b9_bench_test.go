//go:build b9bench && b6bench && b5locality && b1rangeexperiment

package main

// B9 campaign (not a regression test): go test -tags b9bench,b6bench,b5locality,b1rangeexperiment.
// Environment as B6-B8 (ZEROS3_B6_FX, ZEROS3_B6_CORPUS, ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES).

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// b9Families are the pre-registered B9 families (testing-harnesses/next/b9/PREREGISTRATION.md).
func b9Families(c string) []b6Family {
	perFile := func(name, dir string, versions ...string) b6Family {
		f := b6Family{name: name}
		for _, v := range versions {
			root := filepath.Join(c, dir, v)
			var files []string
			filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() != ".done" {
					files = append(files, p)
				}
				return nil
			})
			slices.Sort(files)
			for _, p := range files {
				rel, _ := filepath.Rel(root, p)
				f.puts = append(f.puts, b6Put{rel, p})
			}
		}
		return f
	}
	tools := func(name, dir, ext string) b6Family {
		f := b6Family{name: name}
		for _, v := range []string{"1.22.0", "1.22.6", "1.23.0", "1.23.4"} {
			for _, tool := range []string{"compile", "link", "go", "vet", "asm", "cgo"} {
				f.puts = append(f.puts, b6Put{tool + ext, filepath.Join(c, dir, v, tool+ext)})
			}
		}
		return f
	}
	hist := func(name, key string, files ...string) b6Family {
		f := b6Family{name: name}
		for _, p := range files {
			f.puts = append(f.puts, b6Put{key, filepath.Join(c, p)})
		}
		return f
	}
	dj := b6Family{name: "django"}
	for _, v := range []string{"5.1.1", "5.1.2", "5.1.3"} {
		dj.puts = append(dj.puts, b6Put{"django.tar.gz", filepath.Join(c, "django/Django-"+v+".tar.gz")},
			b6Put{"django.whl", filepath.Join(c, "django/Django-"+v+"-py3-none-any.whl")})
	}
	return []b6Family{
		tools("elf", "elf", ""), tools("pe", "pe", ".exe"),
		hist("ts", "typescript.tgz", "ts/typescript-5.6.2.tgz", "ts/typescript-5.6.3.tgz", "ts/typescript-5.7.2.tgz"), dj,
		perFile("cu-files", "cu", "9.5", "9.6", "9.7"),
		perFile("go-files", "gofiles", "1.22.0", "1.22.6", "1.23.0", "1.23.4"),
		perFile("rnd-files", "rnd", "0", "1", "2"),
		perFile("synth", "synth", "0", "1", "2"),
	}
}

// b9Lanes: B8 with record files (b8), the B8-G grouped price on record files (b8m: planned, the store is
// written with files as B8 would), and the B9 grouped store (b9).
func b9Lanes() []b6Lane {
	np := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	op := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	run := func(p OptimizePolicy, cfg string) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			p.Apply, p.AllowDelta, p.AllowRep = true, true, true
			old := agWanted
			defer func() { agWanted = old }()
			agWanted = cfg == "b9"
			if cfg == "b8m" {
				p.recordGroup, p.Apply = 72, false
			}
			return s.AuthorityOptimize(ctx, p)
		}
	}
	canon := func(exe bool) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			p := OptimizePolicy{AuthPolicy: op, Profile: "archive", Canonical: true, Executables: exe}
			p.Apply, p.AllowDelta, p.AllowRep = true, true, true
			return s.AuthorityOptimize(ctx, p)
		}
	}
	regroup := func(ctx context.Context, s *Store) (any, error) { // the b8 graph, its records moved into groups
		if err := s.ensureFeature(agFeature); err != nil {
			return nil, err
		}
		return s.agCompact(true)
	}
	lanes := []b6Lane{{"cdc", nil, ""}, {"b8c-archive", canon(false), ""}, {"b9n-archive", canon(true), ""}, {"b8g-archive", regroup, "b8-archive"}}
	for _, cfg := range []string{"b8", "b8m", "b9"} {
		lanes = append(lanes, b6Lane{cfg + "-archive", run(OptimizePolicy{AuthPolicy: op, Profile: "archive"}, cfg), ""},
			b6Lane{cfg + "-mixed", run(OptimizePolicy{AuthPolicy: np, Profile: "mixed"}, cfg), ""})
	}
	return lanes
}

func b9Campaign(t *testing.T, test func(*testing.T)) {
	b6FamilySet, b6LaneSet = b9Families, b9Lanes
	defer func() { b6FamilySet, b6LaneSet = b6Families, b6Lanes }()
	test(t)
}

func TestB9Build(t *testing.T) { b9Campaign(t, TestB6Build) }

// TestB9Lanes materializes every lane (B6's runner: optimize, gc + repack + compact, allocation, deep
// verify, the materialized model) and adds the record-store breakdown of each store.
func TestB9Lanes(t *testing.T) {
	b9Campaign(t, func(t *testing.T) {
		TestB6Lanes(t)
		fx := os.Getenv("ZEROS3_B6_FX")
		for _, f := range b9Families(os.Getenv("ZEROS3_B6_CORPUS")) {
			for _, ln := range b9Lanes() {
				if ln.run == nil || !b6Want("ZEROS3_B6_FAMILIES", f.name) || !b6Want("ZEROS3_B6_LANES", ln.name) {
					continue
				}
				dir := filepath.Join(fx, f.name, ln.name)
				if _, err := os.Stat(filepath.Join(dir, ".b9-records")); err == nil {
					continue
				}
				s, done := b6Open(t, dir)
				row := b9RecordStats(t, s, dir)
				row["kind"], row["family"], row["lane"] = "records", f.name, ln.name
				done()
				b6Emit(row)
				b, _ := json.Marshal(row)
				os.WriteFile(filepath.Join(dir, ".b9-records"), b, 0o644)
				t.Logf("%s", b)
			}
		}
	})
}

// b9RecordStats is the physical record store of one lane: record files and group files (allocated), the
// grouped frames, seal indexes, live and dead bytes, depth, and record lookup latency (cold and warm).
func b9RecordStats(t *testing.T, s *Store, dir string) map[string]any {
	row := map[string]any{}
	var fileAlloc, fileN, groupAlloc, groupN int64
	walk := func(root string, fn func(st *syscall.Stat_t, name string)) {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			var st syscall.Stat_t
			if err == nil && !d.IsDir() && syscall.Stat(p, &st) == nil {
				fn(&st, d.Name())
			}
			return nil
		})
	}
	walk(filepath.Join(dir, "bodies"), func(st *syscall.Stat_t, name string) {
		switch filepath.Ext(name) {
		case ".body":
			fileAlloc += st.Blocks * 512
			fileN++
		case agSuffix:
			groupAlloc += st.Blocks * 512
			groupN++
		}
	})
	files, _ := s.authFiles()
	var recBytes, frames, maxDepth int64
	for _, f := range files {
		r, err := s.authLoad(f)
		if err != nil || r == nil {
			t.Fatalf("record %x: %v", f[:4], err)
		}
		b, _ := r.marshal()
		recBytes += int64(len(b))
		maxDepth = max(maxDepth, int64(r.Depth))
		if _, known, _ := s.agLookup(f); known {
			frames += s.agFrameBytes(f)
		}
	}
	ag, err := s.agCompact(false) // reports only: a reclaimed store has nothing below the live threshold
	if err != nil {
		t.Fatal(err)
	}
	row["records"], row["record_bytes"], row["max_depth"] = len(files), recBytes, maxDepth
	row["file_records"], row["file_alloc"], row["groups"], row["group_alloc"] = fileN, fileAlloc, groupN, groupAlloc
	row["frame_bytes"], row["group_bytes"], row["group_live"] = frames, ag.GroupBytes, ag.LiveBytes
	if len(files) > 0 {
		row["alloc_per_record"] = float64(fileAlloc+groupAlloc) / float64(len(files))
	}
	// Record lookup latency: cold (page cache and the index dropped) and warm, over up to 200 records.
	pick := files
	if len(pick) > 200 {
		pick = nil
		for i := range 200 {
			pick = append(pick, files[i*len(files)/200])
		}
	}
	var cold, open, warm []float64
	for _, f := range pick {
		dropCaches() // page cache dropped, the group index kept (a running server)
		t0 := time.Now()
		if r, err := s.authLoad(f); err != nil || r == nil {
			t.Fatal(err)
		}
		cold = append(cold, float64(time.Since(t0).Microseconds()))
		t0 = time.Now()
		s.authLoad(f)
		warm = append(warm, float64(time.Since(t0).Nanoseconds())/1e3)
		dropCaches() // and the index rebuilt first (a fresh process)
		s.auth.ag.mu.Lock()
		s.auth.ag.reset()
		s.auth.ag.mu.Unlock()
		t0 = time.Now()
		s.authLoad(f)
		open = append(open, float64(time.Since(t0).Microseconds()))
	}
	if len(pick) > 0 {
		row["lookup_cold_us_p50"], row["lookup_cold_us_p95"] = b9Pct(cold, .5), b9Pct(cold, .95)
		row["lookup_open_us_p50"], row["lookup_open_us_p95"] = b9Pct(open, .5), b9Pct(open, .95)
		row["lookup_warm_us_p50"], row["lookup_warm_us_p95"] = b9Pct(warm, .5), b9Pct(warm, .95)
	}
	// Recovery: index rebuild from the group files (open path), cold.
	dropCaches()
	t0 := time.Now()
	s.auth.ag.mu.Lock()
	if err := s.agReloadLocked(); err != nil {
		t.Fatal(err)
	}
	s.auth.ag.mu.Unlock()
	row["index_rebuild_cold_ms"] = float64(time.Since(t0).Microseconds()) / 1e3
	return row
}

// TestB9Append times publication of N small pin records one by one (fsync each) as record files and as
// grouped records, then their retirement (gc batch), in a scratch store; and the index rebuild of the result.
func TestB9Append(t *testing.T) {
	n := 1000
	for _, grouped := range []bool{false, true} {
		old := agWanted
		agWanted = grouped
		l := newAuthLab(t)
		l.put("x", genRandomBytes(7, 200_000))
		ps, err := chunkPieces(l.man("x"))
		if err != nil {
			t.Fatal(err)
		}
		var shas [][32]byte
		var lat []float64
		w0, t0 := b6ProcIO("write_bytes"), time.Now()
		for i := range n {
			r := &authRecord{SHA: sha256.Sum256([]byte(fmt.Sprint("b9append", i))), Size: 200_000, Gen: 1, Form: authFormPin, Pieces: ps}
			t1 := time.Now()
			if err := l.s.authPublish(r); err != nil {
				t.Fatal(err)
			}
			lat = append(lat, float64(time.Since(t1).Microseconds())/1e3)
			shas = append(shas, r.SHA)
		}
		wall, written := time.Since(t0).Seconds(), b6ProcIO("write_bytes")-w0
		alloc, _ := allocatedBytes(filepath.Join(l.dir, "bodies"))
		t1 := time.Now()
		if err := l.s.authRetireMany(shas, false); err != nil {
			t.Fatal(err)
		}
		retire := time.Since(t1).Seconds()
		t1 = time.Now()
		ag, err := l.s.agCompact(false)
		if err != nil {
			t.Fatal(err)
		}
		row := map[string]any{"kind": "append", "grouped": grouped, "records": n, "publish_per_s": float64(n) / wall, "publish_p50_ms": b9Pct(lat, .5),
			"publish_p99_ms": b9Pct(lat, .99), "written": written, "bodies_alloc": alloc, "retire_s": retire, "compact_s": time.Since(t1).Seconds(),
			"compact_reclaimed": ag.BytesReclaimed}
		b6Emit(row)
		t.Logf("%v", row)
		l.close()
		agWanted = old
	}
}

func TestB9Reads(t *testing.T)      { b9Campaign(t, TestB6Reads) }
func TestB9Concurrent(t *testing.T) { b9Campaign(t, TestB6Concurrent) }

// ---- B9-B probe: delta literals by section, raw vs normalized (B9_EXE_DIR/<version>/<B9_TOOL>) ----

type exeSec struct {
	name     string
	off, end int64
}

func exeSections(b []byte) []exeSec {
	var out []exeSec
	if string(b[:4]) != "\x7fELF" {
		return nil
	}
	shoff, shnum, shstr := int64(binary.LittleEndian.Uint64(b[40:])), int64(binary.LittleEndian.Uint16(b[60:])), int64(binary.LittleEndian.Uint16(b[62:]))
	if string(b[:4]) != "\x7fELF" {
		return nil
	}
	if shoff == 0 {
		return nil
	}
	sh := func(i int64) []byte { return b[shoff+i*64:] }
	strOff := int64(binary.LittleEndian.Uint64(sh(shstr)[24:]))
	for i := int64(0); i < shnum; i++ {
		h := sh(i)
		no := int64(binary.LittleEndian.Uint32(h))
		name := b[strOff+no:]
		name = name[:bytes.IndexByte(name, 0)]
		off, size := int64(binary.LittleEndian.Uint64(h[24:])), int64(binary.LittleEndian.Uint64(h[32:]))
		if binary.LittleEndian.Uint32(h[4:]) == 8 {
			continue
		}
		out = append(out, exeSec{string(name), off, off + size})
	}
	return out
}

func exeSecOf(s []exeSec, pos int64) string {
	for _, x := range s {
		if pos >= x.off && pos < x.end && x.name != "" {
			return x.name
		}
	}
	return "other"
}

func TestB9ExeProbe(t *testing.T) {
	dir := os.Getenv("B9_EXE_DIR")
	if dir == "" {
		t.Skip("B9_EXE_DIR")
	}
	vers := []string{"1.22.0", "1.22.6", "1.23.0", "1.23.4"}
	for _, tool := range []string{os.Getenv("B9_TOOL")} {
		var prevRaw, prevCanon []byte
		for _, v := range vers {
			raw, err := os.ReadFile(filepath.Join(dir, v, tool))
			if err != nil {
				t.Fatal(err)
			}
			t0 := time.Now()
			im, err := exeParse(raw)
			if err != nil {
				t.Fatalf("%s %s: %v", tool, v, err)
			}
			canon := bytes.Clone(raw)
			tb, err := exeNormalize(canon, im)
			if err != nil {
				t.Fatal(err)
			}
			if os.Getenv("B9_PCLN") != "" {
				b9PclnDelta(t, canon, im)
			}
			canon = append(canon, exeUnitTable(im.units)...)
			nt := time.Since(t0)
			// round trip
			back := bytes.Clone(canon[:len(raw)])
			for ri, r := range tb.regions {
				if r.code {
					tb.exeRestore(ri, back[r.off:r.off+r.size], 0, 0, r.size)
				} else {
					tb.restorePtrs(ri, back[r.off:r.off+r.size], 0, 0, r.size)
				}
			}
			ndiff := 0
			for i := range raw {
				if back[i] != raw[i] {
					ndiff++
				}
			}
			changed := 0
			for i := range raw {
				if canon[i] != raw[i] {
					changed++
				}
			}
			t.Logf("%s %s: size %d units %d regions %d exceptions %d changed bytes %d unit table %d normalize %v roundtrip diffs %d", tool, v, len(raw), len(im.units), len(im.regions), len(tb.except), changed, len(canon)-len(raw), nt, ndiff)
			if prevRaw != nil {
				dr, _, err1 := xdEncode([]xdSrc{{data: prevRaw}}, raw, 1<<20)
				dn, _, err2 := xdEncode([]xdSrc{{data: prevCanon}}, canon, 1<<20)
				t.Logf("  delta raw %d norm %d (%.1f%%) err %v %v", len(dr), len(dn), 100*float64(len(dn))/float64(len(dr)), err1, err2)
				for _, pair := range [][2][]byte{{prevRaw, raw}, {prevCanon, canon}} {
					x, _ := dwDcoreBuildIndex([][]byte{pair[0]}, dwDcoreDefaultParams())
					ops := xdFromDw(x.Match(pair[1]))
					lit := map[string]int64{}
					sec := exeSections(raw)
					for _, o := range ops {
						if o.base < 0 {
							lit[exeSecOf(sec, o.tpos)] += o.n
						}
					}
					t.Logf("    literals by section: %v", lit)
				}
			}
			prevRaw, prevCanon = raw, canon
		}
	}
}

// ---- B9-C decoded-window cache campaign ------------------------------------------

type b9Req struct {
	obj  int
	a, n int64
}

// b9Workloads are the pre-registered access patterns over objs (sizes): each is a list of requests.
func b9Workloads(sizes []int64, seed uint64) map[string][]b9Req {
	r := rand.New(rand.NewPCG(seed, 7))
	pick := func() (int, int64) {
		o := r.IntN(len(sizes))
		return o, int64(r.IntN(int(max(1, sizes[o]-65536))))
	}
	w := map[string][]b9Req{}
	for range 64 { // cold: distinct first touches
		o, a := pick()
		w["cold"] = append(w["cold"], b9Req{o, a, 4096})
	}
	var hot []b9Req
	for range 16 {
		o, a := pick()
		hot = append(hot, b9Req{o, a, 4096})
	}
	for range 8 {
		w["repeat"] = append(w["repeat"], hot...)
	}
	for range 128 { // nearby: within 48 KiB after one of 16 anchors
		h := hot[r.IntN(len(hot))]
		a := min(h.a+int64(r.IntN(48<<10)), sizes[h.obj]-4096)
		w["nearby"] = append(w["nearby"], b9Req{h.obj, max(0, a), 4096})
	}
	off := int64(r.IntN(1 << 20))
	for range 4 { // the same offset in every object (objects sharing canonical streams)
		for o := range sizes {
			w["shared"] = append(w["shared"], b9Req{o, min(off, max(0, sizes[o]-4096)), 4096})
		}
	}
	z := rand.NewZipf(rand.New(rand.NewPCG(seed, 8)), 1.1, 1, 255)
	var pool []b9Req
	for range 256 {
		o, a := pick()
		pool = append(pool, b9Req{o, a, []int64{4096, 4096, 4096, 65536}[r.IntN(4)]})
	}
	for range 400 {
		w["zipf"] = append(w["zipf"], pool[z.Uint64()])
	}
	for range 128 {
		o, a := pick()
		w["unrelated"] = append(w["unrelated"], b9Req{o, a, 4096})
	}
	return w
}

func b9Pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := slices.Clone(xs)
	slices.Sort(ys)
	return ys[min(len(ys)-1, int(float64(len(ys))*p))]
}

// TestB9Cache runs every workload with the cache off and on (matched, both orders) on a lane store, then
// the concurrent Zipf workload with 8 readers and the capacity sweep. Every read is checked byte for byte.
func TestB9Cache(t *testing.T) {
	fx := os.Getenv("ZEROS3_B6_FX")
	lane := cmp.Or(os.Getenv("ZEROS3_B9_LANE"), "b8c-archive")
	if v, err := strconv.Atoi(os.Getenv("ZEROS3_B9_WINDOW_KIB")); err == nil && v > 0 {
		old := winSize
		winSize = int64(v) << 10
		defer func() { winSize = old }()
	}
	for _, fam := range strings.Split(cmp.Or(os.Getenv("ZEROS3_B6_FAMILIES"), "ts,django"), ",") {
		dir := filepath.Join(fx, fam, lane)
		s, done := b6Open(t, dir)
		objs := b6Objects(t, s, 0)
		var sizes []int64
		var truth [][]byte
		for _, o := range objs {
			b, err := b6Read(s, o.man, 0, o.man.TotalLength)
			if err != nil {
				t.Fatal(err)
			}
			truth = append(truth, b)
			sizes = append(sizes, o.man.TotalLength)
		}
		ws := b9Workloads(sizes, 11)
		run := func(name string, budget int64, reqs []b9Req, readers int, cold bool) map[string]any {
			s.SetWindowCache(budget)
			s.purgeReadCaches()
			if cold {
				dropCaches()
			}
			var mu sync.Mutex
			var lat []float64
			var next atomic.Int64
			cpu0, t0 := procCPUSeconds(), time.Now()
			var wg sync.WaitGroup
			for range readers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						i := int(next.Add(1)) - 1
						if i >= len(reqs) {
							return
						}
						q := reqs[i]
						n := min(q.n, sizes[q.obj]-q.a)
						t1 := time.Now()
						got, err := b6Read(s, objs[q.obj].man, q.a, n)
						d := float64(time.Since(t1).Microseconds()) / 1e3
						if err != nil || !bytes.Equal(got, truth[q.obj][q.a:q.a+n]) {
							t.Errorf("%s %s %d %d+%d: %v", fam, name, q.obj, q.a, n, err)
							return
						}
						mu.Lock()
						lat = append(lat, d)
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			wall := time.Since(t0).Seconds()
			st := s.WindowCacheStats()
			row := map[string]any{"kind": "cache", "family": fam, "lane": lane, "workload": name, "budget_mib": budget >> 20, "readers": readers,
				"requests": len(reqs), "wall_s": wall, "cpu_s": procCPUSeconds() - cpu0, "rps": float64(len(reqs)) / wall,
				"p50_ms": b9Pct(lat, .5), "p95_ms": b9Pct(lat, .95), "p99_ms": b9Pct(lat, .99), "hits": st.Hits, "misses": st.Misses,
				"joins": st.Joins, "evictions": st.Evictions, "cache_bytes": st.Bytes, "decode_ms": float64(st.DecodeNS) / 1e6, "window_kib": winSize >> 10, "admission": "second-touch", "first_touches": st.Bypass}
			if st.Hits+st.Misses+st.Joins > 0 {
				row["hit_rate"] = float64(st.Hits+st.Joins) / float64(st.Hits+st.Misses+st.Joins)
				if st.Misses > 0 {
					row["decode_avoided_ms"] = float64(st.DecodeNS) / float64(st.Misses) * float64(st.Hits+st.Joins) / 1e6
				}
			}
			b6Emit(row)
			t.Logf("%v", row)
			return row
		}
		for _, order := range [][2]int64{{0, 64 << 20}, {64 << 20, 0}} {
			for _, b := range order {
				for _, name := range []string{"cold", "repeat", "nearby", "shared", "unrelated"} {
					run(name, b, ws[name], 1, true)
				}
				run("zipf-8", b, ws["zipf"], 8, true)
			}
		}
		for _, b := range []int64{16 << 20, 64 << 20, 256 << 20} {
			run("zipf-8", b, ws["zipf"], 8, true)
		}
		done()
	}
}

// b9PclnDelta (probe only, forward transform) rewrites the Go function table's shifting u32 columns as
// differences from the previous function's value: functab funcoff, and every _func offset field.
func b9PclnDelta(t *testing.T, b []byte, im *exeImage) {
	at := bytes.Index(b, []byte{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00, 0x01, 0x08})
	if at < 0 {
		return
	}
	p := b[at:]
	u64 := func(o int) uint64 { return binary.LittleEndian.Uint64(p[o:]) }
	nfunc, pcln := int(u64(8)), int(u64(64))
	ft := p[pcln:]
	type rec struct{ off, n int }
	var recs []rec
	for i := range nfunc {
		fo := int(binary.LittleEndian.Uint32(ft[i*8+4:]))
		f := ft[fo:]
		npc, nfd := int(binary.LittleEndian.Uint32(f[28:])), int(f[43])
		recs = append(recs, rec{fo, 44 + 4*npc + 4*nfd})
	}
	// columns: functab funcoff; _func nameOff(4), pcsp(16), pcfile(20), pcln(24), cuOffset(32), pcdata[k], funcdata[k]
	prevFO := uint32(0)
	prev := map[int]uint32{}
	for i := range nfunc {
		fo := binary.LittleEndian.Uint32(ft[i*8+4:])
		binary.LittleEndian.PutUint32(ft[i*8+4:], fo-prevFO)
		prevFO = fo
	}
	for _, r := range recs {
		f := ft[r.off : r.off+r.n]
		npc := int(binary.LittleEndian.Uint32(f[28:]))
		cols := []int{4, 16, 20, 24, 32}
		for k := range npc {
			cols = append(cols, 44+4*k)
		}
		for k := range int(f[43]) {
			cols = append(cols, 44+4*npc+4*k)
		}
		for ci, c := range cols {
			key := ci
			if c >= 44 {
				key = 100 + c - 44
			}
			v := binary.LittleEndian.Uint32(f[c:])
			if v == 0 || v == 0xffffffff {
				continue
			}
			binary.LittleEndian.PutUint32(f[c:], v-prev[key])
			prev[key] = v
		}
	}
}
