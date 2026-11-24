package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- B10-0 grouped-record integration ------------------------------------------

// authLegacyLayout pins the pre-B9 layout (one record file per content) for a test that edits record files.
func authLegacyLayout(t testing.TB) {
	old := agWanted
	agWanted = false
	t.Cleanup(func() { agWanted = old })
}

// authRecordBytesAt locates the stored bytes of sha's deciding record in either layout: the file and the
// [off, off+n) region holding the record (a whole legacy file, or one group frame).
func authRecordBytesAt(t testing.TB, s *Store, sha [32]byte) (path string, off, n int64) {
	t.Helper()
	if loc, known, err := s.agLookup(sha); err == nil && known {
		if loc.kind != agRecordFrame {
			t.Fatalf("record %x is retired", sha[:4])
		}
		return s.agPath(loc.seq), loc.off, loc.n
	}
	fi, err := os.Stat(s.authPath(sha))
	if err != nil {
		t.Fatalf("record %x: %v", sha[:4], err)
	}
	return s.authPath(sha), 0, fi.Size()
}

// authRawRecord is sha's marshalled record in either layout.
func authRawRecord(t *testing.T, s *Store, sha [32]byte) []byte {
	t.Helper()
	data, known, err := s.agLoadRecord(sha)
	if !known && err == nil {
		data, err = os.ReadFile(s.authPath(sha))
	}
	if err != nil || len(data) == 0 {
		t.Fatalf("record %x: %v", sha[:4], err)
	}
	return data
}

// authDamageRecord flips one byte in the middle of sha's stored record (file or frame) and returns an undo.
func authDamageRecord(t *testing.T, s *Store, sha [32]byte) func() {
	t.Helper()
	p, off, n := authRecordBytesAt(t, s, sha)
	b, err := os.ReadFile(p)
	mustOK(t, err)
	b[off+n/2] ^= 0x5a
	mustOK(t, os.WriteFile(p, b, 0o644))
	return func() {
		b[off+n/2] ^= 0x5a
		mustOK(t, os.WriteFile(p, b, 0o644))
	}
}

// TestB10GroupedDamagedRecordRefusesGC: in the grouped layout a damaged live record frame must stop
// gc and repack exactly as a damaged record file does; after the frame is restored every object is exact.
func TestB10GroupedDamagedRecordRefusesGC(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%v", grouped), func(t *testing.T) {
			if grouped {
				b9Grouped(t, 1<<20)
			} else {
				authLegacyLayout(t)
			}
			f := eqFixture(t)
			if p, _, _ := authRecordBytesAt(t, f.l.s, f.rec.SHA); grouped != (filepath.Ext(p) == agSuffix) {
				t.Fatalf("record layout %s", p)
			}
			undo := authDamageRecord(t, f.l.s, f.rec.SHA)
			f.l.close()
			if _, err := gcCollect(f.l.dir, true); err == nil {
				t.Fatal("gc ran against a damaged live record")
			}
			if _, err := repackStore(f.l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err == nil {
				t.Fatal("repack ran against a damaged live record")
			}
			undo()
			f.l.open()
			f.l.exact("restored")
			f.l.verifyOK("restored")
		})
	}
}

var _ = bytes.Equal
var _ = filepath.Join

// TestB10GroupedMissingBase: the grouped analogues of a lost base record file (a damaged base frame, a lost
// group file) fail reachability and gc closed; restored, the store is exact and verifies.
func TestB10GroupedMissingBase(t *testing.T) {
	for _, mode := range []string{"damaged-frame", "lost-group"} {
		t.Run(mode, func(t *testing.T) {
			b9Grouped(t, 2<<10) // small groups: the base and its dependents land in different files
			l := newAuthLab(t)
			for i, v := range authHistory(41, 3, 1_000_000) {
				l.put("k"+string(rune('0'+i)), v)
			}
			l.migrate(AuthPolicy{MinSaving: 0.05})
			l.reclaim() // the CDC copies of body-backed contents are gone: the records are their only source
			files, _ := l.s.authFiles()
			var victim [32]byte
			for _, f := range files {
				if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormPin {
					victim = f
				}
			}
			if victim == ([32]byte{}) {
				t.Fatal("no pin record")
			}
			p, _, _ := authRecordBytesAt(t, l.s, victim)
			if filepath.Ext(p) != agSuffix {
				t.Fatalf("pin not grouped: %s", p)
			}
			orig, err := os.ReadFile(p)
			mustOK(t, err)
			if mode == "lost-group" {
				mustOK(t, os.Rename(p, p+".lost"))
				l.close()
				l.open()
			} else {
				authDamageRecord(t, l.s, victim)
				l.close()
				l.open()
			}
			rr, err := l.s.computeReachability(false)
			if err == nil && rr.OK() {
				t.Fatal("a missing grouped base passed reachability")
			}
			l.offline(func() {
				if _, err := gcCollect(l.dir, true); err == nil {
					t.Fatal("gc applied despite a missing grouped base")
				}
			})
			l.close()
			os.Remove(p + ".lost")
			mustOK(t, os.WriteFile(p, orig, 0o644))
			l.open()
			l.exact("restored")
			l.verifyOK("restored")
		})
	}
}

