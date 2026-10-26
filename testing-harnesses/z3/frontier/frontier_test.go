package frontier

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBaseDeterministic(t *testing.T) {
	a, b := Base(), Base()
	if len(a.Entries) != len(b.Entries) || a.Stats() != b.Stats() {
		t.Fatalf("base differs: %v vs %v", a.Stats(), b.Stats())
	}
	st := a.Stats()
	if st.Logical < 128<<20 || st.Logical > 256<<20 {
		t.Fatalf("base logical size %d outside 128-256 MiB", st.Logical)
	}
	ix := a.index()
	small := ix["tiny/d00/s00/f0000.txt"].Body
	h1, h2 := sha256.Sum256(small.Bytes()), sha256.Sum256(b.index()["tiny/d00/s00/f0000.txt"].Body.Bytes())
	if h1 != h2 {
		t.Fatal("content not deterministic")
	}
}

func desc(e *Entry) string {
	b := "-"
	if e.Body != nil {
		b = fmt.Sprintf("%+v", *e.Body)
	}
	return fmt.Sprintf("%s|%d|%o|%d|%v|%s|%s", e.Path, e.Kind, e.Mode, e.Mtime, e.Xattr, e.Link, b)
}

func TestRevisionsDifferAsDesigned(t *testing.T) {
	base := Base()
	count := func(rev string) (changed, added, removed int) {
		r := Revision(base, rev)
		bi, ri := base.index(), r.index()
		for p, e := range ri {
			o, ok := bi[p]
			if !ok {
				if e.Kind != KDir {
					added++
				}
				continue
			}
			if desc(e) != desc(o) {
				changed++
			}
		}
		for p, e := range bi {
			if _, ok := ri[p]; !ok && e.Kind != KDir {
				removed++
			}
		}
		return
	}
	if c, a, r := count("R0"); c+a+r != 0 {
		t.Fatalf("R0 differs from base: %d %d %d", c, a, r)
	}
	if c, a, r := count("R10"); c+a+r != 0 {
		t.Fatalf("R10 must be the same logical tree: %d %d %d", c, a, r)
	}
	if Revision(base, "R10").Order != "rev" {
		t.Fatal("R10 must perturb producer order")
	}
	if c, _, _ := count("R1"); c != 1 {
		t.Fatalf("R1 changes %d files, want 1", c)
	}
	if _, a, r := count("R5"); a != 40 || r != 40 {
		t.Fatalf("R5 add/remove = %d/%d", a, r)
	}
	if _, a, r := count("R4"); a == 0 || r == 0 {
		t.Fatalf("R4 rename must add and remove paths: %d/%d", a, r)
	}
	if c, _, _ := count("R6"); c < 2500 {
		t.Fatalf("R6 changed only %d tiny files", c)
	}
	// metadata-only: no payload description changes
	bi := base.index()
	for _, e := range Revision(base, "R3").Entries {
		if e.Kind == KFile && fmt.Sprintf("%+v", *e.Body) != fmt.Sprintf("%+v", *bi[e.Path].Body) {
			t.Fatalf("R3 changed payload of %s", e.Path)
		}
	}
	// every revision keeps hard-link targets valid
	for _, rev := range Revisions {
		r := Revision(base, rev)
		ix := r.index()
		for _, e := range r.Entries {
			if e.Kind == KHard && (ix[e.Link] == nil || ix[e.Link].Kind != KFile) {
				t.Fatalf("%s: dangling hard link %s -> %s", rev, e.Path, e.Link)
			}
		}
	}
}

func smallTree() *Tree {
	t := &Tree{Rev: "T"}
	t.Entries = append(t.Entries,
		file("a/x.txt", 0o644, "text", 1, 5000),
		file("a/y.bin", 0o755, "rand", 2, 9000),
		&Entry{Path: "a/h.txt", Kind: KHard, Link: "a/x.txt"},
		&Entry{Path: "a/l", Kind: KSymlink, Mode: 0o777, Mtime: 1_700_000_001, Link: "x.txt"})
	sp := file("a/sparse", 0o644, "rand", 0, 1<<20)
	sp.Body.Sparse = true
	sp.Body.Extents = []Extent{{Off: 0, Len: 8192, Seed: 3}, {Off: 512 << 10, Len: 4096, Seed: 4}}
	t.Entries = append(t.Entries, sp)
	t.Entries[0].Xattr = map[string]string{"user.z3.tag": "v"}
	t.ensureDirs()
	t.fixLinks()
	return t
}

