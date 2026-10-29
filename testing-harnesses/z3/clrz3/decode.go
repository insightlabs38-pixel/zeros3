package clrz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/idxref"
)

func unzz(u uint64) int64 { return idxref.Unzigzag(u) }

func idxErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrFormat, err)
}

// writeSlot stores v into the slot's bytes with the width the mask preserved.
func writeSlot(buf []byte, s *Slot, v uint32) error {
	b := buf[s.Off : s.Off+int64(s.Len)]
	switch s.Enc {
	case EncFixed:
		switch s.Len {
		case 2:
			if v > 0xffff {
				return bad("value %d does not fit the 2-byte slot at %d", v, s.Off)
			}
			binary.LittleEndian.PutUint16(b, uint16(v))
		case 4:
			binary.LittleEndian.PutUint32(b, v)
		default:
			return bad("slot width %d", s.Len)
		}
	case EncToken:
		binary.LittleEndian.PutUint32(b, v)
	case EncCompressed:
		if !putCompressed(b, int(s.Len), v) {
			return bad("value %d does not fit the %d-byte compressed integer at %d", v, s.Len, s.Off)
		}
	}
	return nil
}

type lent struct {
	off  int64
	kind int // 0 layout slot, 1 selector, 2 blob mark
	slot int
}

// Decode rebuilds the assembly from Meta and the component bytes (in order). Every length it
// allocates from is bounded by the component bytes it was handed.
func Decode(meta []byte, comps [][]byte) ([]byte, error) {
	if len(meta) > 16<<20 || len(comps) > 1<<16 {
		return nil, limit("representation metadata/groups")
	}
	if len(meta) < len(repMagic)+3 || string(meta[:4]) != repMagic {
		return nil, bad("representation header")
	}
	k0, auto := int(meta[4]), int(meta[5])
	if k0 < 1 || k0 > 4 || auto > 100 {
		return nil, bad("K0 %d auto %d", k0, auto)
	}
	p := meta[6:]
	rd := func() (uint64, error) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, bad("meta header")
		}
		p = p[n:]
		return v, nil
	}
	size, err := rd()
	if err != nil {
		return nil, err
	}
	if size > 32<<20 {
		return nil, limit("representation output size")
	}
	nreg, err := rd()
	if err != nil {
		return nil, err
	}
	if nreg > 16 || size > 1<<31 {
		return nil, bad("meta header")
	}
	regs := make([]Region, nreg)
	var exc int64
	for i := range regs {
		o, err := rd()
		if err != nil {
			return nil, err
		}
		l, err := rd()
		if err != nil {
			return nil, err
		}
		if o > size || l > size-o || (i > 0 && int64(o) < regs[i-1].End()) {
			return nil, bad("meta region")
		}
		regs[i] = Region{int64(o), int64(l)}
		exc += int64(l)
	}
	if int64(len(p)) != exc {
		return nil, bad("meta carries %d region bytes, expected %d", len(p), exc)
	}
	var payloads [][]byte
	var splits [][][]byte
	var plen int64
	totalComp := int64(0)
	var streamLengths [NumStreams]int
	for _, c := range comps {
		if len(c) > 16<<20 || int64(len(c)) > (128<<20)-totalComp {
			return nil, limit("component bytes")
		}
		totalComp += int64(len(c))
		pl, ss, err := idxref.SplitComp(c, NumStreams)
		if err != nil {
			return nil, idxErr(err)
		}
		for k, part := range ss {
			streamLengths[k] += len(part)
		}
		payloads = append(payloads, pl)
		splits = append(splits, ss)
		plen += int64(len(pl))
	}
	if plen != int64(size)-exc {
		return nil, bad("payload is %d bytes, the header describes %d", plen, int64(size)-exc)
	}
	buf := make([]byte, size)
	var in [NumStreams][]byte
	for k, n := range streamLengths {
		in[k] = make([]byte, 0, n)
	}
	tilePos, region, within := int64(0), 0, 0
	for i, part := range payloads {
		for len(part) > 0 {
			if region < len(regs) && tilePos == regs[region].Off {
				r := regs[region]
				copy(buf[r.Off:r.End()], p[within:within+int(r.Len)])
				within += int(r.Len)
				tilePos = r.End()
				region++
				continue
			}
			end := int64(size)
			if region < len(regs) {
				end = regs[region].Off
			}
			n := int64(len(part))
			if n > end-tilePos {
				n = end - tilePos
			}
			if n <= 0 {
				return nil, bad("payload/header tiling")
			}
			copy(buf[tilePos:tilePos+n], part[:n])
			part = part[n:]
			tilePos += n
		}
		for k := range splits[i] {
			in[k] = append(in[k], splits[i][k]...)
		}
	}
	for region < len(regs) && tilePos == regs[region].Off {
		r := regs[region]
		copy(buf[r.Off:r.End()], p[within:within+int(r.Len)])
		within += int(r.Len)
		tilePos = r.End()
		region++
	}
	if tilePos != int64(size) || region != len(regs) || within != len(p) {
		return nil, bad("payload/header tiling")
	}
	f, err := Parse(bytes.NewReader(buf), int64(size), Limits{})
	if err != nil {
		return nil, err
	}
	if len(f.Excised) != len(regs) {
		return nil, bad("header regions differ")
	}
	for i := range regs {
		if f.Excised[i] != regs[i] {
			return nil, bad("header regions differ")
		}
	}
	if f.TabKind != '~' || f.HasPtr || !f.HasStr || !f.HasBlob {
		return nil, bad("unsupported structure in a representation")
	}
	c, err := newCtxDecode(f, buf, k0)
	if err != nil {
		return nil, err
	}
	c.Auto = auto
	count, err := tableSlotCount(f)
	if err != nil {
		return nil, err
	}
	slots, err := compactTableSlots(f, buf, make([]Slot, 0, count))
	if err != nil {
		return nil, err
	}

	// ---- L: layout state ------------------------------------------------------------------
	var ls []lent
	for i := range slots {
		s := &slots[i]
		if s.Sp == SpLayout {
			ls = append(ls, lent{s.Off, 0, i})
		} else if s.Self >= 0 && s.Sp == SpRow {
			ls = append(ls, lent{s.Off, 1, i})
		}
	}
	for i, e := range c.Blobs {
		ls = append(ls, lent{f.Blob.Off + int64(e.Off), 2, i})
	}
	ls = append(ls, lent{f.Str.Off, 3, 0})
	sortLents(ls)
	lv := make([]uint64, len(ls))
	lin := in[StL]
	var extra []int
	for i := range ls {
		v, n := binary.Uvarint(lin)
		if n <= 0 {
			return nil, bad("layout stream truncated")
		}
		lin = lin[n:]
		lv[i] = v
		if ls[i].kind == 3 { // suffix offsets of #Strings: v of them follow as deltas
			if v > uint64(len(c.str)) {
				return nil, bad("%d string suffix offsets", v)
			}
			prev := 0
			for k := uint64(0); k < v; k++ {
				dv, n := binary.Uvarint(lin)
				if n <= 0 || dv > uint64(len(c.str)) {
					return nil, bad("layout stream truncated")
				}
				lin = lin[n:]
				prev += int(dv)
				extra = append(extra, prev)
			}
		}
	}
	if err := c.SetStrExtra(extra); err != nil {
		return nil, err
	}
	if len(lin) != 0 {
		return nil, bad("trailing layout bytes")
	}
	selfDone := map[int64]bool{}
	run := map[[2]uint8]uint32{}
	var prevA, curA, prevFR, prevRes int64
	for i, e := range ls {
		v := lv[i]
		switch e.kind {
		case 3:
			continue
		case 2:
			if v >= uint64(numBlobKinds) {
				return nil, bad("blob mark %d", v)
			}
			c.Marks[e.slot] = byte(v)
		case 1:
			if v == 0 {
				continue
			}
			s := &slots[e.slot]
			row := int64(s.Row) - unzz(v-1)
			if row < 1 || row > int64(f.Rows(int(s.Self))) {
				return nil, bad("self reference to row %d", row)
			}
			cv, ok := EncodeCoded(int(s.Set)-SetCoded0, int(s.Self), uint32(row))
			if !ok || writeSlot(buf, s, cv) != nil {
				return nil, bad("self reference does not fit")
			}
			selfDone[s.Off] = true
		default:
			s := &slots[e.slot]
			var val int64
			switch s.Lay {
			case LList:
				k := [2]uint8{s.Table, s.Col}
				if _, ok := run[k]; !ok {
					run[k] = 1
				}
				val = int64(run[k])
				run[k] += uint32(v)
			case LNestedA:
				prevA += unzz(v)
				curA, val = prevA, prevA
			case LNestedB:
				val = curA - unzz(v)
			case LFieldRVA:
				prevFR += unzz(v)
				val = prevFR
			case LResOff:
				prevRes += unzz(v)
				val = prevRes
			case LRVA:
				continue // restored below, in row order, with the body layout
			}
			if val < 0 || val > 0xffffffff {
				return nil, bad("layout value %d", val)
			}
			if err := writeSlot(buf, s, uint32(val)); err != nil {
				return nil, err
			}
		}
	}
	for k, v := range run {
		cd := schema[k[0]][k[1]]
		if v != f.Rows(cd.T)+1 {
			return nil, bad("child list of %s.%s ends at %d, expected %d", TableName(int(k[0])), cd.Name, v, f.Rows(cd.T)+1)
		}
	}
	// method RVAs and body layout
	var bodies []BodyInfo
	bodySource := &clrSource{r: bytes.NewReader(buf), size: int64(len(buf)), lo: -1}
	bodyCache := make(map[int64]int)
	prevEnd := f.CLIOff + 72
	for i, e := range ls {
		if e.kind != 0 || slots[e.slot].Lay != LRVA {
			continue
		}
		s := &slots[e.slot]
		code := lv[i]
		opaque, rc := code&1 != 0, code>>1
		var rva uint32
		switch {
		case rc == 1:
			rva = 0
		default:
			pred := int64(bodyPred(f, buf, prevEnd))
			if rc >= 2 {
				pred += unzz(rc - 2)
			}
			if pred < 1 || pred > 0xffffffff {
				return nil, bad("method RVA %d", pred)
			}
			rva = uint32(pred)
		}
		if err := writeSlot(buf, s, rva); err != nil {
			return nil, err
		}
		if rva == 0 {
			continue
		}
		bi := BodyInfo{Row: s.Row, Opaque: opaque}
		if !opaque {
			off, _, ok := f.RVAToOff(rva)
			if !ok {
				return nil, bad("method RVA %#x unmapped", rva)
			}
			if cached, found := bodyCache[int64(off)]; found {
				bi.Body = bodies[cached].Body
			} else {
				bodySource.lo = -1
				bodySource.buf = bodySource.buf[:0]
				var parseErr error
				bi.Body, ok, parseErr = parseBodyAt(bodySource, int64(off), false)
				if parseErr != nil {
					return nil, parseErr
				}
				if !ok {
					return nil, bad("method body at %#x", rva)
				}
				bodyCache[int64(off)] = len(bodies)
			}

			prevEnd = bi.Body.End
		}
		if len(bodies) >= 1<<18 {
			return nil, limit("method layout count")
		}
		bodies = append(bodies, bi)
	}
	if err := c.BuildOwners(); err != nil {
		return nil, err
	}

	// ---- reference stages -----------------------------------------------------------------
	pos := [NumStreams][]byte{}
	copy(pos[:], in[:])
	take := func(all []Slot, allBlobs bool, keep func(*Slot) bool) error {
		for i := range all {
			s := &all[i]
			if s.Sp == SpLayout || selfDone[s.Off] || !keep(s) {
				continue
			}
			v, rest, err := c.Get(pos[s.Stream], s, allBlobs)
			if err != nil {
				return idxErr(err)
			}
			pos[s.Stream] = rest
			if err := writeSlot(buf, s, v); err != nil {
				return err
			}
		}
		return c.err
	}
	stream := func(st uint8) func(*Slot) bool { return func(s *Slot) bool { return s.Stream == st } }
	for _, g := range []struct {
		st     uint8
		tables []int
	}{{StG0a, stageG0a}, {StG0b, stageG0b}} {
		for _, t := range g.tables {
			tt, st := t, g.st
			if err := take(slots, false, func(s *Slot) bool { return s.Stream == st && int(s.Table) == tt }); err != nil {
				return nil, err
			}
			if err := c.ComputeTable(t); err != nil {
				return nil, err
			}
		}
	}
	if err := take(BlobSlots(f, buf, c.Blobs, c.Marks), true, func(*Slot) bool { return true }); err != nil {
		return nil, err
	}
	c.SigReady()
	if err := take(slots, true, stream(StG1)); err != nil {
		return nil, err
	}
	for _, t := range stageG1 {
		if err := c.ComputeTable(t); err != nil {
			return nil, err
		}
	}
	for _, t := range stageG2 {
		tt := t
		if err := take(slots, true, func(s *Slot) bool { return s.Stream == StG2 && int(s.Table) == tt }); err != nil {
			return nil, err
		}
		if hasIdentity(t) {
			if err := c.ComputeTable(t); err != nil {
				return nil, err
			}
		}
	}
	if err := take(slots, true, stream(StR)); err != nil {
		return nil, err
	}
	if err := take(ILSlots(buf, bodies), true, func(*Slot) bool { return true }); err != nil {
		return nil, err
	}
	for st := range pos {
		if st != StL && len(pos[st]) != 0 {
			return nil, bad("%d unread bytes in stream %s", len(pos[st]), StreamName[st])
		}
	}
	return buf, nil
}

