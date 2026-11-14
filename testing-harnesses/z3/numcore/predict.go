package numcore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Kind selects the predictor family.
type Kind uint8

const (
	KNone Kind = iota // no base: prediction is zero (the residual is the value, key-ordered)
	KID               // prediction = base value (dtype-converted)
	KAff              // per-tensor / per-block / per-row affine in the float domain
	KLow              // base + sum of rank-1 terms (low-rank update), optional per-tensor scale
	KLin              // linear combination of 2-3 bases (+ offset), per tensor or per row
)

// Residual selects how a prediction is turned into stored words.
type Residual uint8

const (
	RKey Residual = iota // Zigzag(Key(target) - Key(pred)), exact on all bit patterns
	RXor                 // target ^ pred bits
	RSub                 // Zigzag(target - pred) as raw integers
)

// Gran is the affine/linear coefficient granularity.
type Gran uint8

const (
	GTensor Gran = iota
	GBlock
	GRow
)

// Params is the serializable predictor description (everything the decoder needs besides bases).
type Params struct {
	Kind  Kind
	Res   Residual
	Gran  Gran
	Block uint32    // GBlock: elements per block
	NB    uint8     // number of bases used (KID 1, KAff 1, KLow 1, KLin 2-3)
	Rank  uint16    // KLow
	Prec  uint8     // 4 = float32 coefficients, 2 = bfloat16 coefficients
	Coef  []float32 // affine/linear coefficients; KLow: factors [U (rows*rank) | V (cols*rank)]
}

var ErrParams = errors.New("numcore: invalid predictor parameters")

// MarshalParams serializes p compactly.
func (p *Params) Marshal() []byte {
	b := []byte{byte(p.Kind), byte(p.Res), byte(p.Gran), p.NB, p.Prec}
	b = binary.AppendUvarint(b, uint64(p.Block))
	b = binary.AppendUvarint(b, uint64(p.Rank))
	b = binary.AppendUvarint(b, uint64(len(p.Coef)))
	for _, c := range p.Coef {
		bits := math.Float32bits(c)
		if p.Prec == 2 {
			b = append(b, byte(bits>>16), byte(bits>>24))
		} else {
			b = binary.LittleEndian.AppendUint32(b, bits)
		}
	}
	return b
}

// ParseParams decodes Marshal output; nmax bounds the coefficient count.
func ParseParams(b []byte, nmax int) (*Params, int, error) {
	if len(b) < 5 {
		return nil, 0, ErrParams
	}
	p := &Params{Kind: Kind(b[0]), Res: Residual(b[1]), Gran: Gran(b[2]), NB: b[3], Prec: b[4]}
	if p.Kind > KLin || p.Res > RSub || p.Gran > GRow || (p.Prec != 4 && p.Prec != 2) || p.NB > 3 {
		return nil, 0, ErrParams
	}
	pos := 5
	rd := func() (uint64, bool) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, false
		}
		pos += n
		return v, true
	}
	blk, ok1 := rd()
	rank, ok2 := rd()
	cnt, ok3 := rd()
	if !ok1 || !ok2 || !ok3 || blk > 1<<30 || rank > 4096 || cnt > uint64(nmax) {
		return nil, 0, ErrParams
	}
	p.Block, p.Rank = uint32(blk), uint16(rank)
	p.Coef = make([]float32, cnt)
	for i := range p.Coef {
		if p.Prec == 2 {
			if pos+2 > len(b) {
				return nil, 0, ErrParams
			}
			p.Coef[i] = math.Float32frombits(uint32(b[pos])<<16 | uint32(b[pos+1])<<24)
			pos += 2
		} else {
			if pos+4 > len(b) {
				return nil, 0, ErrParams
			}
			p.Coef[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[pos:]))
			pos += 4
		}
	}
	return p, pos, nil
}

// roundCoef quantizes a coefficient to the stored precision (what the decoder will see).
func roundCoef(x float64, prec uint8) float32 {
	f := float32(x)
	if prec == 2 {
		return math.Float32frombits(uint32(F64ToBF16(float64(f))) << 16)
	}
	return f
}

// Base is one decoded base tensor view.
type Base struct {
	DT   DType
	Data []byte
}

// At is the float64 value of element i.
func (b Base) At(i int) float64 { return b.at(i) }