// TestB10GroupStaleViews: what a stale group-index view can observe. In process, a frame location taken
// before compaction (group rewritten and unlinked) resolves to the re-appended frame. Across processes, a
// shared opener's superseded frames stay valid and exact while another process reverts and republishes
// every record, and online maintenance in a third process defers every unlink and compaction while the
// opener holds the reader lease; once it closes, maintenance proceeds and the store is exact.
func TestB10GroupStaleViews(t *testing.T) {
	b9Grouped(t, 4<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 23, 12, 3)
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.reclaim()
	files, _ := l.s.authFiles()
	if len(files) < 8 {
		t.Fatalf("%d records", len(files))
	}
	old := map[[32]byte]agLoc{}
	for _, f := range files {
		loc, known, err := l.s.agLookup(f)
		mustOK(t, err)
		if !known || loc.kind != agRecordFrame {
			t.Fatalf("%x not grouped", f[:4])
		}
		old[f] = loc
	}
	// In process: retire every other record (CDC again), compact, then resolve through the stale locations.
	keep := map[[32]byte]bool{}
	for i, f := range files {
		if i%2 == 0 {
			if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormPin {
				keep[f] = true
				continue
			}
			mustOK(t, l.s.AuthorityRevert(f))
		} else {
			keep[f] = true
		}
	}
	ag, err := l.s.agCompact(false)
	mustOK(t, err)
	if ag.Compacted == 0 {
		t.Fatalf("nothing compacted: %+v", ag)
	}
	moved := 0
	x := &l.s.auth.ag
	for f := range keep {
		lo := old[f]
		if _, err := os.Stat(l.s.agPath(lo.seq)); !os.IsNotExist(err) {
			continue
		}
		moved++
		if _, err := l.s.agReadRecord(f, lo); err == nil {
			t.Fatal("a frame of an unlinked group still read through the index")
		}
		x.mu.Lock()
		x.win[f] = lo // the view a lookup taken before compaction holds
		x.mu.Unlock()
		r, err := l.s.authLoad(f)
		if err != nil || r == nil || r.SHA != f {
			t.Fatalf("stale location after compaction: %v", err)
		}
	}
	if moved == 0 {
		t.Fatal("no live record moved by compaction")
	}
	l.exact("after in-process compaction")
	l.verifyOK("after in-process compaction")
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.reclaim()
	l.close()

	// Across processes.
	a, err := OpenStoreShared(l.dir)
	mustOK(t, err)
	files, _ = a.authFiles()
	stale := map[[32]byte]agLoc{}
	for _, f := range files {
		loc, _, err := a.agLookup(f)
		mustOK(t, err)
		stale[f] = loc
	}
	// readAll reads every content with a record through the authority path alone (no CDC fallback).
	readAll := func(stage string) {
		for k, w := range l.want {
			sha := sha256.Sum256(w)
			if r, _ := a.authLoad(sha); r == nil {
				continue
			}
			c := a.newAuthCtx()
			b, _, err := c.body(sha, int64(len(w)), 1)
			if err != nil {
				t.Fatalf("%s: %s: %v", stage, k, err)
			}
			got := make([]byte, len(w))
			if n, err := b.ReadAt(got, 0); n != len(w) || !bytes.Equal(got, w) {
				t.Fatalf("%s: %s: %d bytes: %v", stage, k, n, err)
			}
			c.close()
		}
	}
	// inject makes a's index decide the chosen contents by their pre-republication frames, as a view that
	// has not yet seen the other process's appends would.
	inject := func(pick func(*authRecord) bool) (n int) {
		ax := &a.auth.ag
		ax.mu.Lock()
		defer ax.mu.Unlock()
		for f, lo := range stale {
			g := ax.groups[lo.seq]
			if g == nil {
				t.Fatalf("group %d of a stale frame is gone", lo.seq)
			}
			buf := make([]byte, lo.n)
			_, err := g.f.ReadAt(buf, lo.off)
			mustOK(t, err)
			_, p, _, ok := agParse(buf)
			if !ok {
				t.Fatal("stale frame no longer parses")
			}
			r, err := unmarshalAuthRecord(p)
			mustOK(t, err)
			if pick(r) {
				ax.win[f] = lo
				n++
			}
		}
		return n
	}
	proveStale := func(stage string) {
		inject(func(*authRecord) bool { return true }) // a wholly stale view: the old generation, consistent
		readAll(stage + ": wholly stale view")
		if inject(func(r *authRecord) bool { return len(r.Bases) > 0 }) == 0 { // old dependents, new bases
			t.Fatal("no dependent record")
		}
		readAll(stage + ": mixed view")
	}
	readAll("shared open")
	runHelper(t, "revert", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	runHelper(t, "groupmigrate", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	proveStale("after another process republished")
	readAll("refreshed")
	changed := 0
	for f, lo := range stale {
		if loc, known, _ := a.agLookup(f); known && loc != lo {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("the shared opener never saw the new generations")
	}
	groups := b9Groups(t, l.dir)
	_, out := runHelper(t, "b10online", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	var rep MaintReport
	mustOK(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &rep))
	if rep.Deferred == "" || rep.RecordGroups != nil {
		t.Fatalf("maintenance unlinked under a shared opener: %+v", rep)
	}
	if g := b9Groups(t, l.dir); !slices.Equal(g, groups) {
		t.Fatal("group files changed under a shared opener")
	}
	proveStale("after deferred maintenance")
	readAll("after deferred maintenance")
	mustOK(t, a.Close())
	_, out = runHelper(t, "b10online", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	rep = MaintReport{}
	mustOK(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &rep))
	if rep.Deferred != "" || rep.RecordGroups == nil {
		t.Fatalf("maintenance did not proceed after the opener closed: %+v", rep)
	}
	l.open()
	l.exact("after maintenance")
	l.verifyOK("after maintenance")
}

// ---- B10 deep canonical streams (kind 16) ----------------------------------------

// b10Tar builds a tar of (name, data) members.
func b10Tar(t testing.TB, files ...any) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		data := files[i+1].([]byte)
		mustOKTB(t, tw.WriteHeader(&tar.Header{Name: files[i].(string), Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(1700000000, 0), Format: tar.FormatPAX}))
		_, err := tw.Write(data)
		mustOKTB(t, err)
	}
	mustOKTB(t, tw.Close())
	return buf.Bytes()
}

func mustOKTB(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func b10Gzip(t testing.TB, data []byte, level int) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, level)
	mustOKTB(t, err)
	w.Write(data)
	mustOKTB(t, w.Close())
	return buf.Bytes()
}

