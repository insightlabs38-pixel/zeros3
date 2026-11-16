package mdelta2

import "zeros3-z3/numcore"

// The numeric core (dtypes, conversions, predictors, NRC/FPE coders) lives in numcore since P7;
// these aliases keep the B5 engine and its drivers source-compatible.

type (
	DType    = numcore.DType
	Params   = numcore.Params
	Base     = numcore.Base
	Residual = numcore.Residual
	Gran     = numcore.Gran
	Kind     = numcore.Kind
)

const (
	DRaw    = numcore.DRaw
	DF32    = numcore.DF32
	DF16    = numcore.DF16
	DBF16   = numcore.DBF16
	DQ8_0   = numcore.DQ8_0
	DQ4_0   = numcore.DQ4_0
	DQ5_0   = numcore.DQ5_0
	DQ4_1   = numcore.DQ4_1
	DQ5_1   = numcore.DQ5_1
	DQ4_K   = numcore.DQ4_K
	DQ6_K   = numcore.DQ6_K
	DQ5_K   = numcore.DQ5_K
	DQOther = numcore.DQOther

	KNone = numcore.KNone
	KID   = numcore.KID
	KAff  = numcore.KAff
	KLow  = numcore.KLow
	KLin  = numcore.KLin

	RKey = numcore.RKey
	RXor = numcore.RXor
	RSub = numcore.RSub

	GTensor = numcore.GTensor
	GBlock  = numcore.GBlock
	GRow    = numcore.GRow

	nrcSyms = numcore.NRCSyms
)

var (
	ErrCorrupt = numcore.ErrCorrupt
	ErrParams  = numcore.ErrParams
)

func le(b []byte, w, i int) uint32             { return numcore.LE(b, w, i) }
func putLE(b []byte, w, i int, v uint32)       { numcore.PutLE(b, w, i, v) }
func mask(w int) uint32                        { return numcore.Mask(w) }
func expField(d DType, bits uint32) int        { return numcore.ExpField(d, bits) }
func bitlen(v uint32) int                      { return numcore.Bitlen(v) }
func zigzag(d uint32, w int) uint32            { return numcore.Zigzag(d, w) }
func unzigzag(z uint32, w int) uint32          { return numcore.Unzigzag(z, w) }
func toF64(d DType, bits uint32) float64       { return numcore.ToF64(d, bits) }
func finite(x float64) bool                    { return numcore.Finite(x) }
func f64ToF16(x float64) uint32                { return numcore.F64ToF16(x) }
func f16ToF64(h uint32) float64                { return numcore.F16ToF64(h) }
func exps(d DType, pred []byte) ([]uint8, int) { return numcore.Exps(d, pred) }

func fpeEncode(d DType, words []byte) ([]byte, bool) { return numcore.FPEEncode(d, words) }
func fpeDecode(d DType, payload []byte, n int) ([]byte, error) {
	return numcore.FPEDecode(d, payload, n)
}
func NRCEncode(d DType, resid, pred []byte) []byte { return numcore.NRCEncode(d, resid, pred) }
func NRCDecode(d DType, payload, pred []byte, n int) ([]byte, error) {
	return numcore.NRCDecode(d, payload, pred, n)
}

func Predict(p *Params, tdt DType, shape []int64, bases []Base) ([]byte, error) {
	return numcore.Predict(p, tdt, shape, bases)
}
func ParseParams(b []byte, nmax int) (*Params, int, error) { return numcore.ParseParams(b, nmax) }
func MakeResidual(res Residual, w int, target, pred []byte) []byte {
	return numcore.MakeResidual(res, w, target, pred)
}
func ApplyResidual(res Residual, w int, resid, pred []byte) []byte {
	return numcore.ApplyResidual(res, w, resid, pred)
}
func Narrow(src []byte, to DType) ([]byte, bool) { return numcore.Narrow(src, to) }
func Widen(src []byte, from DType) []byte        { return numcore.Widen(src, from) }

var (
	FitLin          = numcore.FitLin
	FitAffine       = numcore.FitAffine
	FitAffineRobust = numcore.FitAffineRobust
	LowRankFit      = numcore.LowRankFit
	Deflate         = numcore.Deflate
	DeflateLen      = numcore.DeflateLen
	Planes          = numcore.Planes
	Unplanes        = numcore.Unplanes
	PlaneDeflate    = numcore.PlaneDeflate
	H0              = numcore.H0
	HCtx            = numcore.HCtx
	BF16Bits        = numcore.BF16Bits
)

func key(bits uint32, w int) uint32     { return numcore.Key(bits, w) }
func unkey(k uint32, w int) uint32      { return numcore.Unkey(k, w) }
func f16ToF32(h uint32) uint32          { return numcore.F16ToF32(h) }
func f32ToF16(b uint32) uint32          { return numcore.F32ToF16(b) }
func f32ToBF16(b uint32) uint32         { return numcore.F32ToBF16(b) }
func f64ToBF16(x float64) uint32        { return numcore.F64ToBF16(x) }
func fromF64(d DType, x float64) uint32 { return numcore.FromF64(d, x) }
