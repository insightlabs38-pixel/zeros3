package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- B8 cross-format canonical representations -------------------------------

type b8File struct {
	name string
	data []byte
}

// b8Tree is a deterministic file tree: text files of mixed sizes, an empty file, a duplicate name and a
// directory entry's worth of variety.
func b8Tree(seed uint64, n int) []b8File {
	var fs []b8File
	for i := range n {
		size := []int{0, 300, 5000, 40 << 10, 200 << 10, 700 << 10}[i%6]
		fs = append(fs, b8File{fmt.Sprintf("pkg/dir%d/file%03d.txt", i%4, i), b5Text(seed*1000+uint64(i), size)})
	}
	return fs
}

func b8Edit(fs []b8File, seed uint64) []b8File {
	out := slices.Clone(fs)
	for i := range out {
		if i%5 == 2 && len(out[i].data) > 0 {
			out[i] = b8File{out[i].name, b5Mutate(out[i].data, seed+uint64(i), 3)}
		}
	}
	return append(out, b8File{"pkg/new.txt", b5Text(seed, 9000)})
}

func b8Tar(t testing.TB, fs []b8File, mtime int64) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, f := range fs {
		if err := w.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), ModTime: time.Unix(mtime, 0), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		w.Write(f.data)
	}
	w.Close()
	return buf.Bytes()
}

func b8Gzip(t testing.TB, b []byte, level int) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// b8Zip writes fs in reverse order (a layout unlike the tar's): deflate, stored and empty entries, data
// descriptors (streamed entries), a duplicate name and an archive comment.
func b8Zip(t testing.TB, fs []b8File) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	w.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, 6) })
	for i := len(fs) - 1; i >= 0; i-- {
		f := fs[i]
		m := zip.Deflate
		if i%7 == 3 {
			m = zip.Store
		}
		var fw io.Writer
		var err error
		if i%2 == 0 {
			fw, err = w.Create(f.name) // data descriptor
		} else {
			fw, err = w.CreateHeader(&zip.FileHeader{Name: f.name, Method: m, Modified: time.Unix(1700000000, 0)})
		}
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(f.data)
	}
	dup, _ := w.Create(fs[0].name)
	dup.Write([]byte("duplicate name, different bytes"))
	w.SetComment("b8")
	w.Close()
	return buf.Bytes()
}

func b8Optimize(t *testing.T, l *authLab, p OptimizePolicy) OptimizeReport {
	t.Helper()
	p.Apply, p.AllowRep, p.Canonical = true, true, true
	if p.MaxDepth == 0 {
		p.MaxDepth = 2
	}
	return l.optimize(p)
}

func b8Forms(t *testing.T, l *authLab) map[string]*authRecord {
	t.Helper()
	out := map[string]*authRecord{}
	for key := range l.want {
		sha, _ := decodeHexSHA256(l.man(key).ObjectSHA256)
		r, err := l.s.authLoad(sha)
		if err != nil {
			t.Fatal(err)
		}
		out[key] = r
	}
	return out
}

