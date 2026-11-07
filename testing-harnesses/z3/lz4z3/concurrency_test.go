package lz4z3

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
)

// The predictors share pooled scratch tables; concurrent analysis must stay exact (run with -race).
func TestConcurrentPredictors(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 40; i++ {
				p := randPlain(r, 500+r.Intn(70000))
				id := []int{PredDefault, PredContinue, PredDefault5}[i%3]
				blk, _ := PredictBlock(id, p, 1+i%4)
				dec, seqs, err := Decode(blk, len(p), Limits{})
				if err != nil || !bytes.Equal(dec, p) {
					t.Errorf("goroutine %d: decode: %v", g, err)
					return
				}
				rc := Analyze(p, seqs, true)
				if rep, err := rc.Replay(nil, p, Limits{}); err != nil || !bytes.Equal(rep, blk) {
					t.Errorf("goroutine %d: replay differs: %v", g, err)
					return
				}
				hc, _ := PredictBlock(PredHC, p, 3+i%7)
				_, hs, err := Decode(hc, len(p), Limits{})
				if err != nil {
					t.Errorf("goroutine %d: hc decode: %v", g, err)
					return
				}
				if rc := Analyze(p, hs, true); rc.Kind != KindPredicted {
					t.Errorf("goroutine %d: HC block not predicted (kind %d)", g, rc.Kind)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
