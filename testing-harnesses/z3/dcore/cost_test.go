package dcore

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func TestStatsMatchProgram(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		base := rnd(seed, 30000)
		tgt := mutate(seed+7, base, 10)
		ops, _ := Match([][]byte{base, rnd(seed+1, 5000)}, tgt, DefaultParams())
		prog, order := Program(ops)
		s := Stats(ops)
		if s.RawProgramBytes() != int64(len(prog)) || s.Bases != len(order) {
			t.Fatalf("seed %d: stats %d/%d vs program %d/%d", seed, s.RawProgramBytes(), s.Bases, len(prog), len(order))
		}
		if s.CopyBytes+s.LitBytes != int64(len(tgt)) {
			t.Fatalf("coverage %d+%d vs %d", s.CopyBytes, s.LitBytes, len(tgt))
		}
	}
}

// Upper is a hard bound: the exact envelope can never exceed the raw program plus fixed costs.
// The estimate never claims to be exact and stays within the upper bound.
func TestEstimateBounds(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	for k := 0; k < 60; k++ {
		base := rnd(uint64(k), 5000+r.IntN(200000))
		var tgt []byte
		switch k % 3 {
		case 0:
			tgt = mutate(uint64(k), base, 15)
		case 1:
			tgt = rnd(uint64(k)+99, 20000+r.IntN(50000)) // incompressible, unrelated
		default:
			tgt = bytes.Repeat([]byte("abcabcabd"), 5000) // highly compressible literals
		}
		ops, _ := Match([][]byte{base}, tgt, DefaultParams())
		exact := EnvelopeSize(ops, 128<<10)
		for _, o := range []EstimateOpts{{}, {Entropy: true}} {
			e := EstimateProgram(ops, o)
			// +16: frame table varints of a multi-frame envelope are not in Upper
			if exact > e.Upper+int64(len(tgt)/(128<<10)+2)*16 {
				t.Fatalf("k=%d exact %d exceeds hard upper bound %d", k, exact, e.Upper)
			}
			if e.Est > e.Upper || e.Lower > e.Est {
				t.Fatalf("k=%d ordering violated: %+v", k, e)
			}
		}
	}
}

func TestSelectStagedNeverInvalid(t *testing.T) {
	base := rnd(1, 120000)
	tgt := mutate(2, base, 12)
	cands := []Candidate{
		{ID: 0, Bases: [][]byte{rnd(9, 120000)}},
		{ID: 1, Bases: [][]byte{base}},
		{ID: 2, Bases: [][]byte{base[:60000]}},
		{ID: 3, Bases: [][]byte{nil}},
	}
	ex, _ := Select(tgt, cands, SelectOpts{Exhaustive: true})
	st, _ := Select(tgt, cands, SelectOpts{Slack: 0.05})
	if ex.Best != 1 || st.Best != 1 {
		t.Fatalf("best: exhaustive %d staged %d", ex.Best, st.Best)
	}
	if st.ExactEncoded >= ex.ExactEncoded {
		t.Fatalf("staged did not skip any exact encoding: %d vs %d", st.ExactEncoded, ex.ExactEncoded)
	}
	for _, s := range []Selection{ex, st} {
		got, err := Apply(cands[s.Best].Bases, s.Ops, int64(len(tgt)))
		if err != nil || !bytes.Equal(got, tgt) {
			t.Fatal("selected program does not reconstruct the target")
		}
	}
	// an all-unrelated candidate set keeps the prior form
	un, _ := Select(tgt, cands[:1], SelectOpts{Exhaustive: true, MinSaving: 0.2})
	if un.Best != -1 {
		t.Fatalf("unrelated candidate admitted: %+v", un.Best)
	}
}
