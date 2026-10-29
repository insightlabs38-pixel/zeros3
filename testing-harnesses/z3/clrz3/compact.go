package clrz3

// Mechanically extracted collectors with checked sizing passes; slot semantics unchanged.
func compactTableSlots(f *File, d []byte, dst []Slot) ([]Slot, error) {
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
					x.Val = uint32(le16(d[x.Off:]))
				} else {
					x.Val = le32(d[x.Off:])
				}
				out = append(out, x)
			}
		}
	}
	sortSlots(out)
	return out, nil
}

func compactILSlots(d []byte, bodies []BodyInfo, dst []Slot) ([]Slot, error) {
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
			out = append(out, Slot{Off: b.LocalOff, Len: 4, Enc: EncToken, Sp: SpRow, Set: SetTokSig, Stream: StIL, Table: TabIL, Row: bi.Row, Self: -1, Val: le32(d[b.LocalOff:])})
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

func compactFindBodies(f *File, d []byte, strict bool) ([]BodyInfo, error) {
	count := 0
	for r := uint32(1); r <= f.Rows(TMethodDef); r++ {
		if f.Col(d, TMethodDef, r, 0) != 0 {
			count++
			if count > 1<<18 {
				return nil, limit("body metadata")
			}
		}
	}
	out := make([]BodyInfo, 0, count)
	for r := uint32(1); r <= f.Rows(TMethodDef); r++ {
		rva := f.Col(d, TMethodDef, r, 0)
		if rva == 0 {
			continue
		}
		bi := BodyInfo{Row: r}
		off, _, ok := f.RVAToOff(rva)
		if ok {
			bi.Body, ok = ParseBody(d, int64(off), strict)
		}
		bi.Opaque = !ok
		out = append(out, bi)
	}
	return out, nil
}

func compactBlobSlots(f *File, d []byte, es []BlobEntry, marks []byte, dst []Slot) []Slot {
	out := dst
	if !f.HasBlob {
		return dst
	}
	h := d[f.Blob.Off : f.Blob.Off+f.Blob.Size]
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

func countCompactSlots(f *File, d []byte, es []BlobEntry, marks []byte, bodies []BodyInfo) (int, error) {
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
		h := d[f.Blob.Off : f.Blob.Off+f.Blob.Size]
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
