package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"time"
	"zeros3-z3/dcore"
	"zeros3-z3/simdelta"
)

func runExtra(mode string, args []string, root string) bool {
	switch mode {
	case "index":
		cmdIndex(args)
	case "runs":
		cmdRuns(args)
	case "window":
		cmdWindow(args)
	default:
		return false
	}
	return true
}

func argInt(args []string, i int, def int) int {
	if i < len(args) {
		v, err := strconv.Atoi(args[i])
		must(err)
		return v
	}
	return def
}

func synth(n int, seed uint64) []dcore.Posting {
	r := rand.New(rand.NewPCG(seed, 99))
	p := make([]dcore.Posting, n*4)
	for i := range p {
		p[i] = dcore.Posting{Feature: r.Uint64(), ID: uint32(i / 4)}
	}
	return p
}

func heapMiB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// cmdIndex: build/sort/serialize/lookup of the flat B1 table vs bucket-sort + directory run.
func cmdIndex(args []string) {
	chunks := argInt(args, 0, 1000000)
	which := "all"
	if len(args) > 1 {
		which = args[1]
	}
	dir, err := os.MkdirTemp("", "p5idx")
	must(err)
	defer os.RemoveAll(dir)
	post := synth(chunks, 1)
	type variant struct {
		name string
		sort func([]dcore.Posting) []dcore.Posting
	}
	vs := []variant{
		{"b1-sort.Slice", func(p []dcore.Posting) []dcore.Posting { dcore.SortReflect(p); return p }},
		{"slices.SortFunc", func(p []dcore.Posting) []dcore.Posting { dcore.SortFunc(p); return p }},
		{"bucket1", func(p []dcore.Posting) []dcore.Posting { return dcore.SortBuckets(p, 1) }},
		{"bucket4", func(p []dcore.Posting) []dcore.Posting { return dcore.SortBuckets(p, 4) }},
	}
	var sortedKeep []dcore.Posting
	for _, v := range vs {
		if which != "all" && which != v.name {
			continue
		}
		if chunks >= 10000000 && v.name == "b1-sort.Slice" && which == "all" {
			// still measured: B1's own baseline at 10M
		}
		cp := make([]dcore.Posting, len(post))
		copy(cp, post)
		runtime.GC()
		t0 := time.Now()
		out := v.sort(cp)
		secs := time.Since(t0).Seconds()
		emit(map[string]any{"mode": "index-sort", "chunks": chunks, "postings": len(post), "variant": v.name, "sec": secs, "resident_mib": float64(len(post)) * 16 / (1 << 20)})
		sortedKeep = out
		cp = nil
		runtime.GC()
	}
	if sortedKeep == nil {
		return
	}
	sorted := sortedKeep
	// flat B1 table
	flat := filepath.Join(dir, "flat")
	f, err := os.Create(flat)
	must(err)
	t0 := time.Now()
	must(simdelta.WriteTable(f, toSim(sorted)))
	must(f.Sync())
	flatSec := time.Since(t0).Seconds()
	st, _ := f.Stat()
	f.Close()
	// directory run
	runp := filepath.Join(dir, "run")
	g, err := os.Create(runp)
	must(err)
	t0 = time.Now()
	n, err := dcore.WriteRun(g, sorted, 1)
	must(err)
	must(g.Sync())
	runSec := time.Since(t0).Seconds()
	g.Close()
	emit(map[string]any{"mode": "index-serialize", "chunks": chunks, "flat_bytes": st.Size(), "flat_write_sec": flatSec, "run_bytes": n, "run_write_sec": runSec})
	// lookups, warm page cache, same sampled features
	r := rand.New(rand.NewPCG(7, 7))
	const q = 200000
	feats := make([]uint64, q)
	for i := range feats {
		feats[i] = sorted[r.IntN(len(sorted))].Feature
	}
	ff, _ := os.Open(flat)
	t0 = time.Now()
	hits := 0
	for _, x := range feats {
		ids, err := simdelta.DiskLookup(ff, int64(len(sorted)), x)
		must(err)
		hits += len(ids)
	}
	flatQ := time.Since(t0).Seconds() / q * 1e6
	ff.Close()
	rf, _ := os.Open(runp)
	rst, _ := rf.Stat()
	t0 = time.Now()
	run, err := dcore.OpenRun(rf, rst.Size(), false)
	must(err)
	open := time.Since(t0).Seconds()
	t0 = time.Now()
	hits2 := 0
	for _, x := range feats {
		ids, err := run.Lookup(x, 64)
		must(err)
		hits2 += len(ids)
	}
	runQ := time.Since(t0).Seconds() / q * 1e6
	t0 = time.Now()
	must(run.Verify())
	ver := time.Since(t0).Seconds()
	rf.Close()
	emit(map[string]any{"mode": "index-lookup", "chunks": chunks, "flat_us": flatQ, "run_us": runQ, "hits_flat": hits, "hits_run": hits2, "run_open_sec": open, "run_verify_sec": ver, "queries": q})
	fmt.Fprintf(os.Stderr, "heap=%.0fMiB\n", heapMiB())
}

