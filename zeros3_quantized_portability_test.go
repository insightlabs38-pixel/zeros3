package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
)

func TestZN7EncoderStoresNonportableWindowsAsBytes(t *testing.T) {
	const columns = 256
	for _, tc := range []struct {
		name       string
		values     []float32
		optional   []float32
		wantOpaque bool
	}{
		{"ordinary", []float32{0.125, -0.3}, nil, false},
		{"nonfinite values", []float32{0.125, float32(math.Inf(1))}, nil, true},
		{"nonfinite imatrix", []float32{0.125, -0.3}, []float32{float32(math.NaN())}, true},
		{"nonfinite derived scale", []float32{1e30, -1e30}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, columns*4)
			weights := []float32(nil)
			if tc.optional != nil {
				weights = make([]float32, columns)
			}
			for i := range columns {
				val := tc.values[i%len(tc.values)]
				binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(val))
				if weights != nil {
					weights[i] = tc.optional[i%len(tc.optional)]
				}
			}
			n := int64(len(data))
			base := ncNumcoreRawSource{
				R: bytes.NewReader(data),
				L: &ncNumcoreLayout{Size: n, T: []ncNumcoreTensor{{DT: ncNumcoreDF32, Len: n, Cols: columns}}},
			}
			el, width := ncNumcore_qBlock(ncQcoreIQ4_NL)
			w := int64(columns / el)
			want := make([]byte, w*int64(width))
			for i := range want {
				want[i] = byte(i*7 + 13)
			}
			shape := &ncNumcoreTDir{DT: ncNumcoreDIQ4_NL, Len: int64(len(want)), Cols: columns,
				Win: w, Ref: &ncNumcoreRef{}, Q: &ncNumcoreQParams{Aux: -1}}
			plan := &ncNumcore_tplan{src: base, qw: weights}
			e := ncNumcore_encoder{ctx: context.Background(), st: &ncNumcoreEncStats{}}
			wd, stored, err := e.codeWindow(shape, plan, 0, want, false)
			mustOK(t, err)
			if !wd.valid(shape) || (wd.Mode == ncNumcoreMRaw) != tc.wantOpaque {
				t.Fatalf("window mode %d, want opaque %t", wd.Mode, tc.wantOpaque)
			}
			if tc.wantOpaque {
				rebuilt := make([]byte, len(want))
				mustOK(t, ncNumcore_rebuild(e.ctx, shape, &wd, 0, stored, rebuilt, nil, nil))
				if !bytes.Equal(rebuilt, want) {
					t.Fatal("raw fallback changed the user bytes")
				}
			}
		})
	}
}
