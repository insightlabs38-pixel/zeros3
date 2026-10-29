package idxref

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

func sameError(a, b error) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func TestA4PrefixLegacyDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(4101))
	for _, n := range []int{0, 1, 2, 32, 257, 4096} {
		for _, mode := range []int{0, 1, 2, 3} {
			tab := ids(n, int64(n+mode))
			for i := range tab {
				if mode > 0 {
					clear(tab[i][:4])
				}
				if mode > 1 {
					clear(tab[i][:30])
				}
				if mode == 3 && i > 0 && i%3 == 0 {
					tab[i] = tab[i-1]
				}
			}
			for k := 1; k <= 4; k++ {
				old, new := legacyNewPrefixIndex(tab, k), NewPrefixIndex(tab, k)
				if old.Len() != new.Len() || old.Collisions() != new.Collisions() {
					t.Fatal("counts")
				}
				for i := 0; i <= n; i++ {
					a, ae := old.Put([]byte{99}, uint32(i))
					b, be := new.Put([]byte{99}, uint32(i))
					if !bytes.Equal(a, b) || !sameError(ae, be) {
						t.Fatalf("put n=%d mode=%d k=%d i=%d", n, mode, k, i)
					}
					if ae == nil {
						for _, wire := range [][]byte{a[1:], append(append([]byte(nil), a[1:]...), 17), a[1 : len(a)-1]} {
							ai, ar, ae := old.Get(wire)
							bi, br, be := new.Get(wire)
							if ai != bi || !bytes.Equal(ar, br) || !sameError(ae, be) {
								t.Fatalf("get n=%d mode=%d k=%d i=%d: %v / %v", n, mode, k, i, ae, be)
							}
						}
					}
				}
				for j := 0; j < 100; j++ {
					wire := make([]byte, rng.Intn(36))
					rng.Read(wire)
					ai, ar, ae := old.Get(wire)
					bi, br, be := new.Get(wire)
					if ai != bi || !bytes.Equal(ar, br) || !sameError(ae, be) {
						t.Fatalf("malformed %x: %v / %v", wire, ae, be)
					}
				}
			}
		}
	}
}

func TestA4ClassLegacyDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(4102))
	for _, n := range []int{0, 1, 2, 32, 257, 4096} {
		for _, classes := range []int{1, 7, 257} {
			base := ids(classes, int64(classes))
			tab := make([]Identity, n)
			hs := make([]int, n)
			for i := range tab {
				tab[i] = base[rng.Intn(classes)]
				hs[i] = rng.Intn(n+1) - n/2
			}
			for k := 1; k <= 4; k++ {
				old, new := legacyNewClassIndex(hs, tab, k), NewClassIndex(hs, tab, k)
				if old.Collisions() != new.Collisions() || old.Duplicates() != new.Duplicates() {
					t.Fatal("counts")
				}
				for _, h := range append(append([]int(nil), hs...), -10000000) {
					a, ae := old.Put([]byte{99}, h)
					b, be := new.Put([]byte{99}, h)
					if !bytes.Equal(a, b) || !sameError(ae, be) {
						t.Fatalf("put n=%d classes=%d k=%d h=%d %x/%x %v/%v", n, classes, k, h, a, b, ae, be)
					}
					if ae == nil {
						for _, wire := range [][]byte{a[1:], a[1 : len(a)-1], append(append([]byte(nil), a[1:]...), 17)} {
							ai, ar, ae := old.Get(wire)
							bi, br, be := new.Get(wire)
							if ai != bi || !bytes.Equal(ar, br) || !sameError(ae, be) {
								t.Fatalf("get n=%d k=%d h=%d", n, k, h)
							}
						}
					}
				}
			}
		}
	}
}

func BenchmarkA4DuplicateClassPut(b *testing.B) {
	for _, n := range []int{1024, 4096, 16384, 65536} {
		hs := make([]int, n)
		tab := make([]Identity, n)
		for i := range hs {
			hs[i] = i
		}
		old, new := legacyNewClassIndex(hs, tab, 3), NewClassIndex(hs, tab, 3)
		for _, impl := range []struct {
			name string
			put  func([]byte, int) ([]byte, error)
		}{{"legacy", old.Put}, {"compact", new.Put}} {
			b.Run(fmt.Sprintf("%s/%d", impl.name, n), func(b *testing.B) {
				dst := make([]byte, 0, 16)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var err error
					dst, err = impl.put(dst[:0], i%n)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