func b10Zip(t testing.TB, files ...any) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		m := zip.Deflate
		if i%4 == 2 {
			m = zip.Store
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: files[i].(string), Method: m, Modified: time.Unix(1700000000, 0)})
		mustOKTB(t, err)
		w.Write(files[i+1].([]byte))
	}
	mustOKTB(t, zw.Close())
	return buf.Bytes()
}

// b10SpliceEnv builds the kind-16 representation of data (stored under key in l) in memory and returns an
// environment that reads it, with its deep stream.
func b10SpliceEnv(t *testing.T, l *authLab, key string, data []byte, hard bool) (*repEnv, *repRoot, *repCanon, *SpliceStats) {
	t.Helper()
	l.put(key, data)
	mem := map[[32]byte][]byte{}
	st := &SpliceStats{}
	root, canon, err := l.s.spliceConstruct(l.man(key), bytes.NewReader(data), 0, mem, splOpts{hard: hard}, st)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	t.Cleanup(canon.close)
	src := l.s.newRepSource(root.Descs, nil)
	src.mem = mem
	env := &repEnv{Src: src, Limits: repProdLimits, Body: func(sha [32]byte, n int64) (io.ReaderAt, error) {
		if sha != canon.SHA || n != canon.Size {
			return nil, errRepMissing
		}
		return canon.File, nil
	}}
	return env, root, canon, st
}

// b10CheckRanges reads the whole object and ranges around every segment boundary plus random ones.
func b10CheckRanges(t *testing.T, env *repEnv, root *repRoot, want []byte, seed uint64) {
	t.Helper()
	var buf bytes.Buffer
	n, sum, err := env.reconstruct(root.Rep, &buf)
	if err != nil || n != int64(len(want)) || sum != sha256.Sum256(want) || !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("whole: %v", err)
	}
	call, err := env.open(root.Rep)
	mustOK(t, err)
	tb, err := splRecipe(call)
	mustOK(t, err)
	size := int64(len(want))
	var offs []int64
	for _, s := range tb.segs {
		offs = append(offs, s.out, max(0, s.out-1), max(0, s.out-4000))
	}
	r := rand.New(rand.NewPCG(seed, 3))
	for range 64 {
		offs = append(offs, r.Int64N(size))
	}
	for i, off := range offs {
		ln := []int64{1, 4096, 65536, 1 << 20}[i%4]
		ln = min(ln, size-off)
		if ln <= 0 {
			continue
		}
		produce, err := repRangeReader(call, off, ln)
		if err != nil || produce == nil {
			t.Fatalf("range reader at %d: %v", off, err)
		}
		var b bytes.Buffer
		if err := produce(&b); err != nil || !bytes.Equal(b.Bytes(), want[off:off+ln]) {
			t.Fatalf("range [%d,+%d): %v (%d bytes)", off, ln, err, b.Len())
		}
	}
}

// TestB10SpliceExact: nested gzip and ZIP members of a tar, Parquet pages and RocksDB blocks are decoded
// into one deep stream and every object reproduces exactly, whole and by range across every boundary.
func TestB10SpliceExact(t *testing.T) {
	l := newAuthLab(t)
	text := func(seed uint64, n int) []byte { return b5Text(seed, n) }
	inner := b10Tar(t, "a.txt", text(1, 90_000), "b.txt", text(2, 30_000))
	jar := b10Zip(t, "x/A.class", text(3, 20_000), "x/B.class", text(4, 9_000), "META-INF/MANIFEST.MF", []byte("Manifest-Version: 1.0\n"), "x/C.class", text(5, 12_000))
	gem := b10Tar(t, "metadata.gz", b10Gzip(t, text(6, 60_000), 9), "data.tar.gz", b10Gzip(t, inner, 6),
		"lib/app.jar", jar, "README", text(7, 5_000), "small.gz", b10Gzip(t, text(8, 900), 6), "fake.gz", append([]byte{0x1f, 0x8b, 8}, text(9, 6000)...))
	env, root, canon, st := b10SpliceEnv(t, l, "gem", gem, false)
	if st.Children != 3 || st.Refused != 1 {
		t.Fatalf("children %d refused %d (want the metadata, the data tarball and the jar; the fake refused)", st.Children, st.Refused)
	}
	if canon.Size < int64(len(gem))+90_000 {
		t.Fatalf("deep stream of %d bytes for a %d-byte archive", canon.Size, len(gem))
	}
	b10CheckRanges(t, env, root, gem, 1)
	for _, p := range []string{"testing-harnesses/z3/parquetz3/testdata/pa_snappy_v1.parquet", "testing-harnesses/z3/parquetz3/testdata/pa_snappy_v2.parquet",
		"testing-harnesses/z3/parquetz3/testdata/pa_zstd_v1.parquet", "testdata/p7/pa_lz4_raw.parquet", "testdata/p7/pa_brotli.parquet",
		"testdata/p7/rocksdb_lz4_small.sst", "testdata/p7/rocksdb_lz4hc_small.sst", "testing-harnesses/z3/sstz3/testdata/rocksdb-zstd.sst"} {
		data, err := os.ReadFile(p)
		mustOK(t, err)
		env, root, _, st := b10SpliceEnv(t, l, filepath.Base(p), data, true)
		t.Logf("%s: %d pages, %d refused, deep %d of %d bytes", filepath.Base(p), st.Pages, st.Refused, st.PlainBytes, len(data))
		if st.Pages == 0 {
			t.Fatalf("%s: no page decoded", p)
		}
		b10CheckRanges(t, env, root, data, 2)
	}
}

