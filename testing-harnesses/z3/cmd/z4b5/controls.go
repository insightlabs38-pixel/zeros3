package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/mdelta2"
	"zeros3-z3/st"
)

// cmdControls writes SYNTHETIC_CONTROL edits of a real BF16 model: lora (rank-8 dense update),
// rows (3% of rows changed), scale (0.97x on a layer range), dense-tiny (sub-ulp noise on all
// linear weights). Controls test mechanisms; they are never natural fine-tuning evidence.
// usage: controls SOURCE.safetensors OUTDIR
func cmdControls(args []string) {
	m, err := mdelta2.Open(args[0])
	must(err)
	out := args[1]
	must(os.MkdirAll(out, 0o755))
	type edit func(name string, rows, cols int, x []float32, rng *rand.Rand) []float32
	isLinear := func(n string) bool { return strings.Contains(n, "_proj.weight") }
	layerOf := func(n string) int {
		var l int
		if _, e := fmt.Sscanf(n, "model.layers.%d.", &l); e != nil {
			return -1
		}
		return l
	}
	rms := func(x []float32) float64 {
		var s float64
		for _, v := range x {
			s += float64(v) * float64(v)
		}
		return math.Sqrt(s / float64(len(x)))
	}
	edits := map[string]edit{
		"lora": func(n string, rows, cols int, x []float32, rng *rand.Rand) []float32 {
			if !isLinear(n) {
				return nil
			}
			r := 8
			B := make([]float64, rows*r)
			A := make([]float64, r*cols)
			for i := range B {
				B[i] = rng.NormFloat64()
			}
			for i := range A {
				A[i] = rng.NormFloat64()
			}
			y := make([]float32, len(x))
			sc := 0.005 * rms(x) / math.Sqrt(float64(r))
			for i := 0; i < rows; i++ {
				for j := 0; j < cols; j++ {
					var d float64
					for k := 0; k < r; k++ {
						d += B[i*r+k] * A[k*cols+j]
					}
					y[i*cols+j] = x[i*cols+j] + float32(sc*d)
				}
			}
			return y
		},
		"rows": func(n string, rows, cols int, x []float32, rng *rand.Rand) []float32 {
			if !isLinear(n) {
				return nil
			}
			y := append([]float32(nil), x...)
			s := 0.02 * rms(x)
			for i := 0; i < rows; i++ {
				if rng.Float64() < 0.03 {
					for j := 0; j < cols; j++ {
						y[i*cols+j] += float32(s * rng.NormFloat64())
					}
				}
			}
			return y
		},
		"scale": func(n string, rows, cols int, x []float32, rng *rand.Rand) []float32 {
			if l := layerOf(n); l < 10 || l > 19 {
				return nil
			}
			y := make([]float32, len(x))
			for i, v := range x {
				y[i] = 0.97 * v
			}
			return y
		},
		"dense-tiny": func(n string, rows, cols int, x []float32, rng *rand.Rand) []float32 {
			if !isLinear(n) {
				return nil
			}
			y := make([]float32, len(x))
			s := 0.001 * rms(x)
			for i, v := range x {
				y[i] = v + float32(s*rng.NormFloat64())
			}
			return y
		},
	}
	for name, ed := range edits {
		rng := rand.New(rand.NewSource(20260401))
		var ts []st.BuildTensor
		changed := 0
		for i := range m.Tensors {
			t := &m.Tensors[i]
			d, err := m.Read(i)
			must(err)
			nd := d
			if t.DType == mdelta2.DBF16 && len(t.Shape) == 2 {
				x := mdelta2.FloatsOf(t.DType, d, false)
				if y := ed(t.Name, int(t.Shape[0]), int(t.Shape[1]), x, rng); y != nil {
					nd = make([]byte, len(d))
					for k, v := range y {
						b := mdelta2.BF16Bits(float64(v))
						nd[2*k], nd[2*k+1] = byte(b), byte(b>>8)
					}
					changed++
				}
			}
			ts = append(ts, st.BuildTensor{Name: t.Name, DType: "BF16", Shape: t.Shape, Data: nd})
		}
		must(os.WriteFile(filepath.Join(out, name+".safetensors"), st.Build(nil, ts), 0o644))
		fmt.Println(name, "tensors edited:", changed)
	}
}
