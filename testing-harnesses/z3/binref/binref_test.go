package binref

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// layout builds a two-function text region plus a pointer table.
//
//	f0: call f1 ; jmp f1 ; lea rax,[rip+tbl] ; ret      (+ gap bytes inserted before f0 when pad > 0)
//	f1: nop ; jmp f1+1 (intra) ; ret
//	tbl: 8-byte pointers to f0 and f1
func layout(pad int) (*Image, []Unit) {
	const textVA, dataVA = 0x401000, 0x500000
	text := bytes.Repeat([]byte{0xCC}, pad)
	f0 := len(text)
	text = append(text, 0xE8, 0, 0, 0, 0, 0xE9, 0, 0, 0, 0, 0x48, 0x8D, 0x05, 0, 0, 0, 0, 0xC3)
	f1 := len(text)
	text = append(text, 0x90, 0xEB, 0xFE, 0xE9, 0, 0, 0, 0, 0xC3)
	for len(text)%16 != 0 {
		text = append(text, 0xCC)
	}
	f0va, f1va := uint64(textVA+f0), uint64(textVA+f1)
	put := func(off int, slotVA, t uint64) {
		binary.LittleEndian.PutUint32(text[off:], uint32(t-slotVA-4))
	}
	put(f0+1, f0va+1, f1va)
	put(f0+6, f0va+6, f1va)
	put(f0+13, f0va+13, dataVA)
	put(f1+4, f1va+4, f1va+1)
	data := make([]byte, 16)
	binary.LittleEndian.PutUint64(data, f0va)
	binary.LittleEndian.PutUint64(data[8:], f1va)
	hdr := []byte("HDR0HDR1HDR2HDR3")
	im := &Image{Arch: "x86-64", Data: append(append(append([]byte{}, hdr...), text...), data...)}
	im.Regions = []Region{
		{Name: "hdr", Off: 0, Len: len(hdr)},
		{Name: "text", Off: len(hdr), Len: len(text), VA: textVA, Mapped: true, Exec: true},
		{Name: "data", Off: len(hdr) + len(text), Len: len(data), VA: dataVA, Mapped: true},
	}
	return im, []Unit{{VA: f0va, Size: uint64(f1 - f0), Name: "f0"}, {VA: f1va, Name: "f1"}}
}

func slotsOf(im *Image) []Slot { return append(ScanX86(im), ScanPtr(im)...) }

func TestX86RelativeBranch(t *testing.T) {
	im, _ := layout(0)
	if err := im.Prepare(); err != nil {
		t.Fatal(err)
	}
	var got [nClass]int
	for _, s := range ScanX86(im) {
		got[s.Class]++
	}
	// call, jmp, intra jmp (E9) => 3 branches; lea rip => 1; the EB short jmp is not a rel32 form.
	if got[ClassBranch] != 3 || got[ClassRIP] != 1 {
		t.Fatalf("branch=%d rip=%d", got[ClassBranch], got[ClassRIP])
	}
	if got := len(ScanPtr(im)); got != 2 {
		t.Fatalf("ptr slots %d", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, id := range []Ident{IdentOrdinal, IdentRegionRel, IdentSymbol, IdentContent} {
		for _, pad := range []int{0, 7, 64} {
			im, units := layout(pad)
			n, err := Normalize(im, slotsOf(im), units, id)
			if err != nil {
				t.Fatal(err)
			}
			out, err := Reconstruct(n.Streams)
			if err != nil || !bytes.Equal(out, im.Data) {
				t.Fatalf("%v pad=%d: err=%v equal=%v", id, pad, err, bytes.Equal(out, im.Data))
			}
			if n.Stat.Intra != 1 {
				t.Fatalf("%v: intra=%d want 1", id, n.Stat.Intra)
			}
		}
	}
}

// Moving the code (pad) must leave the symbol-identified reference stream and
// the payload bytes of the data region unchanged, while the raw bytes differ.
func TestShiftInvariance(t *testing.T) {
	a, ua := layout(0)
	b, ub := layout(48)
	na, _ := Normalize(a, slotsOf(a), ua, IdentSymbol)
	nb, _ := Normalize(b, slotsOf(b), ub, IdentSymbol)
	if bytes.Equal(a.Data[16:], b.Data[16:len(a.Data)]) {
		t.Fatal("fixture does not move anything")
	}
	find := func(n *Norm, kind string, ri int) []byte {
		for _, s := range n.Streams {
			if s.Kind == kind && s.Region == ri {
				return s.Data
			}
		}
		return nil
	}
	// refs of the text region differ only by the first delta (leading pad); the data pointers are
	// identified by symbol and so identical.
	if !bytes.Equal(find(na, "refs", 2), find(nb, "refs", 2)) {
		t.Fatal("data refs changed under code movement")
	}
	ta, tb := find(na, "payload", 1), find(nb, "payload", 1)
	if !bytes.Equal(ta, tb[48:]) {
		t.Fatal("masked text payload is not shift invariant")
	}
}

func TestSelfCheckRejectsUnmapped(t *testing.T) {
	im, units := layout(0)
	im.Data[len(im.Data)-1] = 0xFF // pointer into nowhere
	im.Data[len(im.Data)-2] = 0xFF
	n, err := Normalize(im, slotsOf(im), units, IdentSymbol)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Reconstruct(n.Streams)
	if err != nil || !bytes.Equal(out, im.Data) {
		t.Fatal("round trip failed with a wild pointer")
	}
}

func TestReconstructCorrupt(t *testing.T) {
	im, units := layout(0)
	n, _ := Normalize(im, slotsOf(im), units, IdentSymbol)
	for i := range n.Streams {
		if n.Streams[i].Kind == "payload" {
			continue
		}
		cp := append([]Stream(nil), n.Streams...)
		d := cp[i].Data
		for cut := 0; cut < len(d); cut++ {
			cp[i].Data = d[:cut]
			Reconstruct(cp) // must not panic
		}
	}
}
