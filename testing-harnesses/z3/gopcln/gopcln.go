// Package gopcln reads the Go runtime function table (pclntab, Go 1.18+)
// from a binref.Image. Both Go ELF and Go PE carry it, even when stripped, so
// it is a format-independent source of function starts, names, and of the
// 32-bit text-relative entry offsets that move whenever code does.
package gopcln

import (
	"encoding/binary"
	"errors"

	"zeros3-z3/binref"
)

var ErrNotFound = errors.New("gopcln: no pclntab")

const maxName = 1 << 12

type Table struct {
	TextStart uint64
	NFunc     int
	Units     []binref.Unit
	Slots     []binref.Slot
}

// Find locates and validates a pclntab in a mapped, non-executable region.
func Find(im *binref.Image) (*Table, error) {
	for _, r := range im.Regions {
		if !r.Mapped || r.Exec || r.Len < 80 {
			continue
		}
		b := im.Data[r.Off : r.Off+r.Len]
		for o := 0; o+80 <= len(b); o += 8 {
			m := binary.LittleEndian.Uint32(b[o:])
			if m != 0xFFFFFFF0 && m != 0xFFFFFFF1 {
				continue
			}
			if t := parse(im, r, b, o); t != nil {
				return t, nil
			}
		}
	}
	return nil, ErrNotFound
}

func parse(im *binref.Image, r binref.Region, b []byte, o int) *Table {
	le := binary.LittleEndian
	h := b[o:]
	if h[4] != 0 || h[5] != 0 || (h[6] != 1 && h[6] != 2 && h[6] != 4) || h[7] != 8 {
		return nil
	}
	f := func(i int) uint64 { return le.Uint64(h[8+8*i:]) }
	nfunc, textStart := f(0), f(2)
	funcName, pcln := f(3), f(7)
	avail := uint64(len(b) - o)
	if nfunc == 0 || nfunc > avail/8 || funcName >= avail || pcln >= avail || pcln+(nfunc*2+1)*4 > avail {
		return nil
	}
	if textStart == 0 { // the file leaves runtime.text to be filled in at load time
		for _, rg := range im.Regions {
			if rg.Mapped && rg.Exec && (textStart == 0 || rg.VA < textStart) {
				textStart = rg.VA
			}
		}
	}
	if !im.InExec(textStart) {
		return nil
	}
	t := &Table{TextStart: textStart, NFunc: int(nfunc)}
	ftab := b[o+int(pcln):]
	for i := uint64(0); i <= nfunc; i++ {
		ent := le.Uint32(ftab[8*i:])
		t.Slots = append(t.Slots, binref.Slot{Off: r.Off + o + int(pcln) + int(8*i), Enc: binref.EncOff32, Class: binref.ClassTable})
		if i == nfunc {
			break
		}
		fo := uint64(le.Uint32(ftab[8*i+4:]))
		if pcln+fo+8 > avail {
			return nil
		}
		fn := b[o+int(pcln+fo):]
		if le.Uint32(fn) != ent {
			return nil
		}
		t.Slots = append(t.Slots, binref.Slot{Off: r.Off + o + int(pcln+fo), Enc: binref.EncOff32, Class: binref.ClassTable})
		nameOff := uint64(le.Uint32(fn[4:]))
		name := ""
		if funcName+nameOff < avail {
			s := b[o+int(funcName+nameOff):]
			if len(s) > maxName {
				s = s[:maxName]
			}
			for k, c := range s {
				if c == 0 {
					name = string(s[:k])
					break
				}
			}
		}
		t.Units = append(t.Units, binref.Unit{VA: textStart + uint64(ent), Name: name})
	}
	im.Base2 = textStart
	return t
}