func TestMaterializeSnapshotTar(t *testing.T) {
	dir := t.TempDir() + "/tree"
	tr := smallTree()
	if err := Materialize(tr, dir); err != nil {
		t.Skip("materialize (xattr?) unsupported here:", err)
	}
	snap, err := Snapshot(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if s := snap["a/sparse"]; s.Size != 1<<20 || s.Alloc >= 1<<20 {
		t.Fatalf("sparse file not sparse: %+v", s)
	}
	if snap["a/h.txt"].Hard != "a/h.txt" && snap["a/h.txt"].Hard != "a/x.txt" || snap["a/h.txt"].Hard == "" {
		t.Fatalf("hard link lost: %+v", snap["a/h.txt"])
	}
	if snap["a/x.txt"].Xattr != "user.z3.tag=v" {
		t.Fatalf("xattr: %q", snap["a/x.txt"].Xattr)
	}
	if n, s := Diff(snap, snap, Features{Mtime: true, Xattr: true, Hard: true, Mode: true}); n != 0 {
		t.Fatalf("self diff %v", s)
	}
	for _, order := range []string{"", "rev"} {
		tr.Order = order
		var buf bytes.Buffer
		if err := WriteTar(&buf, tr, dir); err != nil {
			t.Fatal(err)
		}
		r := tar.NewReader(bytes.NewReader(buf.Bytes()))
		types := map[string]byte{}
		for {
			h, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			types[h.Name] = h.Typeflag
			if h.Uid != 0 || h.Uname != "" {
				t.Fatalf("non-deterministic owner in %s", h.Name)
			}
		}
		links := 0
		for _, ty := range types {
			if ty == tar.TypeLink {
				links++
			}
		}
		if links != 1 {
			t.Fatalf("order %q: %d hard-link entries, want 1", order, links)
		}
	}
}

func zstdFrame(payload []byte) []byte {
	f := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x20, byte(len(payload))}
	v := uint32(len(payload))<<3 | 1
	f = append(f, byte(v), byte(v>>8), byte(v>>16))
	return append(f, payload...)
}

func TestScanZstdHandcrafted(t *testing.T) {
	a, b := zstdFrame([]byte("hello")), zstdFrame([]byte("world!!"))
	skip := append([]byte{0x50, 0x2A, 0x4D, 0x18, 3, 0, 0, 0}, 1, 2, 3)
	blob := append(append(append([]byte{}, a...), skip...), b...)
	fr, err := ScanZstd(bytes.NewReader(blob), int64(len(blob)), 10, 10)
	if err != nil || len(fr) != 3 || !fr[1].Skippable || fr[0].Len != int64(len(a)) || fr[2].Off != int64(len(a)+len(skip)) || len(fr[2].Blocks) != 1 {
		t.Fatalf("frames %+v err %v", fr, err)
	}
	// every strict prefix must fail or stop cleanly, never panic or over-read
	for n := 1; n < len(blob); n++ {
		if _, err := ScanZstd(bytes.NewReader(blob[:n]), int64(n), 10, 10); err == nil && n != len(a) && n != len(a)+len(skip) {
			t.Fatalf("prefix %d accepted", n)
		}
	}
	if _, err := ScanZstd(bytes.NewReader(blob), int64(len(blob)), 2, 10); err == nil {
		t.Fatal("frame limit not enforced")
	}
	if _, err := ScanZstd(bytes.NewReader(blob), int64(len(blob)), 10, 1); err == nil {
		t.Fatal("block limit not enforced")
	}
	bad := append([]byte{}, a...)
	binary.LittleEndian.PutUint16(bad[6:], 0xffff) // block header claims a huge raw block
	if _, err := ScanZstd(bytes.NewReader(bad), int64(len(bad)), 10, 10); err == nil {
		t.Fatal("overrunning block accepted")
	}
	bad = append([]byte{}, a...)
	bad[6] = bad[6]&^6 | 6 // reserved block type 3
	if _, err := ScanZstd(bytes.NewReader(bad), int64(len(bad)), 10, 10); err == nil {
		t.Fatal("reserved block type accepted")
	}
	huge := []byte{0x50, 0x2A, 0x4D, 0x18, 0xff, 0xff, 0xff, 0xff}
	if _, err := ScanZstd(bytes.NewReader(huge), int64(len(huge)), 10, 10); err == nil {
		t.Fatal("hostile skippable length accepted")
	}
}

