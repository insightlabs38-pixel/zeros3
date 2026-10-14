package pez3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"zeros3-z3/binref"
)

const (
	imageBase = 0x140000000
	// file layout of buildPE
	offPE     = 0x80
	offOpt    = offPE + 4 + 20
	offSec    = offOpt + 240
	offText   = 0x400
	offRdata  = 0x600
	offReloc  = 0x800
	offCert   = 0xA00
	rvaText   = 0x1000
	rvaRdata  = 0x2000
	rvaReloc  = 0x3000
	rvaData   = 0x4000
	certBytes = 64
)

func put32(b []byte, o int, v uint32) { binary.LittleEndian.PutUint32(b[o:], v) }
func put16(b []byte, o int, v uint16) { binary.LittleEndian.PutUint16(b[o:], v) }
func put64(b []byte, o int, v uint64) { binary.LittleEndian.PutUint64(b[o:], v) }

// buildPE writes a PE32+ file: .text (CALL rel32 + RIP-relative LEA), .rdata
// (import directory, thunks, a data pointer, an export directory, .pdata-like
// table), .reloc (one DIR64 entry), .data (virtual only) and a certificate
// table after the last section.
func buildPE() []byte {
	b := make([]byte, offCert+certBytes)
	copy(b, "MZ")
	put32(b, 0x3c, offPE)
	copy(b[offPE:], "PE\x00\x00")
	co := offPE + 4
	put16(b, co, 0x8664)
	put16(b, co+2, 4)
	put16(b, co+16, 240)
	put16(b, offOpt, 0x20b)
	put32(b, offOpt+16, rvaText) // entry point
	put64(b, offOpt+24, imageBase)
	put32(b, offOpt+60, 0x200) // SizeOfHeaders
	put32(b, offOpt+108, 16)   // NumberOfRvaAndSizes
	dir := func(i int, rva, size uint32) {
		put32(b, offOpt+112+8*i, rva)
		put32(b, offOpt+116+8*i, size)
	}
	sec := func(i int, name string, vsize, rva, raw, off, chr uint32) {
		h := offSec + 40*i
		copy(b[h:], name)
		put32(b, h+8, vsize)
		put32(b, h+12, rva)
		put32(b, h+16, raw)
		put32(b, h+20, off)
		put32(b, h+36, chr)
	}
	sec(0, ".text", 0x100, rvaText, 0x200, offText, 0x60000020)
	sec(1, ".rdata", 0x200, rvaRdata, 0x200, offRdata, 0x40000040)
	sec(2, ".reloc", 0x10, rvaReloc, 0x200, offReloc, 0x42000040)
	sec(3, ".data", 0x100, rvaData, 0, 0, 0xc0000040)
	// .text: call func2 (at +0x40), lea rax,[rip+ptr in .rdata], ret ; func2: ret
	t := b[offText:]
	t[0] = 0xE8
	put32(t, 1, 0x40-5)
	t[5], t[6], t[7] = 0x48, 0x8D, 0x05
	// ptr slot lives at .rdata+0x100
	put32(t, 8, uint32(rvaRdata+0x100-(rvaText+12)))
	t[12] = 0xC3
	t[0x40] = 0xC3
	// .rdata: import descriptor at +0, thunks at +0x40, hint/name at +0x80, ptr at +0x100, exports at +0x140
	r := b[offRdata:]
	put32(r, 0, rvaRdata+0x40)  // OriginalFirstThunk
	put32(r, 12, rvaRdata+0x90) // Name
	put32(r, 16, rvaRdata+0x40) // FirstThunk
	put64(r, 0x40, rvaRdata+0x80)
	copy(r[0x80:], "\x00\x00Func\x00")
	copy(r[0x90:], "dll.dll\x00")
	put64(r, 0x100, imageBase+rvaText+0x40) // pointer to func2
	put32(r, 0x140+12, rvaRdata+0x90)
	put32(r, 0x140+20, 1)
	put32(r, 0x140+24, 1)
	put32(r, 0x140+28, rvaRdata+0x180)
	put32(r, 0x140+32, rvaRdata+0x184)
	put32(r, 0x180, rvaText+0x40)  // exported function RVA
	put32(r, 0x184, rvaRdata+0x90) // exported name RVA
	// .pdata-like entry placed in .rdata at +0x1c0
	put32(r, 0x1c0, rvaText)
	put32(r, 0x1c4, rvaText+0x40)
	put32(r, 0x1c8, rvaRdata+0x1d0)
	// .reloc: page rva rdata, one DIR64 at +0x100
	rl := b[offReloc:]
	put32(rl, 0, rvaRdata)
	put32(rl, 4, 12)
	put16(rl, 8, 10<<12|0x100)
	put16(rl, 10, 0)
	dir(0, rvaRdata+0x140, 40)
	dir(1, rvaRdata, 40)
	dir(3, rvaRdata+0x1c0, 12)
	dir(4, offCert, certBytes) // certificate table: a file offset
	dir(5, rvaReloc, 12)
	for i := 0; i < certBytes; i++ {
		b[offCert+i] = byte(0xA0 + i)
	}
	return b
}