// b10Gem is a RubyGems-shaped package: an uncompressed tar holding metadata.gz and data.tar.gz.
func b10Gem(t testing.TB, fs []b8File, mtime int64) []byte {
	meta := []byte(fmt.Sprintf("--- !ruby/object:Gem::Specification\nname: demo\nversion: %d\n", mtime))
	meta = append(meta, b5Text(uint64(mtime), 30_000)...)
	return b10Tar(t, "metadata.gz", b10Gzip(t, meta, 9), "data.tar.gz", b10Gzip(t, b8Tar(t, fs, mtime), 6), "checksums.yaml.gz", b10Gzip(t, []byte("sha256: x\n"), 9))
}

// b10Lab holds three gem versions and a gzipped layer whose tar nests a jar and a tarball (three layers:
// gzip -> tar -> ZIP / gzip -> tar).
func b10Lab(t *testing.T) *authLab {
	l := newAuthLab(t)
	v := []([]b8File){b8Tree(3, 18)}
	v = append(v, b8Edit(v[0], 11), b8Edit(b8Edit(v[0], 11), 23))
	for i, fs := range v {
		l.put(fmt.Sprintf("gems/demo-1.%d.gem", i), b10Gem(t, fs, 1700000000+int64(i)*86400))
	}
	jar := func(fs []b8File) []byte {
		var args []any
		for _, f := range fs[:8] {
			args = append(args, f.name, f.data)
		}
		return b10Zip(t, args...)
	}
	for i := range 2 {
		layer := b10Tar(t, "app/lib.jar", jar(v[i]), "app/vendor.tar.gz", b10Gzip(t, b8Tar(t, v[i][8:], 1700000000), 6), "etc/conf", b5Text(99, 7000))
		l.put(fmt.Sprintf("oci/layer%d.tar.gz", i), b10Gzip(t, layer, 6))
	}
	l.reclaim()
	return l
}

// TestB10SpliceOptimize: the optimizer selects deep streams for gems and nested layers, the plan equals the
// materialized model, every object stays exact through apply, reclamation and reopen, "splice" is raised,
// a rerun converges, the store is smaller than the B8 canonical store, and deleting a version keeps the
// shared deep streams others need.
func TestB10SpliceOptimize(t *testing.T) {
	pol := OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2}, Profile: "archive", Canonical: true}
	base := b10Lab(t)
	b8Optimize(t, base, pol)
	base.reclaim()
	b8Bytes := dirBytes(t, base.dir)

	l := b10Lab(t)
	pol.Splice = true
	rep := b8Optimize(t, l, pol)
	if rep.Splice == nil || rep.Splice.Built < 5 || rep.Splice.Children < 8 {
		t.Fatalf("splice analysis %+v", rep.Splice)
	}
	if rep.Failed != 0 || rep.Applied == 0 {
		t.Fatalf("applied %d failed %d: %+v", rep.Applied, rep.Failed, rep.Changes)
	}
	spliced := 0
	for k, r := range b8Forms(t, l) {
		if r != nil && r.Form == authFormRep && len(r.Bases) == 1 {
			if kd, _, _ := l.s.repTopCodec(r.Root); kd == repKindSplice {
				spliced++
			}
			t.Logf("%s: rep over %x (depth %d)", k, r.Bases[0].SHA[:4], r.Depth)
		}
	}
	if spliced < 3 {
		t.Fatalf("%d deep-stream records", spliced)
	}
	l.exact("applied")
	l.reclaim()
	l.exact("reclaimed")
	after := dirBytes(t, l.dir)
	t.Logf("store: B8 canonical %d bytes, B10 splice %d bytes", b8Bytes, after)
	if after >= b8Bytes*85/100 {
		t.Fatalf("deep streams saved too little: %d vs %d", after, b8Bytes)
	}
	l.offline(func() {})
	l.exact("reopened")
	l.verifyOK("splice")
	g, err := l.s.rgBuild(context.Background(), b6Policy(pol).norm())
	mustOK(t, err)
	g.partition()
	if m := newRGEval(g, g.currentSel()).state("m", false); m.Bytes != rep.Planned.Bytes {
		t.Fatalf("materialized model %d bytes, plan %d", m.Bytes, rep.Planned.Bytes)
	}
	g.close()
	if !slices.Contains(l.s.format.Features, spliceFeature) {
		t.Fatalf("features %v lack %q", l.s.format.Features, spliceFeature)
	}
	if again := b8Optimize(t, l, pol); again.Applied != 0 || len(again.Changes) != 0 {
		t.Fatalf("second run changed %d: %+v", again.Applied, again.Changes)
	}
	l.exact("rerun")
	mustOK(t, l.s.DeleteObject("b", "gems/demo-1.0.gem"))
	delete(l.want, "gems/demo-1.0.gem")
	l.reclaim()
	l.exact("version deleted")
	l.verifyOK("version deleted")
}

