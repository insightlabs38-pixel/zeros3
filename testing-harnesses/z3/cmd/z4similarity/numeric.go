package main

import (
	"bytes"
	"fmt"
	"os"
	"zeros3-z3/simdelta"
	"zeros3-z3/tdelta"
)

// Paired aligned offsets are an optimistic discovery oracle, NOT a deployable
// index or a claim that arbitrary CDC chunks have this alignment.
func numericOracle(f Family) {
	if f.Name != "model-pythia" {
		return
	}
	for rev := 1; rev < len(f.Paths); rev++ {
		a, e := os.ReadFile(f.Paths[rev-1])
		must(e)
		b, e := os.ReadFile(f.Paths[rev])
		must(e)
		size := len(a)
		if len(b) < size {
			size = len(b)
		}
		totals := map[string]int64{}
		count := 0
		for sample := 0; sample < 32; sample++ {
			off := sample * (size - 65536) / 31
			off -= off % 4
			base, target := a[off:off+65536], b[off:off+65536]
			count++
			direct, e := simdelta.Encode("direct", nil, target)
			must(e)
			totals["direct"] += int64(len(direct) + 96)
			for _, c := range []tdelta.Codec{tdelta.XORZ, tdelta.Plane, tdelta.FloatSplit, tdelta.ByteDelta} {
				p, e := tdelta.EncodeFor(c, "F32", base, target, 4)
				must(e)
				out, e := tdelta.DecodeFor(c, "F32", base, p, len(target), 4)
				must(e)
				if !bytes.Equal(out, target) {
					panic("numeric exact")
				}
				totals[c.String()] += int64(len(p) + 74 + 96)
			}
			p, e := tdelta.EncodePXR(base, target, 4)
			must(e)
			out, e := tdelta.DecodePXR(base, p, len(target), 4)
			must(e)
			if !bytes.Equal(out, target) {
				panic("pxr exact")
			}
			totals["pxr"] += int64(len(p) + 74 + 96)
			p, e = tdelta.EncodePXR(nil, target, 4)
			must(e)
			out, e = tdelta.DecodePXR(nil, p, len(target), 4)
			must(e)
			if !bytes.Equal(out, target) {
				panic("standalone predictor")
			}
			totals["predict-no-base"] += int64(len(p) + 41 + 96)
		}
		emit(map[string]any{"family": f.Name, "revision": rev, "oracle": "same-file-offset-alignment", "samples": count, "totals": totals})
		fmt.Fprintln(os.Stderr, "numeric oracle", rev, "done")
	}
}
