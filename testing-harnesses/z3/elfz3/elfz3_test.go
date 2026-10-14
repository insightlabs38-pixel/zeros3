package elfz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"zeros3-z3/binref"
)

type sh struct {
	name        string
	typ         uint32
	flags, addr uint64
	data        []byte
	link, info  uint32
	ents        uint64
	nobits      bool
	nobitsSize  uint64
}

// build writes a little-endian ELF64 ET_EXEC x86-64 file: header, the sections' bytes, section name table, section headers.
func build(secs []sh, shstrndxOverride int) []byte {
	le := binary.LittleEndian
	buf := make([]byte, 64)
	copy(buf, "\x7fELF\x02\x01\x01")
	le.PutUint16(buf[16:], 2)
	le.PutUint16(buf[18:], 62)
	le.PutUint16(buf[52:], 64)
	le.PutUint16(buf[58:], 64)
	all := append([]sh{{}}, secs...)
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
	all = append(all, sh{name: ".shstrtab", typ: 3, data: shstr})
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

func sym(nameOff uint32, info byte, shndx uint16, val, size uint64) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b, nameOff)
	b[4] = info
	binary.LittleEndian.PutUint16(b[6:], shndx)
	binary.LittleEndian.PutUint64(b[8:], val)
	binary.LittleEndian.PutUint64(b[16:], size)
	return b
}

func rela(off uint64, typ uint32, symIdx uint32, add int64) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint64(b, off)
	binary.LittleEndian.PutUint64(b[8:], uint64(symIdx)<<32|uint64(typ))
	binary.LittleEndian.PutUint64(b[16:], uint64(add))
	return b
}

const (
	textVA = 0x401000
	dataVA = 0x402000
)

func goodSections() []sh {
	text := make([]byte, 48)
	text[0] = 0xE8
	binary.LittleEndian.PutUint32(text[1:], uint32(16-5)) // call f1 at +16
	text[5] = 0xC3
	text[16] = 0xC3
	data := make([]byte, 32)
	binary.LittleEndian.PutUint64(data, textVA+16) // pointer to f1, covered by a RELATIVE reloc
	strtab := []byte("\x00f0\x00f1\x00")
	symtab := append(append(append([]byte{}, sym(0, 0, 0, 0, 0)...), sym(1, 2, 1, textVA, 16)...), sym(4, 2, 1, textVA+16, 32)...)
	return []sh{
		{name: ".text", typ: 1, flags: 6, addr: textVA, data: text},
		{name: ".data", typ: 1, flags: 3, addr: dataVA, data: data},
		{name: ".bss", typ: 8, flags: 3, addr: dataVA + 0x100, nobits: true, nobitsSize: 0x40},
		{name: ".strtab", typ: 3, data: strtab},
		{name: ".symtab", typ: 2, data: symtab, link: 4, info: 1, ents: 24},
		{name: ".rela.dyn", typ: 4, flags: 2, data: rela(dataVA, 8, 0, textVA+16), link: 5, ents: 24},
	}
}

func TestParseGood(t *testing.T) {
	data := build(goodSections(), -1)
	im, info, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Syms != 2 || info.Relocs != 1 || info.RelocSlot != 1 || info.SymSlots != 2 {
		t.Fatalf("%+v", info)
	}
	if len(im.Syms) != 2 || im.Syms[1].Name != "f1" {
		t.Fatalf("syms %+v", im.Syms)
	}
	var virt bool
	for _, r := range im.Regions {
		if r.Name == ".bss" && r.Len == 0 && r.VSize == 0x40 && r.Mapped {
			virt = true
		}
	}
	if !virt {
		t.Fatal("NOBITS section not a virtual region")
	}
	// full normalization round trip with the format's own slots plus the x86 scan.
	slots := append(append([]binref.Slot{}, im.Slots...), binref.ScanX86(im)...)
	n, err := binref.Normalize(im, slots, im.UnitsFromSyms(false), binref.IdentSymbol)
	if err != nil {
		t.Fatal(err)
	}
	out, err := binref.Reconstruct(n.Streams)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("round trip: %v", err)
	}
	if n.Stat.Accepted[binref.ClassReloc] != 1 || n.Stat.Accepted[binref.ClassBranch] != 1 {
		t.Fatalf("accepted %v", n.Stat.Accepted)
	}
}

