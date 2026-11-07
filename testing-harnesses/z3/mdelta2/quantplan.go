package mdelta2

import (
	"bytes"
	"encoding/binary"
)

// Quantized-lane operations (stored in Seg.Q, first byte = op).
const (
	QReproduce = 1 // quantize a float base tensor with a ggml port; XOR residual patches mismatches
	QFields    = 2 // no base: block fields split into streams, each coded separately
	QFieldXor  = 3 // same-type base: field-wise XOR residual
	QRescale8  = 4 // Q8_0 base: scale residual + rescaled-quant residual
)

// PermHint tells QReproduce how the converter reordered a source tensor (0 none, 1 llama q/k).
type PermHint struct {
	Kind   byte
	Heads  int
	ViaF16 bool
}

func applyPerm(x []float32, rows, cols int, h PermHint) []float32 {
	if h.Kind != 1 || h.Heads <= 0 || rows%(2*h.Heads) != 0 {
		return x
	}
	out := make([]float32, len(x))
	hd := rows / h.Heads / 2
	for hh := 0; hh < h.Heads; hh++ {
		for a := 0; a < 2; a++ {
			for b := 0; b < hd; b++ {
				copy(out[(hh*2*hd+b*2+a)*cols:], x[(hh*2*hd+a*hd+b)*cols:(hh*2*hd+a*hd+b+1)*cols])
			}
		}
	}
	return out
}

func putBlob(b []byte, x []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(x)))
	return append(b, x...)
}

type blobR struct {
	b   []byte
	err error
}

func (r *blobR) blob() []byte {
	n, k := binary.Uvarint(r.b)
	if k <= 0 || n > uint64(len(r.b)-k) {
		r.err = ErrCorrupt
		return nil
	}
	v := r.b[k : k+int(n)]
	r.b = r.b[k+int(n):]
	return v
}

func bestFlateNRC(data []byte) (byte, []byte) {
	zero := make([]byte, len(data))
	a, _ := EncodeResidual(CFlate, DRaw, RXor, data, zero)
	b, _ := EncodeResidual(CNRC, DRaw, RXor, data, zero)
	if len(b) < len(a) {
		return CNRC, b
	}
	return CFlate, a
}

func decodeBytes(c byte, payload []byte, n int) ([]byte, error) {
	return DecodeResidual(int(c), DRaw, RXor, payload, make([]byte, n), n)
}

// encodeFieldStreams codes a list of equally-sized-per-block field streams.
func encodeFieldStreams(fs [][]byte) []byte {
	var out []byte
	for _, f := range fs {
		c, p := bestFlateNRC(f)
		out = append(out, c)
		out = putBlob(out, p)
	}
	return out
}

