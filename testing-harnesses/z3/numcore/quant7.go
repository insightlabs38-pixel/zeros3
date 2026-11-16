package numcore

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"

	"zeros3-z3/qcore"
)

func qBlock(q qcore.Type) (elems, bytes int) { return qcore.Block(q) }

func mustQ(d DType) qcore.Type { q, _ := d.QType(); return q }

// floatsOf converts base element bytes to the float32 quantizer input (exact widening); viaF16
// first rounds through float16, the value an F16 intermediate file would hold.
func floatsOf(b Base, viaF16 bool) []float32 {
	n := b.n()
	out := make([]float32, n)
	switch b.DT {
	case DF32:
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b.Data[4*i:]))
		}
	case DBF16:
		for i := range out {
			out[i] = math.Float32frombits(uint32(binary.LittleEndian.Uint16(b.Data[2*i:])) << 16)
		}
	default:
		for i := range out {
			out[i] = qcore.F16to32(binary.LittleEndian.Uint16(b.Data[2*i:]))
		}
		return out
	}
	if viaF16 {
		for i, v := range out {
			out[i] = qcore.F16to32(qcore.F16(v))
		}
	}
	return out
}

// quantWindow quantizes the base rows of a window with the recorded producer profile.
func quantWindow(ctx context.Context, t *TDir, k int64, nbytes int, base baseFn, qw []float32) ([]byte, error) {
	q := mustQ(t.DT)
	el, by := qBlock(q)
	nb := nbytes / by
	b, err := base(ctx, k*t.Win*int64(el), nb*el)
	if err != nil {
		return nil, err
	}
	if !b.DT.IsFloat() || b.n() != nb*el {
		return nil, ErrFormat7
	}
	out := make([]byte, nbytes)
	if err := qcore.Quantize(out, q, floatsOf(b, t.Q.ViaF16), int(t.Cols), qw, qcore.Arith(t.Q.Arith)); err != nil {
		return nil, ErrFormat7
	}
	return out, nil
}

// Patch stream: uvarint count, then per patch a uvarint block gap and the raw block. The stream is
// stored or DEFLATE-compressed (WDir.Coder).
func encodePatches(want, got []byte, by int) (payload []byte, coder uint8, n int) {
	var p []byte
	last := 0
	for b := 0; b*by < len(want); b++ {
		if !bytes.Equal(want[b*by:(b+1)*by], got[b*by:(b+1)*by]) {
			p = binary.AppendUvarint(p, uint64(b-last))
			p = append(p, want[b*by:(b+1)*by]...)
			last = b
			n++
		}
	}
	if n == 0 {
		return nil, CStore, 0
	}
	p = append(binary.AppendUvarint(nil, uint64(n)), p...)
	if z := deflateBytes(p); len(z)+4 < len(p) {
		return append(binary.AppendUvarint(nil, uint64(len(p))), z...), CFlate, n
	}
	return p, CStore, n
}

func applyPatches(dst, payload []byte, coder uint8, by int) error {
	if len(payload) == 0 {
		return nil
	}
	p := payload
	if coder == CFlate {
		ln, k := binary.Uvarint(payload)
		if k <= 0 || ln > uint64(len(dst))+uint64(len(dst)/by+1)*10+10 {
			return ErrFormat7
		}
		var err error
		if p, err = inflateExact(payload[k:], int(ln)); err != nil {
			return err
		}
	}
	cnt, k := binary.Uvarint(p)
	if k <= 0 || cnt == 0 || cnt > uint64(len(dst)/by) {
		return ErrFormat7
	}
	p = p[k:]
	blk := uint64(0)
	for i := uint64(0); i < cnt; i++ {
		gap, k := binary.Uvarint(p)
		if k <= 0 || (i > 0 && gap == 0) || gap > uint64(len(dst)/by) {
			return ErrFormat7
		}
		blk += gap
		p = p[k:]
		if blk >= uint64(len(dst)/by) || len(p) < by {
			return ErrFormat7
		}
		copy(dst[int(blk)*by:], p[:by])
		p = p[by:]
	}
	if len(p) != 0 {
		return ErrFormat7
	}
	return nil
}

func rebuildQuant(ctx context.Context, t *TDir, wd *WDir, k int64, payload, dst []byte, base baseFn, aux auxFn) error {
	var qw []float32
	if t.Q.Aux >= 0 {
		var err error
		if qw, err = aux(ctx, t.Q.Aux, t.Q.Name, int(t.Cols)); err != nil {
			return err
		}
		if int64(len(qw)) != t.Cols {
			return ErrMissing7
		}
	}
	out, err := quantWindow(ctx, t, k, len(dst), base, qw)
	if err != nil {
		return err
	}
	_, by := qBlock(mustQ(t.DT))
	if err := applyPatches(out, payload, wd.Coder, by); err != nil {
		return err
	}
	copy(dst, out)
	return nil
}