// b10SmallLab: two gem versions and a gzipped layer nesting a jar, small enough for fault matrices.
func b10SmallLab(t *testing.T) *authLab {
	l := newAuthLab(t)
	v1 := b8Tree(41, 9)
	v2 := b8Edit(v1, 5)
	l.put("g/demo-1.gem", b10Gem(t, v1, 1700000000))
	l.put("g/demo-2.gem", b10Gem(t, v2, 1700086400))
	var args []any
	for _, f := range v2[:6] {
		args = append(args, f.name, f.data)
	}
	l.put("oci/layer.tar.gz", b10Gzip(t, b10Tar(t, "app/lib.jar", b10Zip(t, args...), "etc/x", b5Text(5, 9000)), 6))
	l.reclaim()
	return l
}

// TestB10SpliceDamage: a missing or damaged deep stream fails reads closed (never other bytes), deep verify
// and gc refuse; restored, everything is exact again.
func TestB10SpliceDamage(t *testing.T) {
	authLegacyLayout(t) // removes and edits record files
	l := b10SmallLab(t)
	b8Optimize(t, l, OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2}, Profile: "archive", Splice: true})
	l.reclaim()
	var victim *authRecord
	var key string
	for k, r := range b8Forms(t, l) {
		if r != nil && r.Form == authFormRep && len(r.Bases) == 1 {
			if kd, _, _ := l.s.repTopCodec(r.Root); kd == repKindSplice && strings.HasSuffix(k, ".gem") {
				victim, key = r, k
			}
		}
	}
	if victim == nil {
		t.Fatal("no spliced gem")
	}
	d := victim.Bases[0].SHA
	p := l.s.authPath(d)
	orig, err := os.ReadFile(p)
	mustOK(t, err)
	check := func(stage string) {
		got, err := g1ReadAll(l, key, byteRange{0, l.man(key).TotalLength - 1})
		if err == nil && !bytes.Equal(got, l.want[key]) {
			t.Fatalf("%s: wrong bytes without an error", stage)
		}
		if !bytes.Equal(got, l.want[key][:len(got)]) {
			t.Fatalf("%s: delivered bytes are not an exact prefix", stage)
		}
		if err == nil {
			t.Fatalf("%s: read succeeded without its deep stream", stage)
		}
		if res, err := l.s.Verify(true); err == nil && res.OK() {
			t.Fatalf("%s: deep verify passed", stage)
		}
		l.offline(func() {
			if _, err := gcCollect(l.dir, true); err == nil {
				t.Fatalf("%s: gc ran", stage)
			}
		})
	}
	mustOK(t, os.Remove(p))
	l.s.purgeReadCaches()
	check("deep stream record missing")
	b := slices.Clone(orig)
	b[len(b)/2] ^= 0x41
	mustOK(t, os.WriteFile(p, b, 0o644))
	l.s.purgeReadCaches()
	check("deep stream record damaged")
	mustOK(t, os.WriteFile(p, orig, 0o644))
	l.s.purgeReadCaches()
	l.exact("restored")
	l.verifyOK("restored")
}

// TestB10SpliceRefusals: units the builder must not decode stay verbatim; hostile containers are bounded.
func TestB10SpliceRefusals(t *testing.T) {
	l := newAuthLab(t)
	bomb := b10Gzip(t, make([]byte, 64<<20), 9) // 64 MiB of zeros in ~64 KiB: above the expansion bound
	var multi []byte                            // two gzip members: refused by the canonical gzip builder
	multi = append(b10Gzip(t, b5Text(1, 50_000), 6), b10Gzip(t, b5Text(2, 50_000), 6)...)
	trunc := b10Gzip(t, b5Text(3, 80_000), 6)
	trunc = trunc[:len(trunc)-9]
	zipBad := b10Zip(t, "a", b5Text(4, 40_000), "b", b5Text(5, 40_000))
	zipBad[len(zipBad)-10] ^= 0xff // central directory damaged
	arch := b10Tar(t, "bomb.gz", bomb, "multi.gz", multi, "trunc.gz", trunc, "bad.zip", zipBad, "ok.gz", b10Gzip(t, b5Text(6, 60_000), 6))
	_, root, canon, st := b10SpliceEnv(t, l, "arch", arch, false)
	if st.Children != 1 || st.Refused != 4 {
		t.Fatalf("children %d refused %d", st.Children, st.Refused)
	}
	if canon.Size > int64(len(arch))+70_000 {
		t.Fatalf("deep stream of %d bytes for %d", canon.Size, len(arch))
	}
	_ = root
	// nothing decodable: no representation at all
	l.put("plain", b10Tar(t, "a.txt", b5Text(7, 9000)))
	if _, _, err := l.s.spliceConstruct(l.man("plain"), nil, 0, map[[32]byte][]byte{}, splOpts{}, nil); !errors.Is(err, errRepUnsupported) {
		t.Fatalf("plain tar: %v", err)
	}
	// a truncated Parquet file is refused
	pqb, err := os.ReadFile("testing-harnesses/z3/parquetz3/testdata/pa_snappy_v1.parquet")
	mustOK(t, err)
	l.put("trunc.parquet", append(pqb[:len(pqb)/2:len(pqb)/2], pqb[len(pqb)-8:]...))
	if _, _, err := l.s.spliceConstruct(l.man("trunc.parquet"), nil, 0, map[[32]byte][]byte{}, splOpts{}, nil); err == nil {
		t.Fatal("truncated Parquet accepted")
	}
	// LZ4 pages need the hard-codec policy
	lz, err := os.ReadFile("testdata/p7/pa_lz4_raw.parquet")
	mustOK(t, err)
	l.put("lz4.parquet", lz)
	if _, _, err := l.s.spliceConstruct(l.man("lz4.parquet"), nil, 0, map[[32]byte][]byte{}, splOpts{}, nil); !errors.Is(err, errRepUnsupported) {
		t.Fatalf("LZ4 pages without the hard-codec policy: %v", err)
	}
}

