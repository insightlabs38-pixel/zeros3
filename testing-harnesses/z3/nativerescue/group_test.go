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
	for _, b := range fixtures(f) {
		m, _, _, e := Encode(b, Options{Target: 4096, Compact: true})
		if e != nil {
			f.Fatal(e)
		}
		f.Add(m, uint8(0))
	}
	all := fixtures(f)
	f.Fuzz(func(t *testing.T, m []byte, pick uint8) {
		if len(m) > 1<<16 {
			return
		}
		_, cs, _, e := Encode(all[int(pick)%len(all)], Options{Target: 4096, Compact: true})
		if e != nil {
			t.Fatal(e)
		}
		Decode(m, cs)
	})
}
