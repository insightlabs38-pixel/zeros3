package clrz3

import (
	"sort"
	"zeros3-z3/idxref"
)

// Mechanically adapted collectors retain canonical token/signature validation.
func deriveMarksAt(c *Ctx) []byte {
	f, h := c.F, c.HV
	marks := make([]byte, len(h.Blobs))
	conflict := make([]bool, len(h.Blobs))
	for t := 0; t < NumTables; t++ {
		for ci, cd := range schema[t] {
			if cd.K != KBlob || cd.Sig == SigNone {
				continue
			}
			for r := uint32(1); r <= f.Rows(t); r++ {
				off := c.col(t, r, ci)
				i, ok := h.BlobAt[off]
				if !ok {
					continue
				}
				e := h.Blobs[i]
				k := KindOf(cd.Sig, h.Blob[e.DataOff:e.DataOff+e.Len])
				if k == BlobOpaque {
					continue
				}
				if marks[i] != BlobOpaque && marks[i] != k {
					conflict[i] = true
				}
				marks[i] = k
			}
		}
	}
	for i, e := range h.Blobs {
		if marks[i] == BlobOpaque {
			continue
		}
		sl, ok := ParseSig(marks[i], h.Blob[e.DataOff:e.DataOff+e.Len], true)
		if ok {
			for _, s := range sl {
				t, r, ok2 := RowOf(SetEmbType, s.Val)
				if !ok2 || t < 0 || r > f.Rows(t) {
					ok = false
					break
				}
			}
		}
		if !ok || conflict[i] {
			marks[i] = BlobOpaque
		}
	}
	return marks
}

// ValidateBodies marks a body opaque when a token operand names nothing that exists.
func validateBodiesAt(f *File, s *clrSource, h *HeapView, bodies []BodyInfo) {
	okTok := func(set uint8, v uint32, us bool) bool {
		if us {
			_, ok := h.USAt[v&0xffffff]
			return ok
		}
		t, r, ok := RowOf(set, v)
		return ok && (t < 0 || r <= f.Rows(t))
	}
	for i := range bodies {
		b := &bodies[i]
		if b.Opaque {
			continue
		}
		good := true
		if b.Body.LocalOff != 0 {
			good = okTok(SetTokSig, b.Body.LocalTok, false)
		}
		for _, t := range b.Body.Toks {
			switch t.Class {
			case TkString:
				good = good && okTok(0, t.Val, true)
			case TkMethod:
				good = good && okTok(SetTokMethod, t.Val, false)
			case TkField:
				good = good && okTok(SetTokField, t.Val, false)
			case TkType:
				good = good && okTok(SetTokType, t.Val, false)
			case TkSig:
				good = good && okTok(SetTokSig, t.Val, false)
			default:
				good = good && okTok(SetTokAny, t.Val, false)
			}
		}
		for _, cl := range b.Body.Clauses {
			if cl.TokOff != 0 {
				good = good && okTok(SetTokType, cl.TokVal, false)
			}
		}
		b.Opaque = !good
	}
	// a body must not lie inside a header region or partly overlap another body; methods that
	// share one body (same offset) are fine
	order := make([]int, 0, len(bodies))
	for i := range bodies {
		if !bodies[i].Opaque {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return bodies[order[a]].Body.Off < bodies[order[b]].Body.Off })
	end, off := int64(0), int64(-1)
	for _, i := range order {
		b := &bodies[i]
		bad := false
		for _, r := range f.Excised {
			if b.Body.Off < r.End() && r.Off < b.Body.End {
				bad = true
			}
		}
		if b.Body.Off != off && b.Body.Off < end {
			bad = true
		}
		if bad {
			b.Opaque, b.Body = true, Body{}
			continue
		}
		if b.Body.Off != off {
			off, end = b.Body.Off, b.Body.End
		}
	}
}

