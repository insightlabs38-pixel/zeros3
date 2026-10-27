package snappyz3

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

func fragInputs() map[string][]byte {
	r := rand.New(rand.NewSource(3))
	rnd := make([]byte, 150000)
	r.Read(rnd)
	ints := make([]byte, 0, 300000)
	for i := 0; i < 37500; i++ {
		ints = binary.LittleEndian.AppendUint64(ints, uint64(i))
	}
	text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog; "), 5000)
	return map[string][]byte{"empty": nil, "tiny": []byte("abc"), "small": text[:5000], "one fragment": ints[:FragmentSize], "random": rnd, "ints": ints, "text": text}
}

// Fragment encoders produce valid Snappy that decodes to the input, a single fragment
// is exactly what the single-block encoder produces, and no copy reaches across a
// fragment boundary (the property that makes per-fragment prediction possible).
func TestEncodeFragments(t *testing.T) {
	encs := map[string]func([]byte) []byte{"ref": EncodeRef, "cpp14": EncodeCPP, "cpp15": EncodeCPP15, "crc15": EncodeCPPCRC(15), "cpp16": EncodeCPPBits(16)}
	for in, src := range fragInputs() {
		for name, enc := range encs {
			out := EncodeFragments(src, enc)
			dec, cmds, err := Parse(out, Limits{})
			if err != nil || !bytes.Equal(dec, src) {
				t.Fatalf("%s/%s: does not decode to the input (%v)", name, in, err)
			}
			if len(src) <= FragmentSize && len(src) > 0 && !bytes.Equal(out, enc(src)) {
				t.Fatalf("%s/%s: a single fragment differs from the block encoder", name, in)
			}
			pos := 0
			for _, c := range cmds {
				if c.Kind != Literal && int(c.Off) > pos%FragmentSize {
					t.Fatalf("%s/%s: copy at %d reaches %d bytes back across a fragment", name, in, pos, c.Off)
				}
				pos += int(c.Len)
			}
			var re bytes.Buffer
			if err := Replay(&re, dec, cmds); err != nil || !bytes.Equal(re.Bytes(), out) {
				t.Fatalf("%s/%s: explicit commands do not replay the stream", name, in)
			}
		}
	}
}

// The CRC variant is a different encoder from the multiplicative one (a producer built
// with SSE4.2 emits different commands for the same input); both are valid streams.
func TestCRCVariantDiffers(t *testing.T) {
	src := fragInputs()["ints"][:FragmentSize]
	if bytes.Equal(EncodeCPPCRC(15)(src), EncodeCPP15(src)) {
		t.Fatal("the CRC hash encoder equals the multiplicative one on compressible input")
	}
}