// TestB10SpliceTableFuzz: damaged tables, recipes and member lists never panic and never reproduce the object.
func TestB10SpliceTableFuzz(t *testing.T) {
	l := newAuthLab(t)
	pqb, err := os.ReadFile("testing-harnesses/z3/parquetz3/testdata/pa_snappy_v2.parquet")
	mustOK(t, err)
	gem := b10Gem(t, b8Tree(9, 8), 1700000000)
	for name, data := range map[string][]byte{"gem": gem, "parquet": pqb} {
		env, root, _, _ := b10SpliceEnv(t, l, name, data, false)
		src := env.Src.(*storeRepSource)
		rep, err := env.loadRep(root.Rep)
		mustOK(t, err)
		call := env.call(root.Rep, rep, 1, nil, nil)
		tb, err := splRecipe(call)
		mustOK(t, err)
		var table []byte
		for i := tb.children; i < tb.children+tb.tableN; i++ {
			b, _ := call.get(rep.Members[i].SHA)
			table = append(table, b...)
		}
		want := sha256.Sum256(data)
		r := rand.New(rand.NewPCG(7, uint64(len(name))))
		for range 400 {
			m := slices.Clone(table)
			switch r.IntN(3) {
			case 0:
				m[r.IntN(len(m))] ^= byte(1 + r.IntN(255))
			case 1:
				m = m[:r.IntN(len(m))]
			default:
				m = append(m, byte(r.IntN(256)))
			}
			if t2, err := splParse(m, rep, tb.children, tb.plain); err == nil && !bytes.Equal(m, table) {
				// a parse that still covers everything must not reproduce the object
				r2 := *rep
				tid := sha256.Sum256(m)
				src.mem[tid] = m
				id := []repMember{{Role: repStored, SHA: tid, Size: int64(len(m))}}
				r2.Members = append(append(slices.Clone(rep.Members[:tb.children]), id...), rep.Members[len(rep.Members)-1])
				r2.Recipe = binary.AppendUvarint(binary.AppendUvarint(nil, uint64(tb.children)), uint64(len(id)))
				raw, err := r2.marshal()
				if err != nil {
					continue
				}
				rid := sha256.Sum256(raw)
				src.mem[rid] = raw
				if _, sum, err := env.reconstruct(rid, io.Discard); err == nil && sum == want {
					t.Fatalf("%s: a mutated table (%d segments) reproduced the object", name, len(t2.segs))
				}
			}
		}
		// damaged deep stream bytes never reproduce the object
		canonFile := env.Body
		env.Body = func(sha [32]byte, n int64) (io.ReaderAt, error) {
			f, err := canonFile(sha, n)
			if err != nil {
				return nil, err
			}
			return b10Flip{f, n / 3}, nil
		}
		if _, sum, err := env.reconstruct(root.Rep, io.Discard); err == nil && sum == want {
			t.Fatalf("%s: a damaged deep stream reproduced the object", name)
		}
		env.Body = canonFile
	}
}

// b10Flip serves a body with one byte flipped.
type b10Flip struct {
	r   io.ReaderAt
	off int64
}

func (f b10Flip) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.r.ReadAt(p, off)
	if f.off >= off && f.off < off+int64(n) {
		p[f.off-off] ^= 0x20
	}
	return n, err
}

