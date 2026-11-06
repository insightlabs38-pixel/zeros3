package graphnorm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStabilityDiagnostic(t *testing.T) {
	d := corpusDir()
	if d == "" || os.Getenv("B4_A") == "" {
		t.Skip("set B4_CORPUS, B4_A, B4_B")
	}
	enc := func(name string) (*LLVM, *LLVMEncoding) {
		b, _ := os.ReadFile(filepath.Join(d, "bc", name))
		bc, _ := ParseBitstream(b)
		L, err := AnalyzeLLVM(bc)
		if err != nil {
			t.Fatal(err)
		}
		e, err := EncodeLLVM(b, PolicyAll)
		if err != nil {
			t.Fatal(err)
		}
		return L, e
	}
	L1, e1 := enc(os.Getenv("B4_A"))
	L2, e2 := enc(os.Getenv("B4_B"))
	name := func(L *LLVM, f lfunc) string { return L.Vals[f.val].name }
	idx2 := map[string]int{}
	var fp2 []Piece
	for _, p := range e2.Pieces {
		if p.Kind == 'F' {
			fp2 = append(fp2, p)
		}
	}
	for i, f := range L2.Funcs {
		idx2[name(L2, f)] = i
	}
	var fp1 []Piece
	for _, p := range e1.Pieces {
		if p.Kind == 'F' {
			fp1 = append(fp1, p)
		}
	}
	same, diff, shown := 0, 0, 0
	for i, f := range L1.Funcs {
		j, ok := idx2[name(L1, f)]
		if !ok {
			continue
		}
		if string(fp1[i].Data) == string(fp2[j].Data) {
			same++
			continue
		}
		diff++
		if shown < 3 {
			shown++
			a, b := Disasm(fp1[i].Data, L1.info), Disasm(fp2[j].Data, L2.info)
			t.Logf("func %s: %d vs %d lines", name(L1, f), len(a), len(b))
			n := 0
			for k := 0; k < len(a) && k < len(b) && n < 6; k++ {
				if a[k] != b[k] {
					t.Logf("  [%d] %s\n      %s", k, a[k], b[k])
					n++
				}
			}
		}
	}
	t.Logf("same=%d diff=%d", same, diff)
}
