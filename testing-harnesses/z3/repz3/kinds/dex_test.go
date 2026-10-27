package kinds

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/dexz3"
	"zeros3-z3/repz3"
)

func dexFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "dexz3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// KindDEX rebuilds real D8 output through the serialized representation only, the
// reconstruction reads nothing the contract's reachability would not keep alive, a tampered
// component is rejected, and a DEX the parser refuses is not a candidate.
func TestDEXKindIsExactReachableAndTamperEvident(t *testing.T) {
	cuts := func(p []byte, secs []dexz3.Sec) []int { // small fixed groups: several components and nodes
		var out []int
		for e := 512; e < len(p); e += 512 {
			out = append(out, e)
		}
		return out
	}
	for _, name := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		a := dexFixture(t, name)
		b, stats, err := EncodeDEX(a, dexz3.Config{Cut: cuts})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if stats.Comps < 4 {
			t.Fatalf("%s: %d groups", name, stats.Comps)
		}
		if err := Prove(nil, b, a); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// flip one byte of a stored group: the source digest check or the decoder must refuse
		st := repz3.NewMemStore()
		for _, o := range b.Objects {
			st.Put(o)
		}
		id, _, _ := b.Persist(st)
		bad := repz3.NewMemStore()
		for i, o := range b.Objects {
			if i == len(b.Objects)/2 {
				o = append([]byte(nil), o...)
				o[len(o)/2] ^= 0x40
			}
			bad.Put(o)
		}
		bad.Put(b.Raw)
		var out bytes.Buffer
		if _, _, err := NewEnv(bad).Reconstruct(id, &out); err == nil {
			t.Fatalf("%s: tampered group accepted", name)
		}
	}
	// a truncated DEX is refused, not partially represented
	a := dexFixture(t, "fixture-api28.dex")
	if _, _, err := EncodeDEX(a[:len(a)-40], dexz3.Config{}); err == nil {
		t.Fatal("truncated DEX encoded")
	}
	// two layouts of one program share groups
	b1, _, _ := EncodeDEX(dexFixture(t, "fixture-api24.dex"), dexz3.Config{Cut: cuts})
	b2, _, _ := EncodeDEX(dexFixture(t, "fixture-api28.dex"), dexz3.Config{Cut: cuts})
	mst := repz3.NewMemStore()
	b1.Persist(mst)
	_, added, _ := b2.Persist(mst)
	if len(added) >= len(b2.Objects)+1 {
		t.Fatalf("nothing shared between the API 24 and API 28 builds (%d new of %d)", len(added), len(b2.Objects)+1)
	}
}

type failWriter struct{ n int }

func (w *failWriter) Write(p []byte) (int, error) {
	w.n++
	return 0, errors.New("sink failed")
}

// A failing sink surfaces as an error, never as a short successful rebuild.
func TestDEXKindWriterFailure(t *testing.T) {
	a := dexFixture(t, "fixture-api28.dex")
	b, _, err := EncodeDEX(a, dexz3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, _ := b.Persist(st)
	w := &failWriter{}
	if _, _, err := NewEnv(st).Reconstruct(id, w); err == nil || w.n == 0 {
		t.Fatalf("writer failure not reported (writes %d, err %v)", w.n, err)
	}
}
