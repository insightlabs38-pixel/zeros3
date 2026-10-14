package main

import (
	"math/rand"
	"testing"
)

func TestDeltaCost(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	base := make([]byte, 50000)
	r.Read(base)
	if d := deltaCost(base, base); d.Literal != 0 {
		t.Fatalf("identical input has %d literal bytes", d.Literal)
	}
	cur := append([]byte(nil), base...)
	cur[20000] ^= 0xff
	if d := deltaCost(base, cur); d.Literal == 0 || d.Literal > 8 {
		t.Fatalf("one-byte change: literal %d", d.Literal)
	}
	// insertion shifts everything after it; the delta still finds the tail
	ins := append(append(append([]byte(nil), base[:1000]...), make([]byte, 100)...), base[1000:]...)
	if d := deltaCost(base, ins); d.Literal > 130 {
		t.Fatalf("insertion: literal %d", d.Literal)
	}
	// unrelated data is all literal
	other := make([]byte, 50000)
	r.Read(other)
	if d := deltaCost(base, other); d.Literal < 49000 {
		t.Fatalf("unrelated: literal %d", d.Literal)
	}
}

func TestCDCScaledMatchesNothingWorse(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	b := make([]byte, 1<<20)
	r.Read(b)
	total := 0
	for _, o := range cdcObjsP(b, 1024, 4096, 16384) {
		total += o.size
		if o.size > 16384 {
			t.Fatal("chunk above max")
		}
	}
	if total != len(b) {
		t.Fatalf("chunks cover %d of %d", total, len(b))
	}
}
