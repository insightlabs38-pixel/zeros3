package binref

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
)

// Unit is a named or derived address range (a function, an object) that
// references can be expressed relative to.
type Unit struct {
	VA, Size uint64
	Name     string
}

// Ident selects how a reference target is named in the normalized form.
type Ident uint8

const (
	IdentOrdinal   Ident = iota // rank of the unit in address order (control)
	IdentRegionRel              // region index + offset in the region
	IdentSymbol                 // hash of the unit's name + offset in the unit
	IdentContent                // hash of the unit's reference-masked bytes + offset in the unit
)

func (i Ident) String() string {
	return [...]string{"ordinal", "region-rel", "symbol", "content"}[i]
}

// Stream is one independently content-addressable piece of the representation.
type Stream struct {
	Kind   string // regions | units | payload | refs
	Region int    // region index for payload/refs
	Data   []byte
}

// Stats counts what Normalize did.
type Stats struct {
	Listed    [nClass]int // candidate slots per class
	Accepted  [nClass]int // slots recorded as references
	Intra     int         // target in the same unit as the source (left raw: position independent)
	Unmapped  int         // target outside every mapped region
	Overlap   int
	Collide   int    // target unit id shared by several units (left raw)
	Units     int    // referenced units in the table
	ByEnc     [5]int // accepted references per encoding
	RegionRel int    // references recorded relative to a region (no unit contained the target)
}

// Norm is the normalized representation of one image.
type Norm struct {
	Streams []Stream
	Stat    Stats
	Masked  []byte // payload bytes of the whole file (slot bytes zeroed), for the lab only
}

// MetaBytes is every byte that is not region payload.
func (n *Norm) MetaBytes() int {
	t := 0
	for _, s := range n.Streams {
		if s.Kind != "payload" {
			t += len(s.Data)
		}
	}
	return t
}

type acc struct {
	Slot
	t      uint64
	tr, sr int
}

func uv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// CleanUnits sorts units by address, clips them to mapped regions and drops
// overlaps. Zero or oversized Size extends to the next unit / region end.
func (im *Image) CleanUnits(us []Unit) []Unit {
	us = append([]Unit(nil), us...)
	sort.SliceStable(us, func(a, b int) bool {
		if us[a].VA != us[b].VA {
			return us[a].VA < us[b].VA
		}
		return us[a].Size > us[b].Size // aliases at one address: the sized one first
	})
	k := 0
	for i, u := range us { // one unit per address
		if i == 0 || u.VA != us[k-1].VA {
			us[k] = u
			k++
		}
	}
	us = us[:k]
	var out []Unit
	for i, u := range us {
		ri := im.regionAtVA(u.VA)
		if ri < 0 {
			continue
		}
		r := &im.Regions[ri]
		end := r.VA + r.vsize()
		if i+1 < len(us) && us[i+1].VA > u.VA && us[i+1].VA < end {
			end = us[i+1].VA
		}
		if u.Size != 0 && u.VA+u.Size < end && u.VA+u.Size > u.VA {
			end = u.VA + u.Size
		}
		if n := len(out); n > 0 && u.VA < out[n-1].VA+out[n-1].Size {
			continue
		}
		u.Size = end - u.VA
		if u.Size == 0 {
			continue
		}
		out = append(out, u)
	}
	return out
}

// UnitsFromSyms builds units from the image's symbol table.
func (im *Image) UnitsFromSyms(funcsOnly bool) []Unit {
	var us []Unit
	for _, s := range im.Syms {
		if funcsOnly && !s.Func {
			continue
		}
		us = append(us, Unit{VA: s.VA, Size: s.Size, Name: s.Name})
	}
	return im.CleanUnits(us)
}