// BlobBytes returns the content of the blob entry at heap offset off.
func compactTableSlotsAt(f *File, d *clrSource, dst []Slot) ([]Slot, error) {
	count := 0
	for t, tb := range f.Tables {
		for _, cd := range schema[t] {
			if cd.K != KConst {
				if uint64(tb.Rows) > uint64((2<<20)-count) {
					return nil, limit("table slot metadata")
				}
				count += int(tb.Rows)
			}
		}
	}
	if cap(dst)-len(dst) < count {
		return nil, limit("slot capacity")
	}
	out := dst
	for t := 0; t < NumTables; t++ {
		tb := &f.Tables[t]
		if tb.Rows == 0 {
			continue
		}
		for c, cd := range schema[t] {
			if cd.K == KConst {
				continue
			}
			cl := tb.Cols[c]
			s := Slot{Len: uint8(cl.W), Table: uint8(t), Col: uint8(c), Self: -1, Stream: colStream(t, cd)}
			switch cd.K {
			case KStr:
				s.Sp = SpStr
			case KGuid:
				s.Sp = SpGuid
			case KBlob:
				s.Sp = SpBlob
			case KTable:
				if t == TNestedClass {
					s.Sp, s.Stream = SpLayout, StL
					s.Lay = LNestedA
					if c == 1 {
						s.Lay = LNestedB
					}
				} else {
					s.Sp, s.Set = SpRow, uint8(cd.T)
				}
			case KCoded:
				s.Sp, s.Set = SpRow, uint8(SetCoded0+cd.T)
				if t == TTypeRef || t == TExportedType {
					s.Self = int8(t)
				}
			case KList:
				s.Sp, s.Stream, s.Lay = SpLayout, StL, LList
			case KRVA:
				s.Sp, s.Stream = SpLayout, StL
				switch t {
				case TMethodDef:
					s.Lay = LRVA
				case TFieldRVA:
					s.Lay = LFieldRVA
				default:
					s.Lay = LResOff
				}
			}
			for r := uint32(1); r <= tb.Rows; r++ {
				x := s
				x.Row = r
				x.Off = tb.Off + int64(r-1)*int64(tb.RowSize) + int64(cl.Off)
				if cl.W == 2 {
					x.Val = uint32(uint16(d.col(f, t, r, c)))
				} else {
					x.Val = d.col(f, t, r, c)
				}
				out = append(out, x)
			}
		}
	}
	sortSlots(out)
	return out, nil
}

func compactILSlotsAt(d *clrSource, bodies []BodyInfo, dst []Slot) ([]Slot, error) {
	count := 0
	for _, bi := range bodies {
		if bi.Opaque {
			continue
		}
		n := len(bi.Body.Toks) + len(bi.Body.Clauses)
		if bi.Body.LocalOff != 0 {
			n++
		}
		if n > (2<<20)-count {
			return nil, limit("IL slot metadata")
		}
		count += n
	}
	if cap(dst)-len(dst) < count {
		return nil, limit("slot capacity")
	}
	out := dst
	seen := map[int64]bool{}
	for _, bi := range bodies {
		if bi.Opaque || seen[bi.Body.Off] {
			continue // methods sharing one body are listed once
		}
		seen[bi.Body.Off] = true
		b := &bi.Body
		if b.LocalOff != 0 {
			out = append(out, Slot{Off: b.LocalOff, Len: 4, Enc: EncToken, Sp: SpRow, Set: SetTokSig, Stream: StIL, Table: TabIL, Row: bi.Row, Self: -1, Val: b.LocalTok})
		}
		for _, t := range b.Toks {
			s := Slot{Off: t.Off, Len: 4, Enc: EncToken, Stream: StIL, Table: TabIL, Row: bi.Row, Self: -1, Val: t.Val}
			switch t.Class {
			case TkString:
				s.Sp = SpUS
			case TkMethod:
				s.Sp, s.Set = SpRow, SetTokMethod
			case TkField:
				s.Sp, s.Set = SpRow, SetTokField
			case TkType:
				s.Sp, s.Set = SpRow, SetTokType
			case TkSig:
				s.Sp, s.Set = SpRow, SetTokSig
			default:
				s.Sp, s.Set = SpRow, SetTokAny
			}
			out = append(out, s)
		}
		for _, c := range b.Clauses {
			if c.TokOff != 0 {
				out = append(out, Slot{Off: c.TokOff, Len: 4, Enc: EncToken, Sp: SpRow, Set: SetTokType, Stream: StIL, Table: TabIL, Row: bi.Row, Self: -1, Val: c.TokVal})
			}
		}
	}
	sortSlots(out)
	return out, nil
}

