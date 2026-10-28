package wasmz3

// Byte categories for the pairwise attribution of changed bytes (Z3-33).
const (
	CatLiteral = iota // instruction bytes, locals, non-reference immediates, data payload
	CatStruct         // types, import / export names, limits, constant expressions, segment headers
	CatIdx            // module-level index references (LEB128, exact width)
	CatLen            // section ids and sizes, body sizes, vector counts
	CatCustom         // custom sections
	CatAddr           // linear-memory address constants (padded 5-byte i32.const): layout references, not indexes
	NumCats
)

// CatName names an attribution category.
var CatName = [NumCats]string{"literal", "structure", "index", "length", "custom", "address"}

// maskLEB overwrites the value bits of a LEB128 number and keeps its width (continuation bits).
func maskLEB(b []byte) {
	for i := range b {
		b[i] = 0x80
	}
	b[len(b)-1] = 0
}

// Categorize returns a copy of d with every reference slot masked (width preserved) and the
// category of each byte.
func Categorize(m *Module, d []byte) (masked []byte, cat []uint8) {
	cat = make([]uint8, len(d))
	for i := range cat {
		cat[i] = CatStruct
	}
	set := func(a, b int64, v uint8) {
		for i := max(a, 0); i < b && i < int64(len(d)); i++ {
			cat[i] = v
		}
	}
	for _, s := range m.Dir.Secs {
		switch s.ID {
		case SecCustom:
			set(s.Off, s.End(), CatCustom)
		case SecCode:
			set(s.Body, s.End(), CatLen) // vector count; bodies below
		case SecData:
			set(s.Body, s.End(), CatLen)
		}
		set(s.Off, s.Body, CatLen)
	}
	for _, b := range m.Bodies {
		set(b.SizeOff, b.Start, CatLen)
		set(b.Start, b.End, CatLiteral)
	}
	for _, s := range m.Datas {
		set(s.Off, s.End, CatStruct)
		set(s.DataOff, s.DataOff+s.DataLen, CatLiteral)
	}
	masked = append([]byte(nil), d...)
	for _, s := range m.Slots {
		if s.Sp == SpAddr {
			set(s.Off, s.Off+int64(s.W), CatAddr)
		} else {
			set(s.Off, s.Off+int64(s.W), CatIdx)
		}
		maskLEB(masked[s.Off : s.Off+int64(s.W)])
	}
	return masked, cat
}
