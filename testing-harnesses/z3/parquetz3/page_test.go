package parquetz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

func pattern(kind string, n, bw int, r *rand.Rand) []uint32 {
	mask := uint32(1<<uint(bw) - 1)
	v := make([]uint32, n)
	for i := range v {
		switch kind {
		case "random":
			v[i] = r.Uint32() & mask
		case "constant":
			v[i] = 1 & mask
		case "runs": // runs of 1..40 equal values
			if i > 0 && r.Intn(12) != 0 {
				v[i] = v[i-1]
			} else {
				v[i] = r.Uint32() & mask
			}
		case "sparse-zero":
			if r.Intn(9) == 0 {
				v[i] = r.Uint32() & mask
			}
		case "ramp":
			v[i] = uint32(i) & mask
		}
	}
	return v
}

// The hybrid coder: the standard predictor round-trips, its own run headers replay it
// exactly, and any valid stream (here a handmade one with stale padding) is described
// exactly by explicit headers plus pad values.
func TestHybridRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, bw := range []int{0, 1, 2, 3, 6, 7, 8, 9, 13, 16, 20, 31, 32} {
		for _, kind := range []string{"random", "constant", "runs", "sparse-zero", "ramp"} {
			for _, n := range []int{0, 1, 7, 8, 9, 63, 64, 504, 505, 1000, 4097} {
				vals := pattern(kind, n, bw, r)
				enc := hybridEncodeStd(vals, bw)
				got, hdrs, pad, used, err := hybridDecode(enc, bw, n)
				if err != nil || used != len(enc) || len(got) != n {
					t.Fatalf("bw %d %s n %d: decode %v used %d/%d", bw, kind, n, err, used, len(enc))
				}
				for i := range got {
					if got[i] != vals[i] {
						t.Fatalf("bw %d %s n %d: value %d is %d, want %d", bw, kind, n, i, got[i], vals[i])
					}
				}
				if len(pad) >= 8 {
					t.Fatalf("standard encoder left %d padding values", len(pad))
				}
				again, ok := hybridEncodeRuns(got, bw, hdrs, pad)
				if !ok || !bytes.Equal(again, enc) {
					t.Fatalf("bw %d %s n %d: explicit headers do not replay the stream", bw, kind, n)
				}
			}
		}
	}
}

func TestHybridStalePadding(t *testing.T) {
	// two bit-packed groups of width 1 holding 11 real values and 5 "stale" ones
	stream := []byte{2<<1 | 1, 0b10110101, 0b11111011}
	vals, hdrs, pad, used, err := hybridDecode(stream, 1, 11)
	if err != nil || used != 3 || len(vals) != 11 || len(pad) != 5 {
		t.Fatalf("decode: %v used %d vals %d pad %d", err, used, len(vals), len(pad))
	}
	if again, ok := hybridEncodeRuns(vals, 1, hdrs, pad); !ok || !bytes.Equal(again, stream) {
		t.Fatalf("explicit replay %x ok=%v, want %x", again, ok, stream)
	}
	if short, _ := hybridEncodeRuns(vals, 1, hdrs, pad[:3]); bytes.Equal(short, stream) {
		t.Fatal("a short pad list reproduced the stream (the byte comparison would not catch it)")
	}
	if std := hybridEncodeStd(vals, 1); bytes.Equal(std, stream) {
		t.Fatal("the standard encoder cannot reproduce stale padding; the layout must be explicit")
	}
}