func toSim(p []dcore.Posting) []simdelta.Posting {
	out := make([]simdelta.Posting, len(p))
	for i, v := range p {
		out[i] = simdelta.Posting{Feature: v.Feature, ID: v.ID}
	}
	return out
}

type fileStore struct{ dir string }

func (s fileStore) path(g uint64) string { return filepath.Join(s.dir, fmt.Sprintf("run-%06d.zsr", g)) }
func (s fileStore) Put(g uint64, write func(io.Writer) error) (io.ReaderAt, int64, error) {
	tmp := s.path(g) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, 0, err
	}
	if err := write(f); err != nil {
		f.Close()
		return nil, 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, 0, err
	}
	st, _ := f.Stat()
	f.Close()
	if err := os.Rename(tmp, s.path(g)); err != nil {
		return nil, 0, err
	}
	rf, err := os.Open(s.path(g))
	return rf, st.Size(), err
}
func (s fileStore) Drop(g uint64) error { return os.Remove(s.path(g)) }

// cmdRuns: LSM-style append/delete/compaction/query on N objects x 4 features
// against B1's map index.
func cmdRuns(args []string) {
	objs := argInt(args, 0, 1000000)
	memLimit := argInt(args, 1, 1<<20)
	dir, err := os.MkdirTemp("", "p5runs")
	must(err)
	defer os.RemoveAll(dir)
	rs := dcore.NewRunSet(fileStore{dir}, memLimit)
	r := rand.New(rand.NewPCG(3, 4))
	feats := make([][4]uint64, 0, objs)
	t0 := time.Now()
	for id := 1; id <= objs; id++ {
		var f [4]uint64
		for i := range f {
			f[i] = r.Uint64()
			if r.IntN(10) == 0 { // some shared features create real candidates
				f[i] = uint64(r.IntN(objs/20+1)) * 0x9e3779b97f4a7c15
			}
		}
		feats = append(feats, f)
		must(rs.Add(uint32(id), f[:]))
	}
	must(rs.Flush())
	addSec := time.Since(t0).Seconds()
	emit(map[string]any{"mode": "runs-append", "objects": objs, "sec": addSec, "per_obj_us": addSec / float64(objs) * 1e6, "runs": rs.Runs(), "flushes": rs.Stats.Flushes, "heap_mib": heapMiB()})
	query := func(label string) {
		const q = 20000
		t := time.Now()
		got := 0
		for i := 0; i < q; i++ {
			id := 1 + r.IntN(objs)
			got += len(rs.Query(feats[id-1][:], 8))
		}
		emit(map[string]any{"mode": "runs-query", "label": label, "objects": objs, "runs": rs.Runs(), "query_us": time.Since(t).Seconds() / q * 1e6, "avg_candidates": float64(got) / q})
	}
	query("multi-run")
	// delete 10%
	t0 = time.Now()
	for id := 1; id <= objs; id += 10 {
		rs.Delete(uint32(id))
	}
	emit(map[string]any{"mode": "runs-delete", "deleted": objs / 10, "sec": time.Since(t0).Seconds()})
	t0 = time.Now()
	must(rs.Compact())
	emit(map[string]any{"mode": "runs-compact", "objects": objs, "sec": time.Since(t0).Seconds(), "runs_after": rs.Runs(), "heap_mib": heapMiB()})
	query("compacted")
	// startup: reopen the single run with and without verification
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, verify := range []bool{false, true} {
		t0 = time.Now()
		for _, n := range names {
			f, _ := os.Open(filepath.Join(dir, n))
			st, _ := f.Stat()
			_, err := dcore.OpenRun(f, st.Size(), verify)
			must(err)
			f.Close()
		}
		emit(map[string]any{"mode": "runs-startup", "verify": verify, "sec": time.Since(t0).Seconds(), "runs": len(names)})
	}
}

