package debugsym

import (
	"bytes"
	"encoding/binary"
	"os"
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
