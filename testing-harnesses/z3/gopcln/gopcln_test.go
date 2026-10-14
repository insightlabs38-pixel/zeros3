package gopcln

import (
	"bytes"
	"encoding/binary"
	"testing"

	"zeros3-z3/binref"
)

// synthetic Go 1.20 pclntab: header, funcnametab, ftab (3 entries + end), two _func records.
func synth(textShift uint32) (*binref.Image, []byte) {
	le := binary.LittleEndian
	names := []byte("\x00main.a\x00main.b\x00")
	hdr := make([]byte, 8+8*8)
	le.PutUint32(hdr, 0xFFFFFFF1)
	hdr[6], hdr[7] = 1, 8
	put := func(i int, v uint64) { le.PutUint64(hdr[8+8*i:], v) }
	nfunc := uint64(2)
	funcName := uint64(len(hdr))
	pclnOff := funcName + uint64(len(names))
	put(0, nfunc)
	put(1, 1)
	put(2, 0) // textStart left to the loader: the reader falls back to the first text region
	put(3, funcName)
	put(7, pclnOff)
	ftab := make([]byte, (nfunc*2+1)*4)
	fstart := uint64(len(ftab))
	fn := func(entry uint32, name uint32) []byte {
		b := make([]byte, 16)
		le.PutUint32(b, entry)
		le.PutUint32(b[4:], name)
		return b
	}
	e0, e1 := 0x20+textShift, 0x80+textShift
	le.PutUint32(ftab[0:], e0)
	le.PutUint32(ftab[4:], uint32(fstart))
	le.PutUint32(ftab[8:], e1)
	le.PutUint32(ftab[12:], uint32(fstart+16))
	le.PutUint32(ftab[16:], e1+0x40)
	pc := append(append(append(append(append([]byte{}, hdr...), names...), ftab...), fn(e0, 1)...), fn(e1, 8)...)
	text := bytes.Repeat([]byte{0x90}, 0x200)
	data := append(append([]byte{}, text...), pc...)
	im := &binref.Image{Arch: "x86-64", Data: data, Regions: []binref.Region{
		{Name: ".text", Off: 0, Len: len(text), VA: 0x401000, Mapped: true, Exec: true},
		{Name: ".gopclntab", Off: len(text), Len: len(pc), VA: 0x500000, Mapped: true},
	}}
	return im, data
}

func TestFind(t *testing.T) {
	im, data := synth(0)
	tb, err := Find(im)
	if err != nil {
		t.Fatal(err)
	}
	if tb.NFunc != 2 || tb.TextStart != 0x401000 || len(tb.Units) != 2 || tb.Units[1].Name != "main.b" || tb.Units[1].VA != 0x401080 {
		t.Fatalf("%+v", tb)
	}
	if len(tb.Slots) != 3+2 { // 3 ftab entries (incl. end marker) + 2 _func entryOff
		t.Fatalf("slots %d", len(tb.Slots))
	}
	n, err := binref.Normalize(im, tb.Slots, im.CleanUnits(tb.Units), binref.IdentSymbol)
	if err != nil {
		t.Fatal(err)
	}
	out, err := binref.Reconstruct(n.Streams)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("round trip: %v", err)
	}
	if n.Stat.Accepted[binref.ClassTable] != 4 { // the end marker lies beyond every unit and stays raw-or-region-relative
		t.Logf("accepted %v", n.Stat.Accepted)
	}
}

// Moving every function by the same amount changes the entry offsets in the
// raw table but not the normalized payload or reference stream.
func TestEntryOffsetsAreNormalized(t *testing.T) {
	a, da := synth(0)
	b, db := synth(0x40)
	ta, _ := Find(a)
	tb, _ := Find(b)
	if bytes.Equal(da, db) {
		t.Fatal("fixture does not move")
	}
	na, _ := binref.Normalize(a, ta.Slots, a.CleanUnits(ta.Units), binref.IdentSymbol)
	nb, _ := binref.Normalize(b, tb.Slots, b.CleanUnits(tb.Units), binref.IdentSymbol)
	if !bytes.Equal(na.Masked[0x200:], nb.Masked[0x200:]) {
		t.Fatal("pclntab payload still depends on function addresses")
	}
}

func TestRejectsGarbage(t *testing.T) {
	im, _ := synth(0)
	im.Data[0x200+8+8*7] = 0xff // pclnOffset beyond the table
	im.Data[0x200+8+8*7+1] = 0xff
	if _, err := Find(im); err == nil {
		t.Fatal("accepted a pclntab whose function table lies outside the region")
	}
}
