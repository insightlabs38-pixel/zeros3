package diskimg

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func tinyDisk() *Disk {
	return &Disk{Size: 16 * MiB, Writes: []Extent{{0, 64 * KiB, KMeta, 1}, {3 * MiB, 200 * KiB, KRandom, 2}, {9 * MiB, 8192, KText, 3}, {12 * MiB, 3 * MiB, KDup, 4}}}
}

var fuzzBase struct {
	once       sync.Once
	qcow, vhdx []byte
}

func fuzzFixtures(t testing.TB) ([]byte, []byte) {
	fuzzBase.once.Do(func() {
		dir, _ := os.MkdirTemp("", "z3fuzz")
		defer os.RemoveAll(dir)
		raw := filepath.Join(dir, "d.raw")
		tinyDisk().WriteRaw(raw)
		if _, err := execCombined("qemu-img", "convert", "-f", "raw", "-O", "qcow2", "-o", "cluster_size=16384,extended_l2=on", raw, filepath.Join(dir, "d.qcow2")); err == nil {
			fuzzBase.qcow, _ = os.ReadFile(filepath.Join(dir, "d.qcow2"))
		}
		if _, err := os.Stat(VhdxGen()); err == nil {
			if _, err := execCombined(VhdxGen(), "--out", filepath.Join(dir, "d.vhdx"), "--src", raw, "--block", "1048576"); err == nil {
				fuzzBase.vhdx, _ = os.ReadFile(filepath.Join(dir, "d.vhdx"))
			}
		}
	})
	return fuzzBase.qcow, fuzzBase.vhdx
}

func checkScan(t *testing.T, regs []Region, size int) {
	var pos int64
	for _, r := range regs {
		if r.Off != pos || r.Len <= 0 {
			t.Fatalf("invalid tiling %+v at %d", r, pos)
		}
		pos += r.Len
	}
	if pos != int64(size) {
		t.Fatalf("tiling covers %d of %d", pos, size)
	}
}

// FuzzScanQcow2 patches a valid extended-L2 image at arbitrary offsets: never panic, never hang, tiling exact on success.
func FuzzScanQcow2(f *testing.F) {
	q, _ := fuzzFixtures(f)
	if q == nil {
		f.Skip("qemu-img missing")
	}
	f.Add(uint32(0), []byte{1}, uint32(0))
	f.Add(uint32(36), []byte{0xff, 0xff, 0xff, 0xff}, uint32(0))
	f.Add(uint32(40), []byte{0, 0, 0, 0, 0, 0, 0x10, 0}, uint32(0))
	f.Fuzz(func(t *testing.T, off uint32, patch []byte, trunc uint32) {
		b := append([]byte(nil), q...)
		if int(off) < len(b) {
			copy(b[off:], patch)
		}
		if trunc%3 == 0 && int(trunc/3) < len(b) {
			b = b[:trunc/3]
		}
		if s, err := ScanQcow2(bytes.NewReader(b), int64(len(b))); err == nil {
			checkScan(t, s.Regions, len(b))
		}
	})
}

// FuzzScanVhdx patches the metadata-bearing first MiB and BAT of a valid differencing-capable image.
func FuzzScanVhdx(f *testing.F) {
	_, v := fuzzFixtures(f)
	if v == nil {
		f.Skip("vhdxgen missing")
	}
	f.Add(uint32(0), []byte{1}, uint32(0), false)
	f.Add(uint32(192*KiB+8), []byte{0xff, 0xff}, uint32(0), true)
	f.Fuzz(func(t *testing.T, off uint32, patch []byte, trunc uint32, fixcrc bool) {
		b := append([]byte(nil), v...)
		o := int(off) % (4 * MiB)
		if o < len(b) {
			copy(b[o:], patch)
		}
		if fixcrc { // make the region table / headers checksum-valid so the fuzzer reaches the deeper parsers
			for _, ro := range []int{192 * KiB, 256 * KiB} {
				clear(b[ro+4 : ro+8])
				putLE32(b[ro+4:], crc32Sum(b[ro:ro+64*KiB]))
			}
			for _, ho := range []int{64 * KiB, 128 * KiB} {
				clear(b[ho+4 : ho+8])
				putLE32(b[ho+4:], crc32Sum(b[ho:ho+4096]))
			}
		}
		if trunc%4 == 0 && int(trunc/4) < len(b) {
			b = b[:trunc/4]
		}
		if s, err := ScanVhdx(bytes.NewReader(b), int64(len(b))); err == nil {
			checkScan(t, s.Regions, len(b))
		}
	})
}
