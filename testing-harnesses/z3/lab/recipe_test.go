package lab

import "testing"

func TestRecipeLayouts(t *testing.T) {
	mk := func(n int, tag byte) *Comp {
		var id ID
		id[0] = tag
		return &Comp{ID: id, Size: int64(n)}
	}
	a, b := mk(70000, 1), mk(300, 2)
	p := &Plan{Pieces: []Piece{{Comp: a}, {Comp: b}, {Comp: a}, {Inline: make([]byte, 10)}}}
	got := recipeBytes(p)
	// varint(70000<<1) = 3 bytes, varint(300<<1) = 2 bytes, inline: varint(10<<1|1)=1 + 10
	want := map[string]int64{
		"json":   (88 + 5) + (88 + 3) + (88 + 5) + (10 + InlineRecipeOverhead),
		"flat35": 35*3 + 10 + InlineRecipeOverhead,
		"bin32":  (32 + 3) + (32 + 2) + (32 + 3) + 11,
		"bin16":  (16 + 3) + (16 + 2) + (16 + 3) + 11,
		"loc6":   (6 + 3) + (6 + 2) + (6 + 3) + 11,
		// table: two distinct blobs (32+varint(len)) once, three 1-byte indices, the count, and the inline piece
		"table": (32 + 3) + (32 + 2) + 3 + 1 + 11,
	}
	for l, w := range want {
		if got[l] != w {
			t.Errorf("%s: %d bytes, want %d", l, got[l], w)
		}
	}
	if len(got) != len(RecipeLayouts) {
		t.Errorf("layouts %v vs %v", got, RecipeLayouts)
	}
}
