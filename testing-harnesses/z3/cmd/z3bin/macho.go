package main

import (
	"bytes"
	"debug/macho"
	"fmt"

	"zeros3-z3/binref"
)

// machoImage is a characterization-only view of a 64-bit Mach-O file as a
// binref.Image (sections as regions). It is deliberately not a package: no
// bounds-hardened parser, no relocation or chained-fixup handling, no
// signature handling. It exists to ask whether the same reference problem
// appears in a third container format.
func machoImage(data []byte) (*binref.Image, error) {
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if f.Magic != macho.Magic64 {
		return nil, fmt.Errorf("only 64-bit Mach-O")
	}
	im := &binref.Image{Data: data}
	switch f.Cpu {
	case macho.CpuAmd64:
		im.Arch = "x86-64"
	case macho.CpuArm64:
		im.Arch = "aarch64"
	}
	first := uint64(len(data))
	var spans []binref.Span
	for _, s := range f.Sections {
		if s.Offset != 0 && uint64(s.Offset) < first {
			first = uint64(s.Offset)
		}
	}
	spans = append(spans, binref.Span{Name: "header+loadcmds", Off: 0, End: int(min(first, uint64(len(data))))})
	for _, s := range f.Sections {
		ex := s.Seg == "__TEXT" && s.Name == "__text"
		switch {
		case s.Size == 0:
		case s.Offset == 0: // zerofill
			spans = append(spans, binref.Span{Name: s.Seg + "," + s.Name, VA: s.Addr, VSize: s.Size, Mapped: true})
		case uint64(s.Offset)+s.Size <= uint64(len(data)):
			spans = append(spans, binref.Span{Name: s.Seg + "," + s.Name, Off: int(s.Offset), End: int(uint64(s.Offset) + s.Size), VA: s.Addr, Mapped: true, Exec: ex})
		}
	}
	im.Regions = binref.Partition(len(data), spans)
	if err := im.Prepare(); err != nil {
		return nil, err
	}
	if f.Symtab != nil {
		for _, sy := range f.Symtab.Syms {
			if sy.Type&0x0e == 0x0e && sy.Sect != 0 && sy.Value != 0 { // N_SECT
				im.Syms = append(im.Syms, binref.Sym{Name: sy.Name, VA: sy.Value})
			}
		}
	}
	return im, nil
}
