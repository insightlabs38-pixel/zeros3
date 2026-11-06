package graphnorm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTableStability(t *testing.T) {
	d := corpusDir()
	if d == "" || os.Getenv("B4_A") == "" {
		t.Skip("set B4_CORPUS, B4_A, B4_B")
	}
	load := func(name string) *LLVM {
		b, _ := os.ReadFile(filepath.Join(d, "bc", name))
		bc, err := ParseBitstream(b)
		if err != nil {
			t.Fatal(err)
		}
		L, err := AnalyzeLLVM(bc)
		if err != nil {
			t.Fatal(err)
		}
		return L
	}
	a, b := load(os.Getenv("B4_A")), load(os.Getenv("B4_B"))
	for tab := 0; tab < NumTabs; tab++ {
		sa := map[Key]bool{}
		for _, k := range a.Keys[tab] {
			sa[k] = true
		}
		common := 0
		for _, k := range b.Keys[tab] {
			if sa[k] {
				common++
			}
		}
		// order preserved?
		pos := map[Key]int{}
		for i, k := range a.Keys[tab] {
			pos[k] = i
		}
		last, inv := -1, 0
		for _, k := range b.Keys[tab] {
			if p, ok := pos[k]; ok {
				if p < last {
					inv++
				}
				last = p
			}
		}
		t.Logf("tab %d: %d vs %d entries, %d common, %d order inversions, dup %d/%d", tab, len(a.Keys[tab]), len(b.Keys[tab]), common, inv, len(a.Dup[tab]), len(b.Dup[tab]))
	}
	// kinds of values that changed key
	cnt := map[byte]int{}
	sb := map[Key]bool{}
	for _, k := range b.Keys[0] {
		sb[k] = true
	}
	for i, k := range a.Keys[0] {
		if !sb[k] {
			cnt[a.Vals[i].kind]++
		}
	}
	t.Logf("values only in A by kind: %v", cnt)
}
