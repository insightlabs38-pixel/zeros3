package clrz3

// Byte categories for the pairwise attribution of changed bytes (Z3-33).
const (
	CatLiteral = iota // IL code, method headers, exception tables, FieldRVA data, PE section padding
	CatMeta           // metadata that really says something: heap contents, flags, versions, sizes
	CatIdx            // table / coded / heap indexes, references embedded in signatures
	CatTok            // CIL tokens and ldstr offsets
	CatLayout         // RVAs, list starts, nested-class pairs, PE/CLI/metadata headers
	CatOther          // resources, strong-name signature, debug directory, certificates, win32 resources
	NumCats
)

// CatName names an attribution category.
var CatName = [NumCats]string{"literal", "metadata", "index", "token", "layout", "other"}

// Categorize returns, for the assembly d, a copy with every reference slot masked and the header
// regions zeroed (what the representation stores as payload), and the category of each byte.
func Categorize(f *File, d []byte) (masked []byte, cat []uint8, err error) {
	hv, err := NewHeapView(f, d)
	if err != nil {
		return nil, nil, err
	}
	marks := DeriveMarks(f, d, hv)
	bodies := FindBodies(f, d, true)
	ValidateBodies(f, d, hv, bodies)
	slots := append(append(TableSlots(f, d), BlobSlots(f, d, hv.Blobs, marks)...), ILSlots(d, bodies)...)
	cat = make([]uint8, len(d))
	for i := range cat {
		cat[i] = CatOther
	}
	set := func(a, b int64, v uint8) {
		for i := max(a, 0); i < b && i < int64(len(d)); i++ {
			cat[i] = v
		}
	}
	for _, s := range f.Secs {
		if s.RawSize == 0 {
			continue
		}
		v := uint8(CatOther)
		switch s.Name {
		case ".text", ".sdata", ".data", ".rdata":
			v = CatLiteral
		case ".reloc":
			v = CatLayout
		}
		set(int64(s.RawOff), int64(s.RawOff)+int64(s.RawSize), v)
	}
	for t := 0; t < NumTables; t++ {
		tb := &f.Tables[t]
		set(tb.Off, tb.Off+int64(tb.Rows)*int64(tb.RowSize), CatMeta)
	}
	for _, h := range []Stream{f.Str, f.US, f.GUID, f.Blob} {
		set(h.Off, h.Off+h.Size, CatMeta)
	}
	for _, b := range bodies {
		if !b.Opaque {
			set(b.Body.Off, b.Body.End, CatLiteral)
		}
	}
	if f.CLI.ResSize != 0 {
		if o, _, ok := f.RVAToOff(f.CLI.ResRVA); ok {
			set(int64(o), int64(o)+int64(f.CLI.ResSize), CatOther)
		}
	}
	if f.CLI.SNSize != 0 {
		if o, _, ok := f.RVAToOff(f.CLI.SNRVA); ok {
			set(int64(o), int64(o)+int64(f.CLI.SNSize), CatOther)
		}
	}
	if f.Dirs[6][0] != 0 {
		if o, _, ok := f.RVAToOff(f.Dirs[6][0]); ok {
			set(int64(o), int64(o)+int64(f.Dirs[6][1]), CatOther)
		}
	}
	if f.CertSize != 0 {
		set(f.CertOff, f.CertOff+f.CertSize, CatOther)
	}
	masked = append([]byte(nil), d...)
	for i := range slots {
		s := &slots[i]
		v := uint8(CatIdx)
		switch {
		case s.Sp == SpLayout:
			v = CatLayout
		case s.Table == TabIL || s.Sp == SpUS:
			v = CatTok
		}
		set(s.Off, s.Off+int64(s.Len), v)
		switch s.Enc {
		case EncFixed, EncToken:
			clear(masked[s.Off : s.Off+int64(s.Len)])
		case EncCompressed:
			maskCompressed(masked[s.Off:s.Off+int64(s.Len)], int(s.Len))
		}
	}
	for _, r := range f.Excised {
		set(r.Off, r.End(), CatLayout)
		clear(masked[r.Off:r.End()])
	}
	return masked, cat, nil
}
