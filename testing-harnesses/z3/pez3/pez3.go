// Package pez3 is a bounded PE32+ (x86-64 / ARM64) parser that turns a file
// into a binref.Image: an exact byte partition (DOS stub, PE headers, one
// region per section with its run-time size, COFF symbol table, overlay),
// symbols, and reference slots from the base-relocation table, imports,
// exports, the exception directory and the entry point. RVA, VA and file
// offset stay distinct: slots are file offsets, targets are VAs
// (ImageBase+RVA), RVA fields use binref.EncRVA32. The certificate table is
// never interpreted: it lies after the last section as opaque overlay bytes
// and is reconstructed byte for byte, whatever it contains. Nothing here
// implements loader behaviour.
package pez3

import (
	"encoding/binary"
	"errors"
	"fmt"

	"zeros3-z3/binref"
)

// thunkBudget bounds the import-thunk entries read over all descriptors, so
// descriptors sharing one huge thunk array cannot multiply the work.
var thunkBudget = 1 << 22

var (
	ErrMalformed   = errors.New("pez3: malformed")
	ErrUnsupported = errors.New("pez3: unsupported")
)

const (
	maxSections = 256
	maxName     = 1 << 12
	maxSyms     = 1 << 22
	maxEntries  = 1 << 22

	scnExec = 0x20000000
	scnCode = 0x20
)

// Section is the lab's view of a section header.
type Section struct {
	Name                             string
	VSize, RVA, RawSize, RawOff, Chr uint32
}

// Info summarises a parse.
type Info struct {
	Machine    uint16
	ImageBase  uint64
	Entry      uint32
	Sections   []Section
	Dirs       [16][2]uint32 // RVA, size
	Syms       int
	BaseRelocs int
	RelocSlots int
	Imports    int           // import descriptors
	Exports    int           // exported functions
	Pdata      int           // RUNTIME_FUNCTION entries
	Funcs      []binref.Unit // function ranges from the exception directory
	SlotsByDir [16]int
	CertSize   uint32 // certificate table bytes (opaque overlay), 0 if none
}

func bad(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(f, a...)) }

func span(off, n uint64, size int) bool { return off <= uint64(size) && n <= uint64(size)-off }

type parser struct {
	data []byte
	im   *binref.Image
	info *Info
	secs []Section
}

// rvaOff maps an RVA to a file offset and the bytes available from it in the file.
func (p *parser) rvaOff(rva uint64) (int, int, bool) { return RVAOff(p.secs, len(p.data), rva) }

// RVAOff maps an RVA to a file offset and the bytes available from it in the file (the section's
// raw data; the zero-filled tail of a section is not mapped). It is the mapper shared with clrz3.
func RVAOff(secs []Section, size int, rva uint64) (int, int, bool) {
	for _, s := range secs {
		if s.RawSize > 0 && rva >= uint64(s.RVA) && rva < uint64(s.RVA)+uint64(s.RawSize) {
			d := rva - uint64(s.RVA)
			if d >= uint64(s.RawSize) {
				return 0, 0, false
			}
			off := uint64(s.RawOff) + d
			if off >= uint64(size) {
				return 0, 0, false
			}
			return int(off), int(uint64(s.RawSize) - d), true
		}
	}
	return 0, 0, false
}

func (p *parser) slot(off int, enc binref.Enc, dir int) {
	p.im.Slots = append(p.im.Slots, binref.Slot{Off: off, Enc: enc, Class: binref.ClassTable})
	if dir >= 0 {
		p.info.SlotsByDir[dir]++
	}
}

// rvaField records a 32-bit RVA field at file offset off (skipped when it is zero).
func (p *parser) rvaField(off int, dir int) {
	if off+4 <= len(p.data) && binary.LittleEndian.Uint32(p.data[off:]) != 0 {
		p.slot(off, binref.EncRVA32, dir)
	}
}