func norm(t *testing.T, data []byte) (*binref.Image, *Info) {
	t.Helper()
	im, info, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	slots := append(append([]binref.Slot{}, im.Slots...), binref.ScanX86(im)...)
	slots = append(slots, binref.ScanPtr(im)...)
	n, err := binref.Normalize(im, slots, im.CleanUnits(info.Funcs), binref.IdentRegionRel)
	if err != nil {
		t.Fatal(err)
	}
	out, err := binref.Reconstruct(n.Streams)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("round trip: %v", err)
	}
	if n.Stat.Accepted[binref.ClassReloc] != 1 {
		t.Fatalf("base relocation not normalized: %v", n.Stat.Accepted)
	}
	return im, info
}

func TestParseGood(t *testing.T) {
	data := buildPE()
	im, info := norm(t, data)
	if info.BaseRelocs != 2 || info.RelocSlots != 1 || info.Imports != 1 || info.Exports != 1 || info.Pdata != 1 || info.CertSize != certBytes {
		t.Fatalf("%+v", info)
	}
	var virt, tail bool
	for _, r := range im.Regions {
		if r.Name == ".data" && r.Len == 0 && r.VSize == 0x100 && r.Mapped && r.VA == imageBase+rvaData {
			virt = true
		}
		if r.Off == offCert && r.Len == certBytes {
			tail = true
		}
	}
	if !virt || !tail {
		t.Fatalf("virtual region %v, certificate overlay region %v", virt, tail)
	}
}

// The certificate table (and anything after the last section) is carried as
// opaque bytes: it is reproduced exactly even if it looks like references.
func TestCertificateTableStaysOpaque(t *testing.T) {
	data := buildPE()
	im, _ := norm(t, data)
	for i := 0; i < 8; i++ { // fill with address-like words
		binary.LittleEndian.PutUint64(data[offCert+8*i:], imageBase+rvaText+0x40)
	}
	im2, _ := norm(t, data)
	_ = im
	if !bytes.Equal(data[offCert:], im2.Data[offCert:]) {
		t.Fatal("certificate bytes changed")
	}
}

func TestShiftedSectionsKeepNormalizedPayload(t *testing.T) {
	// Move the base: every absolute pointer changes, the normalized .rdata does not.
	a := buildPE()
	b := buildPE()
	put64(b, offOpt+24, imageBase+0x10000)
	put64(b, offRdata+0x100, imageBase+0x10000+rvaText+0x40)
	ia, infoA := norm(t, a)
	ib, infoB := norm(t, b)
	slots := func(im *binref.Image) []binref.Slot {
		return append(append([]binref.Slot{}, im.Slots...), binref.ScanX86(im)...)
	}
	na, _ := binref.Normalize(ia, slots(ia), ia.CleanUnits(infoA.Funcs), binref.IdentRegionRel)
	nb, _ := binref.Normalize(ib, slots(ib), ib.CleanUnits(infoB.Funcs), binref.IdentRegionRel)
	ra, rb := na.Masked[offRdata:offRdata+0x200], nb.Masked[offRdata:offRdata+0x200]
	if !bytes.Equal(ra, rb) {
		t.Fatal("rebasing changed the normalized .rdata")
	}
	if bytes.Equal(a[offRdata:offRdata+0x200], b[offRdata:offRdata+0x200]) {
		t.Fatal("fixture does not rebase anything")
	}
}

