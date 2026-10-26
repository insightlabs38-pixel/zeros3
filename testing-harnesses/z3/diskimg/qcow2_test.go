package diskimg

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestScanQcow2Tiling(t *testing.T) {
	for _, opts := range []string{"cluster_size=65536", "cluster_size=65536,extended_l2=on", "cluster_size=4096,compat=1.1"} {
		_, b := qcowFixture(t, smallDisk(), opts)
		q, err := ScanQcow2(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", opts, err)
		}
		var sum int64
		var prev int64
		for _, r := range q.Regions {
			if r.Off != prev || r.Len <= 0 {
				t.Fatalf("%s: tiling gap at %d", opts, r.Off)
			}
			prev = r.Off + r.Len
			sum += r.Len
		}
		if sum != int64(len(b)) {
			t.Fatalf("%s: tiling covers %d of %d", opts, sum, len(b))
		}
		var data int
		for _, e := range q.Map {
			if e.State == QData {
				data++
			}
		}
		if data == 0 || q.VirtSize != 48*MiB {
			t.Fatalf("%s: data=%d virt=%d", opts, data, q.VirtSize)
		}
		// the zero range and holes must not be mapped as data (qemu-img convert detects zeros)
		cs := q.ClusterSize
		if e := q.Map[20*MiB/cs]; e.State == QData {
			t.Fatalf("%s: zero range mapped as data", opts)
		}
	}
}

func TestScanQcow2Hostile(t *testing.T) {
	_, b := qcowFixture(t, smallDisk(), "cluster_size=65536")
	// truncations at every structural boundary must error, never panic
	for _, n := range []int{0, 1, 3, 4, 71, 72, 103, 104, 4096, 65535, 65536, 70000, len(b) - 1, len(b) - 65536} {
		if n > len(b) {
			continue
		}
		_, err := ScanQcow2(bytes.NewReader(b[:n]), int64(n))
		if err == nil && n < len(b)-65536 {
			t.Fatalf("truncation to %d accepted", n)
		}
	}
	mut := func(name string, f func(b []byte)) {
		c := append([]byte(nil), b...)
		f(c)
		if _, err := ScanQcow2(bytes.NewReader(c), int64(len(c))); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	mut("magic", func(c []byte) { c[0] = 'X' })
	mut("version", func(c []byte) { c[7] = 9 })
	mut("cluster_bits", func(c []byte) { c[23] = 40 })
	mut("l1 offset", func(c []byte) { putBE64(c[40:], 1<<40) })
	mut("l1 size huge", func(c []byte) { putBE32(c[36:], 0xffffffff) })
	mut("virt size huge", func(c []byte) { putBE64(c[24:], 1<<61) })
	mut("refcount table", func(c []byte) { putBE64(c[48:], 3) })
	mut("snapshots", func(c []byte) { putBE32(c[60:], 1) })
	mut("backing offset", func(c []byte) { putBE64(c[8:], 1<<50); putBE32(c[16:], 5) })
	// pointing an L2 entry beyond the file / into the header must be rejected
	q, _ := ScanQcow2(bytes.NewReader(b), int64(len(b)))
	for i, off := range q.L2Off {
		if off != 0 {
			mut("l2 entry beyond eof", func(c []byte) { putBE64(c[off:], uint64(len(c))*4|1<<63) })
			mut("l2 entry into header", func(c []byte) { putBE64(c[off+8:], 0); putBE64(c[off:], 0x10000*0+0x200) })
			_ = i
			break
		}
	}
}

func putBE64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
func putBE32(b []byte, v uint32) {
	for i := 3; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

// The scanner's guest mapping must agree with qemu-img map (an independent implementation).
func TestQcow2MapAgreesWithQemu(t *testing.T) {
	for _, opts := range []string{"cluster_size=65536", "cluster_size=65536,extended_l2=on"} {
		img, b := qcowFixture(t, smallDisk(), opts)
		q, err := ScanQcow2(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		var segs []struct {
			Start, Length int64
			Data, Zero    bool
			Offset        int64
		}
		if err := json.Unmarshal([]byte(run(t, "qemu-img", "map", "--output=json", img)), &segs); err != nil {
			t.Fatal(err)
		}
		cs := q.ClusterSize
		for _, s := range segs {
			if !s.Data {
				continue
			}
			for g := s.Start / cs; g < (s.Start+s.Length+cs-1)/cs; g++ {
				e := q.Map[g]
				if e.State != QData {
					t.Fatalf("%s: guest cluster %d: qemu says data, scanner state %d", opts, g, e.State)
				}
				if opts == "cluster_size=65536" && e.Host != s.Offset+(g*cs-s.Start) {
					t.Fatalf("%s: guest cluster %d host %d != qemu %d", opts, g, e.Host, s.Offset+(g*cs-s.Start))
				}
			}
		}
		dataBytes := int64(0)
		for _, s := range segs {
			if s.Data {
				dataBytes += s.Length
			}
		}
		var n int64
		for _, e := range q.Map {
			if e.State == QData {
				n += cs
			}
		}
		if opts == "cluster_size=65536" && n != dataBytes {
			t.Fatalf("scanner data %d vs qemu %d", n, dataBytes)
		}
	}
}