// Parse validates data as a PE32+ file and returns its Image.
func Parse(data []byte) (*binref.Image, *Info, error) {
	le := binary.LittleEndian
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return nil, nil, bad("no DOS header")
	}
	pe := uint64(le.Uint32(data[0x3c:]))
	if !span(pe, 24, len(data)) || string(data[pe:pe+4]) != "PE\x00\x00" {
		return nil, nil, bad("no PE signature")
	}
	co := int(pe) + 4
	machine, nsec := le.Uint16(data[co:]), int(le.Uint16(data[co+2:]))
	symPtr, nsym := uint64(le.Uint32(data[co+8:])), uint64(le.Uint32(data[co+12:]))
	optSize := uint64(le.Uint16(data[co+16:]))
	if nsec > maxSections {
		return nil, nil, bad("%d sections", nsec)
	}
	opt := co + 20
	if optSize < 112 || !span(uint64(opt), optSize, len(data)) {
		return nil, nil, bad("optional header outside the file")
	}
	if le.Uint16(data[opt:]) != 0x20b {
		return nil, nil, fmt.Errorf("%w: only PE32+", ErrUnsupported)
	}
	p := &parser{data: data, info: &Info{Machine: machine}}
	info := p.info
	info.Entry = le.Uint32(data[opt+16:])
	info.ImageBase = le.Uint64(data[opt+24:])
	sizeOfHeaders := uint64(le.Uint32(data[opt+60:]))
	nrva := uint64(le.Uint32(data[opt+108:]))
	if nrva > 16 || 112+nrva*8 > optSize {
		return nil, nil, bad("%d data directories", nrva)
	}
	for i := uint64(0); i < nrva; i++ {
		info.Dirs[i] = [2]uint32{le.Uint32(data[uint64(opt)+112+8*i:]), le.Uint32(data[uint64(opt)+116+8*i:])}
	}
	shOff := uint64(opt) + optSize
	if !span(shOff, uint64(nsec)*40, len(data)) {
		return nil, nil, bad("section table outside the file")
	}
	// COFF string table (long section names, symbol names)
	var strtab []byte
	if symPtr != 0 {
		if nsym > maxSyms || !span(symPtr, nsym*18+4, len(data)) {
			return nil, nil, bad("symbol table outside the file")
		}
		so := symPtr + nsym*18
		sl := uint64(le.Uint32(data[so:]))
		if sl < 4 || !span(so, sl, len(data)) {
			return nil, nil, bad("string table outside the file")
		}
		strtab = data[so : so+sl]
	}
	for i := 0; i < nsec; i++ {
		h := data[shOff+uint64(i)*40:]
		s := Section{VSize: le.Uint32(h[8:]), RVA: le.Uint32(h[12:]), RawSize: le.Uint32(h[16:]), RawOff: le.Uint32(h[20:]), Chr: le.Uint32(h[36:])}
		nm := string(h[:8])
		for k := 0; k < 8; k++ {
			if h[k] == 0 {
				nm = string(h[:k])
				break
			}
		}
		if len(nm) > 1 && nm[0] == '/' {
			var idx uint64
			ok := true
			for _, c := range nm[1:] {
				if c < '0' || c > '9' || idx > 1<<30 {
					ok = false
					break
				}
				idx = idx*10 + uint64(c-'0')
			}
			if ok {
				full, err := cstr(strtab, idx)
				if err != nil {
					return nil, nil, err
				}
				nm = full
			}
		}
		s.Name = nm
		if s.RawSize > 0 && !span(uint64(s.RawOff), uint64(s.RawSize), len(data)) {
			return nil, nil, bad("section %q raw data leaves the file", nm)
		}
		p.secs = append(p.secs, s)
	}
	info.Sections = p.secs

	im := &binref.Image{Data: data, Base: info.ImageBase}
	switch machine {
	case 0x8664:
		im.Arch = "x86-64"
	case 0xaa64:
		im.Arch = "aarch64"
	}
	p.im = im
	hdrEnd := int(shOff) + nsec*40
	if sizeOfHeaders > uint64(hdrEnd) && sizeOfHeaders <= uint64(len(data)) {
		hdrEnd = int(sizeOfHeaders)
	}
	spans := []binref.Span{
		{Name: "dos", Off: 0, End: int(pe)},
		{Name: "pe-headers", Off: int(pe), End: hdrEnd},
	}
	for _, s := range p.secs {
		ex := s.Chr&scnExec != 0 || s.Chr&scnCode != 0
		va := info.ImageBase + uint64(s.RVA)
		switch {
		case s.RawSize == 0 && s.VSize > 0:
			spans = append(spans, binref.Span{Name: s.Name, VA: va, VSize: uint64(s.VSize), Mapped: true, Exec: ex})
		case s.RawSize > 0:
			sp := binref.Span{Name: s.Name, Off: int(s.RawOff), End: int(s.RawOff + s.RawSize), VA: va, Mapped: true, Exec: ex}
			if s.VSize > s.RawSize {
				sp.VSize = uint64(s.VSize)
			}
			spans = append(spans, sp)
		}
	}
	if symPtr != 0 {
		spans = append(spans, binref.Span{Name: "coff-symbols", Off: int(symPtr), End: int(symPtr + nsym*18 + uint64(len(strtab)))})
	}
	im.Regions = binref.Partition(len(data), spans)
	if err := im.Prepare(); err != nil {
		return nil, nil, err
	}
	if n := uint64(info.Dirs[4][1]); n != 0 && span(uint64(info.Dirs[4][0]), n, len(data)) {
		info.CertSize = uint32(n) // directory 4 is a file offset, not an RVA: opaque overlay bytes
	}
	if err := p.symbols(symPtr, nsym, strtab); err != nil {
		return nil, nil, err
	}
	p.slot0()
	for _, f := range []func() error{p.baseRelocs, p.imports, p.exports, p.pdata, p.debugDir} {
		if err := f(); err != nil {
			return nil, nil, err
		}
	}
	return im, info, nil
}