// ---- windowed large-object delta -------------------------------------------

// genBase writes a deterministic pseudo-structured file: blocks of random bytes
// interleaved with repeated record patterns, so literals have mixed entropy.
func genBase(path string, size int64, seed uint64) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	r := rand.New(rand.NewPCG(seed, 5))
	buf := make([]byte, 1<<20)
	var w int64
	for w < size {
		n := int(min(int64(len(buf)), size-w))
		b := buf[:n]
		for i := 0; i < n; {
			k := min(n-i, 64+r.IntN(4096))
			if r.IntN(3) == 0 { // low-entropy record text
				for j := 0; j < k; j++ {
					b[i+j] = byte('a' + (j*7+int(w)/997)%26)
				}
			} else {
				for j := 0; j < k; j++ {
					b[i+j] = byte(r.Uint64())
				}
			}
			i += k
		}
		_, err := f.Write(b)
		must(err)
		w += int64(n)
	}
}

type seg struct {
	kind     int // 0 copy base[off:off+n]; 1 insert random n bytes
	off, n   int64
	seedByte uint64
}

// genTarget builds a target from the base by a deterministic edit plan and
// returns the bytes of inserted random data (the ideal literal volume).
func genTarget(basePath, tgtPath string, size int64, seed uint64) (inserted int64, tsize int64) {
	r := rand.New(rand.NewPCG(seed, 11))
	var plan []seg
	pos := int64(0)
	for pos < size {
		run := int64(1<<20) + int64(r.IntN(8<<20)) // keep a stretch
		run = min(run, size-pos)
		plan = append(plan, seg{0, pos, run, 0})
		pos += run
		switch r.IntN(6) {
		case 0: // insertion
			n := int64(1 + r.IntN(60000))
			plan = append(plan, seg{1, 0, n, r.Uint64()})
		case 1: // deletion
			pos += min(int64(1+r.IntN(100000)), size-pos)
		case 2: // move: copy a distant earlier/later block
			n := int64(1<<16) + int64(r.IntN(2<<20))
			from := int64(r.Uint64() % uint64(max(size-n, 1)))
			plan = append(plan, seg{0, from, min(n, size-from), 0})
		case 3: // duplicate recent block
			if pos > 1<<20 {
				plan = append(plan, seg{0, pos - 1<<20, 1 << 20, 0})
			}
		}
	}
	bf, err := os.Open(basePath)
	must(err)
	defer bf.Close()
	tf, err := os.Create(tgtPath)
	must(err)
	defer tf.Close()
	rr := rand.New(rand.NewPCG(seed, 12))
	for _, s := range plan {
		if s.kind == 0 {
			_, err := io.Copy(tf, io.NewSectionReader(bf, s.off, s.n))
			must(err)
			tsize += s.n
			continue
		}
		b := make([]byte, s.n)
		for i := range b {
			b[i] = byte(rr.Uint64())
		}
		_, err := tf.Write(b)
		must(err)
		inserted += s.n
		tsize += s.n
	}
	return
}

func sha(path string) [32]byte {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(err)
	var s [32]byte
	copy(s[:], h.Sum(nil))
	return s
}

