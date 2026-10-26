package frontier

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func miniTree(t *testing.T) (*Tree, string) {
	t.Helper()
	tr := &Tree{Rev: "T"}
	for i := 0; i < 25; i++ {
		tr.Entries = append(tr.Entries, file(fmt.Sprintf("d/t%02d.txt", i), 0o644, "text", uint64(i), int64(100+i*37)))
	}
	tr.Entries = append(tr.Entries,
		file("big.bin", 0o644, "rand", 77, 300<<10),
		&Entry{Path: "h.bin", Kind: KHard, Link: "big.bin"},
		&Entry{Path: "l", Kind: KSymlink, Mode: 0o777, Mtime: baseMtime, Link: "big.bin"})
	sp := file("sp.img", 0o644, "rand", 0, 1<<20)
	sp.Body.Sparse = true
	sp.Body.Extents = []Extent{{Off: 0, Len: 8192, Seed: 1}, {Off: 768 << 10, Len: 4096, Seed: 2}}
	tr.Entries = append(tr.Entries, sp)
	tr.ensureDirs()
	tr.fixLinks()
	dir := filepath.Join(t.TempDir(), "tree")
	if err := Materialize(tr, dir); err != nil {
		t.Skip("cannot materialise:", err)
	}
	return tr, dir
}

func zlibDec(src []byte, max int) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err == nil && len(out) > max {
		err = fmt.Errorf("too large")
	}
	return out, err
}

// findTool prefers the pinned build under the Z3 tools cache (erofs-utils 1.8.x) over a distro copy.
func findTool(name string) string {
	if d := os.Getenv("Z3_TOOLS"); d != "" {
		if _, err := os.Stat(filepath.Join(d, "tools/bin", name)); err == nil {
			return filepath.Join(d, "tools/bin", name)
		}
	}
	if p := filepath.Join("/root/z3cache/tools/bin", name); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// mutate runs scan on prefixes and byte-flipped copies; it must neither panic nor hang.
func mutate(t *testing.T, img []byte, scan func([]byte) error) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rng := rand.New(rand.NewPCG(5, 6))
		for i := 0; i < 60; i++ {
			n := rng.IntN(len(img))
			scan(img[:n])
		}
		for i := 0; i < 400; i++ {
			c := append([]byte(nil), img...)
			for k := 0; k < 1+rng.IntN(4); k++ {
				p := rng.IntN(min(len(c), 8192)) // concentrate on superblock / early structures
				if k%2 == 1 {
					p = rng.IntN(len(c))
				}
				c[p] ^= byte(1 + rng.IntN(255))
			}
			scan(c)
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("scanner hang on malformed input")
	}
}

// rebuildFromPieces stores each piece by content digest and rebuilds the image from the
// ordered digest list (the native-block recipe the Z3-24 cost model assumes), then compares.
func rebuildFromPieces(t *testing.T, img []byte, spans []Span) {
	t.Helper()
	store := map[[32]byte][]byte{}
	var recipe [][32]byte
	for _, sp := range spans {
		p := img[sp.Off : sp.Off+sp.Len]
		id := sha256.Sum256(p)
		store[id] = p
		recipe = append(recipe, id)
	}
	var out bytes.Buffer
	for _, id := range recipe {
		out.Write(store[id])
	}
	if out.Len() != len(img) || sha256.Sum256(out.Bytes()) != sha256.Sum256(img) || !bytes.Equal(out.Bytes(), img) {
		t.Fatalf("rebuild mismatch: %d vs %d bytes", out.Len(), len(img))
	}
}

