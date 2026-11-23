//go:build b6bench && b5locality && b1rangeexperiment

package main

// B6 global-optimization campaign (not a regression test): go test -tags b6bench,b5locality,b1rangeexperiment.
//
//	ZEROS3_B6_CORPUS    corpus (fetch_corpus.sh tars+git, b5/make_inputs.py, b6/make_inputs.py, ts/, go/)
//	ZEROS3_B6_FX        fixture root: <family>/<lane> stores
//	ZEROS3_B6_OUT       JSONL rows (appended)
//	ZEROS3_B6_FAMILIES  comma-separated family filter; ZEROS3_B6_LANES lane filter
//	ZEROS3_B6_N         cold 4 KiB reads per object (64 KiB: half), default 12
//	ZEROS3_B5_COLD=1    drop the page cache before every cold read (needs root)

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type b6Put struct{ key, file string }

type b6Family struct {
	name string
	puts []b6Put
}

func b6Families(c string) []b6Family {
	hist := func(name, key string, files ...string) b6Family {
		f := b6Family{name: name}
		for _, p := range files {
			f.puts = append(f.puts, b6Put{key, filepath.Join(c, p)})
		}
		return f
	}
	fams := []b6Family{
		hist("coreutils", "coreutils.tar", "src/coreutils-9.5.tar", "src/coreutils-9.6.tar", "src/coreutils-9.7.tar"),
		hist("go", "compile.tar", "go/compile-1.22.0.tar", "go/compile-1.22.6.tar", "go/compile-1.23.0.tar", "go/compile-1.23.4.tar"),
		hist("sqlite", "app.db", "sqlite/v0.db", "sqlite/v1.db", "sqlite/v2.db", "sqlite/v3.db"),
		hist("git", "hiredis.pack", "git/h0.pack", "git/h1.pack", "git/h2.pack"),
		hist("ts", "typescript.tgz", "ts/typescript-5.6.2.tgz", "ts/typescript-5.6.3.tgz", "ts/typescript-5.7.2.tgz"),
		hist("random", "r.bin", "random/r0.bin", "random/r1.bin", "random/r2.bin"),
		{name: "bundle", puts: []b6Put{{"vendor/c.tar", filepath.Join(c, "bundle/c.tar")}, {"vendor/a.tar", filepath.Join(c, "bundle/a.tar")}, {"vendor/b.tar", filepath.Join(c, "bundle/b.tar")}}},
	}
	// Per-file Coreutils history: every src/ and lib/ member of at least 4 KiB, three releases.
	cu := b6Family{name: "cu-files"}
	for _, v := range []string{"9.5", "9.6", "9.7"} {
		root := filepath.Join(c, "cu", v)
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
			cu.puts = append(cu.puts, b6Put{rel, p})
		}
	}
	fams = append(fams, cu)
	return fams
}

// b6FamilySet and b6LaneSet are what the lane, read and concurrency campaigns iterate (B7 reuses them).
var b6FamilySet, b6LaneSet = b6Families, b6Lanes

func b6Want(env, name string) bool {
	v := os.Getenv(env)
	return v == "" || slices.Contains(strings.Split(v, ","), name)
}

func b6Emit(row map[string]any) {
	out := os.Getenv("ZEROS3_B6_OUT")
	if out == "" {
		return
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(row)
	f.Write(append(b, '\n'))
}

func b6Open(t *testing.T, dir string) (*Store, func()) {
	t.Helper()
	lock, err := acquireStoreLock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		lock.release()
		t.Fatal(err)
	}
	return s, func() { s.Close(); lock.release() }
}

