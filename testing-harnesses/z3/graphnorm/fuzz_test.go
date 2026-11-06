package graphnorm

import (
	"bytes"
	"os"
	"testing"
)

func FuzzVM(f *testing.F) {
	f.Add([]byte{OpLit, 2, 'a', 'b'})
	f.Add([]byte{OpStream, 0, 0, OpTable, 1, 1, 1, PredRaw})
	f.Add([]byte{OpLenBeg, 1, 0, 0, OpLit, 1, 'x', OpLenEnd})
	f.Add([]byte{OpEnter, 8, 3, 0x83, 1, 0, OpEnd})
	f.Fuzz(func(t *testing.T, prog []byte) {
		m := NewMachine()
		m.Limit = 1 << 20
		m.Blobs = [][]byte{{1, 2, 3, 4, 5}}
		m.Tabs[0] = NewTable([]Key{{1}, {2}})
		_ = m.Exec(prog)
		_ = m.Finish()
	})
}

func FuzzBitstream(f *testing.F) {
	for _, n := range []string{"a.bc", "b.bc"} {
		b, _ := os.ReadFile("testdata/" + n)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, d []byte) {
		bc, err := ParseBitstream(d)
		if err != nil {
			return
		}
		if !bytes.Equal(bc.Bytes(), d) {
			t.Fatal("accepted bitstream does not re-encode exactly")
		}
		if e, err := EncodeLLVM(d, PolicyAll); err == nil {
			if out, err := e.Decode(); err != nil || !bytes.Equal(out, d) {
				t.Fatal("encoded but decode differs")
			}
		}
	})
}

func FuzzRecipe(f *testing.F) {
	rc := &LLVMRecipe{Size: 10, Tail: []byte{1}, Steps: []RecipeStep{{'S', [32]byte{1}}, {'F', [32]byte{2}}}}
	f.Add(rc.Marshal())
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := ParseLLVMRecipe(b)
		if err != nil {
			return
		}
		if !bytes.Equal(r.Marshal(), b) {
			t.Fatal("recipe parse/marshal not canonical")
		}
		_, _ = r.Replay(func([32]byte) ([]byte, error) { return nil, os.ErrNotExist })
	})
}

func FuzzPDBandDEX(f *testing.F) {
	p, _ := os.ReadFile("../debugsym/testdata/portable-small.pdb")
	d, _ := os.ReadFile("../dexz3/testdata/fixture-api24.dex")
	f.Add(p)
	f.Add(d)
	f.Fuzz(func(t *testing.T, b []byte) {
		if e, err := EncodePDB(b, true); err == nil {
			if out, err := e.Decode(); err != nil || !bytes.Equal(out, b) {
				t.Fatal("pdb accepted inexactly")
			}
		}
		if e, err := EncodeDEX(b, true); err == nil {
			m := NewByteMachine()
			m.Blobs = e.Blobs
			if err := m.Exec(e.Program); err != nil || !bytes.Equal(m.Output(), b) {
				t.Fatal("dex accepted inexactly")
			}
		}
	})
}