// TestB8CanonicalCrossFormat: a .tar.gz, the raw .tar of the same bytes, a ZIP of the same files and an edited
// release's .tar.gz share content through canonical streams. The plan applies, equals the materialized model,
// stays byte-exact (whole and ranges) through gc, repack, compact and reopen, deep-verifies, raises the
// "canon" feature, and a second run plans nothing.
func TestB8CanonicalCrossFormat(t *testing.T) {
	l := newAuthLab(t)
	v1, v2 := b8Tree(1, 24), b8Edit(b8Tree(1, 24), 5)
	tar1 := b8Tar(t, v1, 1700000000)
	l.put("rel/v1.tar.gz", b8Gzip(t, tar1, 6))
	l.put("rel/v1.tar", tar1)
	l.put("rel/v1.zip", b8Zip(t, v1))
	l.put("rel/v2.tar.gz", b8Gzip(t, b8Tar(t, v2, 1700000500), 9))
	l.reclaim()
	before := dirBytes(t, l.dir)
	rep := b8Optimize(t, l, OptimizePolicy{})
	if rep.Canon == nil || rep.Canon.Containers != 3 || rep.Canon.Rejected != 0 || rep.Canon.Shared != 1 {
		t.Fatalf("canonical analysis %+v", rep.Canon)
	}
	if rep.Failed != 0 || rep.Applied == 0 {
		t.Fatalf("applied %d failed %d: %+v", rep.Applied, rep.Failed, rep.Changes)
	}
	forms := b8Forms(t, l)
	tarSHA, _ := decodeHexSHA256(l.man("rel/v1.tar").ObjectSHA256)
	for _, k := range []string{"rel/v1.tar.gz", "rel/v2.tar.gz", "rel/v1.zip"} {
		r := forms[k]
		if r == nil || r.Form != authFormRep || len(r.Bases) != 1 {
			t.Fatalf("%s: not a canonical record: %+v", k, r)
		}
	}
	if forms["rel/v1.tar.gz"].Bases[0].SHA != tarSHA {
		t.Fatal("the .tar.gz stream is not the raw .tar object")
	}
	l.exact("applied")
	l.reclaim()
	if after := dirBytes(t, l.dir); after >= before*7/10 {
		t.Fatalf("store %d -> %d bytes: canonical sharing saved too little", before, after)
	}
	l.exact("reclaimed")
	l.offline(func() {})
	l.exact("reopened")
	l.verifyOK("canonical")
	g, err := l.s.rgBuild(context.Background(), OptimizePolicy{}.norm())
	if err != nil {
		t.Fatal(err)
	}
	g.partition()
	if m := newRGEval(g, g.currentSel()).state("m", false); m.Bytes != rep.Planned.Bytes {
		t.Fatalf("materialized model %d bytes, plan %d", m.Bytes, rep.Planned.Bytes)
	}
	if !slices.Contains(l.s.format.Features, authCanonFeature) {
		t.Fatalf("features %v lack %q", l.s.format.Features, authCanonFeature)
	}
	if again := b8Optimize(t, l, OptimizePolicy{}); again.Applied != 0 || len(again.Changes) != 0 {
		t.Fatalf("second run changed %d: %+v", again.Applied, again.Changes)
	}
	l.exact("rerun")
	// delete the raw tar: the canonical stream it was stays as a body the containers need
	if err := l.s.DeleteObject("b", "rel/v1.tar"); err != nil {
		t.Fatal(err)
	}
	delete(l.want, "rel/v1.tar")
	l.reclaim()
	l.exact("raw deleted")
	l.verifyOK("raw deleted")
}

