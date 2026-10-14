package ir

import (
	"errors"
	"fmt"
	"testing"

	"zeros3-z3/deflate"
)

func init() { testCodecs = append(testCodecs, Pred) }

func TestPredictiveRoundtrip(t *testing.T) {
	for _, st := range testStreams(t) {
		for prof := range Profiles {
			for _, p := range []Policy{{PStream, 0}, {PTokRun, 8 << 10}} {
				if testing.Short() && prof > 1 {
					continue
				}
				t.Run(fmt.Sprintf("%s/p%d/%v", st.name, prof, p), func(t *testing.T) {
					roundtrip(t, st.raw, Config{Codec: Pred, Policy: p, Profile: uint8(prof)})
				})
			}
		}
	}
}

// The point of prediction: corrections for a well-modelled producer cost a
// small fraction of the explicit IR.
func TestPredictionBeatsExplicit(t *testing.T) {
	raw := goDeflate(sample(600<<10, 31), 9)
	s, err := deflate.Parse(raw, deflate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := Encode(s, Config{Codec: E2, Policy: Policy{PStream, 0}})
	pr, err := Encode(s, Config{Codec: Pred, Policy: Policy{PStream, 0}, Profile: AutoProfile})
	if err != nil {
		t.Fatal(err)
	}
	a, b := e2.TotalUnitBytes(), pr.TotalUnitBytes()
	t.Logf("explicit %d B, predictive %d B (%.1fx smaller), profile %d", a, b, float64(a)/float64(b), pr.Skel.Profile)
	if b*3 > a {
		t.Errorf("predictive units (%d B) are not much smaller than explicit (%d B)", b, a)
	}
	roundtrip(t, raw, Config{Codec: Pred, Policy: Policy{PTokRun, 4 << 10}, Profile: AutoProfile})
}

// Adversarial plaintext (all zero) makes every candidate walk expensive; the
// per-unit work budget turns that into an error instead of unbounded CPU.
func TestPredictorWorkBudget(t *testing.T) {
	plain := make([]byte, 200<<10)
	dc, de := distCodeOf(1)
	var runs []Run
	for n := 0; n+3 <= len(plain); n += 3 {
		sym, extra := lengthCode(3)
		runs = append(runs, Run{M: deflate.Match(sym, extra, dc, de)})
	}
	p := newPredictor(plain, 1) // zlib-like: 256-deep chains
	if _, err := p.encode(runs, 0, 3*len(runs), nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v, want ErrLimit", err)
	}
}