// cmdWindow: args: sizeMiB workers windowMiB [basefile targetfile]
func cmdWindow(args []string) {
	sizeMiB := argInt(args, 0, 64)
	workers := argInt(args, 1, 4)
	winKiB := argInt(args, 2, 4096)
	dir, err := os.MkdirTemp(os.Getenv("P5TMP"), "p5win")
	must(err)
	defer os.RemoveAll(dir)
	basep, tgtp := filepath.Join(dir, "base"), filepath.Join(dir, "target")
	var inserted int64
	if len(args) >= 5 {
		basep, tgtp = args[3], args[4]
	} else {
		genBase(basep, int64(sizeMiB)<<20, 1)
		inserted, _ = genTarget(basep, tgtp, int64(sizeMiB)<<20, 2)
	}
	bf, _ := os.Open(basep)
	tf, _ := os.Open(tgtp)
	bst, _ := bf.Stat()
	tst, _ := tf.Stat()
	wp := dcore.DefaultWParams()
	wp.Workers = workers
	wp.Window = int64(winKiB) << 10
	if v := os.Getenv("P5ANCHORS"); v != "" {
		a, _ := strconv.Atoi(v)
		wp.Anchor.MaxAnchors = a
	}
	t0 := time.Now()
	idx, err := dcore.BuildWIndex(bf, bst.Size(), wp.Anchor, workers)
	must(err)
	idxSec := time.Since(t0).Seconds()
	recp := filepath.Join(dir, "record")
	rf, _ := os.Create(recp)
	t0 = time.Now()
	st, err := dcore.WEncode(rf, tf, tst.Size(), bf, bst.Size(), idx, wp)
	must(err)
	must(rf.Sync())
	encSec := time.Since(t0).Seconds()
	rf.Close()
	encRSS := rss()
	// decode in-process, streaming into a hasher (no output file)
	rf, _ = os.Open(recp)
	rst, _ := rf.Stat()
	rec, err := dcore.OpenWRecord(rf, rst.Size())
	must(err)
	h := sha256.New()
	t0 = time.Now()
	ds, err := rec.Decode(h, bf, wp)
	must(err)
	decSec := time.Since(t0).Seconds()
	var got [32]byte
	copy(got[:], h.Sum(nil))
	want := sha(tgtp)
	ok := got == want && rec.SHA == want
	// range decode amplification
	rr := rand.New(rand.NewPCG(9, 9))
	rng := map[string]any{}
	for _, n := range []int64{4 << 10, 64 << 10, 1 << 20} {
		var tb, payload, bread int64
		const trials = 50
		t0 = time.Now()
		for i := 0; i < trials; i++ {
			off := int64(rr.Uint64() % uint64(tst.Size()-n))
			b, ds, err := rec.DecodeRange(bf, wp, off, n)
			must(err)
			exp := make([]byte, n)
			tf.ReadAt(exp, off)
			if !bytes.Equal(b, exp) {
				ok = false
			}
			tb += n
			payload += ds.PayloadBytes
			bread += ds.BaseReadBytes
		}
		rng[fmt.Sprintf("%dk", n>>10)] = map[string]any{"us": time.Since(t0).Seconds() / trials * 1e6, "payload_amp": float64(payload) / float64(tb), "base_read_amp": float64(bread) / float64(tb)}
	}
	emit(map[string]any{"mode": "window", "base": bst.Size(), "target": tst.Size(), "workers": workers, "window_kib": winKiB, "anchors_budget": wp.Anchor.MaxAnchors,
		"index_sec": idxSec, "index_mib_s": float64(bst.Size()) / idxSec / (1 << 20), "index_anchors": idx.Anchors,
		"encode_sec": encSec, "encode_mib_s": float64(tst.Size()) / encSec / (1 << 20), "record_bytes": st.RecordBytes,
		"inserted_literal_bytes": inserted, "literal_bytes": st.LiteralBytes, "copy_bytes": st.CopyBytes, "frames": st.Frames, "base_read_amp_encode": float64(st.BaseReadBytes) / float64(bst.Size()),
		"decode_sec": decSec, "decode_mib_s": float64(tst.Size()) / decSec / (1 << 20), "decode_base_read": ds.BaseReadBytes,
		"verified": ok, "rss_after_encode_mib": encRSS, "heap_mib": heapMiB(), "ranges": rng})
}