func TestScanZstdReal(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	in := bytes.Repeat([]byte("zeros3 frontier "), 300000)
	cmd := exec.Command("zstd", "-3", "-q", "-c")
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ScanZstd(bytes.NewReader(out), int64(len(out)), 10, 1<<10)
	if err != nil || len(fr) != 1 || fr[0].Len != int64(len(out)) || !fr[0].Checksum {
		t.Fatalf("%+v %v", fr, err)
	}
	var sum int64
	for _, b := range fr[0].Blocks {
		sum += b.Len
	}
	if sum+4+4+int64(len(out))-int64(len(out)) > int64(len(out)) { // blocks + header + checksum fit
		t.Fatal("blocks overflow frame")
	}
}

func TestGzipMembers(t *testing.T) {
	var blob bytes.Buffer
	for i := 0; i < 3; i++ {
		w, _ := gzip.NewWriterLevel(&blob, gzip.BestCompression)
		w.Write(bytes.Repeat([]byte{byte(i)}, 1000*(i+1)))
		w.Close()
	}
	b := blob.Bytes()
	sp, err := GzipMembers(b, 10)
	if err != nil || len(sp) != 3 {
		t.Fatalf("%v %v", sp, err)
	}
	var end int64
	for _, s := range sp {
		if s.Off != end {
			t.Fatalf("spans do not tile: %v", sp)
		}
		end += s.Len
	}
	if end != int64(len(b)) {
		t.Fatal("spans do not cover blob")
	}
	if _, err := GzipMembers(b, 2); err == nil {
		t.Fatal("member limit")
	}
	for _, bad := range [][]byte{b[:len(b)-3], append(append([]byte{}, b...), 0xde, 0xad), b[:5]} {
		if _, err := GzipMembers(bad, 10); err == nil {
			t.Fatal("malformed gzip stream accepted")
		}
	}
}

func TestTOCChunks(t *testing.T) {
	js := `{"version":1,"entries":[
	 {"name":"a","type":"reg","size":10,"offset":100,"chunkDigest":"sha256:aa"},
	 {"name":"b","type":"reg","size":30,"offset":150,"chunkSize":20,"chunkDigest":"sha256:bb"},
	 {"name":"b","type":"chunk","offset":190,"chunkOffset":20,"chunkSize":10,"chunkDigest":"sha256:cc"},
	 {"name":"d","type":"dir"}]}`
	toc, err := ParseTOC([]byte(js), 10)
	if err != nil {
		t.Fatal(err)
	}
	cs := toc.Chunks(300)
	if len(cs) != 3 || cs[0].Len != 50 || cs[1].Len != 40 || cs[2].Len != 110 || cs[2].ULen != 10 || cs[0].ULen != 10 {
		t.Fatalf("%+v", cs)
	}
	if _, err := ParseTOC([]byte(js), 2); err == nil {
		t.Fatal("entry limit")
	}
	if _, err := ParseTOC([]byte(strings.Repeat("{", 10)), 2); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestChainAndFloor(t *testing.T) {
	c := NewChain()
	data := bytes.Repeat([]byte("abcdefgh"), 100000)
	a, err := c.AddStream(bytes.NewReader(data))
	if err != nil || a.NewChunks == 0 {
		t.Fatal(err)
	}
	b, _ := c.AddStream(bytes.NewReader(data))
	if b.NewChunks != 0 || b.Packed != 0 || b.Incr != RefBytes*int64(b.Chunks) {
		t.Fatalf("identical stream must be all reuse: %+v", b)
	}
	bs := NewBlockSet()
	ids := [][32]byte{{1}, {2}, {1}}
	bc := bs.Add(ids, []int64{10, 20, 10})
	if bc.New != 2 || bc.NewBytes != 30 || bc.Refs != 3*NativeRefBytes {
		t.Fatalf("%+v", bc)
	}
	dir := t.TempDir() + "/tree"
	tr := smallTree()
	if err := Materialize(tr, dir); err != nil {
		t.Skip(err)
	}
	f := NewFloor()
	k1, err := f.Add(tr, dir)
	if err != nil || k1.Files != 3 || k1.ObjNew != 3 {
		t.Fatalf("%+v %v", k1, err)
	}
	k2, _ := f.Add(tr, dir)
	if k2.FilePacked != 0 || k2.ObjNew != 0 {
		t.Fatalf("second identical revision must add no file bytes: %+v", k2)
	}
	_ = os.Remove
}