// TestB8ZipExact: ZIP variants reconstruct exactly through the canonical representation, whole and at every
// segment boundary; unsupported or damaged archives are refused, never misread.
func TestB8ZipExact(t *testing.T) {
	l := newAuthLab(t)
	fs := b8Tree(7, 18)
	var empty bytes.Buffer
	zw := zip.NewWriter(&empty)
	zw.Create("empty.txt")
	zw.Close()
	cases := map[string][]byte{"mixed": b8Zip(t, fs), "only-empty": empty.Bytes()}
	for name, data := range cases {
		l.put(name, data)
	}
	for name, data := range cases {
		man := l.man(name)
		root, c, err := l.s.repConstructCanon(man, nil, 0, nil, false)
		if name == "only-empty" {
			if !errors.Is(err, errRepUnsupported) {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		env := &repEnv{Src: l.s.newRepSource(root.Descs, nil), Limits: repProdLimits, Body: func(sha [32]byte, n int64) (io.ReaderAt, error) {
			if sha != c.SHA || n != c.Size {
				return nil, errRepMissing
			}
			return c.File, nil
		}}
		call, err := env.open(root.Rep)
		if err != nil {
			t.Fatal(err)
		}
		segs, err := zipParse(call)
		if err != nil {
			t.Fatal(err)
		}
		var cuts []int64
		for _, s := range segs {
			cuts = append(cuts, s.out, s.out+s.outLen)
		}
		r := rand.New(rand.NewPCG(3, 4))
		for range 200 {
			a := cuts[r.IntN(len(cuts))] + int64(r.IntN(9)) - 4
			a = min(max(a, 0), int64(len(data))-1)
			n := min(int64(1+r.IntN(70000)), int64(len(data))-a)
			var out bytes.Buffer
			if err := zipReadRange(call, a, n, &out); err != nil || !bytes.Equal(out.Bytes(), data[a:a+n]) {
				t.Fatalf("%s: range %d+%d inexact (%v)", name, a, n, err)
			}
		}
		var whole bytes.Buffer
		if n, sum, err := env.reconstruct(root.Rep, &whole); err != nil || n != int64(len(data)) || sum != sha256.Sum256(data) {
			t.Fatalf("%s: whole %v", name, err)
		}
		// without a body resolver (not an authority read) the representation refuses
		if _, _, err := (&repEnv{Src: env.Src, Limits: repProdLimits}).reconstruct(root.Rep, io.Discard); !errors.Is(err, errRepUnsupported) {
			t.Fatalf("%s: reconstruction without bodies: %v", name, err)
		}
		// a damaged stream fails closed
		bad := make([]byte, c.Size)
		c.File.ReadAt(bad, 0)
		bad[len(bad)/2] ^= 1
		env.Body = func([32]byte, int64) (io.ReaderAt, error) { return bytes.NewReader(bad), nil }
		env.Cache = nil
		if _, sum, err := env.reconstruct(root.Rep, io.Discard); err == nil && sum == sha256.Sum256(data) {
			t.Fatalf("%s: damaged stream reproduced the archive", name)
		}
		c.close()
	}
	// damaged archives: no representation, and never a panic
	z := cases["mixed"]
	for _, cut := range []int{len(z) - 1, len(z) - 30, len(z) / 2, 100} {
		l.put("cut", z[:cut])
		if _, c, err := l.s.repConstructCanon(l.man("cut"), nil, 0, nil, false); err == nil {
			c.close()
			t.Fatalf("truncated at %d: accepted", cut)
		}
	}
}

// TestB8ZipTableFuzz: a damaged table never panics and never yields a representation that parses as valid
// with different totals.
func TestB8ZipTableFuzz(t *testing.T) {
	l := newAuthLab(t)
	data := b8Zip(t, b8Tree(9, 8))
	l.put("z", data)
	root, c, err := l.s.repConstructCanon(l.man("z"), nil, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	env := &repEnv{Src: l.s.newRepSource(root.Descs, nil), Limits: repProdLimits}
	call, err := env.open(root.Rep)
	if err != nil {
		t.Fatal(err)
	}
	nt := len(call.Rep.Members) - 1
	table, err := readTable(call, nt, zipMaxTable)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(5, 6))
	for range 400 {
		bad := slices.Clone(table)
		for range 1 + r.IntN(4) {
			bad[r.IntN(len(bad))] = byte(r.Uint32())
		}
		if r.IntN(4) == 0 {
			bad = bad[:r.IntN(len(bad))]
		}
		mem := map[[32]byte][]byte{}
		sum := sha256.Sum256(bad)
		mem[sum] = bad
		rep := *call.Rep
		rep.Members = append([]repMember{{Role: repStored, SHA: sum, Size: int64(len(bad))}}, call.Rep.Members[nt])
		rep.Recipe = []byte{1}
		src := l.s.newRepSource(nil, nil)
		src.mem = mem
		fc := (&repEnv{Src: src, Limits: repProdLimits}).call([32]byte{1}, &rep, 1, nil, nil)
		segs, err := zipParse(fc)
		if err == nil {
			var out, plain int64
			for _, s := range segs {
				out, plain = out+s.outLen, plain+s.plainLen
			}
			if out != rep.Size || plain != rep.Members[1].Size {
				t.Fatal("a damaged table parsed with inconsistent totals")
			}
		}
	}
}

// TestB8RecordShape: rep records with bases are checked against the body references of their
// representation; a mismatch is refused at publication and faults reachability.
func TestB8RecordShape(t *testing.T) {
	// Removes, renames or substitutes record files: the legacy one-file-per-record layout (B10 has the grouped analogues).
	authLegacyLayout(t)
	l := newAuthLab(t)
	fs := b8Tree(11, 10)
	tarb := b8Tar(t, fs, 1)
	l.put("a.tar.gz", b8Gzip(t, tarb, 6))
	l.put("a.tar", tarb)
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{})
	sha, _ := decodeHexSHA256(l.man("a.tar.gz").ObjectSHA256)
	rec, err := l.s.authLoad(sha)
	if err != nil || rec == nil || len(rec.Bases) != 1 {
		t.Fatalf("no canonical record: %v %+v", err, rec)
	}
	// shape: bases without depth, depth without bases
	for _, bad := range []authRecord{{SHA: rec.SHA, Size: rec.Size, Gen: rec.Gen, Form: authFormRep, Root: rec.Root, Bases: rec.Bases},
		{SHA: rec.SHA, Size: rec.Size, Gen: rec.Gen, Form: authFormRep, Root: rec.Root, Depth: 1}} {
		b, err := bad.marshal()
		if err != nil {
			continue
		}
		if _, err := unmarshalAuthRecord(b); err == nil {
			t.Fatalf("record shape bases %d depth %d accepted", len(bad.Bases), bad.Depth)
		}
	}
	// a base that is not the referenced body
	other, _ := decodeHexSHA256(l.man("a.tar").ObjectSHA256)
	forged := *rec
	forged.Gen++
	forged.Bases = []xdBaseRef{{other, rec.Bases[0].Size + 1}}
	if err := l.s.authPublish(&forged); err == nil {
		t.Fatal("a rep whose bases are not its body references was published")
	}
	// remove the stream's record: reads fail closed (never other bytes), reachability faults
	if err := os.Rename(l.s.authPath(rec.Bases[0].SHA), filepath.Join(l.dir, "tmp", "moved")); err != nil {
		t.Fatal(err)
	}
	l.s.authInvalidate()
	if got, err := b8TryRead(l.s, l.man("a.tar.gz")); err == nil && !bytes.Equal(got, l.want["a.tar.gz"]) {
		t.Fatal("inexact read with the stream record missing")
	}
	if res, err := l.s.Verify(true); err == nil && res.OK() {
		t.Fatal("deep verify passed with the stream record missing")
	}
	os.Rename(filepath.Join(l.dir, "tmp", "moved"), l.s.authPath(rec.Bases[0].SHA))
	l.s.authInvalidate()
	l.exact("restored")
}

func b8TryRead(s *Store, man manifestV1) ([]byte, error) {
	rd := s.openObjectReader(man, byteRange{0, man.TotalLength - 1})
	defer rd.close()
	var out []byte
	for {
		b, err := rd.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
}

// TestB8EvalCycles: streams never name each other (a cycle of nodes nothing else requires would keep itself
// required); random moves, including cycles through live containers, keep the incremental closure equal to a
// from-scratch evaluation.
func TestB8EvalCycles(t *testing.T) {
	l := newAuthLab(t)
	v := b8Tree(13, 12)
	for i := range 3 {
		l.put(fmt.Sprintf("r%d.tar.gz", i), b8Gzip(t, b8Tar(t, v, int64(i)), 6))
		v = b8Edit(v, uint64(20+i))
	}
	l.reclaim()
	g := b6Graph(t, l, OptimizePolicy{AuthPolicy: AuthPolicy{AllowRep: true}, Canonical: true})
	defer g.close()
	streams, mutual := 0, 0
	for i, nd := range g.nodes {
		if nd.canon != nil {
			streams++
		}
		for _, o := range nd.opts {
			for _, b := range o.bases {
				for _, ob := range g.nodes[b].opts {
					if slices.Contains(ob.bases, int32(i)) {
						if !nd.live && !g.nodes[b].live {
							t.Fatalf("streams %d and %d can name each other", i, b)
						}
						mutual++
					}
				}
			}
		}
	}
	if streams != 3 || mutual == 0 {
		t.Fatalf("%d streams, %d mutual option pairs: the test would not exercise cycles", streams, mutual)
	}
	r := rand.New(rand.NewPCG(7, 8))
	e := newRGEval(g, g.currentSel())
	for range 1500 {
		vtx := int32(r.IntN(len(g.nodes)))
		e.set(vtx, int32(r.IntN(len(g.nodes[vtx].opts))))
		if e.depth(vtx) < 0 || !e.feasible(vtx) {
			continue // a cycle: requirements may over-count while it lasts
		}
		f := newRGEval(g, e.sel)
		if f.bytes != e.bytes || f.writes != e.writes || !slices.Equal(f.req, e.req) || !slices.Equal(f.ref, e.ref) {
			t.Fatalf("incremental bytes %d writes %d, scratch %d %d", e.bytes, e.writes, f.bytes, f.writes)
		}
		if a, b := e.readSum(), f.readSum(); math.Abs(a-b) > 1e-6*max(1, b) {
			t.Fatalf("read cost %f vs %f", a, b)
		}
	}
}

// TestB8IncumbentAndProfiles: offering canonical options never plans a worse objective than the same run
// without them, the hot profile refuses replayed representations, and unsupported containers keep CDC.
func TestB8IncumbentAndProfiles(t *testing.T) {
	l := newAuthLab(t)
	v := b8Tree(17, 12)
	for i := range 3 {
		l.put("h.tar.gz", b8Gzip(t, b8Tar(t, v, int64(i)), 6))
		v = b8Edit(v, uint64(40+i))
	}
	l.put("noise.bin", b7Rand(5, 300<<10))
	l.put("fake.gz", append([]byte{0x1f, 0x8b, 8, 0}, b7Rand(6, 70000)...))
	l.reclaim()
	for _, prof := range []string{"archive", "mixed", "hot"} {
		base := OptimizePolicy{AuthPolicy: AuthPolicy{AllowRep: true, MaxDepth: 2}, Profile: prof}
		b8p := base
		b8p.Canonical = true
		without, with := b6Graph(t, l, base), b6Graph(t, l, b8p)
		e0, _ := without.solve("global", without.pol.ReadWeight)
		e1, _ := with.solve("global", with.pol.ReadWeight)
		if e1.objective() > e0.objective()+1 {
			t.Fatalf("%s: canonical options planned %.0f, without %.0f", prof, e1.objective(), e0.objective())
		}
		if prof == "hot" {
			for i := range with.nodes {
				if o := e1.opt(int32(i)); o.replay != rgReplayNone && !o.cur {
					t.Fatalf("hot profile selected replayed %s", o.label)
				}
			}
		}
		with.close()
	}
	rep := b8Optimize(t, l, OptimizePolicy{})
	if rep.Canon.Containers != 4 || rep.Canon.Rejected != 1 {
		t.Fatalf("containers %+v", rep.Canon)
	}
	for k, r := range b8Forms(t, l) {
		if strings.HasSuffix(k, ".bin") || strings.HasPrefix(k, "fake") {
			if r != nil && r.Form != authFormPin {
				t.Fatalf("%s: %+v", k, r)
			}
		}
	}
	l.exact("mixed store")
	l.verifyOK("mixed store")
}

// TestB8CrashMatrix SIGKILLs a canonical optimize at every publication boundary it crosses (stream chunks
// flushed, a record written, renamed into place, the optimizer about to publish), occurrence by occurrence.
// After each cut: every acknowledged object reads exactly, gc + repack + compact retire what the cut left
// unreferenced (a stream published before its container), deep verify passes, and a rerun completes the plan.
func TestB8CrashMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("process-kill matrix")
	}
	base := newAuthLab(t)
	v1, v2 := b8Tree(21, 10), b8Edit(b8Tree(21, 10), 3)
	base.put("a.tar.gz", b8Gzip(t, b8Tar(t, v1, 1), 6))
	base.put("a.zip", b8Zip(t, v1))
	base.put("b.tar.gz", b8Gzip(t, b8Tar(t, v2, 2), 6))
	base.reclaim()
	base.close()
	cuts := 0
	for _, point := range []string{hookCASAfterFlush, hookAuthRecordWritten, hookAuthAfterRename, hookOptimizeBeforePublish} {
		for occ := 1; occ <= 8; occ++ {
			l := &authLab{t: t, dir: t.TempDir(), want: base.want}
			if err := os.CopyFS(l.dir, os.DirFS(base.dir)); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(l.dir, "LOCK"))
			if !runKilled(t, "canonical", l.dir, point, occ) {
				break
			}
			cuts++
			stage := fmt.Sprintf("%s#%d", point, occ)
			l.open()
			l.exact(stage + " after kill")
			l.reclaim()
			l.exact(stage + " reclaimed")
			l.verifyOK(stage)
			rep := b8Optimize(t, l, OptimizePolicy{})
			if rep.Failed != 0 {
				t.Fatalf("%s: rerun failed %d: %+v", stage, rep.Failed, rep.Changes)
			}
			l.reclaim()
			l.exact(stage + " completed")
			l.verifyOK(stage + " completed")
			if again := b8Optimize(t, l, OptimizePolicy{}); again.Applied != 0 {
				t.Fatalf("%s: not converged: %+v", stage, again.Changes)
			}
			l.close()
		}
	}
	if cuts < 12 {
		t.Fatalf("only %d cuts reached", cuts)
	}
	t.Logf("%d process-kill cuts", cuts)
}

