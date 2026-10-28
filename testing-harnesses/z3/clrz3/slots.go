package clrz3

import (
	"sort"
)

// Reference slots: every place whose bytes are a number that names something else. Slots are
// classified by the *space* the number lives in; the spaces are never merged.

// Enc is how a slot's number is stored.
type Enc uint8

const (
	EncFixed      Enc = iota // little-endian, Len bytes (table / coded / heap index, RVA)
	EncCompressed            // ECMA compressed unsigned integer inside a signature blob
	EncToken                 // 4-byte CIL metadata token (table byte << 24 | row)
	EncNone                  // layout item with no bytes of its own (blob mark)
)

// Space is what the number refers to.
type Space uint8

const (
	SpStr    Space = iota + 1 // #Strings byte offset
	SpGuid                    // #GUID 1-based ordinal
	SpUS                      // #US byte offset (ldstr token low 24 bits)
	SpBlob                    // #Blob byte offset
	SpRow                     // metadata row: simple index, coded index or token
	SpLayout                  // layout state (list start, RVA, nested-class pair, blob mark)
)

// Lay is the kind of a layout slot.
type Lay uint8

const (
	LNone     Lay = iota
	LList         // child-list start index (TypeDef.FieldList ...)
	LRVA          // MethodDef.RVA
	LFieldRVA     // FieldRVA.RVA
	LResOff       // ManifestResource.Offset
	LNestedA      // NestedClass.NestedClass
	LNestedB      // NestedClass.EnclosingClass
	LMark         // blob heap entry: signature kind mark
)

// Component streams (every group carries one byte string per stream).
const (
	StG0a = iota // Module, ModuleRef, Assembly, AssemblyRef, File key slots
	StG0b        // TypeRef, TypeDef, ExportedType, ManifestResource key slots
	StE          // type references embedded in signature blobs
	StG1         // TypeSpec
	StG2         // remaining key-bearing tables
	StR          // every other table slot
	StIL         // CIL tokens
	StL          // layout items
	NumStreams
)

// StreamName names a component stream.
var StreamName = [NumStreams]string{"keys0a", "keys0b", "sigrefs", "typespec", "keys2", "rest", "il", "layout"}

// Reference sets (candidate target tables of a row reference).
const (
	SetCoded0    = 64 // + coded family
	SetTokMethod = 96 + iota - 1
	SetTokField
	SetTokType
	SetTokSig
	SetTokAny
	SetEmbType
	numSets
)

// Slot is one reference or layout item.
type Slot struct {
	Off    int64
	Len    uint8
	Enc    Enc
	Sp     Space
	Lay    Lay
	Set    uint8 // SpRow: candidate set (a table number, SetCoded0+family, or a token/embedded set)
	Stream uint8
	Table  uint8 // owning table; 0xff blob-embedded, 0xfe IL
	Col    uint8
	Row    uint32
	Self   int8   // coded slot that may point into its own table (delta-coded in the layout stream), else -1
	Val    uint32 // the number as found in the file
}

const (
	TabEmb = 0xff
	TabIL  = 0xfe
)

// SetTables lists the tables a reference set can name.
func SetTables(s uint8) []int {
	switch {
	case s < NumTables:
		return []int{int(s)}
	case s >= SetCoded0 && s < SetCoded0+NumCoded:
		var out []int
		for _, t := range coded[s-SetCoded0].tabs {
			if t >= 0 {
				out = append(out, t)
			}
		}
		return out
	}
	switch s {
	case SetTokMethod:
		return []int{TMethodDef, TMemberRef, TMethodSpec}
	case SetTokField:
		return []int{TField, TMemberRef}
	case SetTokType:
		return []int{TTypeDef, TTypeRef, TTypeSpec}
	case SetTokSig:
		return []int{TStandAloneSig}
	case SetTokAny:
		return []int{TTypeDef, TTypeRef, TTypeSpec, TField, TMethodDef, TMemberRef, TMethodSpec}
	case SetEmbType:
		return []int{TTypeDef, TTypeRef}
	}
	return nil
}

