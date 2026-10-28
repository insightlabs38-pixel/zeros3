package debugsym

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
)

func msfFixture() []byte {
	b := make([]byte, 6*512)
	copy(b, "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00")
	for i, v := range []uint32{512, 1, 6, 12, 0, 3} {
		binary.LittleEndian.PutUint32(b[32+i*4:], v)
	}
	binary.LittleEndian.PutUint32(b[3*512:], 4)
	for i, v := range []uint32{1, 3, 5} {
		binary.LittleEndian.PutUint32(b[4*512+i*4:], v)
	}
	copy(b[5*512:], "abc")
	return b
}
func portableFixture(t testing.TB) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/portable-small.pdb")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestSerializedExactDebugPartitions(t *testing.T) {
	for name, b := range map[string][]byte{"msf": msfFixture(), "portable": portableFixture(t)} {
		t.Run(name, func(t *testing.T) {
			for _, target := range []int{0, 1024, 4096} {
				p, in, e := Plan(b, target)
				if e != nil {
					t.Fatal(e)
				}
				if e := Proof(p, b); e != nil {
					t.Fatal(e)
				}
				if name == "portable" && in.Rows["MethodDebugInformation"] != 200 {
					t.Fatal("PDB debug row count differs")
				}
			}
		})
	}
}
func TestMSFHostileGeometry(t *testing.T) {
	for name, edit := range map[string]func([]byte){
		"free-live-page":      func(b []byte) { b[512] |= 1 << 5 },
		"block-size":          func(b []byte) { le.PutUint32(b[32:], 513) },
		"count":               func(b []byte) { le.PutUint32(b[40:], 1<<31) },
		"directory-limit":     func(b []byte) { le.PutUint32(b[44:], 1<<31) },
		"directory-duplicate": func(b []byte) { le.PutUint32(b[4*512+8:], 4) },
		"outside":             func(b []byte) { le.PutUint32(b[4*512+8:], 6) },
		"stream-count":        func(b []byte) { le.PutUint32(b[4*512:], 1<<31) },
		"stream-size":         func(b []byte) { le.PutUint32(b[4*512+4:], 1<<31) },
	} {
		t.Run(name, func(t *testing.T) {
			b := msfFixture()
			edit(b)
			if _, e := Parse(bytes.NewReader(b), int64(len(b))); e == nil {
				t.Fatal("hostile MSF accepted")
			}
		})
	}
}
func TestTruncatedPortablePDB(t *testing.T) {
	b := portableFixture(t)
	for n := 0; n < len(b); n += 37 {
		if _, e := Parse(bytes.NewReader(b[:n]), int64(n)); e == nil {
			t.Fatalf("truncated PDB at %d accepted", n)
		}
	}
}
func FuzzDebugContainers(f *testing.F) {
	f.Add(debugTestELF([]debugTestSection{{name: ".debug_future", typ: 1, data: []byte("opaque")}}, -1))
	f.Add(msfFixture())
	f.Add(portableFixture(f))
	f.Add([]byte("\x7fELF"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		in, e := Parse(bytes.NewReader(b), int64(len(b)))
		if e == nil {
			var at int64
			for _, s := range in.Spans {
				if s.Off != at || s.End < s.Off || s.End > int64(len(b)) {
					t.Fatal("invalid partition")
				}
				at = s.End
			}
			if at != int64(len(b)) {
				t.Fatal("incomplete partition")
			}
		}
	})
}

// Fixture constructor mechanically extracted from elfz3 tests.
type debugTestSection struct {
	name        string
	typ         uint32
	flags, addr uint64
	data        []byte
	link, info  uint32
	ents        uint64
	nobits      bool
	nobitsSize  uint64
}