// TestB6Build writes every family's CDC store (versions put oldest first under their history key) and reclaims it.
func TestB6Build(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, f := range b6FamilySet(c) {
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

// b6Lane is one matched configuration applied to a copy of the family's CDC store.
type b6Lane struct {
	name string
	run  func(ctx context.Context, s *Store) (any, error)
	from string // lane whose materialized store this one starts from ("" = cdc)
}

func b6Opt(p OptimizePolicy) func(context.Context, *Store) (any, error) {
	return func(ctx context.Context, s *Store) (any, error) {
		p.Apply = true
		p.AllowDelta, p.AllowRep = true, true
		p.Pareto = []float64{0, 2, 5, 20, 100, 400}
		p.Oracle = true
		return s.AuthorityOptimize(ctx, p)
	}
}

func b6Lanes() []b6Lane {
	local := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, MaxReadAmp: 4, MinReadRatio: 0.5, AllowDelta: true, AllowRep: true}
	archive := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, AllowDelta: true, AllowRep: true}
	mig := func(p AuthPolicy) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			p.Apply = true
			return s.AuthorityMigrateContext(ctx, p)
		}
	}
	np := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	op := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	return []b6Lane{
		{"cdc", nil, ""},
		{"local", mig(local), ""},
		{"local-archive", mig(archive), ""},
		{"seq-loc", b6Opt(OptimizePolicy{AuthPolicy: np, Algorithm: "local", Profile: "archive", Locality: []int{0, 256}}), ""},
		{"global", b6Opt(OptimizePolicy{AuthPolicy: np, Profile: "mixed", Locality: []int{0}}), ""},
		{"global-loc", b6Opt(OptimizePolicy{AuthPolicy: np, Profile: "mixed", Locality: []int{0, 256}}), ""},
		{"global-archive", b6Opt(OptimizePolicy{AuthPolicy: op, Profile: "archive", Locality: []int{0, 256}}), ""},
		{"global-hot", b6Opt(OptimizePolicy{AuthPolicy: np, Profile: "hot", Locality: []int{0, 256}}), ""},
		{"global-loc3", b6Opt(OptimizePolicy{AuthPolicy: np, Profile: "mixed", Locality: []int{0, 32, 256}}), ""},
		{"global-archive3", b6Opt(OptimizePolicy{AuthPolicy: op, Profile: "archive", Locality: []int{0, 32, 256}}), ""},
		{"reopt", b6Opt(OptimizePolicy{AuthPolicy: np, Profile: "mixed", Locality: []int{0, 256}}), "local"},
		{"reopt-archive", b6Opt(OptimizePolicy{AuthPolicy: op, Profile: "archive", Locality: []int{0, 256}}), "local-archive"},
	}
}

