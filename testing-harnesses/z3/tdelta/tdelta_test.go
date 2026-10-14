package tdelta

import (
	"bytes"
	"math/rand"
	"testing"
)

func pair(r *rand.Rand, n, w int, frac float64) (a, b []byte) {
	a = make([]byte, n)
	r.Read(a)
	b = append([]byte(nil), a...)
	for e := 0; e < n/w; e++ {
		if r.Float64() < frac {
			// flip low mantissa-ish bits of the element
			b[e*w] ^= byte(1 + r.Intn(255))
			if w > 1 && r.Intn(4) == 0 {
				b[e*w+w-1] ^= 0x80 // sign
			}
		}
	}
	return
}

func TestCodecsRoundtrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, d := range []struct {
		dtype string
		w     int
	}{{"F32", 4}, {"F16", 2}, {"BF16", 2}, {"I8", 1}, {"I64", 8}} {
		for _, frac := range []float64{0, 0.001, 0.2, 1} {
			for _, n := range []int{0, d.w, 8 * d.w, 5000 * d.w, 5001*d.w + 1} { // the last is not a multiple of w
				base, tgt := pair(r, n, d.w, frac)
				for c := XOR; c < NumCodecs; c++ {
					if c == FloatSplit && (d.dtype != "F32" && d.dtype != "F16" && d.dtype != "BF16" || n%d.w != 0) {
						continue
					}
					p, err := EncodeFor(c, d.dtype, base, tgt, d.w)
					if err != nil {
						t.Fatalf("%s %v n=%d: %v", d.dtype, c, n, err)
					}
					got, err := DecodeFor(c, d.dtype, base, p, n, d.w)
					if err != nil || !bytes.Equal(got, tgt) {
						t.Fatalf("%s %v frac=%v n=%d roundtrip: %v", d.dtype, c, frac, n, err)
					}
				}
			}
		}
	}
}

// Decoders must reject (not crash on) arbitrary payloads and never produce a
// result of the wrong length.
func TestDecodeGarbage(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	base := make([]byte, 1024)
	r.Read(base)
	for c := XOR; c < NumCodecs; c++ {
		for i := 0; i < 300; i++ {
			p := make([]byte, r.Intn(200))
			r.Read(p)
			for _, dt := range []string{"F32", "BF16", "I8"} {
				out, err := DecodeFor(c, dt, base, p, len(base), map[string]int{"F32": 4, "BF16": 2, "I8": 1}[dt])
				if err == nil && len(out) != len(base) {
					t.Fatalf("%v accepted garbage with a %d byte result", c, len(out))
				}
			}
		}
	}
}

func TestCompareFloatFields(t *testing.T) {
	// F32: flip sign, one exponent bit, two mantissa bits of three elements
	base := make([]byte, 16)
	tgt := append([]byte(nil), base...)
	tgt[3] ^= 0x80 // element 0 sign
	tgt[7] ^= 0x40 // element 1 exponent (bit 30)
	tgt[8] ^= 0x03 // element 2 mantissa
	s := Compare("F32", base, tgt, 4, nil)
	if s.ChangedElems != 3 || s.SignChanged != 1 || s.ExpChanged != 1 || s.MantChanged != 1 || s.EqualElems != 1 || s.XorPopcount != 4 {
		t.Fatalf("%+v", s)
	}
	if s.MantBitsFlipped != 2 || s.ExpBitsFlipped != 1 {
		t.Fatalf("%+v", s)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(uint8(XORZ), []byte{1, 2, 3}, 16, 4)
	f.Add(uint8(Sparse), []byte{0, 0, 0}, 64, 2)
	f.Add(uint8(FloatSplit), []byte{5, 0, 0, 0, 0}, 8, 4)
	f.Fuzz(func(t *testing.T, c uint8, payload []byte, n, w int) {
		if n < 0 || n > 1<<16 || w < 0 || w > 16 {
			return
		}
		base := make([]byte, n)
		out, err := DecodeFor(Codec(c), "F32", base, payload, n, w)
		if err == nil && len(out) != n {
			t.Fatalf("decoded %d bytes, want %d", len(out), n)
		}
	})
}