// debugTestELF writes a little-endian ELF64 ET_EXEC x86-64 file: header, the sections' bytes, section name table, section headers.
func debugTestELF(secs []debugTestSection, shstrndxOverride int) []byte {
	le := binary.LittleEndian
	buf := make([]byte, 64)
	copy(buf, "\x7fELF\x02\x01\x01")
	le.PutUint16(buf[16:], 2)
	le.PutUint16(buf[18:], 62)
	le.PutUint32(buf[20:], 1) // standard ELF reader also validates e_version
	le.PutUint16(buf[52:], 64)
	le.PutUint16(buf[58:], 64)
	all := append([]debugTestSection{{}}, secs...)
	var shstr []byte
	shstr = append(shstr, 0)
	nameOff := make([]uint32, len(all)+1)
	for i := 1; i < len(all); i++ {
		nameOff[i] = uint32(len(shstr))
		shstr = append(shstr, all[i].name...)
		shstr = append(shstr, 0)
	}
	nameOff[len(all)] = uint32(len(shstr))
	shstr = append(shstr, ".shstrtab\x00"...)
	all = append(all, debugTestSection{name: ".shstrtab", typ: 3, data: shstr})
	offs := make([]uint64, len(all))
	for i := 1; i < len(all); i++ {
		offs[i] = uint64(len(buf))
		if !all[i].nobits {
			buf = append(buf, all[i].data...)
		}
	}
	for len(buf)%8 != 0 {
		buf = append(buf, 0)
	}
	shoff := len(buf)
	for i, s := range all {
		h := make([]byte, 64)
		if i > 0 {
			le.PutUint32(h, nameOff[i])
			le.PutUint32(h[4:], s.typ)
			le.PutUint64(h[8:], s.flags)
			le.PutUint64(h[16:], s.addr)
			le.PutUint64(h[24:], offs[i])
			sz := uint64(len(s.data))
			if s.nobits {
				sz = s.nobitsSize
			}
			le.PutUint64(h[32:], sz)
			le.PutUint32(h[40:], s.link)
			le.PutUint32(h[44:], s.info)
			le.PutUint64(h[56:], s.ents)
		}
		buf = append(buf, h...)
	}
	le.PutUint64(buf[40:], uint64(shoff))
	le.PutUint16(buf[60:], uint16(len(all)))
	idx := len(all) - 1
	if shstrndxOverride >= 0 {
		idx = shstrndxOverride
	}
	le.PutUint16(buf[62:], uint16(idx))
	return buf
}

func TestWholeELFOpaqueSections(t *testing.T) {
	b := debugTestELF([]debugTestSection{{name: ".debug_future", typ: 1, data: []byte("unsupported future records stay opaque")}, {name: ".bss", typ: 8, nobits: true, nobitsSize: 1 << 30}}, -1)
	p, _, e := Plan(b, 4096)
	if e != nil {
		t.Fatal(e)
	}
	if e = Proof(p, b); e != nil {
		t.Fatal(e)
	}
	le.PutUint16(b[60:], 0)
	if _, e = Parse(bytes.NewReader(b), int64(len(b))); e == nil {
		t.Fatal("extended count accepted without bound")
	}
}
func TestControlledProducerHistories(t *testing.T) {
	dir := os.Getenv("ZEROS3_Q4_CONTROLLED")
	if dir == "" {
		t.Skip("set ZEROS3_Q4_CONTROLLED to fixed-directory controlled build cache")
	}
	for rev := 0; rev < 5; rev++ {
		for _, name := range []string{"gcc-dwarf4", "gcc-dwarf5", "clang-dwarf5", "sample.pdb", "portable.pdb"} {
			b, e := os.ReadFile(fmt.Sprintf("%s/rev%d/%s", dir, rev, name))
			if e != nil {
				t.Fatal(e)
			}
			p, in, e := Plan(b, 4096)
			if e != nil {
				t.Fatal(e)
			}
			if e = Proof(p, b); e != nil {
				t.Fatal(e)
			}
			if name == "portable.pdb" {
				want := uint32(200)
				if rev == 3 {
					want = 201
				}
				if rev == 4 {
					want = 202
				}
				if in.Rows["MethodDebugInformation"] != want {
					t.Fatal("native row oracle differs")
				}
			}
			if strings.Contains(name, "dwarf") {
				snap, e := SnapshotDWARF(b)
				if e != nil {
					t.Fatal(e)
				}
				if snap.Entries < 200 || len(snap.Named) < 200 {
					t.Fatal("missing DWARF attribution")
				}
			}
		}
	}
}

func TestUnsupportedUsesExactCDC(t *testing.T) {
	for _, b := range [][]byte{[]byte("unknown opaque control"), []byte("BSJB corrupt metadata"), []byte("Microsoft C/C++ MSF 7.00 invalid")} {
		p, in, fallback, e := Analyze(b, 4096)
		if e != nil || !fallback || in.Format != "opaque-CDC" {
			t.Fatalf("fallback: %v", e)
		}
		if e := Proof(p, b); e != nil {
			t.Fatal(e)
		}
	}
}
