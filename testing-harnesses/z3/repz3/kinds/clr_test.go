package kinds

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/clrz3"
	"zeros3-z3/idxref"
	"zeros3-z3/repz3"
)

func clrFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "clrz3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// KindCLR rebuilds real D8 output through the serialized representation only, the
// reconstruction reads nothing the contract's reachability would not keep alive, a tampered
// component is rejected, and a CLR the parser refuses is not a candidate.
func TestCLRKindIsExactReachableAndTamperEvident(t *testing.T) {
	cuts := func(p []byte, secs []idxref.Sec) []int { // small fixed groups: several components and nodes
		var out []int
		for e := 512; e < len(p); e += 512 {
			out = append(out, e)
		}
		return out
	}
	for _, name := range []string{"fixture-r0.dll", "fixture-earlytype.dll"} {
		a := clrFixture(t, name)
		b, stats, err := EncodeCLR(a, clrz3.Config{Cut: cuts})
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
	// a truncated CLR is refused, not partially represented
	a := clrFixture(t, "fixture-earlytype.dll")
	if _, _, err := EncodeCLR(a[:len(a)-40], clrz3.Config{}); err == nil {
		t.Fatal("truncated CLR encoded")
	}
	// two layouts of one program share groups
	b1, _, _ := EncodeCLR(clrFixture(t, "fixture-r0.dll"), clrz3.Config{Cut: cuts})
	b2, _, _ := EncodeCLR(clrFixture(t, "fixture-earlytype.dll"), clrz3.Config{Cut: cuts})
	mst := repz3.NewMemStore()
	b1.Persist(mst)
	_, added, _ := b2.Persist(mst)
	if len(added) >= len(b2.Objects)+1 {
		t.Fatalf("nothing shared between the API 24 and API 28 builds (%d new of %d)", len(added), len(b2.Objects)+1)
	}
}

// A failing sink surfaces as an error, never as a short successful rebuild.
func TestCLRKindWriterFailure(t *testing.T) {
	a := clrFixture(t, "fixture-earlytype.dll")
	b, _, err := EncodeCLR(a, clrz3.Config{})
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
