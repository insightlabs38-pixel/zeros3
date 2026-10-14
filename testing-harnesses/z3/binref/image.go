// Package binref is the format-independent substrate of the Z3-12/13 binary
// laboratory: a binary is an exact partition of its bytes into Regions, plus
// reference Slots (fields whose bytes encode a target address). Normalize
// replaces slot bytes by compact target identities and Reconstruct restores
// the original file byte for byte. Format parsers (elfz3, pez3) only produce
// an Image; there is one normalization engine.
package binref

import (
	"errors"
	"fmt"
	"sort"
)

// Region is a contiguous byte range of the file. Regions partition the file.
type Region struct {
	Name   string
	Off    int
	Len    int
	VA     uint64 // virtual address of the first byte when Mapped
	VSize  uint64 // run-time size when larger than Len (zero-filled tail, NOBITS); 0 means Len
	Mapped bool   // file bytes appear at VA..VA+Len at run time
	Exec   bool
}

func (r *Region) vsize() uint64 {
	if r.VSize > uint64(r.Len) {
		return r.VSize
	}
	return uint64(r.Len)
}

// Sym is a named address range from a symbol-like table (optional).
type Sym struct {
	Name     string
	VA, Size uint64
	Func     bool
}

// Enc is how a slot's bytes encode its target.
type Enc uint8

const (
	EncRel32 Enc = iota + 1 // int32 relative to the end of the slot (+Bias): branches, RIP-relative
	EncAbs64                // absolute VA
	EncAbs32                // absolute VA, 32 bits
	EncRVA32                // VA - Image.Base, 32 bits
	EncOff32                // VA - Image.Base2, 32 bits (Go pclntab: offsets from the text start)
)

func (e Enc) width() int {
	if e == EncAbs64 {
		return 8
	}
	return 4
}

// Class says where a slot came from (accounting only; reconstruction ignores it).
type Class uint8

const (
	ClassReloc  Class = iota + 1 // listed by the format's relocation tables
	ClassTable                   // listed by a format structure (symbol values, RVAs, directories)
	ClassBranch                  // x86 CALL/JMP/Jcc rel32
	ClassRIP                     // x86 RIP-relative data reference
	ClassPtr                     // aligned absolute pointer found in data
	nClass
)

var classNames = [...]string{"?", "reloc", "table", "branch", "rip", "ptr"}

func (c Class) String() string { return classNames[c] }

// Slot is one reference field: Enc.width() bytes at file offset Off.
type Slot struct {
	Off   int
	Enc   Enc
	Bias  uint8 // bytes between the end of the slot and the end of its instruction (EncRel32)
	Class Class
}

// Image is a parsed binary. Data is never modified.
type Image struct {
	Data    []byte
	Regions []Region
	Syms    []Sym
	Slots   []Slot // format-listed slots (relocations, table fields)
	Arch    string // "x86-64" enables the x86 reference scan; anything else is structure-only
	Base    uint64 // ImageBase for EncRVA32
	Base2   uint64 // text start for EncOff32
	byVA    []int  // mapped region indices ordered by VA (built by Prepare)
}

var ErrBadImage = errors.New("binref: malformed image")

// Prepare validates the partition and builds the VA index. Regions whose VA
// range overlaps an earlier one are demoted to unmapped.
func (im *Image) Prepare() error {
	end := 0
	for i, r := range im.Regions {
		if r.Off != end || r.Len < 0 || r.Off+r.Len < r.Off {
			return fmt.Errorf("%w: region %d breaks the partition", ErrBadImage, i)
		}
		end = r.Off + r.Len
	}
	if end != len(im.Data) {
		return fmt.Errorf("%w: regions cover %d of %d bytes", ErrBadImage, end, len(im.Data))
	}
	im.byVA = im.byVA[:0]
	for i, r := range im.Regions {
		if r.Mapped && r.vsize() > 0 && r.VA+r.vsize() > r.VA {
			im.byVA = append(im.byVA, i)
		} else {
			im.Regions[i].Mapped = false
		}
	}
	sort.SliceStable(im.byVA, func(a, b int) bool { return im.Regions[im.byVA[a]].VA < im.Regions[im.byVA[b]].VA })
	keep := im.byVA[:0]
	var hi uint64
	for _, i := range im.byVA {
		r := &im.Regions[i]
		if len(keep) > 0 && r.VA < hi {
			r.Mapped = false
			continue
		}
		hi = r.VA + r.vsize()
		keep = append(keep, i)
	}
	im.byVA = keep
	return nil
}

// regionAtOff returns the index of the region containing file offset off.
func (im *Image) regionAtOff(off int) int {
	i := sort.Search(len(im.Regions), func(i int) bool { return im.Regions[i].Off+im.Regions[i].Len > off })
	if i == len(im.Regions) || im.Regions[i].Off > off {
		return -1
	}
	return i
}

// regionAtVA returns the index of the mapped region containing va, or -1.
func (im *Image) regionAtVA(va uint64) int {
	i := sort.Search(len(im.byVA), func(i int) bool {
		r := &im.Regions[im.byVA[i]]
		return r.VA+r.vsize() > va
	})
	if i == len(im.byVA) || im.Regions[im.byVA[i]].VA > va {
		return -1
	}
	return im.byVA[i]
}

// OffOfVA maps a virtual address to a file offset (ok=false if unmapped).
func (im *Image) OffOfVA(va uint64) (int, bool) {
	ri := im.regionAtVA(va)
	if ri < 0 {
		return 0, false
	}
	r := &im.Regions[ri]
	if va-r.VA >= uint64(r.Len) {
		return 0, false // zero-filled tail: no file bytes
	}
	return r.Off + int(va-r.VA), true
}

func decodeSlot(e Enc, bias uint8, slotVA, base, base2, v uint64) uint64 {
	switch e {
	case EncRel32:
		return slotVA + 4 + uint64(bias) + uint64(int64(int32(uint32(v))))
	case EncAbs32:
		return uint64(uint32(v))
	case EncRVA32:
		return base + uint64(uint32(v))
	case EncOff32:
		return base2 + uint64(uint32(v))
	}
	return v
}

func encodeSlot(e Enc, bias uint8, slotVA, base, base2, t uint64) uint64 {
	switch e {
	case EncRel32:
		return uint64(uint32(t - slotVA - 4 - uint64(bias)))
	case EncAbs32:
		return uint64(uint32(t))
	case EncRVA32:
		return uint64(uint32(t - base))
	case EncOff32:
		return uint64(uint32(t - base2))
	}
	return t
}

func le(b []byte, w int) uint64 {
	var v uint64
	for i := w - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func putLE(b []byte, w int, v uint64) {
	for i := 0; i < w; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

// InExec reports whether va is inside a mapped executable region.
func (im *Image) InExec(va uint64) bool {
	if im.byVA == nil {
		if im.Prepare() != nil {
			return false
		}
	}
	i := im.regionAtVA(va)
	return i >= 0 && im.Regions[i].Exec
}
