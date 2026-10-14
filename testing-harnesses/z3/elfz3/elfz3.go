// Package elfz3 is a bounded ELF64 little-endian parser that turns a file
// into a binref.Image: an exact byte partition (header tables, one region per
// file-backed section, unnamed gaps), symbols, and reference slots listed by
// relocation sections and symbol values. It never allocates from an untrusted
// count beyond what the file's own length allows and rejects any table that
// leaves the file. Everything it does not understand stays in an opaque region.
package elfz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"zeros3-z3/binref"
)

var (
	ErrMalformed   = errors.New("elfz3: malformed")
	ErrUnsupported = errors.New("elfz3: unsupported")
)

const (
	maxName   = 1 << 12
	maxSyms   = 1 << 22
	maxRelocs = 1 << 23
	shfAlloc  = 2
	shfExec   = 4
)

// Section is the lab's view of a section header.
type Section struct {
	Name                    string
	Type                    uint32
	Flags                   uint64
	Addr, Off, Size         uint64
	Link, Info, Align, Ents uint64
}

// Info summarises a parse.
type Info struct {
	Machine   uint16
	Type      uint16
	Sections  []Section
	Syms      int
	Relocs    int // relocation entries read
	RelocSlot int // of those, slots usable as references
	SymSlots  int
	BuildIDs  []string // "kind:offset:len" for GNU/Go notes
}

func bad(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(f, a...)) }

func span(off, n uint64, size int) bool {
	return off <= uint64(size) && n <= uint64(size)-off
}

type iv struct {
	off, end int
	name     string
	sec      int // section index or -1
}