// UnitsFromBranches derives function starts without any symbol information:
// every CALL target (E8 slot in slots) is taken to start a unit.
func (im *Image) UnitsFromBranches(slots []Slot) []Unit {
	seen := map[uint64]bool{}
	var us []Unit
	for _, s := range slots {
		if s.Class != ClassBranch || s.Off < 1 || im.Data[s.Off-1] != 0xE8 {
			continue
		}
		ri := im.regionAtOff(s.Off)
		if ri < 0 || !im.Regions[ri].Mapped {
			continue
		}
		r := &im.Regions[ri]
		va := r.VA + uint64(s.Off-r.Off)
		t := decodeSlot(EncRel32, 0, va, 0, 0, le(im.Data[s.Off:], 4))
		if !seen[t] {
			seen[t] = true
			us = append(us, Unit{VA: t})
		}
	}
	return im.CleanUnits(us)
}

func hash32(b []byte) uint32 {
	h := sha256.Sum256(b)
	return binary.LittleEndian.Uint32(h[:4])
}

// Normalize builds the normalized representation of im. slots are the
// candidate reference fields (format-listed and/or scanned); units give the
// naming scope for targets. The image's bytes are untouched.
func Normalize(im *Image, slots []Slot, units []Unit, ident Ident) (*Norm, error) {
	if err := im.Prepare(); err != nil {
		return nil, err
	}
	n := &Norm{}
	st := &n.Stat
	for _, s := range slots {
		st.Listed[s.Class]++
	}
	// 1. sanitize slots: sorted, non-overlapping, inside one region, decodable.
	ss := append([]Slot(nil), slots...)
	sort.SliceStable(ss, func(a, b int) bool { return ss[a].Off < ss[b].Off })
	var as []acc
	prevEnd := -1
	for _, s := range ss {
		w := s.Enc.width()
		if s.Off < 0 || s.Off+w > len(im.Data) || s.Off < prevEnd {
			st.Overlap++
			continue
		}
		sr := im.regionAtOff(s.Off)
		r := &im.Regions[sr]
		if s.Off+w > r.Off+r.Len || (s.Enc == EncRel32 && !r.Mapped) || s.Bias > 7 {
			st.Overlap++
			continue
		}
		var va uint64
		if r.Mapped {
			va = r.VA + uint64(s.Off-r.Off)
		}
		v := le(im.Data[s.Off:], w)
		t := decodeSlot(s.Enc, s.Bias, va, im.Base, im.Base2, v)
		tr := im.regionAtVA(t)
		if tr < 0 || encodeSlot(s.Enc, s.Bias, va, im.Base, im.Base2, t) != v {
			st.Unmapped++
			continue
		}
		prevEnd = s.Off + w
		as = append(as, acc{Slot: s, t: t, tr: tr, sr: sr})
	}
	masked := append([]byte(nil), im.Data...)
	for _, a := range as {
		for i := 0; i < a.Enc.width(); i++ {
			masked[a.Off+i] = 0
		}
	}
	// 2. unit ids; ambiguous ids are excluded.
	us := im.CleanUnits(units)
	ids := make([]uint32, len(us))
	valid := make([]bool, len(us))
	count := map[uint32]int{}
	for i, u := range us {
		valid[i] = true
		switch ident {
		case IdentOrdinal:
			ids[i] = uint32(i)
		case IdentSymbol:
			if u.Name == "" {
				valid[i] = false
				continue
			}
			h := fnv.New32a()
			h.Write([]byte(u.Name))
			ids[i] = h.Sum32()
		case IdentContent:
			off, ok := im.OffOfVA(u.VA)
			rg := &im.Regions[im.regionAtVA(u.VA)]
			if !ok || u.VA+u.Size > rg.VA+uint64(rg.Len) {
				valid[i] = false // not entirely file-backed: nothing to name it by
				continue
			}
			ids[i] = hash32(masked[off : off+int(u.Size)])
		}
		if ident != IdentRegionRel {
			count[ids[i]]++
		}
	}
	if ident == IdentRegionRel {
		for i := range valid {
			valid[i] = false
		}
	} else {
		for i := range us {
			if valid[i] && count[ids[i]] > 1 {
				valid[i] = false
			}
		}
	}
	unitAt := func(va uint64) int {
		i := sort.Search(len(us), func(i int) bool { return us[i].VA+us[i].Size > va })
		if i < len(us) && us[i].VA <= va {
			return i
		}
		return -1
	}
	// 3. classify each slot; build per-region reference streams.
	type rec struct {
		a    acc
		unit int // -1 = region-relative
	}
	byRegion := map[int][]rec{}
	used := map[int]bool{}
	for _, a := range as {
		sr := &im.Regions[a.sr]
		ui := unitAt(a.t)
		if sr.Mapped && ui >= 0 && ui == unitAt(sr.VA+uint64(a.Off-sr.Off)) {
			st.Intra++
			copy(masked[a.Off:], im.Data[a.Off:a.Off+a.Enc.width()])
			continue
		}
		if ui >= 0 && ident != IdentRegionRel && !valid[ui] {
			st.Collide++
			copy(masked[a.Off:], im.Data[a.Off:a.Off+a.Enc.width()])
			continue
		}
		if ui >= 0 && valid[ui] {
			used[ui] = true
		} else {
			ui = -1
		}
		if ui < 0 {
			st.RegionRel++
		}
		byRegion[a.sr] = append(byRegion[a.sr], rec{a, ui})
		st.Accepted[a.Class]++
		st.ByEnc[a.Enc]++
	}
	// 4. streams.
	var hdr []byte
	hdr = uv(hdr, im.Base)
	hdr = uv(hdr, im.Base2)
	hdr = uv(hdr, uint64(len(im.Regions)))
	for _, r := range im.Regions {
		hdr = uv(hdr, uint64(r.Len))
		var f byte
		if r.Mapped {
			f |= 1
		}
		if r.Mapped && r.vsize() > uint64(r.Len) {
			f |= 2
		}
		hdr = append(hdr, f)
		if r.Mapped {
			hdr = uv(hdr, r.VA)
		}
		if f&2 != 0 {
			hdr = uv(hdr, r.vsize())
		}
	}
	n.Streams = append(n.Streams, Stream{Kind: "regions", Data: hdr})
	var ut []byte
	var uidx []int
	for i := range us {
		if used[i] {
			uidx = append(uidx, i)
		}
	}
	ut = uv(ut, uint64(len(uidx)))
	var prev uint64
	for _, i := range uidx {
		ut = binary.LittleEndian.AppendUint32(ut, ids[i])
		ut = uv(ut, us[i].VA-prev)
		prev = us[i].VA
	}
	st.Units = len(uidx)
	n.Streams = append(n.Streams, Stream{Kind: "units", Data: ut})
	for ri, r := range im.Regions {
		n.Streams = append(n.Streams, Stream{Kind: "payload", Region: ri, Data: masked[r.Off : r.Off+r.Len]})
		recs := byRegion[ri]
		if len(recs) == 0 {
			continue
		}
		var rb []byte
		rb = uv(rb, uint64(len(recs)))
		pe := r.Off
		for _, x := range recs {
			rb = uv(rb, uint64(x.a.Off-pe))
			pe = x.a.Off + x.a.Enc.width()
			kb := byte(x.a.Enc) | x.a.Bias<<3
			if x.unit < 0 {
				kb |= 0x80
				rb = append(rb, kb)
				rb = uv(rb, uint64(x.a.tr))
				rb = uv(rb, x.a.t-im.Regions[x.a.tr].VA)
			} else {
				rb = append(rb, kb)
				rb = binary.LittleEndian.AppendUint32(rb, ids[x.unit])
				rb = uv(rb, x.a.t-us[x.unit].VA)
			}
		}
		n.Streams = append(n.Streams, Stream{Kind: "refs", Region: ri, Data: rb})
	}
	n.Masked = masked
	return n, nil
}

