package eraez3

import (
	"bytes"
	"testing"
)

func build(t *testing.T, name string, f []byte) (*Rep, *Stats) {
	t.Helper()
	rep, st, err := Build(bytes.NewReader(f), int64(len(f)), DefaultLimits(), true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	m, sz := rep.Marshal()
	if sz.Total != len(m) {
		t.Fatalf("%s: sizes", name)
	}
	r2, err := Unmarshal(m)
	if err != nil {
		t.Fatalf("%s: unmarshal: %v", name, err)
	}
	r2.Streams = rep.Streams
	var out bytes.Buffer
	if err := r2.Reconstruct(&out, r2.Streams); err != nil || !bytes.Equal(out.Bytes(), f) {
		t.Fatalf("%s: reconstruct from the serialised recipe differs (%v)", name, err)
	}
	return rep, st
}

func TestRebuild(t *testing.T) {
	bs := SynthBlocks(1, 100, 300)
	acc := bytes.Repeat([]byte{0xab}, 32)
	cases := map[string]WriteOpts{
		"grouped":          {Accumulator: acc, IndexStart: 100},
		"grouped-no-acc":   {IndexStart: 100},
		"interleaved":      {Interleaved: true, Accumulator: acc, IndexStart: 100},
		"no-td":            {NoTD: true, IndexStart: 100},
		"no-index":         {NoIndex: true},
		"raw-framing":      {Frame: FrameEncodeRaw, IndexStart: 100},
		"shifted-index":    {IndexStart: 100, ShiftIndex: 3},
		"interleaved-noTD": {Interleaved: true, NoTD: true, IndexStart: 7},
		"unknown-framing":  {Frame: frameWithPadding, IndexStart: 100},
	}
	for name, o := range cases {
		f := Write(bs, o)
		rep, st := build(t, name, f)
		switch name {
		case "grouped", "grouped-no-acc", "interleaved", "no-td", "interleaved-noTD":
			if rep.IndexMode != IndexDerived {
				t.Errorf("%s: index mode %d, want derived", name, rep.IndexMode)
			}
		case "no-index":
			if rep.IndexMode != IndexAbsent {
				t.Errorf("%s: index mode %d", name, rep.IndexMode)
			}
		case "shifted-index":
			if rep.IndexMode != IndexVerbatim {
				t.Errorf("%s: a non-derivable index must be kept verbatim, mode %d", name, rep.IndexMode)
			}
		case "raw-framing":
			if st.NonCanonical != 0 || st.RawFramed == 0 {
				t.Errorf("%s: the uncompressed-chunk encoder is a recognised producer (%+v)", name, st)
			}
		}
		if name == "unknown-framing" && (st.NonCanonical != len(bs) || st.Verbatim < len(bs)) {
			t.Errorf("%s: an unrecognised producer must fall back to verbatim (%+v)", name, st)
		}
	}
	// the interleaved layout is one repeated pattern
	rep, _ := build(t, "il", Write(bs, WriteOpts{Interleaved: true, IndexStart: 1}))
	if len(rep.Layout) != 2 || rep.Layout[1].Repeat != 300 {
		t.Errorf("interleaved layout not compressed: %+v", rep.Layout)
	}
}

// frameWithPadding is a valid framing stream no lab encoder produces: a
// skippable-free stream whose compressed chunks are split into 100-byte
// uncompressed chunks.
func frameWithPadding(plain []byte) []byte {
	out := append([]byte(nil), streamID...)
	for len(plain) > 0 {
		blk := plain[:min(len(plain), 100)]
		plain = plain[len(blk):]
		l := 4 + len(blk)
		out = append(out, 0x01, byte(l), byte(l>>8), byte(l>>16))
		out = append(out, byte(maskedCRC(blk)), byte(maskedCRC(blk)>>8), byte(maskedCRC(blk)>>16), byte(maskedCRC(blk)>>24))
		out = append(out, blk...)
	}
	return out
}

func TestParseRejects(t *testing.T) {
	good := Write(SynthBlocks(1, 0, 5), WriteOpts{IndexStart: 0})
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	for name, b := range map[string][]byte{
		"empty":        nil,
		"trunc-header": good[:5],
		"trunc-entry":  good[:len(good)-1],
		"no-version":   good[8:],
		"reserved":     mut(func(b []byte) []byte { b[6] = 1; return b }),
		"huge-entry":   mut(func(b []byte) []byte { b[10], b[11], b[12] = 0xff, 0xff, 0xff; return b }),
	} {
		if _, _, err := Build(bytes.NewReader(b), int64(len(b)), DefaultLimits(), true); err == nil {
			t.Errorf("%s accepted", name)
		} else if !IsFallback(err) {
			t.Errorf("%s: unclassified %v", name, err)
		}
	}
	lim := DefaultLimits()
	lim.MaxEntries = 3
	if _, _, err := Build(bytes.NewReader(good), int64(len(good)), lim, true); err == nil {
		t.Error("entry bound not enforced")
	}
}

func TestRealRebuild(t *testing.T) {
	for _, name := range []string{"sepolia-00000-8e3e7dc9.erae", "sepolia-00001-c78ee2bd.erae", "mainnet-00000-a6860fef.erae"} {
		f := realFile(t, name)
		rep, st := build(t, name, f)
		if rep.IndexMode != IndexDerived || st.NonCanonical != 0 {
			t.Errorf("%s: index mode %d, non-canonical %d", name, rep.IndexMode, st.NonCanonical)
		}
		blocks, acc, start, err := ExtractBlocks(f)
		if err != nil {
			t.Fatal(err)
		}
		if got := Write(blocks, WriteOpts{Accumulator: acc, IndexStart: start}); !bytes.Equal(got, f) {
			t.Errorf("%s: the writer does not reproduce the real file (%d vs %d bytes)", name, len(got), len(f))
		}
		t.Logf("%s: %d bytes, %d entries, plain %d fixed %d verbatim %d, blocks %d, index %d B derived=%v, framed %d B -> plaintext %d B, layout %+v",
			name, len(f), st.Entries, st.Plain, st.Fixed, st.Verbatim, st.Blocks, st.IndexBytes, st.IndexDerived, st.CompressedBytes, st.PlainBytes, rep.Layout)
	}
}
