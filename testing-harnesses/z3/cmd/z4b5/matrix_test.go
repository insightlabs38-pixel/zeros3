package main

import (
	"math"
	"math/rand"
	"testing"
)

// brute force: every parent assignment (virtual root = n), keep acyclic ones.
func bruteArb(direct []float64, c [][]float64) float64 {
	n := len(direct)
	best := math.Inf(1)
	par := make([]int, n)
	var rec func(j int)
	rec = func(j int) {
		if j == n {
			for s := 0; s < n; s++ { // cycle check
				x, steps := s, 0
				for x != n && steps <= n {
					x = par[x]
					steps++
				}
				if x != n {
					return
				}
			}
			tot := 0.0
			for v := 0; v < n; v++ {
				if par[v] == n {
					tot += direct[v]
				} else {
					tot += c[par[v]][v]
				}
			}
			if tot < best {
				best = tot
			}
			return
		}
		for p := 0; p <= n; p++ {
			if p != j {
				par[j] = p
				rec(j + 1)
			}
		}
	}
	rec(0)
	return best
}

func TestArborescenceMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for it := 0; it < 300; it++ {
		n := 2 + rng.Intn(5)
		direct := make([]float64, n)
		c := make([][]float64, n)
		for i := range c {
			c[i] = make([]float64, n)
			direct[i] = float64(50 + rng.Intn(50))
			for j := range c[i] {
				c[i][j] = float64(1 + rng.Intn(100))
			}
		}
		if got, want := minArborescence(direct, c), bruteArb(direct, c); math.Abs(got-want) > 1e-9 {
			t.Fatalf("n=%d got %v want %v", n, got, want)
		}
	}
}