func compactFindBodiesAt(f *File, d *clrSource, strict bool) ([]BodyInfo, error) {
	count := 0
	for r := uint32(1); r <= f.Rows(TMethodDef); r++ {
		if d.col(f, TMethodDef, r, 0) != 0 {
			count++
			if count > 1<<18 {
				return nil, limit("body metadata")
			}
		}
	}
	out := make([]BodyInfo, 0, count)
	cached := make(map[int64]int)
	for r := uint32(1); r <= f.Rows(TMethodDef); r++ {
		rva := d.col(f, TMethodDef, r, 0)
		if rva == 0 {
			continue
		}
		bi := BodyInfo{Row: r}
		off, _, ok := f.RVAToOff(rva)
		if ok {
			if j, found := cached[int64(off)]; found {
				bi.Body = out[j].Body
				ok = !out[j].Opaque
			} else {
				var err error
				bi.Body, ok, err = parseBodyAt(d, int64(off), strict)
				if err != nil {
					return nil, err
				}
				cached[int64(off)] = len(out)
			}
		}
		bi.Opaque = !ok
		out = append(out, bi)
	}
	return out, nil
}

func compactBlobSlotsAt(f *File, d *clrSource, blob []byte, es []BlobEntry, marks []byte, dst []Slot) []Slot {
	out := dst
	if !f.HasBlob {
		return dst
	}
	h := blob
	for i, e := range es {
		if marks[i] == BlobOpaque {
			continue
		}
		sl, ok := ParseSig(marks[i], h[e.DataOff:e.DataOff+e.Len], false)
		if !ok {
			continue
		}
		for _, s := range sl {
			out = append(out, Slot{Off: f.Blob.Off + int64(e.DataOff) + int64(s.Off), Len: uint8(s.Width), Enc: EncCompressed, Sp: SpRow,
				Set: SetEmbType, Stream: StE, Table: TabEmb, Self: -1, Val: s.Val})
		}
	}
	return out
}

func countCompactSlotsAt(f *File, d *clrSource, blob []byte, es []BlobEntry, marks []byte, bodies []BodyInfo) (int, error) {
	count := 0
	add := func(n int) bool {
		if n < 0 || n > (2<<20)-count {
			return false
		}
		count += n
		return true
	}
	for t, tb := range f.Tables {
		for _, cd := range schema[t] {
			if cd.K != KConst && !add(int(tb.Rows)) {
				return 0, limit("slot metadata")
			}
		}
	}
	for _, bi := range bodies {
		if bi.Opaque {
			continue
		}
		if !add(len(bi.Body.Toks) + len(bi.Body.Clauses)) {
			return 0, limit("IL metadata")
		}
		if bi.Body.LocalOff != 0 && !add(1) {
			return 0, limit("IL metadata")
		}
	}
	if f.HasBlob {
		h := blob
		for i, e := range es {
			if marks[i] == BlobOpaque {
				continue
			}
			sl, ok := ParseSig(marks[i], h[e.DataOff:e.DataOff+e.Len], false)
			if ok && !add(len(sl)) {
				return 0, limit("signature metadata")
			}
		}
	}
	return count, nil
}

func buildSecsAt(f *File, d []byte, hv *HeapView, bodies []bodyGeom, pm posMap, plen int) []idxref.Sec {
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
		str := d
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
			if b.Opaque && b.End == 0 {
				continue
			}
			it = append(it, b.Off)
			lo = min(lo, b.Off)
			hi = max(hi, b.End)
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

func bodyPredAt(f *File, d *clrSource, prevEnd int64) uint32 {
	p := prevEnd
	for k := 0; k < 4 && p < d.size && d.span(p, 1)[0] == 0; k++ {
		p++
	}
	return offToRVA(f, p)
}