// TestB8OnlineMaintenance: B4 online passes over a canonical store retire what deleted containers leave (the
// raw tar a stream used to be, then a container and the stream only it needed) while live readers stay exact.
func TestB8OnlineMaintenance(t *testing.T) {
	l := newAuthLab(t)
	v1, v2 := b8Tree(31, 12), b8Edit(b8Tree(31, 12), 9)
	tar1 := b8Tar(t, v1, 1)
	l.put("v1.tar", tar1)
	l.put("v1.tar.gz", b8Gzip(t, tar1, 6))
	l.put("v1.zip", b8Zip(t, v1))
	l.put("v2.tar.gz", b8Gzip(t, b8Tar(t, v2, 2), 9))
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{})
	l.reclaim()
	files0, _ := l.s.authFiles()
	l.close()
	s := b4OpenMaint(t, l.dir)
	l.s = s
	zero := 0
	for _, del := range []string{"v1.tar", "v2.tar.gz"} {
		mustOK(t, s.DeleteObject("b", del))
		delete(l.want, del)
		res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
		mustOK(t, err)
		mustOK(t, s.ApplyHistoryPrune(&res))
		time.Sleep(10 * time.Millisecond)
		for range 2 {
			_, err := s.maintainOnline(context.Background(), b4Opts())
			mustOK(t, err)
			l.exact("online after deleting " + del)
		}
		l.verifyOK("online after deleting " + del)
	}
	files1, _ := l.s.authFiles()
	if len(files1) >= len(files0) {
		t.Fatalf("records %d -> %d: nothing retired", len(files0), len(files1))
	}
	l.close()
	l.open()
	l.exact("reopened")
	l.verifyOK("reopened")
}

