package clrz3

import (
	"bytes"
	"testing"

	"zeros3-z3/idxref"
)

// FuzzParse: the structural parser and everything it feeds (slots, heaps, marks, bodies, the
// representation) must reject or represent any input without panicking; whatever is represented
// must decode to the input.
func FuzzParse(f *testing.F) {
	f.Add(build(spec{types: 2, methods: 3, fields: 1, attrs: 4}))
	f.Add(build(spec{types: 3, methods: 2, fat: true}))
	f.Add(build(spec{types: 1}))
	f.Fuzz(func(t *testing.T, d []byte) {
		fl, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
		if err != nil {
			return
		}
		TableSlots(fl, d)
		hv, err := NewHeapView(fl, d)
		if err != nil {
			return
		}
		marks := DeriveMarks(fl, d, hv)
		bodies := FindBodies(fl, d, true)
		ValidateBodies(fl, d, hv, bodies)
		BlobSlots(fl, d, hv.Blobs, marks)
		ILSlots(d, bodies)
		rep, err := Encode(fl, d, Config{Auto: 12, Cut: cutEvery(900)})
		if err != nil {
			return
		}
		got, err := Decode(rep.Meta, rep.Comps)
		if err != nil || !bytes.Equal(got, d) {
			t.Fatalf("represented but does not decode: %v", err)
		}
	})
}

// FuzzDecode: arbitrary bytes presented as a representation must fail cleanly.
func FuzzDecode(f *testing.F) {
	d := build(spec{types: 3, methods: 3, fields: 1, attrs: 3})
	fl, _ := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
	rep, err := Encode(fl, d, Config{Auto: 12, Cut: cutEvery(1200)})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(rep.Meta, rep.Comps[0], 0)
	f.Add(rep.Meta, rep.Comps[len(rep.Comps)-1], len(rep.Comps)-1)
	f.Fuzz(func(t *testing.T, meta, comp []byte, at int) {
		comps := append([][]byte(nil), rep.Comps...)
		if at >= 0 && at < len(comps) {
			comps[at] = comp
		}
		Decode(meta, comps)
		Decode(rep.Meta, comps)
		pl, ss, err := idxref.SplitComp(comp, NumStreams)
		if err == nil && at >= 0 && at < len(rep.Comps) {
			comps[at] = idxref.PackComp(pl, ss)
			Decode(rep.Meta, comps)
		}
	})
}
