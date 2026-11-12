package qcore

import (
	"encoding/binary"
	"errors"
	"math"
)

// IMatrix is llama.cpp's legacy importance-matrix file (llama-imatrix output), as
// llama-quantize loads it: per tensor name, the activation sums divided by the call count.
type IMatrix struct {
	W map[string][]float32
}

var ErrIMatrix = errors.New("qcore: malformed imatrix")

const (
	maxIMEntries = 1 << 20
	maxIMName    = 1 << 12
)

// ParseIMatrix decodes the binary format (n, then len/name/ncall/nval/f32[nval] per entry; an
// optional trailer is ignored). Non-finite weights are rejected as llama-quantize does.
func ParseIMatrix(b []byte) (*IMatrix, error) {
	p := 0
	i32 := func() (int, bool) {
		if len(b)-p < 4 {
			return 0, false
		}
		v := int32(binary.LittleEndian.Uint32(b[p:]))
		p += 4
		return int(v), true
	}
	n, ok := i32()
	if !ok || n < 1 || n > maxIMEntries {
		return nil, ErrIMatrix
	}
	im := &IMatrix{W: make(map[string][]float32, n)}
	for i := 0; i < n; i++ {
		l, ok := i32()
		if !ok || l < 0 || l > maxIMName || len(b)-p < l {
			return nil, ErrIMatrix
		}
		name := string(b[p : p+l])
		p += l
		ncall, ok1 := i32()
		nval, ok2 := i32()
		if !ok1 || !ok2 || nval < 1 || (len(b)-p)/4 < nval {
			return nil, ErrIMatrix
		}
		w := make([]float32, nval)
		for j := range w {
			v := math.Float32frombits(binary.LittleEndian.Uint32(b[p+4*j:]))
			if ncall > 0 {
				v /= float32(ncall)
			}
			if v != v || v > math.MaxFloat32 || v < -math.MaxFloat32 {
				return nil, ErrIMatrix
			}
			w[j] = v
		}
		p += 4 * nval
		im.W[name] = w
	}
	return im, nil
}

// For returns the importance vector llama-quantize uses for a 2-D tensor with nPerRow columns
// (nil when the tensor has no entry or the size differs: the reference quantizer applies).
func (im *IMatrix) For(name string, nPerRow int) []float32 {
	if im == nil {
		return nil
	}
	if w := im.W[name]; len(w) == nPerRow {
		return w
	}
	return nil
}
