package clrz3

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/econx"
	"zeros3-z3/idxref"
)

func parse(t *testing.T, d []byte) *File {
	t.Helper()
	f, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

// cut4 groups the payload at fixed offsets so small fixtures produce several components.
func cutEvery(n int) func([]byte, []idxref.Sec) []int {
	return func(p []byte, _ []idxref.Sec) []int {
		var c []int
		for o := n; o < len(p); o += n {
			c = append(c, o)
		}
		return c
	}
}

func roundTrip(t *testing.T, d []byte, cfg Config) *Rep {
	t.Helper()
	f := parse(t, d)
	rep, err := Encode(f, d, cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// serialize and drop all analysis state: only Meta and the component bytes survive
	meta := append([]byte(nil), rep.Meta...)
	comps := make([][]byte, len(rep.Comps))
	for i, c := range rep.Comps {
		comps[i] = append([]byte(nil), c...)
	}
	got, err := Decode(meta, comps)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, d) {
		t.Fatalf("decoded %d bytes differ from the %d original", len(got), len(d))
	}
	return rep
}

func TestCodedIndex(t *testing.T) {
	for fam := 0; fam < NumCoded; fam++ {
		for tag, tb := range CodedTables(fam) {
			if tb < 0 {
				continue
			}
			for _, row := range []uint32{1, 2, 77, 1<<16 - 1, 1 << 20} {
				v, ok := EncodeCoded(fam, tb, row)
				if !ok {
					t.Fatalf("family %d table %d row %d does not encode", fam, tb, row)
				}
				if int(v)&(1<<uint(CodedBits(fam))-1) != tag {
					t.Errorf("family %d tag of %s is %d", fam, TableName(tb), v&3)
				}
				gt, gr, ok := DecodeCoded(fam, v)
				if !ok || gt != tb || gr != row {
					t.Errorf("family %d: %s/%d round-trips to %d/%d (%v)", fam, TableName(tb), row, gt, gr, ok)
				}
			}
		}
	}
	if _, _, ok := DecodeCoded(CCustomAttributeType, 0); ok {
		t.Error("CustomAttributeType tag 0 names a table")
	}
}

func TestSyntheticRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		sp   spec
		cfg  Config
	}{
		{"tiny tiny-body", spec{types: 3, methods: 4, fields: 1}, Config{}},
		{"fat bodies", spec{types: 3, methods: 4, fields: 2, fat: true}, Config{K0: 2}},
		{"adaptive prefixes", spec{types: 40, methods: 6, fields: 2, attrs: 100}, Config{Auto: 12, Cut: cutEvery(2000)}},
		{"no methods", spec{types: 5}, Config{}},
		{"one-byte prefixes", spec{types: 6, methods: 2}, Config{K0: 1, Cut: cutEvery(700)}},
		{"four-byte prefixes", spec{types: 6, methods: 2}, Config{K0: 4, Cut: cutEvery(700)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := build(c.sp)
			rep := roundTrip(t, d, c.cfg)
			if c.sp.methods > 0 && rep.Stats.OpaqueBody != 0 {
				t.Errorf("%d opaque bodies", rep.Stats.OpaqueBody)
			}
		})
	}
}

