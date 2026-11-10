package simdelta

import (
	"bytes"
	"crypto/sha256"
	"math/rand/v2"
	"testing"
)

func p5rand(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint64())
	}
	return b
}

func p5mutate(seed uint64, src []byte, edits int) []byte {
	r := rand.New(rand.NewPCG(seed, 2))
	out := append([]byte{}, src...)
	for e := 0; e < edits && len(out) > 64; e++ {
		at := r.IntN(len(out) - 32)
		switch r.IntN(3) {
		case 0:
			out = append(out[:at], append(p5rand(r.Uint64(), 1+r.IntN(300)), out[at:]...)...)
		case 1:
			out = append(out[:at], out[min(at+1+r.IntN(200), len(out)):]...)
		default:
			copy(out[at:], p5rand(r.Uint64(), 1+r.IntN(40)))
		}
	}
	return out
}

// The optimized matcher must produce programs the unchanged decoder replays
// exactly, for every COPY/ADD algorithm, and must not be materially larger than
// the reference matcher on mutated data.
func TestOptimizedMatcherDifferential(t *testing.T) {
	defer SetReferenceMatcher(false)
	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 9))
		base := p5rand(seed, 2000+r.IntN(400000))
		tgt := p5mutate(seed, base, 3+r.IntN(25))
		for _, alg := range []string{"rolling", "git", "adaptive"} {
			var sizes [2]int
			for mode := 0; mode < 2; mode++ {
				SetReferenceMatcher(mode == 1)
				w, err := Encode(alg, [][]byte{base}, tgt)
				if err != nil {
					t.Fatal(err)
				}
				got, err := Decode(w, func(h [32]byte) ([]byte, error) {
					if h == sha256.Sum256(base) {
						return base, nil
					}
					return nil, bad
				})
				if err != nil || !bytes.Equal(got, tgt) {
					t.Fatalf("seed %d %s mode %d: decode failed: %v", seed, alg, mode, err)
				}
				sizes[mode] = len(w)
			}
			if float64(sizes[0]) > float64(sizes[1])*1.05+64 {
				t.Fatalf("seed %d %s: optimized %d vs reference %d", seed, alg, sizes[0], sizes[1])
			}
		}
	}
}

func TestOptimizedMatcherMultiBase(t *testing.T) {
	a, b := p5rand(1, 50000), p5rand(2, 50000)
	tgt := append(append([]byte{}, a[1000:30000]...), b[5000:40000]...)
	w, err := Encode("adaptive", [][]byte{a, b}, tgt)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := Inspect(w)
	if len(h.Bases) != 2 {
		t.Fatalf("two bases expected, got %d", len(h.Bases))
	}
	got, err := Decode(w, func(s [32]byte) ([]byte, error) {
		switch s {
		case sha256.Sum256(a):
			return a, nil
		case sha256.Sum256(b):
			return b, nil
		}
		return nil, bad
	})
	if err != nil || !bytes.Equal(got, tgt) {
		t.Fatal("multi-base decode", err)
	}
	if len(w) > 600 {
		t.Fatalf("two long copies should be tiny, got %d bytes", len(w))
	}
}
