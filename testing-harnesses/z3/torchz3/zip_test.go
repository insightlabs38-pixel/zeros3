package torchz3

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

type mem struct {
	name string
	data []byte
}

func le(b []byte, v uint64, n int) []byte {
	for i := 0; i < n; i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// buildTorch writes the shape torch.save produces: stored members, flags 0x0808, a local extra
// field that pads the data to a 64-byte boundary, a data descriptor with signature and 4-byte
// sizes (8-byte when zip64Desc), a central directory, a ZIP64 end record and locator, an EOCD.
func buildTorch(ms []mem, zip64Desc bool) []byte {
	var out, cd []byte
	for _, m := range ms {
		off := len(out)
		hl := 30 + len(m.name) + 4
		pad := (64 - (off+hl)%64) % 64
		xl := 4 + pad
		crc := crc32.ChecksumIEEE(m.data)
		out = le(out, sigLocal, 4)
		out = le(out, 0, 2)      // version
		out = le(out, 0x0808, 2) // flags
		out = le(out, 0, 2)      // method
		out = le(out, 0, 4)      // time
		out = le(out, 0, 4)      // crc (in descriptor)
		out = le(out, 0, 8)      // sizes
		out = le(out, uint64(len(m.name)), 2)
		out = le(out, uint64(xl), 2)
		out = append(out, m.name...)
		out = append(out, 'F', 'B')
		out = le(out, uint64(pad), 2)
		out = append(out, bytes.Repeat([]byte{'Z'}, pad)...)
		out = append(out, m.data...)
		out = le(out, sigDesc, 4)
		out = le(out, uint64(crc), 4)
		w := 4
		if zip64Desc {
			w = 8
		}
		out = le(out, uint64(len(m.data)), w)
		out = le(out, uint64(len(m.data)), w)
		cd = le(cd, sigCentral, 4)
		cd = le(cd, 0, 2)
		cd = le(cd, 0, 2)
		cd = le(cd, 0x0808, 2)
		cd = le(cd, 0, 2)
		cd = le(cd, 0, 4)
		cd = le(cd, uint64(crc), 4)
		if zip64Desc {
			cd = le(cd, 0xffffffff, 4)
			cd = le(cd, 0xffffffff, 4)
		} else {
			cd = le(cd, uint64(len(m.data)), 4)
			cd = le(cd, uint64(len(m.data)), 4)
		}
		cd = le(cd, uint64(len(m.name)), 2)
		xlen := 0
		if zip64Desc {
			xlen = 4 + 16
		}
		cd = le(cd, uint64(xlen), 2)
		cd = le(cd, 0, 2)
		cd = le(cd, 0, 2)
		cd = le(cd, 0, 2)
		cd = le(cd, 0, 4)
		cd = le(cd, uint64(off), 4)
		cd = append(cd, m.name...)
		if zip64Desc {
			cd = le(cd, 1, 2)
			cd = le(cd, 16, 2)
			cd = le(cd, uint64(len(m.data)), 8)
			cd = le(cd, uint64(len(m.data)), 8)
		}
	}
	cdOff := len(out)
	out = append(out, cd...)
	z64 := len(out)
	out = le(out, sigEOCD64, 4)
	out = le(out, 44, 8)
	out = le(out, 0x2d, 2)
	out = le(out, 0x2d, 2)
	out = le(out, 0, 4)
	out = le(out, 0, 4)
	out = le(out, uint64(len(ms)), 8)
	out = le(out, uint64(len(ms)), 8)
	out = le(out, uint64(len(cd)), 8)
	out = le(out, uint64(cdOff), 8)
	out = le(out, sigLoc64, 4)
	out = le(out, 0, 4)
	out = le(out, uint64(z64), 8)
	out = le(out, 1, 4)
	out = le(out, sigEOCD, 4)
	out = le(out, 0, 4)
	out = le(out, uint64(len(ms)), 2)
	out = le(out, uint64(len(ms)), 2)
	out = le(out, uint64(len(cd)), 4)
	out = le(out, uint64(cdOff), 4)
	out = le(out, 0, 2)
	return out
}

var members = []mem{
	{"archive/data.pkl", bytes.Repeat([]byte("pkl"), 50)},
	{"archive/byteorder", []byte("little")},
	{"archive/data/0", bytes.Repeat([]byte{1, 2, 3, 4}, 300)},
	{"archive/data/1", []byte{}},
	{"archive/data/10", bytes.Repeat([]byte{9}, 77)},
	{"archive/version", []byte("3\n")},
	{"archive/.data/serialization_id", []byte("0123456789012345678901234567890123456789")},
}

func TestTorchShapeDifferentialAndTiling(t *testing.T) {
	for _, z64d := range []bool{false, true} {
		b := buildTorch(members, z64d)
		f, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{})
		if err != nil {
			t.Fatalf("zip64Desc=%v: %v", z64d, err)
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if len(zr.File) != len(f.Members) {
			t.Fatalf("%d members, archive/zip sees %d", len(f.Members), len(zr.File))
		}
		for i, zf := range zr.File {
			m := f.Members[i]
			rc, _ := zf.Open()
			want, _ := io.ReadAll(rc)
			rc.Close()
			if m.Name != zf.Name || !bytes.Equal(b[m.DataBegin:m.DataEnd], want) || m.CRC != zf.CRC32 {
				t.Errorf("member %d %q differs from archive/zip", i, m.Name)
			}
			if m.DataBegin%64 != 0 {
				t.Errorf("member %q data at %d is not 64-byte aligned", m.Name, m.DataBegin)
			}
		}
		var out []byte
		kinds := map[string]int{}
		for _, s := range f.Segments() {
			if s.Begin != int64(len(out)) {
				t.Fatalf("segment gap at %d", len(out))
			}
			kinds[s.Kind]++
			out = append(out, b[s.Begin:s.End]...)
		}
		if !bytes.Equal(out, b) {
			t.Fatal("segments do not rebuild the archive")
		}
		if kinds["pickle"] != 1 || kinds["storage"] != 2 || kinds["central"] != 1 || kinds["other"] != 3 {
			t.Errorf("kinds %v", kinds)
		}
		if !f.Zip64 {
			t.Error("ZIP64 end record not noticed")
		}
	}
}

func TestRefusals(t *testing.T) {
	b := buildTorch(members, false)
	parse := func(b []byte) error { _, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{}); return err }
	// every proper prefix is refused (never a panic, never accepted)
	for n := 0; n < len(b); n++ {
		if err := parse(b[:n]); err == nil {
			t.Fatalf("prefix %d accepted", n)
		}
	}
	// deflate member
	c := append([]byte(nil), b...)
	i := bytes.Index(c, []byte("PK\x01\x02"))
	binary.LittleEndian.PutUint16(c[i+10:], 8)
	if err := parse(c); !errors.Is(err, ErrUnsupported) {
		t.Errorf("deflate: %v", err)
	}
	// central name differs from local name
	c = append([]byte(nil), b...)
	i = bytes.Index(c, []byte("PK\x01\x02")) + 46
	c[i+3] ^= 1
	if err := parse(c); err == nil {
		t.Error("central/local name mismatch accepted")
	}
	// a gap between members (extra byte inside the first local extra field is fine, a stray byte is not)
	c = append(append([]byte(nil), b[:40]...), append([]byte{0}, b[40:]...)...)
	if err := parse(c); err == nil {
		t.Error("shifted archive accepted")
	}
	// hostile entry count and central size
	for _, o := range []int{len(b) - 22 + 10, len(b) - 22 + 12} {
		c = append([]byte(nil), b...)
		binary.LittleEndian.PutUint16(c[o:], 0xfffe)
		if o == len(b)-22+12 {
			binary.LittleEndian.PutUint32(c[o:], 0x7fffffff)
		}
		if err := parse(c); err == nil {
			t.Errorf("hostile end record at %d accepted", o)
		}
	}
	// trailing garbage after the EOCD is a comment-length mismatch
	if err := parse(append(append([]byte(nil), b...), 'x')); err == nil {
		t.Error("trailing byte accepted")
	}
	// duplicate member names
	dup := buildTorch([]mem{{"a/data/0", []byte("x")}, {"a/data/0", []byte("y")}}, false)
	if err := parse(dup); err == nil {
		t.Error("duplicate names accepted")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(buildTorch(members, false))
	f.Add(buildTorch(members[:2], true))
	f.Fuzz(func(t *testing.T, b []byte) {
		g, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{MaxEntries: 1 << 12})
		if err != nil {
			return
		}
		var n int64
		for _, s := range g.Segments() {
			if s.Begin != n || s.End < s.Begin || s.End > int64(len(b)) {
				t.Fatalf("segments do not tile: %+v", s)
			}
			n = s.End
		}
		if n != int64(len(b)) {
			t.Fatalf("segments end at %d of %d", n, len(b))
		}
	})
}