func (b Base) at(i int) float64 {
	switch b.DT {
	case DF32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b.Data[4*i:])))
	case DBF16:
		return float64(math.Float32frombits(uint32(binary.LittleEndian.Uint16(b.Data[2*i:])) << 16))
	}
	return F16ToF64(uint32(binary.LittleEndian.Uint16(b.Data[2*i:])))
}

func (b Base) n() int { return len(b.Data) / b.DT.Width() }

func idPred(tdt DType, b Base, i int) uint32 {
	if b.DT == tdt {
		return LE(b.Data, tdt.Width(), i)
	}
	switch {
	case b.DT == DF32 && tdt == DF16:
		return F32ToF16(binary.LittleEndian.Uint32(b.Data[4*i:]))
	case b.DT == DF32 && tdt == DBF16:
		return F32ToBF16(binary.LittleEndian.Uint32(b.Data[4*i:]))
	case b.DT == DF16 && tdt == DF32:
		return F16ToF32(uint32(binary.LittleEndian.Uint16(b.Data[2*i:])))
	case b.DT == DBF16 && tdt == DF32:
		return uint32(binary.LittleEndian.Uint16(b.Data[2*i:])) << 16
	}
	return FromF64(tdt, b.at(i))
}

// Predict computes the predicted bit patterns for an n-element target of dtype tdt.
func Predict(p *Params, tdt DType, shape []int64, bases []Base) ([]byte, error) {
	w := tdt.Width()
	n := 1
	for _, s := range shape {
		n *= int(s)
	}
	out := make([]byte, n*w)
	rows, cols := 1, n
	if len(shape) > 0 && shape[len(shape)-1] > 0 {
		cols = int(shape[len(shape)-1])
		rows = n / cols
	}
	if p.Kind != KNone {
		if len(bases) < int(p.NB) || p.NB == 0 {
			return nil, ErrParams
		}
		for _, b := range bases[:p.NB] {
			if b.n() != n {
				return nil, fmt.Errorf("%w: base has %d elements, target %d", ErrParams, b.n(), n)
			}
		}
	}
	switch p.Kind {
	case KNone:
		return out, nil // zero prediction
	case KID:
		if bases[0].DT == tdt {
			copy(out, bases[0].Data)
			break
		}
		for i := 0; i < n; i++ {
			PutLE(out, w, i, idPred(tdt, bases[0], i))
		}
	case KAff:
		blk := affBlock(p, rows, cols, n)
		if blk <= 0 || len(p.Coef) != 2*((n+blk-1)/blk) {
			return nil, ErrParams
		}
		for i := 0; i < n; i++ {
			k := 2 * (i / blk)
			a, c := float64(p.Coef[k]), float64(p.Coef[k+1])
			x := float64(a * bases[0].at(i))
			pv := x + c
			if !Finite(pv) || !Finite(bases[0].at(i)) {
				PutLE(out, w, i, idPred(tdt, bases[0], i))
				continue
			}
			PutLE(out, w, i, FromF64(tdt, pv))
		}
	case KLow:
		r := int(p.Rank)
		if r <= 0 || len(p.Coef) != (rows+cols)*r {
			return nil, ErrParams
		}
		U, V := p.Coef[:rows*r], p.Coef[rows*r:]
		for i := 0; i < rows; i++ {
			ui := U[i*r : (i+1)*r]
			for j := 0; j < cols; j++ {
				vj := V[j*r : (j+1)*r]
				var s float64
				for k := 0; k < r; k++ {
					s += float64(float64(ui[k]) * float64(vj[k]))
				}
				idx := i*cols + j
				bv := bases[0].at(idx)
				pv := bv + s
				if !Finite(pv) || !Finite(bv) {
					PutLE(out, w, idx, idPred(tdt, bases[0], idx))
					continue
				}
				PutLE(out, w, idx, FromF64(tdt, pv))
			}
		}
	case KLin:
		m := int(p.NB)
		ng := 1
		if p.Gran == GRow {
			ng = rows
		}
		if m < 2 || len(p.Coef) != ng*(m+1) {
			return nil, ErrParams
		}
		for i := 0; i < n; i++ {
			g := 0
			if p.Gran == GRow {
				g = i / cols
			}
			co := p.Coef[g*(m+1) : (g+1)*(m+1)]
			var s float64
			ok := true
			for k := 0; k < m; k++ {
				x := bases[k].at(i)
				if !Finite(x) {
					ok = false
				}
				s += float64(float64(co[k]) * x)
			}
			s += float64(co[m])
			if !ok || !Finite(s) {
				PutLE(out, w, i, idPred(tdt, bases[0], i))
				continue
			}
			PutLE(out, w, i, FromF64(tdt, s))
		}
	default:
		return nil, ErrParams
	}
	return out, nil
}

