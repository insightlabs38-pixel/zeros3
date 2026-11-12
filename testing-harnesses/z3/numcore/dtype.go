// Package numcore is the portable numeric core of the model lane (standard library + qcore only):
// dtypes and exact float conversions, predictors, residual transforms, the NRC/FPE entropy coders
// and the bounded-memory windowed model container. Research stores and corpus tooling stay in
// mdelta2 and the drivers.
package numcore

import "zeros3-z3/qcore"

// DType is the element type of a tensor payload.
type DType uint8

const (
	DRaw DType = iota // opaque bytes (integers, buffers, anything not predicted numerically)
	DF32
	DF16
	DBF16
	DQ8_0 // GGUF block types are kept as opaque typed blocks
	DQ4_0
	DQ5_0
	DQ4_1
	DQ5_1
	DQ4_K
	DQ6_K
	DQ5_K
	DQOther
	DQ2_K
	DQ3_K
	DIQ4_NL
	DIQ4_XS
)

var dtNames = map[DType]string{DRaw: "RAW", DF32: "F32", DF16: "F16", DBF16: "BF16", DQ8_0: "Q8_0", DQ4_0: "Q4_0", DQ5_0: "Q5_0", DQ4_1: "Q4_1", DQ5_1: "Q5_1",
	DQ4_K: "Q4_K", DQ6_K: "Q6_K", DQ5_K: "Q5_K", DQOther: "QOTHER", DQ2_K: "Q2_K", DQ3_K: "Q3_K", DIQ4_NL: "IQ4_NL", DIQ4_XS: "IQ4_XS"}

func (d DType) String() string { return dtNames[d] }

// Width is the element byte width for float dtypes, 1 otherwise.
func (d DType) Width() int {
	switch d {
	case DF32:
		return 4
	case DF16, DBF16:
		return 2
	}
	return 1
}

// IsFloat reports whether numerical prediction applies.
func (d DType) IsFloat() bool { return d == DF32 || d == DF16 || d == DBF16 }

var qtypes = map[DType]qcore.Type{DQ8_0: qcore.Q8_0, DQ4_0: qcore.Q4_0, DQ4_1: qcore.Q4_1, DQ5_0: qcore.Q5_0, DQ5_1: qcore.Q5_1, DQ2_K: qcore.Q2_K, DQ3_K: qcore.Q3_K,
	DQ4_K: qcore.Q4_K, DQ5_K: qcore.Q5_K, DQ6_K: qcore.Q6_K, DIQ4_NL: qcore.IQ4_NL, DIQ4_XS: qcore.IQ4_XS}

// QType is the ggml quantization type of a block dtype.
func (d DType) QType() (qcore.Type, bool) { t, ok := qtypes[d]; return t, ok }

// IsQuant reports a block-quantized dtype with a known geometry and quantizer.
func (d DType) IsQuant() bool { _, ok := qtypes[d]; return ok }

// FromGGML maps a ggml tensor type id to a DType (DQOther for block types without a quantizer here).
func FromGGML(id uint32) DType {
	switch id {
	case 0:
		return DF32
	case 1:
		return DF16
	case 30:
		return DBF16
	}
	for d, q := range qtypes {
		if uint32(q) == id {
			return d
		}
	}
	return DQOther
}