// Width transitions: the metadata tables change width globally when a table or heap crosses a
// 16-bit threshold; the representation must stay byte-exact on both sides and across them.
func TestWidthTransitions(t *testing.T) {
	cases := []struct {
		name         string
		below, above spec
		check        func(t *testing.T, below, above *File)
	}{
		{"HasCustomAttribute at 2048 rows", spec{types: 100, methods: 20, attrs: 30}, spec{types: 105, methods: 20, attrs: 30}, func(t *testing.T, b, a *File) {
			if b.Rows(TMethodDef) >= 2048 || a.Rows(TMethodDef) < 2048 {
				t.Fatalf("MethodDef rows %d / %d do not straddle 2048", b.Rows(TMethodDef), a.Rows(TMethodDef))
			}
			if b.CodedWidth(CHasCustomAttribute) != 2 || a.CodedWidth(CHasCustomAttribute) != 4 {
				t.Errorf("HasCustomAttribute widths %d -> %d", b.CodedWidth(CHasCustomAttribute), a.CodedWidth(CHasCustomAttribute))
			}
		}},
		{"#Strings at 64 KiB", spec{types: 4, methods: 2, padStr: 3400}, spec{types: 4, methods: 2, padStr: 3600}, func(t *testing.T, b, a *File) {
			bs, _, _ := b.HeapWidths()
			as, _, _ := a.HeapWidths()
			if bs != 2 || as != 4 {
				t.Errorf("string index widths %d -> %d (heaps %d, %d)", bs, as, b.Str.Size, a.Str.Size)
			}
		}},
		{"#Blob at 64 KiB", spec{types: 4, methods: 2, padBlob: 3600}, spec{types: 4, methods: 2, padBlob: 4400}, func(t *testing.T, b, a *File) {
			_, _, bb := b.HeapWidths()
			_, _, ab := a.HeapWidths()
			if bb != 2 || ab != 4 {
				t.Errorf("blob index widths %d -> %d (heaps %d, %d)", bb, ab, b.Blob.Size, a.Blob.Size)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, da := build(c.below), build(c.above)
			c.check(t, parse(t, db), parse(t, da))
			cfg := Config{Auto: 12, Cut: cutEvery(4000)}
			roundTrip(t, db, cfg)
			roundTrip(t, da, cfg)
		})
	}
}

// A representation that has been altered must never decode to the original, and must never panic.
func TestTamper(t *testing.T) {
	d := build(spec{types: 12, methods: 5, fields: 2, attrs: 20})
	f := parse(t, d)
	rep, err := Encode(f, d, Config{Auto: 12, Cut: cutEvery(1500)})
	if err != nil {
		t.Fatal(err)
	}
	try := func(name string, meta []byte, comps [][]byte, mustDiffer bool) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: panic %v", name, r)
			}
		}()
		got, err := Decode(meta, comps)
		if mustDiffer && err == nil && bytes.Equal(got, d) {
			t.Errorf("%s: the altered representation decoded to the original", name)
		}
	}
	// a changed reference byte names another target (or none): it can never decode to the original
	for ci, c := range rep.Comps {
		pl, ss, err := idxref.SplitComp(c, NumStreams)
		if err != nil {
			t.Fatal(err)
		}
		for si, s := range ss {
			if len(s) == 0 {
				continue
			}
			for _, off := range []int{0, len(s) / 2, len(s) - 1} {
				mut := make([][]byte, len(ss))
				for k := range ss {
					mut[k] = append([]byte(nil), ss[k]...)
				}
				mut[si][off] ^= 0x5a
				comps := append([][]byte(nil), rep.Comps...)
				comps[ci] = idxref.PackComp(pl, mut)
				try("flip reference byte", rep.Meta, comps, true)
			}
		}
		// payload bytes under a mask are don't-care by construction: only require a clean failure
		for _, off := range []int{0, len(pl) / 2, len(pl) - 1} {
			mut := append([]byte(nil), pl...)
			if len(mut) == 0 {
				continue
			}
			mut[off] ^= 0xa5
			comps := append([][]byte(nil), rep.Comps...)
			comps[ci] = idxref.PackComp(mut, ss)
			try("flip payload byte", rep.Meta, comps, false)
		}
	}
	try("drop component", rep.Meta, rep.Comps[1:], true)
	try("truncated component", rep.Meta, append([][]byte{rep.Comps[0][:len(rep.Comps[0])-1]}, rep.Comps[1:]...), true)
	for n := 0; n < len(rep.Meta); n += 7 {
		try("truncated meta", rep.Meta[:n], rep.Comps, true)
	}
	m := append([]byte(nil), rep.Meta...)
	m[4] = 9
	try("bad K0", m, rep.Comps, true)
	m = append([]byte(nil), rep.Meta...)
	m[len(m)-1] ^= 1
	try("flipped header byte", m, rep.Comps, false)
}

