package graphcore

import (
	"math/rand"
	"os"
	"testing"
	"time"
)

// TestMutationLatency bounds worst-case parse+encode latency on structurally mutated inputs.
func TestMutationLatency(t *testing.T) {
	d, _ := os.ReadFile("testdata/b.bc")
	r := rand.New(rand.NewSource(99))
	var worst time.Duration
	for i := 0; i < 3000; i++ {
		x := append([]byte(nil), d...)
		switch r.Intn(3) {
		case 0: // duplicate a random span
			a := r.Intn(len(x))
			b := min(len(x), a+r.Intn(64))
			x = append(x[:b:b], append(append([]byte(nil), x[a:b]...), x[b:]...)...)
		case 1: // overwrite a 4-byte word
			a := r.Intn(len(x) - 4)
			for k := 0; k < 4; k++ {
				x[a+k] = byte(r.Intn(256))
			}
		default: // set a word to a huge little-endian count
			a := r.Intn(len(x) - 4)
			x[a], x[a+1], x[a+2], x[a+3] = 0xFF, 0xFF, 0xFF, 0x7F
		}
		t0 := time.Now()
		if e, err := EncodeLLVM(x, PolicyAll); err == nil {
			if _, err := e.Decode(); err != nil {
				t.Fatal(err)
			}
		}
		if el := time.Since(t0); el > worst {
			worst = el
		}
	}
	t.Logf("worst latency %v", worst)
	if worst > 2*time.Second {
		t.Fatalf("pathological latency %v", worst)
	}
}