// TestB10SpliceSharedChild: two packages nesting the identical data tarball and a standalone copy of it
// share one child representation. Reachability is order-independent (the standalone root keeps its own body
// reference however the walk met the child first), and deleting parents one by one keeps the rest exact.
func TestB10SpliceSharedChild(t *testing.T) {
	l := newAuthLab(t)
	fs := b8Tree(51, 16)
	data := b10Gzip(t, b8Tar(t, fs, 1700000000), 6)
	l.put("a.gem", b10Tar(t, "metadata.gz", b10Gzip(t, b5Text(1, 40_000), 9), "data.tar.gz", data))
	l.put("b.gem", b10Tar(t, "metadata.gz", b10Gzip(t, b5Text(2, 40_000), 9), "data.tar.gz", data))
	l.put("data.tar.gz", data)
	// The walk order of the two splice roots and the standalone canonical root must not matter.
	mem := map[[32]byte][]byte{}
	var roots [][32]byte
	var descs []map[[32]byte]*repDesc
	for _, k := range []string{"a.gem", "b.gem"} {
		root, c, err := l.s.spliceConstruct(l.man(k), nil, 0, mem, splOpts{}, nil)
		mustOK(t, err)
		c.close()
		roots, descs = append(roots, root.Rep), append(descs, root.Descs)
	}
	root, c, err := l.s.repConstructCanon(l.man("data.tar.gz"), nil, 0, mem, false)
	mustOK(t, err)
	c.close()
	roots, descs = append(roots, root.Rep), append(descs, root.Descs)
	all := map[[32]byte]*repDesc{}
	for _, d := range descs {
		maps.Copy(all, d)
	}
	src := l.s.newRepSource(all, nil)
	src.mem = mem
	env := &repEnv{Src: src, Limits: repProdLimits}
	fwd, err := env.reachableAll(roots)
	mustOK(t, err)
	rev, err := env.reachableAll([][32]byte{roots[2], roots[1], roots[0]})
	mustOK(t, err)
	alone, err := env.reachable(roots[2])
	mustOK(t, err)
	src.close()
	if len(fwd.Role) != len(rev.Role) || !maps.Equal(fwd.Bodies, rev.Bodies) || len(fwd.Bodies) != 3 || len(alone.Bodies) != 1 {
		t.Fatalf("reachability depends on order: %d/%d objects, bodies %d vs %d (standalone %d)", len(fwd.Role), len(rev.Role), len(fwd.Bodies), len(rev.Bodies), len(alone.Bodies))
	}
	for b := range alone.Bodies {
		if _, ok := fwd.Bodies[b]; !ok {
			t.Fatal("the standalone container's stream was lost behind a splice child")
		}
	}
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2}, Profile: "archive", Splice: true})
	l.reclaim()
	l.exact("optimized")
	for _, k := range []string{"a.gem", "data.tar.gz"} {
		mustOK(t, l.s.DeleteObject("b", k))
		delete(l.want, k)
		l.reclaim()
		l.exact("deleted " + k)
		l.verifyOK("deleted " + k)
	}
}

// TestB10SpliceConcurrentReads: eight readers of random ranges and whole objects over spliced gems, a nested
// layer and Parquet/SST pages stay exact (run with -race).
func TestB10SpliceConcurrentReads(t *testing.T) {
	l := b10SmallLab(t)
	for _, p := range []string{"testing-harnesses/z3/parquetz3/testdata/pa_snappy_v1.parquet", "testdata/p7/rocksdb_lz4_small.sst"} {
		data, err := os.ReadFile(p)
		mustOK(t, err)
		l.put(filepath.Base(p), data)
	}
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2, HardCodecs: true}, Profile: "archive", Splice: true})
	l.reclaim()
	keys := slices.Sorted(maps.Keys(l.want))
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 9))
			for range 40 {
				k := keys[r.IntN(len(keys))]
				want := l.want[k]
				off := r.Int64N(int64(len(want)))
				n := min(int64(1+r.IntN(200_000)), int64(len(want))-off)
				if r.IntN(8) == 0 {
					off, n = 0, int64(len(want))
				}
				got, err := g1ReadAll(l, k, byteRange{off, off + n - 1})
				if err != nil || !bytes.Equal(got, want[off:off+n]) {
					errc <- fmt.Errorf("%s [%d,+%d): %v", k, off, n, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

// TestB10SpliceCrashMatrix: real SIGKILL cuts of a deep-stream optimize (deep-stream and component chunks
// flushed, record written and renamed, before publication; legacy and grouped records). After each cut every
// acknowledged object is exact, reclamation and deep verify pass, a rerun completes and converges.
func TestB10SpliceCrashMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("process-kill matrix")
	}
	pol := OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2}, Profile: "archive", Splice: true}
	cuts := 0
	for _, grouped := range []bool{false, true} {
		env := []string{"ZEROS3_RECORD_GROUPS=0"}
		points := []string{hookCASAfterFlush, hookAuthRecordWritten, hookAuthAfterRename, hookOptimizeBeforePublish}
		if grouped {
			b9Grouped(t, 1<<20)
			env = []string{"ZEROS3_RECORD_GROUPS=1"}
			points = []string{hookAGFrameWritten, hookAGSynced, hookOptimizeBeforePublish}
		} else {
			authLegacyLayout(t)
		}
		base := b10SmallLab(t)
		base.close()
		for _, point := range points {
			for occ := 1; occ <= 5; occ++ {
				l := &authLab{t: t, dir: t.TempDir(), want: base.want}
				mustOK(t, os.CopyFS(l.dir, os.DirFS(base.dir)))
				os.Remove(filepath.Join(l.dir, "LOCK"))
				if killed, _ := runHelper(t, "splice", l.dir, point, occ, env...); !killed {
					break
				}
				cuts++
				stage := fmt.Sprintf("grouped=%v %s#%d", grouped, point, occ)
				l.open()
				l.exact(stage + " after kill")
				l.reclaim()
				l.exact(stage + " reclaimed")
				l.verifyOK(stage)
				if rep := b8Optimize(t, l, pol); rep.Failed != 0 {
					t.Fatalf("%s: rerun failed %d: %+v", stage, rep.Failed, rep.Changes)
				}
				l.reclaim()
				l.exact(stage + " completed")
				l.verifyOK(stage + " completed")
				if again := b8Optimize(t, l, pol); again.Applied != 0 {
					t.Fatalf("%s: not converged: %+v", stage, again.Changes)
				}
				l.close()
			}
		}
	}
	if cuts < 16 {
		t.Fatalf("only %d cuts reached", cuts)
	}
	t.Logf("%d process-kill cuts", cuts)
}