func sortLents(a []lent) { sort.SliceStable(a, func(i, j int) bool { return a[i].off < a[j].off }) }

// buildSecs describes the payload for the grouping cutters: tables (rows), heaps (entries) and
// the body area (bodies) are record sections; everything else is plain.
func buildSecs(f *File, d []byte, hv *HeapView, bodies []BodyInfo, pm posMap, plen int) []idxref.Sec {
	type cand struct {
		off, end int64
		typ      uint16
		items    []int64
	}
	var cs []cand
	for t := 0; t < NumTables; t++ {
		tb := &f.Tables[t]
		if tb.Rows == 0 {
			continue
		}
		it := make([]int64, tb.Rows)
		for r := range it {
			it[r] = tb.Off + int64(r)*int64(tb.RowSize)
		}
		cs = append(cs, cand{tb.Off, tb.Off + int64(tb.Rows)*int64(tb.RowSize), uint16(0x100 + t), it})
	}
	if f.HasStr {
		var it []int64
		str := d[f.Str.Off : f.Str.Off+f.Str.Size]
		for i := range str {
			if i == 0 || str[i-1] == 0 {
				it = append(it, f.Str.Off+int64(i))
			}
		}
		cs = append(cs, cand{f.Str.Off, f.Str.Off + f.Str.Size, 0x200, it})
	}
	if f.HasBlob {
		it := make([]int64, len(hv.Blobs))
		for i, e := range hv.Blobs {
			it[i] = f.Blob.Off + int64(e.Off)
		}
		cs = append(cs, cand{f.Blob.Off, f.Blob.Off + f.Blob.Size, 0x201, it})
	}
	if f.HasUS {
		it := make([]int64, len(hv.USes))
		for i, e := range hv.USes {
			it[i] = f.US.Off + int64(e.Off)
		}
		cs = append(cs, cand{f.US.Off, f.US.Off + f.US.Size, 0x202, it})
	}
	if f.HasGUID {
		var it []int64
		for o := int64(0); o < f.GUID.Size; o += 16 {
			it = append(it, f.GUID.Off+o)
		}
		cs = append(cs, cand{f.GUID.Off, f.GUID.Off + f.GUID.Size, 0x203, it})
	}
	if len(bodies) > 0 {
		var it []int64
		lo, hi := int64(1<<62), int64(0)
		for _, b := range bodies {
			if b.Opaque && b.Body.End == 0 {
				continue
			}
			it = append(it, b.Body.Off)
			lo = min(lo, b.Body.Off)
			hi = max(hi, b.Body.End)
		}
		sortInt64(it)
		if len(it) > 0 {
			cs = append(cs, cand{lo, hi, 0x204, it})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].off < cs[j].off })
	var out []idxref.Sec
	cur := int64(0)
	add := func(a, b int64, typ uint16, items []int64) {
		pa, pb := pm.payload(a), pm.payload(b)
		if pb <= pa {
			return
		}
		s := idxref.Sec{Type: typ, Off: pa, End: pb}
		for _, x := range items {
			s.Items = append(s.Items, pm.payload(x))
		}
		out = append(out, s)
	}
	for _, s := range cs {
		if s.off < cur { // overlapping region: keep the earlier section
			continue
		}
		add(cur, s.off, 0, nil)
		add(s.off, s.end, s.typ, s.items)
		cur = s.end
	}
	add(cur, int64(f.Size), 0, nil)
	if len(out) > 0 && out[len(out)-1].End != plen {
		out[len(out)-1].End = plen
	}
	return out
}

func sortInt64(a []int64) { sort.Slice(a, func(i, j int) bool { return a[i] < a[j] }) }