func affBlock(p *Params, rows, cols, n int) int {
	switch p.Gran {
	case GTensor:
		return n
	case GRow:
		return cols
	}
	return int(p.Block)
}

// MakeResidual converts (target, prediction) to residual words (same width as target).
func MakeResidual(res Residual, w int, target, pred []byte) []byte {
	out := make([]byte, len(target))
	switch w {
	case 4:
		for i := 0; i+4 <= len(target); i += 4 {
			t, p := binary.LittleEndian.Uint32(target[i:]), binary.LittleEndian.Uint32(pred[i:])
			var r uint32
			switch res {
			case RKey:
				r = zigzag4(key4(t) - key4(p))
			case RXor:
				r = t ^ p
			default:
				r = zigzag4(t - p)
			}
			binary.LittleEndian.PutUint32(out[i:], r)
		}
	case 2:
		for i := 0; i+2 <= len(target); i += 2 {
			t, p := binary.LittleEndian.Uint16(target[i:]), binary.LittleEndian.Uint16(pred[i:])
			var r uint16
			switch res {
			case RKey:
				r = zigzag2(key2(t) - key2(p))
			case RXor:
				r = t ^ p
			default:
				r = zigzag2(t - p)
			}
			binary.LittleEndian.PutUint16(out[i:], r)
		}
	default:
		for i := range target {
			t, p := uint32(target[i]), uint32(pred[i])
			var r uint32
			switch res {
			case RKey:
				r = Zigzag((Key(t, 1)-Key(p, 1))&0xff, 1)
			case RXor:
				r = t ^ p
			default:
				r = Zigzag((t-p)&0xff, 1)
			}
			out[i] = byte(r)
		}
	}
	return out
}

// ApplyResidual inverts MakeResidual.
func ApplyResidual(res Residual, w int, resid, pred []byte) []byte {
	out := make([]byte, len(resid))
	switch w {
	case 4:
		for i := 0; i+4 <= len(resid); i += 4 {
			r, p := binary.LittleEndian.Uint32(resid[i:]), binary.LittleEndian.Uint32(pred[i:])
			var t uint32
			switch res {
			case RKey:
				t = unkey4(key4(p) + unzigzag4(r))
			case RXor:
				t = r ^ p
			default:
				t = p + unzigzag4(r)
			}
			binary.LittleEndian.PutUint32(out[i:], t)
		}
	case 2:
		for i := 0; i+2 <= len(resid); i += 2 {
			r, p := binary.LittleEndian.Uint16(resid[i:]), binary.LittleEndian.Uint16(pred[i:])
			var t uint16
			switch res {
			case RKey:
				t = unkey2(key2(p) + unzigzag2(r))
			case RXor:
				t = r ^ p
			default:
				t = p + unzigzag2(r)
			}
			binary.LittleEndian.PutUint16(out[i:], t)
		}
	default:
		for i := range resid {
			r, p := uint32(resid[i]), uint32(pred[i])
			var t uint32
			switch res {
			case RKey:
				t = Unkey((Key(p, 1)+Unzigzag(r, 1))&0xff, 1)
			case RXor:
				t = r ^ p
			default:
				t = (p + Unzigzag(r, 1)) & 0xff
			}
			out[i] = byte(t)
		}
	}
	return out
}

func key4(b uint32) uint32 {
	if b&0x80000000 != 0 {
		return ^b
	}
	return b | 0x80000000
}
func unkey4(k uint32) uint32 {
	if k&0x80000000 != 0 {
		return k &^ 0x80000000
	}
	return ^k
}
func key2(b uint16) uint16 {
	if b&0x8000 != 0 {
		return ^b
	}
	return b | 0x8000
}
func unkey2(k uint16) uint16 {
	if k&0x8000 != 0 {
		return k &^ 0x8000
	}
	return ^k
}
func zigzag4(d uint32) uint32   { x := int32(d); return uint32(x<<1) ^ uint32(x>>31) }
func unzigzag4(z uint32) uint32 { return (z >> 1) ^ -(z & 1) }
func zigzag2(d uint16) uint16   { x := int16(d); return uint16(x<<1) ^ uint16(x>>15) }
func unzigzag2(z uint16) uint16 { return (z >> 1) ^ -(z & 1) }