func decodeFieldStreams(r *blobR, sizes []int) ([][]byte, error) {
	var out [][]byte
	for _, n := range sizes {
		if len(r.b) < 1 {
			return nil, ErrCorrupt
		}
		c := r.b[0]
		r.b = r.b[1:]
		p := r.blob()
		if r.err != nil {
			return nil, r.err
		}
		d, err := decodeBytes(c, p, n)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func fieldSizes(d DType, nb int) []int {
	g := qgeoms[d]
	s := make([]int, len(g.fields))
	for i, f := range g.fields {
		s[i] = nb * f.n
	}
	return s
}

func xorBytes(a, b []byte) []byte {
	o := make([]byte, len(a))
	for i := range o {
		o[i] = a[i] ^ b[i]
	}
	return o
}

// quantCandidates returns encoded Q streams for a quantized target tensor.
func quantCandidates(dt DType, target []byte, floatBase *Base, hint PermHint, shape []int64, sameBase []byte) map[string][]byte {
	g := qgeoms[dt]
	nb := len(target) / g.bytes
	out := map[string][]byte{}
	out["fields"] = append([]byte{QFields}, encodeFieldStreams(SplitFields(dt, target))...)
	if floatBase != nil {
		n := len(target) / g.bytes * g.elems
		x := FloatsOf(floatBase.DT, floatBase.Data, hint.ViaF16)
		if len(x) == n {
			rows, cols := 1, n
			if len(shape) > 0 {
				cols = int(shape[len(shape)-1])
				rows = n / cols
			}
			x = applyPerm(x, rows, cols, hint)
			if pred, ok := QuantizeAny(dt, x); ok && len(pred) == len(target) {
				res := xorBytes(target, pred)
				c, p := bestFlateNRC(res)
				q := []byte{QReproduce, hint.Kind}
				q = binary.AppendUvarint(q, uint64(hint.Heads))
				if hint.ViaF16 {
					q = append(q, 1)
				} else {
					q = append(q, 0)
				}
				q = append(q, c)
				out["repro"] = putBlob(q, p)
			}
		}
	}
	if sameBase != nil && len(sameBase) == len(target) {
		bf, tf := SplitFields(dt, sameBase), SplitFields(dt, target)
		x := make([][]byte, len(bf))
		for i := range bf {
			x[i] = xorBytes(tf[i], bf[i])
		}
		out["fieldxor"] = append([]byte{QFieldXor}, encodeFieldStreams(x)...)
		if dt == DQ8_0 {
			// stage 1: scale residual vs base scale; stage 2: quant residual vs rescaled prediction
			ts, bs := tf[0], bf[0]
			rs := xorBytes(ts, bs)
			pred := Q8Rescale(sameBase, ts)
			rq := make([]byte, len(pred))
			for i := range rq {
				rq[i] = byte(zigzag(uint32(int8(tf[1][i])-int8(pred[i]))&0xff, 1))
			}
			q := []byte{QRescale8}
			q = append(q, encodeFieldStreams([][]byte{rs, rq})...)
			out["rescale8"] = q
		}
	}
	_ = nb
	return out
}

func (c *Container) decodeQuant(t *TRec, s *Seg, rv Resolver) ([]byte, error) {
	dt := t.DType
	g, ok := qgeoms[dt]
	if !ok || len(s.Q) < 1 || len(s.Refs) > 1 {
		return nil, ErrFormat
	}
	nb := int(s.Elems) / g.bytes
	if int64(nb*g.bytes) != s.Elems {
		return nil, ErrFormat
	}
	r := &blobR{b: s.Q[1:]}
	switch s.Q[0] {
	case QFields:
		fs, err := decodeFieldStreams(r, fieldSizes(dt, nb))
		if err != nil || len(r.b) != 0 {
			return nil, ErrCorrupt
		}
		return JoinFields(dt, fs, nb), nil
	case QFieldXor, QRescale8, QReproduce:
		if len(s.Refs) != 1 {
			return nil, ErrFormat
		}
		bv, err := c.baseViewRaw(s, 0, rv)
		if err != nil {
			return nil, err
		}
		switch s.Q[0] {
		case QFieldXor:
			if bv.DT != dt || len(bv.Data) != nb*g.bytes {
				return nil, ErrIdentity
			}
			xs, err := decodeFieldStreams(r, fieldSizes(dt, nb))
			if err != nil || len(r.b) != 0 {
				return nil, ErrCorrupt
			}
			bf := SplitFields(dt, bv.Data)
			for i := range xs {
				xs[i] = xorBytes(xs[i], bf[i])
			}
			return JoinFields(dt, xs, nb), nil
		case QRescale8:
			if dt != DQ8_0 || bv.DT != dt || len(bv.Data) != nb*34 {
				return nil, ErrIdentity
			}
			xs, err := decodeFieldStreams(r, []int{nb * 2, nb * 32})
			if err != nil || len(r.b) != 0 {
				return nil, ErrCorrupt
			}
			bf := SplitFields(dt, bv.Data)
			ts := xorBytes(xs[0], bf[0])
			pred := Q8Rescale(bv.Data, ts)
			qs := make([]byte, len(pred))
			for i := range qs {
				qs[i] = byte(int8(pred[i]) + int8(unzigzag(uint32(xs[1][i]), 1)))
			}
			return JoinFields(dt, [][]byte{ts, qs}, nb), nil
		default: // QReproduce
			if len(r.b) < 3 {
				return nil, ErrCorrupt
			}
			h := PermHint{Kind: r.b[0]}
			hv, k := binary.Uvarint(r.b[1:])
			if k <= 0 || hv > 1<<16 || len(r.b) < 1+k+2 {
				return nil, ErrCorrupt
			}
			h.Heads, h.ViaF16 = int(hv), r.b[1+k] == 1
			cod := r.b[2+k]
			r.b = r.b[3+k:]
			p := r.blob()
			if r.err != nil || len(r.b) != 0 {
				return nil, ErrCorrupt
			}
			n := nb * g.elems
			x := FloatsOf(bv.DT, bv.Data, h.ViaF16)
			if len(x) != n {
				return nil, ErrIdentity
			}
			cols := n
			if len(t.Shape) > 0 && t.Shape[len(t.Shape)-1] > 0 {
				cols = int(t.Shape[len(t.Shape)-1])
			}
			x = applyPerm(x, n/cols, cols, h)
			pred, ok := QuantizeAny(dt, x)
			if !ok || len(pred) != nb*g.bytes {
				return nil, ErrFormat
			}
			res, err := decodeBytes(cod, p, len(pred))
			if err != nil {
				return nil, err
			}
			return xorBytes(res, pred), nil
		}
	}
	return nil, ErrFormat
}

// baseViewRaw returns the whole referenced base tensor (no element-offset slicing) with tag check.
func (c *Container) baseViewRaw(s *Seg, k int, rv Resolver) (Base, error) {
	r := s.Refs[k]
	dt, data, err := rv.Tensor(c.Bases[r.Slot].SHA, int(r.TIdx))
	if err != nil {
		return Base{}, err
	}
	if tag(data) != r.Tag {
		return Base{}, ErrIdentity
	}
	return Base{DT: dt, Data: data}, nil
}

var _ = bytes.Equal
