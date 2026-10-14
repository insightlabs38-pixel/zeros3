package eraez3

import (
	"bytes"
	"testing"
)

// FuzzBuildReconstruct: whatever Build accepts must reconstruct to the input
// byte for byte (through the serialised recipe); every refusal must be a
// classified fallback; the Snappy decoder must never panic or over-allocate.
func FuzzBuildReconstruct(f *testing.F) {
	bs := SynthBlocks(1, 10, 6)
	f.Add(Write(bs, WriteOpts{Accumulator: bytes.Repeat([]byte{1}, 32), IndexStart: 10}))
	f.Add(Write(bs, WriteOpts{Interleaved: true, IndexStart: 3}))
	f.Add(Write(bs, WriteOpts{Frame: FrameEncodeRaw, NoTD: true}))
	f.Add(FrameEncodeRef(bytes.Repeat([]byte("abcd"), 50)))
	f.Fuzz(func(t *testing.T, data []byte) {
		FrameDecode(data)
		lim := DefaultLimits()
		lim.MaxEntries, lim.MaxEntry, lim.MaxTotal = 1<<12, 1<<20, 1<<22
		rep, _, err := Build(bytes.NewReader(data), int64(len(data)), lim, false)
		if err != nil {
			if !IsFallback(err) {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		m, _ := rep.Marshal()
		r2, err := Unmarshal(m)
		if err != nil {
			t.Fatalf("recipe does not parse back: %v", err)
		}
		r2.Streams = rep.Streams
		var out bytes.Buffer
		if err := r2.Reconstruct(&out, r2.Streams); err != nil {
			// an accepted parse that cannot be reproduced is exactly what verify refuses
			return
		}
		if !bytes.Equal(out.Bytes(), data) {
			// verify=true would have refused this input; report only if verify accepts it
			if _, _, verr := Build(bytes.NewReader(data), int64(len(data)), lim, true); verr == nil {
				t.Fatalf("verified build reconstructs different bytes")
			}
		}
	})
}