func TestScanSquashfs(t *testing.T) {
	mk := findTool("mksquashfs")
	if mk == "" {
		t.Skip("mksquashfs not installed")
	}
	_, dir := miniTree(t)
	img := filepath.Join(t.TempDir(), "i.sqfs")
	if out, err := exec.Command(mk, dir, img, "-comp", "gzip", "-b", "131072", "-noappend", "-quiet", "-no-progress", "-mkfs-time", "1700000000").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(img)
	im, err := ScanSquashfs(bytes.NewReader(b), int64(len(b)), zlibDec, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	pos := int64(0)
	for _, bl := range im.Blocks {
		if bl.Off != pos {
			t.Fatalf("regions do not tile the image at %d (block %+v)", pos, bl)
		}
		pos += bl.Len
		sum += bl.Len
	}
	if sum != int64(len(b)) {
		t.Fatalf("regions cover %d of %d", sum, len(b))
	}
	var sp0 []Span
	for _, bl := range im.Blocks {
		sp0 = append(sp0, Span{bl.Off, bl.Len})
	}
	rebuildFromPieces(t, b, sp0)
	big := im.Files["big.bin"]
	if big == nil || len(big.Blocks) != 3 || big.Size != 300<<10 {
		t.Fatalf("big.bin: %+v", big)
	}
	if im.Files["h.bin"] != big {
		t.Fatal("hard link should resolve to the same inode")
	}
	sp := im.Files["sp.img"]
	if sp == nil || sp.Holes == 0 || len(sp.Blocks) != 2 {
		t.Fatalf("sparse file: %+v", sp)
	}
	if tiny := im.Files["d/t03.txt"]; tiny == nil || tiny.Frag < 0 || len(tiny.Blocks) != 0 {
		t.Fatalf("tiny file should live in a fragment: %+v", tiny)
	}
	if len(im.DirMeta["d"]) == 0 {
		t.Fatal("no directory metadata spans")
	}
	mutate(t, b, func(c []byte) error {
		_, err := ScanSquashfs(bytes.NewReader(c), int64(len(c)), zlibDec, 1000)
		return err
	})
	// hostile declarations
	for _, off := range []int{12, 40, 64, 72, 80} { // block size, bytes used, tables
		c := append([]byte(nil), b...)
		c[off], c[off+1], c[off+2], c[off+3] = 0xff, 0xff, 0xff, 0x7f
		if _, err := ScanSquashfs(bytes.NewReader(c), int64(len(c)), zlibDec, 1000); err == nil {
			t.Fatalf("bogus superblock field at %d accepted", off)
		}
	}
	if _, err := ScanSquashfs(bytes.NewReader(b), int64(len(b)), zlibDec, 5); err == nil {
		t.Fatal("inode limit not enforced")
	}
}

func TestScanErofs(t *testing.T) {
	mk := findTool("mkfs.erofs")
	if mk == "" {
		t.Skip("mkfs.erofs not installed")
	}
	_, dir := miniTree(t)
	for _, mode := range []struct {
		name string
		args []string
	}{{"plain", nil}, {"chunk", []string{"--chunksize=65536"}}} {
		img := filepath.Join(t.TempDir(), mode.name+".erofs")
		args := append(append([]string{"-T1700000000", "--mkfs-time", "-U", "11111111-1111-1111-1111-111111111111", "--quiet"}, mode.args...), img, dir)
		if out, err := exec.Command(mk, args...).CombinedOutput(); err != nil {
			t.Skipf("mkfs.erofs unsupported option: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(img)
		im, err := ScanErofs(bytes.NewReader(b), int64(len(b)), nil, 1000)
		if err != nil {
			t.Fatalf("%s: %v", mode.name, err)
		}
		var sum int64
		pos := int64(0)
		for _, g := range im.Regions {
			if g.Off != pos {
				t.Fatalf("%s: regions do not tile at %d", mode.name, pos)
			}
			pos += g.Len
			sum += g.Len
		}
		if sum != int64(len(b)) {
			t.Fatalf("%s: regions cover %d of %d", mode.name, sum, len(b))
		}
		var sp0 []Span
		for _, g := range im.Regions {
			sp0 = append(sp0, Span{g.Off, g.Len})
		}
		rebuildFromPieces(t, b, sp0)
		big := im.Files["big.bin"]
		if big == nil || big.Size != 300<<10 || im.Files["h.bin"] != big {
			t.Fatalf("%s: big.bin / hardlink: %+v", mode.name, big)
		}
		var phys int64
		for _, e := range big.Extents {
			phys += e.Len
		}
		if phys < 300<<10 {
			t.Fatalf("%s: big.bin extents cover %d", mode.name, phys)
		}
		sp := im.Files["sp.img"]
		if mode.name == "chunk" && (sp == nil || sp.Holes == 0 || im.HoleBytes == 0) {
			t.Fatalf("chunk-based sparse file should report holes: %+v", sp)
		}
		if mode.name == "plain" && im.HoleBytes != 0 {
			t.Fatal("plain layout cannot represent holes")
		}
		if len(im.DirData["d"]) == 0 || im.InodeAt["d/t03.txt"].Len == 0 || im.EntryAt["d/t03.txt"].Len == 0 {
			t.Fatal("path-resolution spans missing")
		}
		mutate(t, b, func(c []byte) error { _, err := ScanErofs(bytes.NewReader(c), int64(len(c)), nil, 1000); return err })
	}
	if _, err := ScanErofs(bytes.NewReader(make([]byte, 4096)), 4096, nil, 10); err == nil {
		t.Fatal("zero image accepted")
	}
}

// FuzzScanImages is a short smoke target: selector byte 0 picks the scanner.
func FuzzScanImages(f *testing.F) {
	dir := f.TempDir() + "/tree"
	tr := &Tree{Rev: "F"}
	for i := 0; i < 8; i++ {
		tr.Entries = append(tr.Entries, file(fmt.Sprintf("d/t%d.txt", i), 0o644, "text", uint64(i), int64(100+i*500)))
	}
	tr.Entries = append(tr.Entries, file("big.bin", 0o644, "rand", 3, 140<<10))
	tr.ensureDirs()
	if err := Materialize(tr, dir); err != nil {
		f.Skip(err)
	}
	if mk := findTool("mksquashfs"); mk != "" {
		img := f.TempDir() + "/s.img"
		if exec.Command(mk, dir, img, "-comp", "gzip", "-noappend", "-quiet", "-no-progress", "-mkfs-time", "1").Run() == nil {
			b, _ := os.ReadFile(img)
			f.Add(append([]byte{0}, b...))
		}
	}
	if mk := findTool("mkfs.erofs"); mk != "" {
		img := f.TempDir() + "/e.img"
		if exec.Command(mk, "-T1", "--mkfs-time", "--quiet", "-U", "11111111-1111-1111-1111-111111111111", img, dir).Run() == nil {
			b, _ := os.ReadFile(img)
			f.Add(append([]byte{1}, b...))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 || len(data) > 1<<20 {
			return
		}
		body := data[1:]
		if data[0]&1 == 0 {
			ScanSquashfs(bytes.NewReader(body), int64(len(body)), zlibDec, 200)
		} else {
			ScanErofs(bytes.NewReader(body), int64(len(body)), nil, 200)
		}
	})
}
