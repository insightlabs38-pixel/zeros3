package deflate

import "testing"

func TestValidLengths(t *testing.T) {
	cases := []struct {
		name       string
		lens       []uint8
		allowEmpty bool
		want       bool
	}{
		{"complete", []uint8{1, 2, 3, 3}, false, true},
		{"incomplete", []uint8{2, 2, 2}, false, false},
		{"oversubscribed", []uint8{1, 1, 1}, false, false},
		{"single length-1 code", []uint8{0, 1}, false, true},
		{"single length-2 code", []uint8{0, 2}, false, false},
		{"empty as distance code", []uint8{0, 0}, true, true},
		{"empty as literal code", []uint8{0, 0}, false, false},
		{"too long", []uint8{16, 1}, true, false},
	}
	for _, c := range cases {
		if got := ValidLengths(c.lens, c.allowEmpty); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestExportedTables(t *testing.T) {
	if CLOrder() != clOrder {
		t.Fatal("CLOrder differs")
	}
	for sym := 257; sym <= 285; sym++ {
		tk := Match(sym, 0, 0, 0)
		if tk.Length() != LengthBase(sym) || LengthExtraBits(sym) != int(lenExtra[sym-257]) {
			t.Errorf("length symbol %d", sym)
		}
	}
	for dc := 0; dc < 30; dc++ {
		tk := Match(257, 0, dc, 0)
		if tk.Distance() != DistBase(dc) || DistExtraBits(dc) != int(distExtr[dc]) {
			t.Errorf("distance code %d", dc)
		}
	}
}
