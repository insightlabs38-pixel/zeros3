package clrz3

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

// TestMutationStress feeds randomly damaged synthetic assemblies through the whole pipeline and
// fails on a panic or an input that takes more than 300 ms. Z3_STRESS=N raises the 3000 iterations.
func TestMutationStress(t *testing.T) {
	n := 3000
	if v := os.Getenv("Z3_STRESS"); v != "" {
		fmt.Sscan(v, &n)
	}
	seeds := [][]byte{build(spec{types: 2, methods: 3, fields: 1, attrs: 4}), build(spec{types: 3, methods: 2, fat: true})}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < n; i++ {
		d := append([]byte(nil), seeds[i%2]...)
		for k := 0; k < 1+r.Intn(4); k++ {
			switch r.Intn(6) {
			case 0:
				p := r.Intn(len(d))
				d = append(d[:p], d[min(p+1+r.Intn(8), len(d)):]...)
			case 1:
				p := r.Intn(len(d))
				d = append(d[:p], append(make([]byte, 1+r.Intn(8)), d[p:]...)...)
			default:
				d[r.Intn(len(d))] = byte(r.Intn(256))
			}
		}
		t0 := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					os.WriteFile("/tmp/claude-0/work/panic.bin", d, 0o644)
					t.Fatalf("iteration %d panicked: %v (saved)", i, r)
				}
			}()
			fl, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
			if err == nil {
				Encode(fl, d, Config{Auto: 12, Cut: cutEvery(900)})
			}
		}()
		if el := time.Since(t0); el > 300*time.Millisecond {
			os.WriteFile("/tmp/claude-0/work/slow.bin", d, 0o644)
			t.Fatalf("iteration %d took %v (saved)", i, el)
		}
	}
}