func (p *parser) slot0() {
	// AddressOfEntryPoint lives at optional header + 16
	le := binary.LittleEndian
	co := int(le.Uint32(p.data[0x3c:])) + 4
	p.slot(co+20+16, binref.EncRVA32, -1)
}

func cstr(tab []byte, idx uint64) (string, error) {
	if idx >= uint64(len(tab)) {
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

// symbols reads the COFF symbol table into Syms (section-relative values -> VAs).
func (p *parser) symbols(symPtr, nsym uint64, strtab []byte) error {
	if symPtr == 0 || nsym == 0 {
		return nil
	}
	le := binary.LittleEndian
	for i := uint64(0); i < nsym; i++ {
		e := p.data[symPtr+i*18:]
		naux := uint64(e[17])
		if i+1+naux > nsym {
			return bad("auxiliary symbols leave the table")
		}
		sec, class, typ, val := int(int16(le.Uint16(e[12:]))), e[16], le.Uint16(e[14:]), uint64(le.Uint32(e[8:]))
		p.info.Syms++
		next := i + 1 + naux
		if sec > 0 && sec <= len(p.secs) && (class == 2 || class == 3) && p.secs[sec-1].RawSize > 0 {
			var nm string
			if le.Uint32(e) == 0 {
				var err error
				if nm, err = cstr(strtab, uint64(le.Uint32(e[4:]))); err != nil {
					return err
				}
			} else {
				nm = string(e[:8])
				for k := 0; k < 8; k++ {
					if e[k] == 0 {
						nm = string(e[:k])
						break
					}
				}
			}
			s := p.secs[sec-1]
			p.im.Syms = append(p.im.Syms, binref.Sym{Name: nm, VA: p.info.ImageBase + uint64(s.RVA) + val, Func: typ == 0x20})
		}
		i = next - 1
	}
	return nil
}

// baseRelocs: directory 5. Every block header's page RVA is a table slot; each typed entry is a reloc slot.
func (p *parser) baseRelocs() error {
	rva, size := uint64(p.info.Dirs[5][0]), uint64(p.info.Dirs[5][1])
	if size == 0 {
		return nil
	}
	off, avail, ok := p.rvaOff(rva)
	if !ok || size > uint64(avail) {
		return bad("base relocation table outside the file")
	}
	le := binary.LittleEndian
	end := off + int(size)
	for o := off; o+8 <= end; {
		page, bsz := uint64(le.Uint32(p.data[o:])), int(le.Uint32(p.data[o+4:]))
		if bsz < 8 || bsz%2 != 0 || o+bsz > end {
			return bad("base relocation block size %d", bsz)
		}
		p.slot(o, binref.EncRVA32, 5)
		for e := o + 8; e < o+bsz; e += 2 {
			v := le.Uint16(p.data[e:])
			typ, ofs := v>>12, uint64(v&0xfff)
			p.info.BaseRelocs++
			var enc binref.Enc
			switch typ {
			case 10:
				enc = binref.EncAbs64
			default:
				continue
			}
			fo, left, ok := p.rvaOff(page + ofs)
			if !ok || left < enc2w(enc) {
				continue
			}
			p.im.Slots = append(p.im.Slots, binref.Slot{Off: fo, Enc: enc, Class: binref.ClassReloc})
			p.info.RelocSlots++
			p.info.SlotsByDir[5]++
		}
		o += bsz
	}
	return nil
}

func enc2w(e binref.Enc) int {
	if e == binref.EncAbs64 {
		return 8
	}
	return 4
}

// imports: directory 1; descriptors, name RVAs and both thunk arrays.
func (p *parser) imports() error {
	rva, size := uint64(p.info.Dirs[1][0]), uint64(p.info.Dirs[1][1])
	if size == 0 {
		return nil
	}
	off, avail, ok := p.rvaOff(rva)
	if !ok || uint64(avail) < 20 {
		return bad("import table outside the file")
	}
	le := binary.LittleEndian
	maxN := min(int(size)/20, avail/20, maxEntries)
	budget := thunkBudget
	for i := 0; i < maxN; i++ {
		d := off + i*20
		oft, name, ft := le.Uint32(p.data[d:]), le.Uint32(p.data[d+12:]), le.Uint32(p.data[d+16:])
		if oft == 0 && name == 0 && ft == 0 {
			break
		}
		p.info.Imports++
		for _, f := range []int{0, 12, 16} {
			p.rvaField(d+f, 1)
		}
		for _, t := range []uint32{oft, ft} {
			if t == 0 {
				continue
			}
			to, left, ok := p.rvaOff(uint64(t))
			if !ok {
				return bad("import thunk array outside the file")
			}
			for k := 0; k+8 <= left; k += 8 {
				v := le.Uint64(p.data[to+k:])
				if v == 0 {
					break
				}
				if budget--; budget < 0 {
					return bad("import thunk entries exceed the budget")
				}
				if v>>63 == 0 && v < 1<<31 { // by name: RVA of a hint/name entry
					p.slot(to+k, binref.EncRVA32, 1)
				}
			}
		}
	}
	return nil
}

// exports: directory 0; name RVA, the three table RVAs, and the function / name tables.
func (p *parser) exports() error {
	rva, size := uint64(p.info.Dirs[0][0]), uint64(p.info.Dirs[0][1])
	if size == 0 {
		return nil
	}
	off, avail, ok := p.rvaOff(rva)
	if !ok || avail < 40 {
		return bad("export directory outside the file")
	}
	le := binary.LittleEndian
	nfunc, nname := uint64(le.Uint32(p.data[off+20:])), uint64(le.Uint32(p.data[off+24:]))
	if nfunc > maxEntries || nname > maxEntries {
		return bad("export counts")
	}
	p.info.Exports = int(nfunc)
	p.rvaField(off+12, 0)
	for _, f := range []int{28, 32, 36} {
		p.rvaField(off+f, 0)
	}
	table := func(rva uint32, n uint64) error {
		if n == 0 {
			return nil
		}
		to, left, ok := p.rvaOff(uint64(rva))
		if !ok || uint64(left) < n*4 {
			return bad("export table outside the file")
		}
		for k := uint64(0); k < n; k++ {
			p.rvaField(to+int(k)*4, 0)
		}
		return nil
	}
	if err := table(le.Uint32(p.data[off+28:]), nfunc); err != nil {
		return err
	}
	return table(le.Uint32(p.data[off+32:]), nname)
}

// pdata: directory 3, RUNTIME_FUNCTION {begin, end, unwind} RVAs; begin addresses are function starts.
func (p *parser) pdata() error {
	rva, size := uint64(p.info.Dirs[3][0]), uint64(p.info.Dirs[3][1])
	if size == 0 || p.info.Machine != 0x8664 {
		return nil
	}
	off, avail, ok := p.rvaOff(rva)
	if !ok || size > uint64(avail) {
		return bad("exception directory outside the file")
	}
	n := int(size) / 12
	if n > maxEntries {
		return bad("exception directory entries")
	}
	p.info.Pdata = n
	le := binary.LittleEndian
	for i := 0; i < n; i++ {
		for f := 0; f < 12; f += 4 {
			p.rvaField(off+i*12+f, 3)
		}
		b, e := uint64(le.Uint32(p.data[off+i*12:])), uint64(le.Uint32(p.data[off+i*12+4:]))
		if e > b {
			p.info.Funcs = append(p.info.Funcs, binref.Unit{VA: p.info.ImageBase + b, Size: e - b})
		}
	}
	return nil
}

// debugDir: directory 6; AddressOfRawData is an RVA (PointerToRawData is a file offset and left raw).
func (p *parser) debugDir() error {
	rva, size := uint64(p.info.Dirs[6][0]), uint64(p.info.Dirs[6][1])
	if size == 0 {
		return nil
	}
	off, avail, ok := p.rvaOff(rva)
	if !ok || size > uint64(avail) {
		return bad("debug directory outside the file")
	}
	for i := 0; i+28 <= int(size); i += 28 {
		p.rvaField(off+i+20, 6)
	}
	return nil
}