var errCorrupt = errors.New("binref: corrupt normalized representation")

type rd struct {
	b   []byte
	err error
}

func (r *rd) uv() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *rd) take(n int) []byte {
	if n < 0 || n > len(r.b) {
		r.err = errCorrupt
		return make([]byte, max(n, 0)&0xff)
	}
	x := r.b[:n]
	r.b = r.b[n:]
	return x
}

// Reconstruct rebuilds the original bytes from the streams alone.
func Reconstruct(streams []Stream) ([]byte, error) {
	var hdr, units []byte
	payload := map[int][]byte{}
	refs := map[int][]byte{}
	for _, s := range streams {
		switch s.Kind {
		case "regions":
			hdr = s.Data
		case "units":
			units = s.Data
		case "payload":
			payload[s.Region] = s.Data
		case "refs":
			refs[s.Region] = s.Data
		}
	}
	h := &rd{b: hdr}
	base := h.uv()
	base2 := h.uv()
	nreg := h.uv()
	if h.err != nil || nreg > 1<<20 {
		return nil, errCorrupt
	}
	type reg struct {
		off, ln int
		va      uint64
		mapped  bool
	}
	regs := make([]reg, nreg)
	total := 0
	for i := range regs {
		l := h.uv()
		f := h.take(1)
		if h.err != nil || l > 1<<40 {
			return nil, errCorrupt
		}
		regs[i] = reg{off: total, ln: int(l), mapped: f[0]&1 != 0}
		if regs[i].mapped {
			regs[i].va = h.uv()
		}
		if f[0]&2 != 0 {
			h.uv() // run-time size: only needed to resolve targets when normalizing
		}
		total += int(l)
	}
	if h.err != nil {
		return nil, h.err
	}
	out := make([]byte, 0, total)
	for i, r := range regs {
		p := payload[i]
		if len(p) != r.ln {
			return nil, fmt.Errorf("%w: payload %d has %d bytes, want %d", errCorrupt, i, len(p), r.ln)
		}
		out = append(out, p...)
	}
	u := &rd{b: units}
	nu := u.uv()
	uva := map[uint32]uint64{}
	var prev uint64
	for i := uint64(0); i < nu && u.err == nil; i++ {
		id := binary.LittleEndian.Uint32(u.take(4))
		prev += u.uv()
		uva[id] = prev
	}
	if u.err != nil {
		return nil, u.err
	}
	for ri, rb := range refs {
		if ri < 0 || ri >= len(regs) {
			return nil, errCorrupt
		}
		r := regs[ri]
		d := &rd{b: rb}
		cnt := d.uv()
		pe := 0
		for i := uint64(0); i < cnt && d.err == nil; i++ {
			pe += int(d.uv())
			kb := d.take(1)[0]
			enc, bias := Enc(kb&7), (kb>>3)&7
			var t uint64
			if kb&0x80 != 0 {
				tr := d.uv()
				off := d.uv()
				if tr >= uint64(len(regs)) {
					return nil, errCorrupt
				}
				t = regs[tr].va + off
			} else {
				id := binary.LittleEndian.Uint32(d.take(4))
				off := d.uv()
				va, ok := uva[id]
				if !ok {
					return nil, fmt.Errorf("%w: unknown unit id", errCorrupt)
				}
				t = va + off
			}
			if d.err != nil || enc < EncRel32 || enc > EncOff32 {
				return nil, errCorrupt
			}
			w := enc.width()
			if pe < 0 || pe+w > r.ln {
				return nil, errCorrupt
			}
			var va uint64
			if r.mapped {
				va = r.va + uint64(pe)
			} else if enc == EncRel32 {
				return nil, errCorrupt
			}
			putLE(out[r.off+pe:], w, encodeSlot(enc, bias, va, base, base2, t))
			pe += w
		}
		if d.err != nil {
			return nil, d.err
		}
	}
	return out, nil
}