// TestB8ConcurrentReads: eight readers of random ranges and whole objects over canonical gzip and ZIP
// containers sharing streams (shared body readers, ZIP read-ahead, the representation cache) stay exact.
func TestB8ConcurrentReads(t *testing.T) {
	l := newAuthLab(t)
	v1 := b8Tree(41, 16)
	tar1 := b8Tar(t, v1, 1)
	l.put("a.tar.gz", b8Gzip(t, tar1, 6))
	l.put("a.zip", b8Zip(t, v1))
	l.put("b.tar.gz", b8Gzip(t, b8Tar(t, b8Edit(v1, 2), 2), 6))
	l.reclaim()
	b8Optimize(t, l, OptimizePolicy{})
	l.reclaim()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 9))
			keys := slices.Sorted(maps.Keys(l.want))
			for i := range 40 {
				k := keys[r.IntN(len(keys))]
				want := l.want[k]
				a := int64(r.IntN(len(want)))
				n := min(int64(1+r.IntN(200000)), int64(len(want))-a)
				if i%10 == 0 {
					a, n = 0, int64(len(want))
				}
				_, _, got, err := l.s.GetObjectRange("b", k, byteRange{a, a + n - 1})
				if err != nil || !bytes.Equal(got, want[a:a+n]) {
					errs <- fmt.Errorf("%s %d+%d: %v", k, a, n, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