// TestB10SpliceWindowCache: with the opt-in cache a replayed page is kept whole at its second touch and a
// nested child's windows are shared; every cached and uncached read is exact.
func TestB10SpliceWindowCache(t *testing.T) {
	l := b10SmallLab(t)
	data, err := os.ReadFile("testing-harnesses/z3/parquetz3/testdata/pa_snappy_v1.parquet")
	mustOK(t, err)
	l.put("p.parquet", data)
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2}, Profile: "archive", Splice: true})
	l.reclaim()
	l.s.SetWindowCache(16 << 20)
	defer l.s.SetWindowCache(0)
	wc := l.s.win.Load()
	for pass := range 3 {
		for k, want := range l.want {
			for _, off := range []int64{int64(len(want)) / 3, int64(len(want)) / 2} {
				n := min(int64(4096), int64(len(want))-off)
				if got := l.read(k, byteRange{off, off + n - 1}); !bytes.Equal(got, want[off:off+n]) {
					t.Fatalf("pass %d %s at %d", pass, k, off)
				}
			}
		}
	}
	if wc.st.hits.Load() == 0 {
		t.Fatal("no cache hit on repeated ranges")
	}
	l.exact("cached")
}

// TestB10IncumbentNoWorse: more options never plan a worse objective. A history whose nested jar is byte-
// identical in every version (decoding it only adds a recipe) and the nested labs, each planned with and
// without deep streams (every nested unit decodable) on the archive and mixed profiles.
func TestB10IncumbentNoWorse(t *testing.T) {
	jarLab := func(t *testing.T) *authLab {
		l := newAuthLab(t)
		fs := b8Tree(61, 12)
		var args []any
		for _, f := range fs[:8] {
			args = append(args, f.name, f.data)
		}
		jar := b10Zip(t, args...)
		for i := range 3 {
			l.put("dist.tar.gz", b10Gzip(t, b10Tar(t, "lib/core.jar", jar, "README", b5Text(uint64(70+i), 30_000), "bin/run", b5Text(uint64(80+i), 9000)), 6))
		}
		l.reclaim()
		return l
	}
	for name, mk := range map[string]func(*testing.T) *authLab{"identical-jar": jarLab, "small": b10SmallLab} {
		for _, prof := range []string{"archive", "mixed"} {
			l := mk(t)
			plan := func(splice bool) OptimizeReport {
				p := b6Policy(OptimizePolicy{AuthPolicy: AuthPolicy{MaxDepth: 2, AllowRep: true}, Profile: prof, Canonical: true, Splice: splice})
				p.spliceAll = true
				rep, err := l.s.AuthorityOptimize(context.Background(), p)
				mustOK(t, err)
				return rep
			}
			without, with := plan(false), plan(true)
			if with.Planned.Objective > without.Planned.Objective+1e-6 {
				t.Fatalf("%s/%s: deep streams planned %.0f, without %.0f", name, prof, with.Planned.Objective, without.Planned.Objective)
			}
			t.Logf("%s/%s: objective %.0f -> %.0f", name, prof, without.Planned.Objective, with.Planned.Objective)
		}
	}
}

// TestB10SSTKindRanges: ranges of a structured SST representation (kind 9) rebuild only their data blocks and
// are exact at every block boundary and at random offsets.
func TestB10SSTKindRanges(t *testing.T) {
	l := newAuthLab(t)
	for _, p := range []string{"testdata/p7/rocksdb_lz4_small.sst", "testing-harnesses/z3/sstz3/testdata/rocksdb-zstd.sst"} {
		data, err := os.ReadFile(p)
		mustOK(t, err)
		key := filepath.Base(p)
		l.put(key, data)
		l.s.rep.hardCodecs.Store(true)
		mem := map[[32]byte][]byte{}
		root, _, err := l.s.repConstructMem(l.man(key), bytes.NewReader(data), nil, 0, true, mem)
		l.s.rep.hardCodecs.Store(false)
		mustOK(t, err)
		src := l.s.newRepSource(root.Descs, nil)
		src.mem = mem
		env := &repEnv{Src: src, Limits: repProdLimits}
		call, err := env.open(root.Rep)
		mustOK(t, err)
		if call.Rep.Kind != repKindSST {
			t.Fatalf("%s: kind %d", key, call.Rep.Kind)
		}
		r := rand.New(rand.NewPCG(4, 4))
		size := int64(len(data))
		for i := range 200 {
			off := r.Int64N(size)
			n := min(int64([]int{1, 4096, 65536}[i%3]), size-off)
			produce, err := repRangeReader(call, off, n)
			if err != nil || produce == nil {
				t.Fatalf("%s: no range reader: %v", key, err)
			}
			var b bytes.Buffer
			if err := produce(&b); err != nil || !bytes.Equal(b.Bytes(), data[off:off+n]) {
				t.Fatalf("%s [%d,+%d): %v", key, off, n, err)
			}
		}
		src.close()
	}
}
