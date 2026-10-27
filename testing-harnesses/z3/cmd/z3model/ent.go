package main

import (
	"fmt"
	"time"

	"zeros3-z3/modelz3"
	"zeros3-z3/tdelta"
)

// runEntropy compares, per ggml type, the DEFLATE size of the XOR-planes residual with its
// order-0 per-plane entropy (a lower bound for a byte-wise static coder) and with PXR run
// byte-wise (w=1, the Z3-10 adaptive range coder) on the same exact XOR.
func runEntropy(s *store, a, b string) {
	fa, fb := s.get(a), s.get(b)
	la, lb := layoutOf(fa, "gguf"), layoutOf(fb, "gguf")
	pairs, _ := modelz3.Match(la, lb)
	type acc struct {
		n, defl, ent, pxr int64
		tPXR              time.Duration
	}
	by := map[string]*acc{}
	for _, p := range pairs {
		t, bs := p.Target, p.Base
		tb, bb := make([]byte, t.Size()), make([]byte, t.Size())
		check(readAtFull(fb, tb, t.Begin))
		check(readAtFull(fa, bb, bs.Begin))
		x := modelz3.XOR(nil, tb, bb)
		pl := modelz3.Planes(nil, x, t.Width)
		d := by[t.Type]
		if d == nil {
			d = &acc{}
			by[t.Type] = d
		}
		d.n += t.Size()
		d.defl += modelz3.PackedLen(pl)
		n := len(pl) / t.Width
		var ent float64
		for k := 0; k < t.Width; k++ {
			ent += tdelta.Entropy(pl[k*n:(k+1)*n]) / 8 * float64(n)
		}
		d.ent += int64(ent)
		if t.Size() < 40<<20 {
			t0 := time.Now()
			enc, err := tdelta.EncodePXR(bb, tb, 1)
			check(err)
			d.tPXR += time.Since(t0)
			d.pxr += int64(len(enc))
		} else {
			d.pxr += t.Size() // skipped (very large tensor)
		}
	}
	fmt.Printf("%s > %s\n%-8s %12s %8s %8s %8s\n", a, b, "type", "bytes", "deflate", "H0/plane", "PXR w=1")
	for t, d := range by {
		fmt.Printf("%-8s %12d %7.1f%% %7.1f%% %7.1f%%  (PXR enc %.1f MiB/s)\n", t, d.n, pct(d.defl, d.n), pct(d.ent, d.n), pct(d.pxr, d.n), float64(d.n)/1048576/d.tPXR.Seconds())
	}
}
