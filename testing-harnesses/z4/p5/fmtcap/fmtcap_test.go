package fmtcap

import "testing"

var (
	main54  = Binary{"current main (P1C segments)", Segments}
	draft   = Binary{"B2 draft #97 (authority, no segments)", Authority}
	future  = Binary{"future combined build", Segments | Authority}
	legacy  = Binary{"pre-P1C (representations only)", 0}
	allBins = []Binary{legacy, main54, draft, future}
)

// The real current-main gate (measured by scripts/formatmatrix.sh) is: levels 1-9 and 11.
func TestModelMatchesObservedBinaries(t *testing.T) {
	for l := 0; l <= 14; l++ {
		wantMain := l >= 1 && l <= 9 || l == 11
		if main54.Accepts(l) != wantMain {
			t.Fatalf("level %d: main model %v, observed gate %v", l, main54.Accepts(l), wantMain)
		}
		wantDraft := l >= 1 && l <= 10
		if draft.Accepts(l) != wantDraft {
			t.Fatalf("level %d: draft model %v, observed gate %v", l, draft.Accepts(l), wantDraft)
		}
	}
}

// Authority must never be published at a level any non-authority binary accepts.
func TestAuthorityNeverVisibleToNonAuthorityBinaries(t *testing.T) {
	for f := 0; f < 4; f++ {
		if f&Authority == 0 {
			continue
		}
		for _, b := range allBins {
			if b.Features&Authority == 0 && b.Accepts(Level(f)) {
				t.Fatalf("level %d (features %b) accepted by %s", Level(f), f, b.Name)
			}
		}
	}
}

// No binary accepts a store requiring a feature it does not implement.
func TestNoBinaryAcceptsMissingFeature(t *testing.T) {
	for l := 0; l <= 14; l++ {
		f, ok := Features(l)
		for _, b := range allBins {
			if b.Accepts(l) && (!ok || f&^b.Features != 0) {
				t.Fatalf("%s accepts level %d requiring %b", b.Name, l, f)
			}
		}
	}
}

// Every adoption step lands on a label refused by every binary lacking the new feature,
// including adopting authority on a store that is already at the segments level (11 -> 12)
// and adopting segments on an authority store (10 -> 12): never a numeric "up" that old code accepts.
func TestAdoptionSteps(t *testing.T) {
	for _, from := range []int{5, 9, 11, 10, 12} {
		for _, x := range []int{Segments, Authority} {
			to, ok := RaiseTo(from, x)
			if !ok {
				t.Fatal("bad from", from)
			}
			for _, b := range allBins {
				if b.Features&x == 0 && b.Accepts(to) {
					t.Fatalf("adopting feature %b from %d -> %d is accepted by %s", x, from, to, b.Name)
				}
			}
			f0, _ := Features(from)
			f1, _ := Features(to)
			if f1&f0 != f0 {
				t.Fatalf("adoption dropped features: %d -> %d", from, to)
			}
		}
	}
	if to, _ := RaiseTo(11, Authority); to != 12 {
		t.Fatalf("authority on a segments store must publish 12, got %d (10 would DOWNGRADE the segment semantics)", to)
	}
	if to, _ := RaiseTo(9, Authority); to != 10 {
		t.Fatalf("authority on a pre-segment store: %d", to)
	}
}

// Downgrades happen only after the feature's records are gone.
func TestRetireRule(t *testing.T) {
	if _, ok := CanRetire(Segments|Authority, Authority, 3); ok {
		t.Fatal("retired authority with live bodies")
	}
	if l, ok := CanRetire(Segments|Authority, Authority, 0); !ok || l != 11 {
		t.Fatalf("12 -> %d ok=%v", l, ok)
	}
}

// The numeric rule is wrong: a build that implements only segments and uses
// `level <= max` with max 12 would open authority stores (10, 12) it cannot read.
func TestNumericMaxRuleIsUnsafe(t *testing.T) {
	gate := MaxGate(12)
	unsafe := 0
	for l := 1; l <= 12; l++ {
		if gate(l) && !main54.Accepts(l) {
			unsafe++
		}
	}
	if unsafe == 0 {
		t.Fatal("expected the numeric gate to accept unreadable levels")
	}
	t.Logf("numeric gate (max 12) would wrongly accept %d levels for a segments-only build", unsafe)
}