// RowOf resolves a stored number to (table, row). A zero number is the null reference
// (table -1, row 0, ok). ok is false when the number names no table of the set.
func RowOf(set uint8, v uint32) (table int, row uint32, ok bool) {
	if v == 0 {
		return -1, 0, true
	}
	switch {
	case set < NumTables:
		return int(set), v, true
	case set >= SetCoded0 && set < SetCoded0+NumCoded, set == SetEmbType:
		fam := int(set) - SetCoded0
		if set == SetEmbType {
			fam = CTypeDefOrRef
		}
		t, r, ok := DecodeCoded(fam, v)
		if !ok || r == 0 {
			return -1, 0, false
		}
		if set == SetEmbType && t == TTypeSpec {
			return -1, 0, false
		}
		return t, r, true
	}
	t, r := int(v>>24), v&0xffffff
	if r == 0 {
		return -1, 0, false
	}
	for _, x := range SetTables(set) {
		if x == t {
			return t, r, true
		}
	}
	return -1, 0, false
}

// ValOf is the inverse of RowOf.
func ValOf(set uint8, table int, row uint32) (uint32, bool) {
	if row == 0 {
		return 0, true
	}
	switch {
	case set < NumTables:
		if int(set) != table {
			return 0, false
		}
		return row, true
	case set >= SetCoded0 && set < SetCoded0+NumCoded, set == SetEmbType:
		fam := int(set) - SetCoded0
		if set == SetEmbType {
			fam = CTypeDefOrRef
		}
		return EncodeCoded(fam, table, row)
	}
	for _, x := range SetTables(set) {
		if x == table {
			if row > 0xffffff {
				return 0, false
			}
			return uint32(table)<<24 | row, true
		}
	}
	return 0, false
}

func keyStream(t int) uint8 {
	switch t {
	case TModule, TModuleRef, TAssembly, TAssemblyRef, TFile:
		return StG0a
	case TTypeRef, TTypeDef, TExportedType, TManifestResource:
		return StG0b
	case TTypeSpec:
		return StG1
	}
	return StG2
}

func colStream(t int, c ColDef) uint8 {
	if !c.Key {
		return StR
	}
	return keyStream(t)
}

// TableSlots lists the slots of every table row. Values are read from d (a masked file yields
// zeros; the structure of the list does not depend on them).
func TableSlots(f *File, d []byte) []Slot {
	var out []Slot
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
	return out
}

func sortSlots(s []Slot) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].Off < s[j].Off })
}

// BlobEntry is one length-prefixed entry of #Blob or #US (heap-relative offsets).
type BlobEntry struct {
	Off     uint32 // offset of the length prefix
	DataOff uint32
	Len     uint32
}

// HeapEntries walks a length-prefixed heap. ragged is true when the heap does not end on an
// entry boundary.
func HeapEntries(h []byte) (es []BlobEntry, ragged bool) {
	p := 0
	for p < len(h) {
		c := h[p]
		var n, w int
		switch {
		case c&0x80 == 0:
			n, w = int(c), 1
		case c&0xC0 == 0x80 && p+2 <= len(h):
			n, w = int(c&0x3f)<<8|int(h[p+1]), 2
		case c&0xE0 == 0xC0 && p+4 <= len(h):
			n, w = int(c&0x1f)<<24|int(h[p+1])<<16|int(h[p+2])<<8|int(h[p+3]), 4
		default:
			return es, true
		}
		if p+w+n > len(h) {
			return es, true
		}
		es = append(es, BlobEntry{uint32(p), uint32(p + w), uint32(n)})
		p += w + n
	}
	return es, false
}

// BlobSlots lists the type references embedded in the blobs marked as signatures. marks is
// indexed like entries; the blob heap is read from d.
func BlobSlots(f *File, d []byte, es []BlobEntry, marks []byte) []Slot {
	var out []Slot
	if !f.HasBlob {
		return nil
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

// BodyInfo is one method body with its owner and encoding state.
type BodyInfo struct {
	Row    uint32 // MethodDef row
	Body   Body
	Opaque bool
}

// ILSlots lists the metadata-token operands of the non-opaque bodies, the local-signature token
// of fat headers and the class tokens of typed catch clauses.
func ILSlots(d []byte, bodies []BodyInfo) []Slot {
	var out []Slot
	for _, bi := range bodies {
		if bi.Opaque {
			continue
		}
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
	return out
}

// FindBodies parses the body of every MethodDef with a non-zero RVA. Rows whose RVA is unmapped
// or whose body does not parse are returned Opaque with a zero Body.
func FindBodies(f *File, d []byte, strict bool) []BodyInfo {
	var out []BodyInfo
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
	return out
}