func b6ProcIO(field string) int64 {
	b, _ := os.ReadFile("/proc/self/io")
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && k == field {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// TestB6Lanes materializes every lane of every family and records storage, migration cost and the plans.
func TestB6Lanes(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	for _, f := range b6FamilySet(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		base := filepath.Join(fx, f.name, "cdc")
		for _, ln := range b6LaneSet() {
			if !b6Want("ZEROS3_B6_LANES", ln.name) {
				continue
			}
			dir := filepath.Join(fx, f.name, ln.name)
			marker := filepath.Join(dir, ".b6-ready")
			if ln.run == nil {
				marker = filepath.Join(base, ".b6-lane")
			}
			if _, err := os.Stat(marker); err == nil {
				continue
			}
			row := map[string]any{"kind": "lane", "family": f.name, "lane": ln.name}
			if ln.run != nil {
				src := base
				if ln.from != "" {
					src = filepath.Join(fx, f.name, ln.from)
					if _, err := os.Stat(filepath.Join(src, ".b6-ready")); err != nil {
						t.Fatalf("%s/%s needs lane %s", f.name, ln.name, ln.from)
					}
				}
				os.RemoveAll(dir)
				if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
					t.Fatal(err)
				}
				os.Remove(filepath.Join(dir, "LOCK"))
				os.Remove(filepath.Join(dir, ".b6-ready"))
				s, done := b6Open(t, dir)
				resetRSSPeak()
				w0, cpu0, t0 := b6ProcIO("write_bytes"), procCPUSeconds(), time.Now()
				res, err := ln.run(context.Background(), s)
				row["migrate_wall_s"], row["migrate_cpu_s"] = time.Since(t0).Seconds(), procCPUSeconds()-cpu0
				row["migrate_written"], row["migrate_rss_mib"] = b6ProcIO("write_bytes")-w0, rssPeakMiB()
				done()
				if err != nil {
					t.Fatalf("%s/%s: %v", f.name, ln.name, err)
				}
				row["report"] = res
				peak, _ := allocatedBytes(dir)
				row["peak_allocated"] = peak
				gcs, rps := econReclaim(t, dir)
				row["reclaim_s"] = gcs + rps
			} else {
				dir = base
			}
			alloc, apparent := allocatedBytes(dir)
			row["allocated"], row["apparent"] = alloc, apparent
			s, done := b6Open(t, dir)
			row["retained_model_units"] = b6Measured(t, s)
			st, err := s.AuthorityStatus()
			if err == nil {
				row["authority"] = st
			}
			// the model's view of the materialized graph (no candidates): exact check of the plan's estimate
			g, err := s.rgBuild(context.Background(), OptimizePolicy{}.norm())
			if err == nil {
				g.partition()
				e := newRGEval(g, g.currentSel())
				row["materialized_model"] = e.state("materialized", true)
			}
			res, err := s.Verify(true)
			row["verify_ok"] = err == nil && res.OK()
			done()
			b6Emit(row)
			b, _ := json.Marshal(map[string]any{"family": f.name, "lane": ln.name, "allocated": alloc, "verify": row["verify_ok"], "wall": row["migrate_wall_s"]})
			t.Logf("%s", b)
			os.WriteFile(marker, b, 0o644)
		}
	}
}

// b6Obj is one live content of a lane store (any retained version).
type b6Obj struct {
	key  string
	sha  string
	man  manifestV1
	data []byte
}

func b6Objects(t *testing.T, s *Store, max int) []b6Obj {
	var all []b6Obj
	seen := map[string]bool{}
	if err := s.eachLiveManifest(nil, func(lm liveMan) error {
		if !seen[lm.man.ObjectSHA256] && lm.man.TotalLength >= 4096 {
			seen[lm.man.ObjectSHA256] = true
			all = append(all, b6Obj{key: lm.key, sha: lm.man.ObjectSHA256, man: lm.man})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(all, func(a, b b6Obj) int { return strings.Compare(a.sha, b.sha) })
	if max > 0 && len(all) > max {
		var out []b6Obj
		for i := range max {
			out = append(out, all[i*len(all)/max])
		}
		all = out
	}
	return all
}

func b6Read(s *Store, man manifestV1, off, n int64) ([]byte, error) {
	rd := s.openObjectReader(man, byteRange{off, off + n - 1})
	defer rd.close()
	var out []byte
	for {
		b, err := rd.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
}

type b6Cfg struct {
	name string
	s    *Store
	x    *b5Tracer
	g    *rgGraph
	e    *rgEval
	done func()
}

// TestB6Reads reads every sampled content of every lane at the same offsets in rotating lane order: cold
// 4 KiB and 64 KiB ranges (page cache dropped, in-process caches emptied), a warm repeat, and whole reads.
// Each row carries the model's prediction for the same read on the same graph.
func TestB6Reads(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	n4 := 12
	if v, err := strconv.Atoi(os.Getenv("ZEROS3_B6_N")); err == nil && v > 0 {
		n4 = v
	}
	for _, f := range b6FamilySet(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		var cfgs []*b6Cfg
		for _, ln := range b6LaneSet() {
			dir := filepath.Join(fx, f.name, ln.name)
			if ln.run == nil {
				dir = filepath.Join(fx, f.name, "cdc")
			}
			if !b6Want("ZEROS3_B6_LANES", ln.name) {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, ".b6-ready")); err != nil {
				continue
			}
			s, done := b6Open(t, dir)
			cf := &b6Cfg{name: ln.name, s: s, x: newB5Tracer(s), done: done}
			if g, err := s.rgBuild(context.Background(), OptimizePolicy{}.norm()); err == nil {
				g.partition()
				cf.g, cf.e = g, newRGEval(g, g.currentSel())
			}
			cfgs = append(cfgs, cf)
		}
		if len(cfgs) == 0 {
			continue
		}
		objs := b6Objects(t, cfgs[0].s, 24)
		for i := range objs {
			d, err := b6Read(cfgs[0].s, objs[i].man, 0, objs[i].man.TotalLength)
			if err != nil || hex.EncodeToString(func() []byte { h := sha256.Sum256(d); return h[:] }()) != objs[i].sha {
				t.Fatalf("%s %s: reference read", f.name, objs[i].key)
			}
			objs[i].data = d
		}
		rot := 0
		measure := func(o b6Obj, mode string, off, n int64, cold bool) {
			for k := range cfgs {
				cf := cfgs[(k+rot)%len(cfgs)]
				if cold {
					b5Purge(cf.s)
					b5Drop()
				}
				cf.x.reset()
				io0, sec0 := b5DiskReads()
				cpu0, t0 := procCPUSeconds(), time.Now()
				got, err := b6Read(cf.s, o.man, off, n)
				wall, cpu := time.Since(t0), procCPUSeconds()-cpu0
				io1, sec1 := b5DiskReads()
				if err != nil || string(got) != string(o.data[off:off+n]) {
					t.Fatalf("%s/%s %s %d+%d: inexact (%v)", f.name, cf.name, o.key, off, n, err)
				}
				r := b5Row{Label: f.name, Store: cf.name, Key: o.key, Mode: mode, Off: off, Len: n, DiskReads: io1 - io0, DiskBytes: (sec1 - sec0) * 512,
					WallUS: wall.Microseconds(), CPUUS: int64(cpu * 1e6)}
				cf.x.summarize(&r)
				row := map[string]any{"kind": "read", "family": f.name, "lane": cf.name, "sha": o.sha[:12], "row": r}
				if cf.e != nil {
					sum, _ := decodeHexSHA256(o.sha)
					if v, ok := cf.g.idx[sum]; ok {
						ms := cf.g.readStats(cf.e, v, off, off+n)
						row["model_us"], row["model_records"], row["model_decoded"], row["model_nodes"] = ms.US, ms.Records, ms.Decoded, ms.Nodes
						row["model_whole_us"] = cf.g.wholeCost(cf.e, v)
						row["depth"] = cf.e.depth(v)
						row["form"] = cf.e.opt(v).label
					}
				}
				b6Emit(row)
			}
			rot++
		}
		for _, o := range objs {
			size := o.man.TotalLength
			seed := binary.LittleEndian.Uint64(func() []byte { b, _ := hex.DecodeString(o.sha[:16]); return b }())
			for i := range n4 {
				off := int64((seed + uint64(i)*0x9e3779b97f4a7c15) % uint64(max(1, size-4096)))
				measure(o, "cold4k", off, min(4096, size-off), true)
				if i == 0 {
					measure(o, "warm4k", off, min(4096, size-off), false)
				}
				if i%2 == 0 && size > 64<<10 {
					off64 := int64((seed ^ uint64(i)*0xbf58476d1ce4e5b9) % uint64(size-(64<<10)))
					measure(o, "cold64k", off64, 64<<10, true)
				}
			}
			for range 2 {
				measure(o, "whole", 0, size, true)
			}
		}
		for _, cf := range cfgs {
			cf.done()
		}
	}
}

// TestB6Concurrent runs 8 closed-loop readers of 4 KiB (75%) and 64 KiB ranges over every sampled content of
// each lane for ZEROS3_B6_SECONDS (default 10), starting cold; lanes are run in both orders.
func TestB6Concurrent(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	secs := 10
	if v, err := strconv.Atoi(os.Getenv("ZEROS3_B6_SECONDS")); err == nil && v > 0 {
		secs = v
	}
	for _, f := range b6FamilySet(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		var lanes []string
		for _, ln := range b6LaneSet() {
			if b6Want("ZEROS3_B6_LANES", ln.name) {
				lanes = append(lanes, ln.name)
			}
		}
		rev := slices.Clone(lanes)
		slices.Reverse(rev)
		for _, order := range [][]string{lanes, rev} {
			for _, name := range order {
				dir := filepath.Join(fx, f.name, name)
				if name == "cdc" {
					dir = filepath.Join(fx, f.name, "cdc")
				}
				if _, err := os.Stat(filepath.Join(dir, ".b6-ready")); err != nil {
					continue
				}
				s, done := b6Open(t, dir)
				objs := b6Objects(t, s, 24)
				b5Purge(s)
				b5Drop()
				lat := make([][]int64, 8)
				stop := time.Now().Add(time.Duration(secs) * time.Second)
				errs := make(chan error, 8)
				cpu0 := procCPUSeconds()
				for w := range 8 {
					go func() {
						r := uint64(w*7919 + 1)
						for time.Now().Before(stop) {
							r = r*6364136223846793005 + 1442695040888963407
							o := objs[(r>>33)%uint64(len(objs))]
							n := int64(4096)
							if (r>>20)%4 == 0 {
								n = 64 << 10
							}
							n = min(n, o.man.TotalLength)
							off := int64((r >> 7) % uint64(o.man.TotalLength-n+1))
							t0 := time.Now()
							if _, err := b6Read(s, o.man, off, n); err != nil {
								errs <- err
								return
							}
							lat[w] = append(lat[w], time.Since(t0).Microseconds())
						}
						errs <- nil
					}()
				}
				for range 8 {
					if err := <-errs; err != nil {
						t.Fatal(err)
					}
				}
				cpu := procCPUSeconds() - cpu0
				done()
				var all []int64
				for _, l := range lat {
					all = append(all, l...)
				}
				slices.Sort(all)
				q := func(p float64) int64 { return all[min(len(all)-1, int(p*float64(len(all))))] }
				b6Emit(map[string]any{"kind": "concurrent", "family": f.name, "lane": name, "order": order[0], "reads": len(all), "seconds": secs,
					"p50_us": q(0.5), "p95_us": q(0.95), "p99_us": q(0.99), "cpu_s": cpu})
				t.Logf("%s/%s: %d reads p50 %d p99 %d", f.name, name, len(all), q(0.5), q(0.99))
			}
		}
	}
}

// TestB6Scale times planning on a synthetic graph of n contents (40-90 KB) in 4-version histories with shared boilerplate
// (candidate generation, partitioning, search and memory), the bounded-search check beyond the real corpora.
func TestB6Scale(t *testing.T) {
	ns := []int{200, 800, 3200}
	if v := os.Getenv("ZEROS3_B6_SCALE"); v != "" {
		ns = nil
		for _, f := range strings.Split(v, ",") {
			n, _ := strconv.Atoi(f)
			ns = append(ns, n)
		}
	}
	for _, n := range ns {
		l := newAuthLab(t)
		hdr := b5Text(7, 2000)
		for i := range n / 4 {
			v := append(slices.Clone(hdr), b5Text(uint64(1000+i), 40000+i%7*8000)...)
			for k := range 4 {
				l.s.PutObject("b", fmt.Sprintf("f%05d", i), v, "", nil)
				v = b5Mutate(v, uint64(i*4+k), 3)
			}
		}
		l.reclaim()
		resetRSSPeak()
		t0 := time.Now()
		rep, err := l.s.AuthorityOptimize(context.Background(), OptimizePolicy{AuthPolicy: AuthPolicy{Order: "newest", MinSaving: 0.1, AllowDelta: true}, Profile: "mixed"})
		if err != nil {
			t.Fatal(err)
		}
		row := map[string]any{"kind": "scale", "contents": rep.Nodes, "options": rep.Options, "partitions": rep.Partitions, "largest": rep.LargestPartition,
			"encodes": rep.Encodes, "candidate_s": rep.CandidateSeconds, "search_s": rep.SearchSeconds, "evals": rep.Evals, "wall_s": time.Since(t0).Seconds(),
			"rss_mib": rssPeakMiB(), "heap_mib": rep.HeapMiB, "cdc": rep.CDCOnly.Bytes, "local": rep.Local.Bytes, "planned": rep.Planned.Bytes}
		b6Emit(row)
		b, _ := json.Marshal(row)
		t.Logf("%s", b)
		l.close()
	}
}

// TestB6Plan plans (no apply) on ZEROS3_B6_STORE and logs the report; for profiling the planner.
func TestB6Plan(t *testing.T) {
	dir := os.Getenv("ZEROS3_B6_STORE")
	if dir == "" {
		t.Skip("set ZEROS3_B6_STORE")
	}
	s, done := b6Open(t, dir)
	defer done()
	t0 := time.Now()
	rep, err := s.AuthorityOptimize(context.Background(), OptimizePolicy{AuthPolicy: AuthPolicy{Order: "newest", MinSaving: 0.1, AllowDelta: true}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	os.WriteFile(filepath.Join(os.TempDir(), "b6plan.json"), b, 0o644)
	t.Logf("%.1fs cand %.1f search %.2f encodes %d (%.1fs) evals %d", time.Since(t0).Seconds(), rep.CandidateSeconds, rep.SearchSeconds, rep.Encodes, rep.EncodeSeconds, rep.Evals)
}

// TestB6LocalitySweep encodes every candidate pair of each family's CDC store with each COPY-locality slack and
// records the envelope bytes, the modeled sparse read and the base scatter of the same probes: the share of
// base bytes a probe copies from outside the 256 KiB cluster around its median source (the B5 metric).
func TestB6LocalitySweep(t *testing.T) {
	fx, c := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_CORPUS")
	if fx == "" || c == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_CORPUS")
	}
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "64")
	slacks := []int{0, 32, 256, 1024, 4096}
	for _, f := range b6FamilySet(c) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) || f.name == "cu-files" {
			continue
		}
		dir := filepath.Join(fx, f.name, "cdc")
		s, done := b6Open(t, dir)
		pol := OptimizePolicy{AuthPolicy: AuthPolicy{Order: "oldest", AllowDelta: true, MaxBases: 1, Candidates: 2}, Locality: slacks}.norm()
		g, err := s.rgBuild(context.Background(), pol)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.generate(context.Background()); err != nil {
			t.Fatal(err)
		}
		e := newRGEval(g, g.currentSel())
		for i, nd := range g.nodes {
			v := int32(i)
			for k, o := range nd.opts {
				if o.kind != rgDelta || len(o.bases) != 1 {
					continue
				}
				e.set(v, int32(k))
				var bytes int64
				for _, cp := range o.comps {
					bytes += g.cphys[cp]
				}
				var scatter, copied float64
				for _, p := range rgProbes(nd.size)[:8] {
					var srcs []int64
					var ns []int64
					ci, _ := slices.BinarySearchFunc(o.copies, p[0], func(x rgCopy, y int64) int { return cmpI64(x.tpos+x.n, y+1) })
					for ; ci < len(o.copies) && o.copies[ci].tpos < p[1]; ci++ {
						cp := o.copies[ci]
						lo, hi := max(p[0], cp.tpos), min(p[1], cp.tpos+cp.n)
						srcs, ns = append(srcs, cp.off+lo-cp.tpos), append(ns, hi-lo)
					}
					if len(srcs) == 0 {
						continue
					}
					sorted := slices.Clone(srcs)
					slices.Sort(sorted)
					m := sorted[len(sorted)/2]
					for j, sOff := range srcs {
						copied += float64(ns[j])
						if sOff < m-128<<10 || sOff > m+128<<10 {
							scatter += float64(ns[j])
						}
					}
				}
				rs := g.readStats(e, v, 0, 0)
				_ = rs
				var c4 float64
				var recs, dec float64
				for _, p := range rgProbes(nd.size)[:8] {
					r := g.readStats(e, v, p[0], p[1])
					c4, recs, dec = c4+r.US/8, recs+float64(r.Records)/8, dec+float64(r.Decoded)/8
				}
				b6Emit(map[string]any{"kind": "locality", "family": f.name, "target": hex.EncodeToString(nd.sha[:6]), "base": hex.EncodeToString(g.nodes[o.bases[0]].sha[:6]),
					"slack": o.loc, "win": o.win, "bytes": bytes, "copies": len(o.copies), "model_4k_us": c4, "records_4k": recs, "decoded_4k": dec,
					"scatter": scatter / max(copied, 1)})
				e.set(v, nd.curOpt)
			}
		}
		done()
	}
}

func cmpI64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestB6Refit recomputes the model's features of every measured read in ZEROS3_B6_ROWS on its lane's graph
// (current code) and appends them to ZEROS3_B6_OUT as "feature" rows for fitting the model's coefficients.
func TestB6Refit(t *testing.T) {
	fx, in := os.Getenv("ZEROS3_B6_FX"), os.Getenv("ZEROS3_B6_ROWS")
	if fx == "" || in == "" {
		t.Skip("set ZEROS3_B6_FX and ZEROS3_B6_ROWS")
	}
	b, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ fam, lane string }
	graphs := map[key]*b6Cfg{}
	for _, l := range strings.Split(string(b), "\n") {
		var r struct {
			Kind, Family, Lane, Sha string
			Row                     b5Row
		}
		if json.Unmarshal([]byte(l), &r) != nil || r.Kind != "read" {
			continue
		}
		k := key{r.Family, r.Lane}
		cf := graphs[k]
		if cf == nil {
			dir := filepath.Join(fx, r.Family, r.Lane)
			s, done := b6Open(t, dir)
			g, err := s.rgBuild(context.Background(), OptimizePolicy{}.norm())
			if err != nil {
				t.Fatal(err)
			}
			g.partition()
			cf = &b6Cfg{name: r.Lane, s: s, g: g, e: newRGEval(g, g.currentSel()), done: done}
			graphs[k] = cf
		}
		var v int32 = -1
		for i, nd := range cf.g.nodes {
			if hex.EncodeToString(nd.sha[:])[:12] == r.Sha {
				v = int32(i)
			}
		}
		if v < 0 {
			continue
		}
		ms := cf.g.readStats(cf.e, v, r.Row.Off, r.Row.Off+r.Row.Len)
		f := ms.f
		var wrec, winfl int64
		if r.Row.Mode == "whole" {
			wrec, winfl = int64(f.rec[0]+f.rec[1]), f.infl[0]+f.infl[1]
		}
		b6Emit(map[string]any{"kind": "feature", "family": r.Family, "lane": r.Lane, "mode": r.Row.Mode, "wall_us": r.Row.WallUS, "cpu_us": r.Row.CPUUS,
			"disk_reads": r.Row.DiskReads, "records": f.rec[0], "inflated": f.infl[0], "raw": f.raw[0], "records_b": f.rec[1], "inflated_b": f.infl[1], "raw_b": f.raw[1], "nodes": ms.Nodes, "len": r.Row.Len, "depth": cf.e.depth(v),
			"size": cf.g.nodes[v].size, "traced_records": r.Row.Records, "traced_decoded": r.Row.LogicalBytes,
			"whole_records": wrec, "whole_inflated": winfl})
	}
	for _, cf := range graphs {
		cf.done()
	}
}
