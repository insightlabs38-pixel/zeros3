package nativerescue

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

func fixtures(t testing.TB) [][]byte {
	t.Helper()
	var out [][]byte
	for _, name := range []string{"small.elf", "small.exe"} {
		b, e := os.ReadFile("testdata/" + name)
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, b)
	}
	return out
}
func TestGroupedNativeExact(t *testing.T) {
	for i, data := range fixtures(t) {
		for _, class := range []string{"reloc", "table", "branch", "rip", "ptr", "go", "struct", "x86", "full"} {
			for _, target := range []int{-1, 0, 2048, 4096, 16384, 65536} {
				for _, separate := range []bool{false, true} {
					t.Run(fmt.Sprintf("%d-%s-%d-%t", i, class, target, separate), func(t *testing.T) {
						m, cs, _, e := Encode(data, Options{Target: target, Selection: class, Compact: true, Functions: true, Separate: separate})
						if e != nil {
							t.Fatal(e)
						}
						out, e := Decode(m, cs)
						if e != nil || !bytes.Equal(out, data) || sha256.Sum256(out) != sha256.Sum256(data) {
							t.Fatalf("exact native replay: %v", e)
						}
					})
				}
			}
		}
	}
}
func TestRecipeHostileBounds(t *testing.T) {
	data := fixtures(t)[0]
	m, cs, _, e := Encode(data, Options{Target: 4096, Compact: true})
	if e != nil {
		t.Fatal(e)
	}
	for n := 0; n < len(m); n++ {
		if _, e := Decode(m[:n], cs); e == nil {
			t.Fatalf("truncated recipe %d accepted", n)
		}
	}
	if _, e = Decode(m, append(cs, []byte("extra"))); e == nil {
		t.Fatal("surplus component accepted")
	}
	bad := append([]byte("NBR1"), binary.AppendUvarint(nil, 1<<40)...)
	bad = append(bad, 0, 0)
	if _, e = Decode(bad, nil); e == nil {
		t.Fatal("giant output claim accepted")
	}
	for i, c := range cs {
		changed := append([][]byte(nil), cs...)
		changed[i] = c[:len(c)/2]
		if _, e := Decode(m, changed); e == nil {
			t.Fatalf("short component %d accepted", i)
		}
	}
}
func FuzzGroupedNativeDecode(f *testing.F) {
	type fixture struct {
		meta  []byte
		comps [][]byte
	}
	var all []fixture
	for _, data := range fixtures(f) {
		m, cs, _, e := Encode(data, Options{Target: 4096, Compact: true})
		if e != nil {
			f.Fatal(e)
		}
		all = append(all, fixture{m, cs})
		f.Add(m, []byte{}, uint8(len(all)-1))
	}
	f.Fuzz(func(t *testing.T, m, mutated []byte, pick uint8) {
		if len(m) > 1<<16 || len(mutated) > 1<<16 {
			return
		}
		x := all[int(pick)%len(all)]
		cs := append([][]byte(nil), x.comps...)
		if len(mutated) > 0 {
			cs[int(pick)%len(cs)] = mutated
		}
		out, _ := Decode(m, cs)
		if len(out) > MaxNative {
			t.Fatal("output bound")
		}
	})
}

func TestClosedEnvelopeAndCorruption(t *testing.T) {
	data := fixtures(t)[0]
	m, cs, _, e := Encode(data, Options{Target: 4096, Compact: true, Functions: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = Proof(m, cs, data); e != nil {
		t.Fatal(e)
	}
	f, e := Freeze(m, cs)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range f.Objects {
		if len(b) == 0 {
			continue
		}
		saved := append([]byte(nil), b...)
		b[0] ^= 1
		if _, e := Replay(f.Root, f.Get); e == nil {
			t.Fatal("addressed corruption accepted")
		}
		copy(b, saved)
		break
	}
	for id := range f.Objects {
		if id == f.Root {
			continue
		}
		saved := f.Objects[id]
		delete(f.Objects, id)
		if _, e := Replay(f.Root, f.Get); e == nil {
			t.Fatal("missing dependency accepted")
		}
		f.Objects[id] = saved
		break
	}
}

func FuzzFrozenNativeRoot(f *testing.F) {
	b := fixtures(f)[0]
	m, cs, _, e := Encode(b, Options{Target: 4096, Compact: true})
	if e != nil {
		f.Fatal(e)
	}
	frozen, e := Freeze(m, cs)
	if e != nil {
		f.Fatal(e)
	}
	f.Add(frozen.Objects[frozen.Root])
	f.Add([]byte("NBF1"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			return
		}
		id := sha256.Sum256(raw)
		Replay(id, func(want [32]byte) ([]byte, error) {
			if want == id {
				return raw, nil
			}
			return frozen.Get(want)
		})
	})
}

func TestContentIdentityExact(t *testing.T) {
	for _, data := range fixtures(t) {
		for _, class := range []string{"struct", "x86", "full"} {
			m, cs, st, e := Encode(data, Options{Target: 2048, Selection: class, Identity: "content", Compact: true, Functions: true})
			if e != nil {
				t.Fatal(e)
			}
			if st.Identity != "content" {
				t.Fatal("identity selection")
			}
			if e = Proof(m, cs, data); e != nil {
				t.Fatal(e)
			}
		}
	}
}
