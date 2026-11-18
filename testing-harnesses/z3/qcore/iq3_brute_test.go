package qcore

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"testing"
)

func TestIQ3Brute(t *testing.T) {
	dir := os.Getenv("IQ3_ORACLES")
	if dir == "" {
		t.Skip("IQ3_ORACLES")
	}
	r := rand.New(rand.NewSource(7))
	const npr = 512
	for _, name := range []string{"o3-avx2-fma", "o3-avx2-nocontract"} {
		for _, weighted := range []bool{true, false} {
			x := make([]float32, 1500*npr)
			for i := range x {
				s := float32(math.Exp(r.NormFloat64()))
				x[i] = float32(r.NormFloat64()) * s * 0.02
				if r.Intn(40) == 0 {
					x[i] = 0
				}
			}
			var qw []float32
			if weighted {
				qw = make([]float32, npr)
				for i := range qw {
					qw[i] = float32(math.Exp(2 * r.NormFloat64()))
				}
			}
			want := runOracle(t, dir+"/qoracle-"+name, vec{t: IQ3_S, npr: npr, x: x, qw: qw})
			var good []uint32
			for _, fx := range append([]uint32{0, iq3FMA}, func() []uint32 {
				var v []uint32
				for b := uint32(1); b <= fx3All; b <<= 1 {
					v = append(v, iq3FMA^b)
				}
				return v
			}()...) {
				got := make([]byte, len(want))
				var st iq3Scratch
				for r := 0; r*npr < len(x); r++ {
					rowIQ3S(&st, fx, x[r*npr:(r+1)*npr], got[r*(npr/256)*110:(r+1)*(npr/256)*110], qw)
				}
				if bytes.Equal(got, want) {
					good = append(good, fx)
				}
			}
			t.Logf("%s weighted=%v matching masks: %v", name, weighted, good)
		}
	}
}