// Parse validates data as an ELF64 LE file and returns its Image.
func Parse(data []byte) (*binref.Image, *Info, error) {
	if len(data) < 64 || string(data[:4]) != "\x7fELF" {
		return nil, nil, bad("no ELF header")
	}
	if data[4] != 2 || data[5] != 1 {
		return nil, nil, fmt.Errorf("%w: only ELF64 little-endian", ErrUnsupported)
	}
	le := binary.LittleEndian
	info := &Info{Type: le.Uint16(data[16:]), Machine: le.Uint16(data[18:])}
	phoff, shoff := le.Uint64(data[32:]), le.Uint64(data[40:])
	phentsize, phnum := uint64(le.Uint16(data[54:])), uint64(le.Uint16(data[56:]))
	shentsize, shnum, shstrndx := uint64(le.Uint16(data[58:])), uint64(le.Uint16(data[60:])), uint64(le.Uint16(data[62:]))
	if phnum > 0 && (phentsize < 56 || !span(phoff, phnum*phentsize, len(data))) {
		return nil, nil, bad("program header table outside the file")
	}
	if shnum == 0 && shoff != 0 {
		return nil, nil, fmt.Errorf("%w: extended section count", ErrUnsupported)
	}
	if shnum > 0 && (shentsize < 64 || !span(shoff, shnum*shentsize, len(data))) {
		return nil, nil, bad("section header table outside the file")
	}
	secs := make([]Section, shnum)
	nameIdx := make([]uint32, shnum)
	for i := range secs {
		h := data[shoff+uint64(i)*shentsize:]
		nameIdx[i] = le.Uint32(h)
		s := &secs[i]
		s.Type, s.Flags = le.Uint32(h[4:]), le.Uint64(h[8:])
		s.Addr, s.Off, s.Size = le.Uint64(h[16:]), le.Uint64(h[24:]), le.Uint64(h[32:])
		s.Link, s.Info, s.Align, s.Ents = uint64(le.Uint32(h[40:])), uint64(le.Uint32(h[44:])), le.Uint64(h[48:]), le.Uint64(h[56:])
		if s.Type != 0 && s.Type != 8 && !span(s.Off, s.Size, len(data)) {
			return nil, nil, bad("section %d leaves the file", i)
		}
	}
	// section names
	var shstr []byte
	if shnum > 0 {
		if shstrndx >= shnum || secs[shstrndx].Type == 8 || secs[shstrndx].Type == 0 {
			return nil, nil, bad("bad section name table index")
		}
		s := secs[shstrndx]
		shstr = data[s.Off : s.Off+s.Size]
	}
	for i := range secs {
		nm, err := cstr(shstr, nameIdx[i])
		if err != nil {
			return nil, nil, err
		}
		secs[i].Name = nm
	}
	info.Sections = secs

	// regions: header tables and file-backed sections, first-come on overlap.
	ivs := []iv{{0, 64, "ehdr", -1}}
	if phnum > 0 {
		ivs = append(ivs, iv{int(phoff), int(phoff + phnum*phentsize), "phdr", -1})
	}
	if shnum > 0 {
		ivs = append(ivs, iv{int(shoff), int(shoff + shnum*shentsize), "shdr", -1})
	}
	for i, s := range secs {
		if s.Type != 0 && s.Type != 8 && s.Size > 0 {
			ivs = append(ivs, iv{int(s.Off), int(s.Off + s.Size), s.Name, i})
		}
	}
	sort.SliceStable(ivs, func(a, b int) bool {
		if ivs[a].off != ivs[b].off {
			return ivs[a].off < ivs[b].off
		}
		return ivs[a].end > ivs[b].end
	})
	im := &binref.Image{Data: data}
	switch info.Machine {
	case 62:
		im.Arch = "x86-64"
	case 183:
		im.Arch = "aarch64"
	}
	cur := 0
	add := func(r binref.Region) { im.Regions = append(im.Regions, r) }
	for _, v := range ivs {
		if v.end <= cur {
			continue
		}
		if v.off > cur {
			add(binref.Region{Name: "gap", Off: cur, Len: v.off - cur})
			cur = v.off
		}
		r := binref.Region{Name: v.name, Off: cur, Len: v.end - cur}
		if v.sec >= 0 && secs[v.sec].Flags&shfAlloc != 0 {
			r.Mapped, r.VA = true, secs[v.sec].Addr+uint64(cur-v.off)
			r.Exec = secs[v.sec].Flags&shfExec != 0
		}
		add(r)
		cur = v.end
	}
	if cur < len(data) {
		add(binref.Region{Name: "tail", Off: cur, Len: len(data) - cur})
	}
	// NOBITS sections occupy addresses but no file bytes: zero-length regions at the end.
	for _, s := range secs {
		if s.Type == 8 && s.Flags&shfAlloc != 0 && s.Size > 0 {
			add(binref.Region{Name: s.Name, Off: len(data), VA: s.Addr, VSize: s.Size, Mapped: true, Exec: s.Flags&shfExec != 0})
		}
	}
	if err := im.Prepare(); err != nil {
		return nil, nil, err
	}

	// symbols (and their value fields as table slots)
	exec := info.Type == 2 || info.Type == 3
	symCount := map[int]uint64{}
	for i, s := range secs {
		if s.Type != 2 && s.Type != 11 {
			continue
		}
		if s.Ents != 24 || s.Size%24 != 0 || s.Size/24 > maxSyms || s.Link >= shnum || secs[s.Link].Type != 3 {
			return nil, nil, bad("symbol table %d", i)
		}
		n := s.Size / 24
		symCount[i] = n
		str := secs[s.Link]
		strb := data[str.Off : str.Off+str.Size]
		for k := uint64(1); k < n; k++ {
			e := data[s.Off+k*24:]
			nm, err := cstr(strb, le.Uint32(e))
			if err != nil {
				return nil, nil, err
			}
			ty := e[4] & 0xf
			shndx, val, sz := le.Uint16(e[6:]), le.Uint64(e[8:]), le.Uint64(e[16:])
			info.Syms++
			if shndx == 0 || shndx >= 0xff00 || val == 0 {
				continue
			}
			if exec {
				im.Slots = append(im.Slots, binref.Slot{Off: int(s.Off + k*24 + 8), Enc: binref.EncAbs64, Class: binref.ClassTable})
				info.SymSlots++
			}
			if (ty == 1 || ty == 2) && sz > 0 {
				im.Syms = append(im.Syms, binref.Sym{Name: nm, VA: val, Size: sz, Func: ty == 2})
			}
		}
	}
	// relocations (executables / shared objects: r_offset is a virtual address)
	for i, s := range secs {
		if s.Type != 4 && s.Type != 9 {
			continue
		}
		ent := uint64(24)
		if s.Type == 9 {
			ent = 16
		}
		if s.Ents != ent || s.Size%ent != 0 || s.Size/ent > maxRelocs {
			return nil, nil, bad("relocation section %d", i)
		}
		n := s.Size / ent
		if s.Link != 0 {
			if _, ok := symCount[int(s.Link)]; !ok {
				return nil, nil, bad("relocation section %d links a non-symbol table", i)
			}
		}
		for k := uint64(0); k < n; k++ {
			e := data[s.Off+k*ent:]
			off, inf := le.Uint64(e), le.Uint64(e[8:])
			typ, sym := uint32(inf), inf>>32
			if sym != 0 && (s.Link == 0 || sym >= symCount[int(s.Link)]) {
				return nil, nil, bad("relocation %d.%d symbol index %d", i, k, sym)
			}
			info.Relocs++
			if !exec {
				continue
			}
			enc, w := relocEnc(info.Machine, typ)
			if enc == 0 {
				continue
			}
			fo, ok := im.OffOfVA(off)
			if !ok || fo+w > len(data) {
				continue
			}
			ri := sort.Search(len(im.Regions), func(j int) bool { return im.Regions[j].Off+im.Regions[j].Len > fo })
			if ri == len(im.Regions) || fo+w > im.Regions[ri].Off+im.Regions[ri].Len {
				continue
			}
			im.Slots = append(im.Slots, binref.Slot{Off: fo, Enc: enc, Class: binref.ClassReloc})
			info.RelocSlot++
		}
	}
	// notes
	for _, s := range secs {
		if s.Type != 7 || s.Size < 12 {
			continue
		}
		b := data[s.Off : s.Off+s.Size]
		nl, dl, ty := uint64(le.Uint32(b)), uint64(le.Uint32(b[4:])), le.Uint32(b[8:])
		if 12+((nl+3)&^3) <= uint64(len(b)) && dl <= uint64(len(b)) {
			info.BuildIDs = append(info.BuildIDs, fmt.Sprintf("%s:type%d:%d:%d", s.Name, ty, s.Off, dl))
		}
	}
	return im, info, nil
}