func TestMalformed(t *testing.T) {
	good := buildPE()
	mut := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	sh := func(i, field int) int { return offSec + 40*i + field }
	cases := map[string][]byte{
		"truncated":                 good[:0x30],
		"e_lfanew beyond":           mut(func(b []byte) { put32(b, 0x3c, uint32(len(b))) }),
		"e_lfanew overflow":         mut(func(b []byte) { put32(b, 0x3c, 0xffffffff) }),
		"no PE signature":           mut(func(b []byte) { b[offPE] = 'X' }),
		"too many sections":         mut(func(b []byte) { put16(b, offPE+6, 5000) }),
		"section table beyond":      mut(func(b []byte) { put16(b, offPE+6, 96) }),
		"optional header too small": mut(func(b []byte) { put16(b, offPE+4+16, 60) }),
		"optional header beyond":    mut(func(b []byte) { put16(b, offPE+4+16, 0xffff) }),
		"directories > 16":          mut(func(b []byte) { put32(b, offOpt+108, 17) }),
		"section raw beyond":        mut(func(b []byte) { put32(b, sh(0, 20), uint32(len(b))) }),
		"section raw overflow":      mut(func(b []byte) { put32(b, sh(1, 20), 0xfffffff0) }),
		"long name beyond table":    mut(func(b []byte) { copy(b[sh(0, 0):], "/99999\x00\x00") }),
		"reloc block size 0":        mut(func(b []byte) { put32(b, offReloc+4, 0) }),
		"reloc block odd":           mut(func(b []byte) { put32(b, offReloc+4, 11) }),
		"reloc block beyond dir":    mut(func(b []byte) { put32(b, offReloc+4, 0x100) }),
		"reloc dir outside":         mut(func(b []byte) { put32(b, offOpt+112+8*5, 0x90000) }),
		"import table outside":      mut(func(b []byte) { put32(b, offOpt+112+8*1, 0x90000) }),
		"import thunk outside":      mut(func(b []byte) { put32(b, offRdata, 0x90000) }),
		"export dir outside":        mut(func(b []byte) { put32(b, offOpt+112, 0x90000) }),
		"export count huge":         mut(func(b []byte) { put32(b, offRdata+0x140+20, 0xffffffff) }),
		"export table outside":      mut(func(b []byte) { put32(b, offRdata+0x140+28, 0x90000) }),
		"pdata beyond section":      mut(func(b []byte) { put32(b, offOpt+112+8*3+4, 0x100000) }),
		"symbols beyond":            mut(func(b []byte) { put32(b, offPE+4+8, 0x700); put32(b, offPE+4+12, 1000) }),
		"symbol count huge":         mut(func(b []byte) { put32(b, offPE+4+8, 0x700); put32(b, offPE+4+12, 0xffffffff) }),
	}
	for name, b := range cases {
		if _, _, err := Parse(b); err == nil || !(errors.Is(err, ErrMalformed) || errors.Is(err, ErrUnsupported)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	p32 := mut(func(b []byte) { put16(b, offOpt, 0x10b) })
	if _, _, err := Parse(p32); !errors.Is(err, ErrUnsupported) {
		t.Errorf("PE32: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(buildPE())
	f.Fuzz(func(t *testing.T, b []byte) {
		im, info, err := Parse(b)
		if err != nil {
			return
		}
		slots := append(append([]binref.Slot{}, im.Slots...), binref.ScanX86(im)...)
		n, err := binref.Normalize(im, slots, im.CleanUnits(info.Funcs), binref.IdentSymbol)
		if err != nil {
			t.Fatal(err)
		}
		out, err := binref.Reconstruct(n.Streams)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("accepted input does not round trip: %v", err)
		}
	})
}

func TestImportThunkAmplification(t *testing.T) {
	b := buildPE()
	// four descriptors, all sharing one thunk array of four entries at +0x70
	for i := 0; i < 4; i++ {
		d := offRdata + 20*i
		put32(b, d, rvaRdata+0x70)
		put32(b, d+12, rvaRdata+0x90)
		put32(b, d+16, rvaRdata+0x70)
	}
	for i := 0; i < 20; i++ {
		b[offRdata+0x50+i] = 0
	}
	for k := 0; k < 4; k++ {
		put64(b, offRdata+0x70+8*k, rvaRdata+0x80)
	}
	put32(b, offOpt+116+8, 20*5)
	if _, _, err := Parse(b); err != nil {
		t.Fatalf("fixture itself must parse: %v", err)
	}
	old := thunkBudget
	thunkBudget = 15 // 4 descriptors x 2 arrays x 4 entries = 32 reads
	defer func() { thunkBudget = old }()
	if _, _, err := Parse(b); !errors.Is(err, ErrMalformed) || err == nil || !bytes.Contains([]byte(err.Error()), []byte("budget")) {
		t.Fatalf("err = %v", err)
	}
}