func TestUnsupportedFallsBack(t *testing.T) {
	d := build(spec{types: 3, methods: 2})
	for name, mut := range map[string]func([]byte) []byte{
		"not a PE":  func(b []byte) []byte { return append([]byte("junk"), b[4:]...) },
		"truncated": func(b []byte) []byte { return b[:len(b)/2] },
		"no CLI directory": func(b []byte) []byte {
			c := append([]byte(nil), b...)
			clear(c[0x84+20+96+14*8 : 0x84+20+96+14*8+8])
			return c
		},
		"bad metadata magic": func(b []byte) []byte {
			c := append([]byte(nil), b...)
			i := bytes.Index(c, []byte("BSJB"))
			c[i] = 'X'
			return c
		},
	} {
		_, err := Parse(bytes.NewReader(mut(d)), int64(len(mut(d))), Limits{})
		if err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// a valid assembly whose table stream is the uncompressed form is parsed but not represented
	c := append([]byte(nil), d...)
	i := bytes.Index(c, []byte("#~\x00"))
	c[i+1] = '-'
	f, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{})
	if err != nil {
		t.Fatalf("#- stream: %v", err)
	}
	if _, err := Encode(f, c, Config{}); err == nil {
		t.Error("an uncompressed (#-) table stream was represented")
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Real Roslyn output: exact round trip, and a type inserted first (every row index shifts) shares
// almost every group with the baseline once the references are semantic.
func TestRoslynFixtures(t *testing.T) {
	cfg := Config{Auto: 12, Cut: econx.UnitCut(1).Cut}
	var reps [3]*Rep
	for i, n := range []string{"fixture-r0.dll", "fixture-body.dll", "fixture-earlytype.dll"} {
		d := fixture(t, n)
		f := parse(t, d)
		if f.PE32Plus || f.TabKind != '~' || len(f.Streams) != 5 {
			t.Errorf("%s: unexpected shape (pe32+=%v table stream #%c, %d streams)", n, f.PE32Plus, f.TabKind, len(f.Streams))
		}
		reps[i] = roundTrip(t, d, cfg)
		if reps[i].Stats.OpaqueBody != 0 || reps[i].Stats.Marked == 0 {
			t.Errorf("%s: %d opaque bodies, %d signatures normalized", n, reps[i].Stats.OpaqueBody, reps[i].Stats.Marked)
		}
	}
	shared := func(a, b *Rep) (same, total int) {
		in := map[string]bool{}
		for _, c := range a.Comps {
			in[string(c)] = true
		}
		for _, c := range b.Comps {
			total++
			if in[string(c)] {
				same++
			}
		}
		return
	}
	// the same cutter over the raw files (structure only) shares fewer groups than the masked
	// payload with semantic references does once every row index has shifted
	rawShared := func(x, y string) (int, int) {
		groups := func(name string) [][]byte {
			d := fixture(t, name)
			f := parse(t, d)
			secs, err := RawSecs(f, d)
			if err != nil {
				t.Fatal(err)
			}
			var out [][]byte
			prev := 0
			for _, e := range econx.UnitCut(1).Cut(d, secs) {
				out = append(out, d[prev:e])
				prev = e
			}
			return append(out, d[prev:])
		}
		in := map[string]bool{}
		for _, g := range groups(x) {
			in[string(g)] = true
		}
		same, total := 0, 0
		for _, g := range groups(y) {
			total++
			if in[string(g)] {
				same++
			}
		}
		return same, total
	}
	nSame, nTotal := shared(reps[0], reps[2])
	rSame, rTotal := rawShared("fixture-r0.dll", "fixture-earlytype.dll")
	if float64(nSame)/float64(nTotal) <= float64(rSame)/float64(rTotal) {
		t.Errorf("early type: %d/%d groups shared with semantic references, %d/%d raw", nSame, nTotal, rSame, rTotal)
	}
	if same, total := shared(reps[0], reps[1]); same*2 < total {
		t.Errorf("body edit: %d of %d groups shared", same, total)
	}
}

// Every table of the schema has a finite layout whose widths follow the row counts.
func TestSchemaWidths(t *testing.T) {
	f := parse(t, fixture(t, "fixture-r0.dll"))
	for tb := 0; tb < NumTables; tb++ {
		tt := &f.Tables[tb]
		sum := 0
		for _, c := range tt.Cols {
			sum += c.W
		}
		if sum != tt.RowSize {
			t.Errorf("%s: row size %d, columns sum to %d", TableName(tb), tt.RowSize, sum)
		}
		if tt.Rows > 0 && tt.Off+int64(tt.Rows)*int64(tt.RowSize) > f.TabEnd {
			t.Errorf("%s runs past the table end", TableName(tb))
		}
	}
	if f.Rows(TModule) != 1 || f.Rows(TTypeDef) < 7 || f.Rows(TMethodDef) < 60 {
		t.Errorf("fixture rows: Module %d TypeDef %d MethodDef %d", f.Rows(TModule), f.Rows(TTypeDef), f.Rows(TMethodDef))
	}
}

// The same input always produces the same bytes (no map-order or time dependence).
func TestDeterministic(t *testing.T) {
	d := fixture(t, "fixture-r0.dll")
	f := parse(t, d)
	var first *Rep
	for i := 0; i < 3; i++ {
		rep, err := Encode(f, d, Config{Auto: 12, Cut: cutEvery(500)})
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = rep
			continue
		}
		if !bytes.Equal(rep.Meta, first.Meta) || len(rep.Comps) != len(first.Comps) {
			t.Fatal("meta or component count differs between runs")
		}
		for k := range rep.Comps {
			if !bytes.Equal(rep.Comps[k], first.Comps[k]) {
				t.Fatalf("component %d differs between runs", k)
			}
		}
	}
}