func TestHybridMalformed(t *testing.T) {
	huge := binary.AppendUvarint(nil, 1<<31|1) // 2^30 groups of width 0 would loop 8 G times
	for _, tc := range []struct {
		name string
		b    []byte
		bw   int
		n    int
	}{
		{"width above 32", []byte{3, 0}, 33, 1},
		{"empty RLE run", []byte{0, 0}, 8, 1},
		{"empty bit-packed run", []byte{1}, 8, 1},
		{"truncated bit-packed run", []byte{4<<1 | 1, 1, 2}, 8, 32},
		{"truncated run value", []byte{8 << 1}, 16, 8},
		{"RLE value wider than the width", []byte{8 << 1, 0xff}, 4, 8},
		{"RLE run overshoots the page", []byte{20 << 1, 1}, 1, 8},
		{"no runs at all", nil, 1, 3},
		{"non-minimal run header", []byte{0x88, 0x00, 1}, 1, 4},
	} {
		if _, _, _, _, err := hybridDecode(tc.b, tc.bw, tc.n); err == nil || !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if v, _, _, _, err := hybridDecode(huge, 0, 10); err != nil || len(v) != 10 {
		t.Errorf("zero-width bomb: %v %d", err, len(v))
	}
	if _, ok := hybridEncodeRuns(nil, 0, []uint64{1<<31 | 1}, nil); ok {
		t.Error("zero-width run replay accepted a hostile group count")
	}
}

func TestPlainValues(t *testing.T) {
	lim := Limits{}.norm()
	cases := []struct {
		name string
		c    colSpec
		in   []byte
		n    int
		ok   bool
	}{
		{"int32", colSpec{Phys: TInt32}, make([]byte, 12), 3, true},
		{"int32 short", colSpec{Phys: TInt32}, make([]byte, 11), 3, false},
		{"bool", colSpec{Phys: TBoolean}, []byte{0b101}, 3, true},
		{"bool short", colSpec{Phys: TBoolean}, nil, 3, false},
		{"flba", colSpec{Phys: TFixedLen, TypeLen: 4}, make([]byte, 8), 2, true},
		{"byte array", colSpec{Phys: TByteArr}, []byte{2, 0, 0, 0, 'a', 'b', 0, 0, 0, 0}, 2, true},
		{"byte array length beyond data", colSpec{Phys: TByteArr}, []byte{9, 0, 0, 0, 'a'}, 1, false},
		{"byte array hostile length", colSpec{Phys: TByteArr}, []byte{0xff, 0xff, 0xff, 0xff}, 1, false},
	}
	for _, tc := range cases {
		r, used, err := decodePlain(tc.c, tc.in, tc.n, lim)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v", tc.name, err)
			continue
		}
		if err == nil {
			if r.n != tc.n || used != len(tc.in) {
				t.Errorf("%s: n %d used %d of %d", tc.name, r.n, used, len(tc.in))
			}
			if !bytes.Equal(encodePlain(tc.c, &r), tc.in) {
				t.Errorf("%s: PLAIN does not re-encode", tc.name)
			}
		}
	}
	if _, _, err := decodePlain(colSpec{Phys: TByteArr}, []byte{9, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 1, Limits{MaxValue: 4}.norm()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("value above the logical limit: %v", err)
	}
}

// ---- real producer pages: codec predictors, layouts, replay ----

func analyzeFixture(t *testing.T, name string) (*File, *Analyzer, *bytes.Reader) {
	t.Helper()
	b := load(t, name)
	r := bytes.NewReader(b)
	f, err := Open(r, int64(len(b)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return f, NewAnalyzer(r, f, Limits{}), r
}

func TestProducerPagesAreExactlyReplayable(t *testing.T) {
	type want struct {
		comp     int // the compression predictor every non-trivial page must use
		opaque   string
		explicit bool // some pages need an explicit layout
	}
	exp := map[string]want{
		"pa_snappy_v1.parquet":  {comp: CompSnapCPP},
		"pa_snappy_v2.parquet":  {comp: CompSnapCPP},
		"pa_zstd_v1.parquet":    {comp: CompZstd},
		"pa_none_plain.parquet": {comp: CompNone},
		"pa_bss.parquet":        {comp: CompSnapCPP},
		"pa_rg500.parquet":      {comp: CompSnapCPP},
		"dd_snappy.parquet":     {comp: CompSnapCRC, explicit: true},
		"dd_zstd.parquet":       {comp: CompZstd, explicit: true},
		"dd_none.parquet":       {comp: CompNone, explicit: true},
		"pa_gzip.parquet":       {opaque: "codec GZIP"},
		"pa_nested.parquet":     {opaque: "repeated"},
	}
	for name, w := range exp {
		t.Run(name, func(t *testing.T) {
			f, an, r := analyzeFixture(t, name)
			sawExplicit, sawLogical := false, false
			for gi := range f.RowGroups {
				for ci := range f.RowGroups[gi].Chunks {
					c := &f.RowGroups[gi].Chunks[ci]
					plan, err := an.Chunk(c, nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					if w.opaque != "" {
						if !plan.Opaque || !strings.Contains(plan.Reason, w.opaque) {
							if !(w.opaque == "repeated" && !plan.Opaque && f.Columns[ci].MaxRep == 0) {
								t.Fatalf("chunk %d/%d (%s): opaque=%v %q, want %q", gi, ci, f.Columns[ci].Name, plan.Opaque, plan.Reason, w.opaque)
							}
						}
						continue
					}
					if plan.Opaque {
						t.Fatalf("chunk %d/%d (%s) is opaque: %s", gi, ci, f.Columns[ci].Name, plan.Reason)
					}
					sawLogical = true
					i := 0
					err = f.Pages(r, c, func(p *Page, _ []byte) error {
						pi := plan.Pages[i]
						i++
						l, v := pi.Lay.Explicit()
						sawExplicit = sawExplicit || l || v
						if pi.Comp.ID != CompNone && pi.Comp.ID != uint8(w.comp) && !(w.comp == CompSnapCPP && pi.Comp.ID == CompSnapCPP5) && !(w.comp == CompSnapCRC && pi.Comp.ID == CompSnapCPP5) {
							t.Errorf("chunk %d/%d page %d uses %s, want %s", gi, ci, i-1, CompName(int(pi.Comp.ID)), CompName(w.comp))
						}
						body, rec, err := an.PageBody(c, p)
						if err != nil {
							return err
						}
						payload, _ := ReadPayload(r, p)
						again, err := an.PagePayload(p, body, rec)
						if err != nil || !bytes.Equal(again, payload) {
							t.Errorf("chunk %d/%d page %d: payload does not replay (%v)", gi, ci, i-1, err)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if w.opaque == "" && (!sawLogical || sawExplicit != w.explicit) {
				t.Errorf("logical %v, explicit layouts seen %v, want %v", sawLogical, sawExplicit, w.explicit)
			}
		})
	}
}

// Dictionary-encoded chunks of both producers have a dictionary that is the first N
// distinct values of the column; the analysis says so only after comparing bytes.
func TestDictionaryPrediction(t *testing.T) {
	for _, name := range []string{"pa_snappy_v1.parquet", "dd_snappy.parquet"} {
		f, an, _ := analyzeFixture(t, name)
		predicted := 0
		for gi := range f.RowGroups {
			for ci := range f.RowGroups[gi].Chunks {
				plan, err := an.Chunk(&f.RowGroups[gi].Chunks[ci], nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if plan.DictMode == DictPredicted {
					predicted++
				}
				if plan.DictMode == DictExplicit {
					t.Errorf("%s chunk %d/%d needs an explicit dictionary", name, gi, ci)
				}
			}
		}
		if predicted == 0 {
			t.Errorf("%s: no predicted dictionaries", name)
		}
	}
}

func TestNoPredictForcesExplicitRecipes(t *testing.T) {
	f, an, _ := analyzeFixture(t, "pa_snappy_v1.parquet")
	an.NoPredict = true
	c := &f.RowGroups[0].Chunks[0]
	plan, err := an.Chunk(c, nil, nil)
	if err != nil || plan.Opaque {
		t.Fatalf("explicit recipes must still describe the chunk: %v %q", err, plan.Reason)
	}
	for i, pi := range plan.Pages {
		if pi.Comp.ID != CompSnapCmds || pi.CompRecBytes == 0 {
			t.Fatalf("page %d: %s with %d recipe bytes", i, CompName(int(pi.Comp.ID)), pi.CompRecBytes)
		}
	}
	if plan.DictMode == DictPredicted {
		t.Fatal("dictionary predicted with predictors disabled")
	}
}