func TestMalformed(t *testing.T) {
	good := build(goodSections(), -1)
	le := binary.LittleEndian
	shoff := int(le.Uint64(good[40:]))
	secOff := func(i int) int { return shoff + i*64 } // section header i
	mut := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"truncated header":             good[:40],
		"shoff beyond file":            mut(func(b []byte) { le.PutUint64(b[40:], uint64(len(b))) }),
		"shoff overflow":               mut(func(b []byte) { le.PutUint64(b[40:], ^uint64(0)-10) }),
		"shnum too many":               mut(func(b []byte) { le.PutUint16(b[60:], 0xfff0) }),
		"section off overflow":         mut(func(b []byte) { le.PutUint64(b[secOff(1)+24:], ^uint64(0)-3) }),
		"section size beyond":          mut(func(b []byte) { le.PutUint64(b[secOff(1)+32:], uint64(len(b))) }),
		"shstrndx is the null section": mut(func(b []byte) { le.PutUint16(b[62:], 0) }),
		"shstrndx out of range":        mut(func(b []byte) { le.PutUint16(b[62:], 200) }),
		"name index out of table":      mut(func(b []byte) { le.PutUint32(b[secOff(1):], 1<<20) }),
		"symtab entsize":               mut(func(b []byte) { le.PutUint64(b[secOff(5)+56:], 16) }),
		"symtab link bad":              mut(func(b []byte) { le.PutUint32(b[secOff(5)+40:], 99) }),
		"rela entsize":                 mut(func(b []byte) { le.PutUint64(b[secOff(6)+56:], 8) }),
		"rela link not symtab":         mut(func(b []byte) { le.PutUint32(b[secOff(6)+40:], 1) }),
		"phdr table beyond": mut(func(b []byte) {
			le.PutUint64(b[32:], uint64(len(b)))
			le.PutUint16(b[56:], 4)
			le.PutUint16(b[54:], 56)
		}),
	}
	for name, b := range cases {
		if _, _, err := Parse(b); err == nil || !(errors.Is(err, ErrMalformed) || errors.Is(err, ErrUnsupported)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// a relocation naming a symbol past the table
	secs := goodSections()
	secs[5].data = rela(dataVA, 8, 77, 0)
	if _, _, err := Parse(build(secs, -1)); !errors.Is(err, ErrMalformed) {
		t.Errorf("reloc symbol index: %v", err)
	}
	// an unterminated symbol name (string table without the final NUL, name index at its end)
	secs = goodSections()
	secs[3].data = []byte("\x00f0\x00f1")
	if _, _, err := Parse(build(secs, -1)); !errors.Is(err, ErrMalformed) {
		t.Errorf("unterminated name: %v", err)
	}
	// ELF32 and big-endian are unsupported, not malformed
	b32 := append([]byte(nil), good...)
	b32[4] = 1
	if _, _, err := Parse(b32); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ELF32: %v", err)
	}
}

// Overlapping sections must still give an exact partition.
func TestOverlappingSections(t *testing.T) {
	secs := goodSections()
	data := build(secs, -1)
	le := binary.LittleEndian
	shoff := int(le.Uint64(data[40:]))
	// make .data start inside .text
	le.PutUint64(data[shoff+2*64+24:], le.Uint64(data[shoff+1*64+24:])+8)
	im, _, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := im.Prepare(); err != nil {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(build(goodSections(), -1))
	f.Fuzz(func(t *testing.T, b []byte) {
		im, _, err := Parse(b)
		if err != nil {
			return
		}
		slots := append(append([]binref.Slot{}, im.Slots...), binref.ScanX86(im)...)
		n, err := binref.Normalize(im, slots, im.UnitsFromSyms(false), binref.IdentSymbol)
		if err != nil {
			t.Fatal(err)
		}
		out, err := binref.Reconstruct(n.Streams)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("accepted input does not round trip: %v", err)
		}
	})
}

// Overlapping relocation sections must not multiply the work.
func TestRelocationAmplification(t *testing.T) {
	secs := goodSections()
	for i := 0; i < 6; i++ {
		secs = append(secs, sh{name: ".rela.dyn", typ: 4, flags: 2, data: bytes.Repeat(rela(dataVA, 8, 0, textVA+16), 4), link: 5, ents: 24})
	}
	data := build(secs, -1)
	// point every extra rela section at the same file range as the first one
	le := binary.LittleEndian
	shoff := int(le.Uint64(data[40:]))
	first := le.Uint64(data[shoff+7*64+24:])
	for i := 8; i < 13; i++ {
		le.PutUint64(data[shoff+i*64+24:], first)
	}
	if _, _, err := Parse(data); err != nil {
		t.Fatalf("fixture must parse: %v", err)
	}
	old := relocBudget
	relocBudget = 20 // 7 sections x 4 entries + 1
	defer func() { relocBudget = old }()
	if _, _, err := Parse(data); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v", err)
	}
}