// relocEnc maps an ELF relocation type to a slot encoding for the machines
// the lab knows; unknown types are ignored (their bytes stay raw).
func relocEnc(machine uint16, typ uint32) (binref.Enc, int) {
	switch machine {
	case 62: // x86-64
		switch typ {
		case 1, 6, 7, 8, 37: // R_X86_64_64, GLOB_DAT, JUMP_SLOT, RELATIVE, IRELATIVE
			return binref.EncAbs64, 8
		case 2, 4: // PC32, PLT32
			return binref.EncRel32, 4
		case 10, 11: // 32, 32S
			return binref.EncAbs32, 4
		}
	case 183: // AArch64
		switch typ {
		case 257, 1025, 1026, 1027, 1032: // ABS64, GLOB_DAT, JUMP_SLOT, RELATIVE, IRELATIVE
			return binref.EncAbs64, 8
		}
	}
	return 0, 0
}

func cstr(tab []byte, idx uint32) (string, error) {
	if uint64(idx) >= uint64(len(tab)) {
		if len(tab) == 0 && idx == 0 {
			return "", nil
		}
		return "", bad("string index %d outside table of %d", idx, len(tab))
	}
	rest := tab[idx:]
	if len(rest) > maxName {
		rest = rest[:maxName]
	}
	for i, c := range rest {
		if c == 0 {
			return string(rest[:i]), nil
		}
	}
	return "", bad("unterminated or over-long string")
}
