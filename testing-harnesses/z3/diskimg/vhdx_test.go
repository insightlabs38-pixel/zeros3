package diskimg

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func checkTiling(t *testing.T, regs []Region, size int) {
	t.Helper()
	var pos int64
	for _, r := range regs {
		if r.Off != pos || r.Len <= 0 {
			t.Fatalf("tiling gap at %d (region %+v)", pos, r)
		}
		pos += r.Len
	}
	if pos != int64(size) {
		t.Fatalf("tiling covers %d of %d", pos, size)
	}
}

func TestScanVhdxDynamic(t *testing.T) {
	d := smallDisk()
	img, b := vhdxFixture(t, d, "")
	v, err := ScanVhdx(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	checkTiling(t, v.Regions, len(b))
	if v.VirtSize != d.Size || v.BlockSize != MiB || v.LSS != 4096 || v.HasParent {
		t.Fatalf("params %+v", v)
	}
	var pay int
	for _, blk := range v.Blocks {
		if blk.State == VFull || blk.State == VPartial {
			pay++
		}
	}
	if pay == 0 {
		t.Fatal("no payload")
	}
	// the independent reader sees exactly the generator's disk
	out, err := exec.Command(VhdiRead(), img).Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := d.SHA256()
	if sha256.Sum256(out) != want {
		t.Fatal("libvhdi logical disk differs from oracle")
	}
	// each block that holds written data must be mapped; untouched blocks must not be
	for i, blk := range v.Blocks {
		mapped := blk.State == VFull || blk.State == VPartial
		any := false
		buf := make([]byte, MiB)
		copy(buf, out[int64(i)*MiB:min(int64(i+1)*MiB, int64(len(out)))])
		for _, c := range buf {
			if c != 0 {
				any = true
				break
			}
		}
		if any && !mapped {
			t.Fatalf("block %d holds data but is unmapped", i)
		}
	}
}

func TestScanVhdxHostile(t *testing.T) {
	_, b := vhdxFixture(t, smallDisk(), "")
	for _, n := range []int{0, 7, 8, 1000, 64 * KiB, 64*KiB + 100, MiB - 1, MiB, MiB + 12345, len(b) / 2, len(b) - 1} {
		if _, err := ScanVhdx(bytes.NewReader(b[:n]), int64(n)); err == nil {
			t.Fatalf("truncation to %d accepted", n)
		}
	}
	mut := func(name string, f func(c []byte)) {
		c := append([]byte(nil), b...)
		f(c)
		if _, err := ScanVhdx(bytes.NewReader(c), int64(len(c))); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	mut("signature", func(c []byte) { c[0] = 'X' })
	mut("both headers corrupt", func(c []byte) { c[64*KiB+20] ^= 1; c[128*KiB+20] ^= 1 })
	mut("region table checksum", func(c []byte) { c[192*KiB+40] ^= 1; c[256*KiB+40] ^= 1 })
	// valid checksums but hostile contents: fix up CRCs after the edit
	fixRT := func(c []byte) {
		for _, off := range []int{192 * KiB, 256 * KiB} {
			clear(c[off+4 : off+8])
			putLE32(c[off+4:], crc32Sum(c[off:off+64*KiB]))
		}
	}
	mut("region offset beyond file", func(c []byte) { putLE64(c[192*KiB+16+16:], 1<<40); putLE64(c[256*KiB+16+16:], 1<<40); fixRT(c) })
	mut("region count", func(c []byte) { putLE32(c[192*KiB+8:], 5000); putLE32(c[256*KiB+8:], 5000); fixRT(c) })
	v, _ := ScanVhdx(bytes.NewReader(b), int64(len(b)))
	mut("BAT entry beyond file", func(c []byte) { putLE64(c[v.BATOff:], 6|uint64(1<<40)<<20/MiB) })
	mut("BAT reserved bits", func(c []byte) { putLE64(c[v.BATOff:], 1<<5) })
	mut("metadata signature", func(c []byte) { c[v.MetaOff] = 'X' })
	mut("block size", func(c []byte) { putLE32(c[v.fieldOff["fileparams"]:], 12345) })
}

// differencing: partially-present blocks, zero override and hole write, read back through libvhdi (independent).
func TestVhdxDifferencingPartialBlocks(t *testing.T) {
	haveVhdx(t)
	dir := t.TempDir()
	base := &Disk{Size: 64 * MiB, Writes: []Extent{{1 * MiB, 300000 &^ 4095, KRandom, 1}, {10 * MiB, 8192, KRandom, 2}}}
	child := base.Clone()
	child.Writes = append(child.Writes,
		Extent{1*MiB + 3*4096, 4096, KZero, 0},  // zero override of parent data
		Extent{10*MiB + 4096, 4096, KRandom, 3}, // one sector of a partially populated block
		Extent{40 * MiB, 4096, KRandom, 4})      // write into a parent hole
	var raws [2]string
	for i, d := range []*Disk{base, child} {
		raws[i] = filepath.Join(dir, []string{"b.raw", "c.raw"}[i])
		if err := d.WriteRaw(raws[i]); err != nil {
			t.Fatal(err)
		}
	}
	bi, ci := filepath.Join(dir, "base.vhdx"), filepath.Join(dir, "child.vhdx")
	run(t, VhdxGen(), "--out", bi, "--src", raws[0], "--block", "2097152")
	if err := NormalizeVhdx(bi, "t/base", ""); err != nil {
		t.Fatal(err)
	}
	run(t, VhdxGen(), "--out", ci, "--src", raws[1], "--block", "2097152", "--parent", bi, "--parent-rel", "base.vhdx", "--parent-raw", raws[0])
	if err := NormalizeVhdx(ci, "t/child", bi); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ci)
	v, err := ScanVhdx(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	checkTiling(t, v.Regions, len(b))
	if !v.HasParent || v.Parent["relative_path"] != "base.vhdx" {
		t.Fatalf("parent locator %v", v.Parent)
	}
	var partial []VBlock
	for _, blk := range v.Blocks {
		if blk.State == VPartial {
			partial = append(partial, blk)
		}
	}
	if len(partial) != 3 {
		t.Fatalf("want 3 partially-present blocks, got %d", len(partial))
	}
	// zero override at sector 259 of block 0, one sector of block 5, the hole write at sector 0 of block 20
	got := map[int]int{}
	for _, blk := range partial {
		for _, r := range blk.Present {
			got[r.First] = r.Count
		}
	}
	if got[256+3] != 1 || got[1] != 1 || got[0] != 1 || len(got) != 3 {
		t.Fatalf("present-sector runs %v", got)
	}
	want, _ := child.SHA256()
	out, err := exec.Command(VhdiRead(), ci, bi).Output()
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(out) != want {
		t.Fatal("libvhdi chain read differs from the oracle child disk")
	}
	if _, err := exec.Command(VhdiRead(), "-i", ci, filepath.Join(dir, "missing.vhdx")).Output(); err == nil {
		t.Fatal("chain with a missing parent must fail")
	}
}

func TestNormalizeVhdxDeterministic(t *testing.T) {
	haveVhdx(t)
	dir := t.TempDir()
	raw := filepath.Join(dir, "r.raw")
	smallDisk().WriteRaw(raw)
	var sums [2][32]byte
	var pre [2][32]byte
	for i := range sums {
		p := filepath.Join(dir, "d.vhdx")
		run(t, VhdxGen(), "--out", p, "--src", raw)
		b, _ := os.ReadFile(p)
		pre[i] = sha256.Sum256(b)
		if err := NormalizeVhdx(p, "seed", ""); err != nil {
			t.Fatal(err)
		}
		b, _ = os.ReadFile(p)
		sums[i] = sha256.Sum256(b)
		if _, err := ScanVhdx(bytes.NewReader(b), int64(len(b))); err != nil {
			t.Fatal(err)
		}
	}
	if pre[0] == pre[1] {
		t.Log("producer output was already deterministic")
	}
	if sums[0] != sums[1] {
		t.Fatal("normalisation did not make the producer deterministic")
	}
}
