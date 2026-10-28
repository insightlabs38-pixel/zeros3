package wasmz3

import (
	"bytes"
	"testing"

	"zeros3-z3/idxref"
)

// FuzzParse: parser, walker and representation must reject or represent any input without
// panicking; whatever is represented must decode to the input.
func FuzzParse(f *testing.F) {
	f.Add(full(1))
	f.Add(full(5))
	f.Add(cat(header, sec(SecType, vec(ft("", ""))), sec(SecFunction, vec(uleb(0))), sec(SecCode, vec(body([]byte{0x10, 0})))))
	f.Fuzz(func(t *testing.T, d []byte) {
		dir, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
		if err != nil {
			return
		}
		m, err := Analyze(dir, d, Limits{})
		if err != nil {
			return
		}
		RawSecs(m, d)
		Categorize(m, d)
		rep, err := Encode(m, d, Config{Auto: 12, Cut: cutEvery(30)})
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
	d := full(2)
	dir, _ := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
	m, _ := Analyze(dir, d, Limits{})
	rep, err := Encode(m, d, Config{Auto: 12, Cut: cutEvery(50)})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(rep.Meta, rep.Comps[0], 0)
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
