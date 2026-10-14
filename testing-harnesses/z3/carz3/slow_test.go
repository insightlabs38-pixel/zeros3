package carz3

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"
)

// TestMutatedInputsAreFast mutates valid archives at random and requires every
// Build to finish quickly: the bounds must make hostile headers cheap.
func TestMutatedInputsAreFast(t *testing.T) {
	bs, root := Gen(GenSpec{Seed: 3, N: 12, Min: 20, Max: 300, Fan: 3})
	v1 := V1([][]byte{root}, bs)
	seeds := [][]byte{v1, V2From(v1, V2Opts{PadBefore: 3, IndexCodec: MultihashIndexSortedCodec})}
	rng := rand.New(rand.NewPCG(1, 2))
	var worst time.Duration
	for i := 0; i < 30000; i++ {
		b := append([]byte(nil), seeds[i%2]...)
		for k := 0; k < 1+rng.IntN(4); k++ {
			b[rng.IntN(len(b))] = byte(rng.Uint32())
		}
		t0 := time.Now()
		Build(bytes.NewReader(b), int64(len(b)), DefaultOptions())
		if d := time.Since(t0); d > worst {
			worst = d
		}
	}
	if worst > 200*time.Millisecond {
		t.Errorf("slowest mutated input took %v", worst)
	}
	t.Logf("slowest of 30000 mutated inputs: %v", worst)
}
