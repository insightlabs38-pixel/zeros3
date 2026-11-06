package graphnorm

import (
	"os"
	"path/filepath"
	"testing"
)

// bodyGraph builds an anonymous function graph: Lit = body structure with externs abstracted,
// Set edges = defined callees. Names are used only as ground truth, never as identity input.
func bodyGraph(t *testing.T, path string) (*Graph, []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := ParseBitstream(b)
	if err != nil {
		t.Fatal(err)
	}
	L, err := AnalyzeLLVM(bc)
	if err != nil {
		t.Fatal(err)
	}
	g := &Graph{}
	var names []string
	byVal := map[int]int32{}
	for i, f := range L.Funcs {
		byVal[f.val] = int32(i)
	}
	type pending struct {
		node int
		keys []Key
	}
	keyToFunc := map[Key]int32{}
	for i, f := range L.Funcs {
		keyToFunc[L.Keys[0][f.val]] = int32(i)
	}
	for _, f := range L.Funcs {
		tm, st, ok := L.BuildFuncTemplate(f, PolicyAll)
		if !ok {
			tm = L.LiteralTemplate(f)
		}
		// strip the extern header so the literal is the abstract body structure
		r := &opReader{b: tm, pos: 1}
		for k := 0; k < NumTabs; k++ {
			n := r.uv()
			r.pos += int(n) * KeyLen
		}
		n := Node{Class: 5000, Lit: append([]byte(nil), tm[r.pos:]...)}
		seen := map[int32]bool{}
		for _, k := range st.Ext[0] {
			if fi, ok := keyToFunc[k]; ok && !seen[fi] {
				seen[fi] = true
				n.Set = append(n.Set, Edge{1, fi})
			}
		}
		g.Add(n)
		names = append(names, L.Vals[f.val].name)
	}
	return g, names
}

func TestRealFunctionGraphMatching(t *testing.T) {
	d := corpusDir()
	if d == "" {
		t.Skip()
	}
	pairs := [][2]string{{"sqlite-3450100-O2.bc", "sqlite-3450200-O2.bc"}, {"sqlite-3450200-O2.bc", "sqlite-3450300-O2.bc"}, {"sqlite-3450300-O2.bc", "sqlite-3460000-O2.bc"}, {"sqlite-3460100-O2.bc", "sqlite-3490100-O2.bc"}}
	for _, p := range pairs {
		ga, na := bodyGraph(t, filepath.Join(d, "bc", p[0]))
		gb, nb := bodyGraph(t, filepath.Join(d, "bc", p[1]))
		nameB := map[string]int32{}
		for i, n := range nb {
			nameB[n] = int32(i)
		}
		for _, mode := range []string{"local", "refine", "scc"} {
			var ia, ib *Idents
			switch mode {
			case "local":
				ia, ib = ga.LocalIdentities(), gb.LocalIdentities()
			case "refine":
				ia, ib = ga.RefineIdentities(8), gb.RefineIdentities(8)
			default:
				ia, ib = ga.SCCIdentities(), gb.SCCIdentities()
			}
			m := Match(ga, gb, ia, ib, DefaultMatch())
			truth, got, right, wrong := 0, 0, 0, 0
			var byBasis [5]int
			var wrongBasis [5]int
			for i, name := range na {
				if j, ok := nameB[name]; ok {
					truth++
					_ = j
				}
				if m.Map[i] < 0 {
					continue
				}
				got++
				byBasis[m.Basis[i]]++
				if j, ok := nameB[name]; ok && j == m.Map[i] {
					right++
				} else {
					wrong++
					wrongBasis[m.Basis[i]]++
				}
			}
			t.Logf("%s→%s %-6s: common-names=%d matched=%d correct=%d wrong=%d (basis exact/dup/ctx/assign=%v wrong-by-basis=%v) classes=%d/%d symmetric/poisoned/dup=%d", p[0][7:14], p[1][7:14], mode, truth, got, right, wrong, byBasis[1:], wrongBasis[1:], ia.Classes, len(ga.Nodes), countAmbig(ia))
		}
	}
}

func countAmbig(i *Idents) int {
	n := 0
	for _, a := range i.Ambig {
		if a != AmbigNone {
			n++
		}
	}
	return n
}
