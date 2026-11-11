package dcore

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func rnd(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x55))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint64())
	}
	return b
}

// mutate applies insertions, deletions, replacements and block moves.
func mutate(seed uint64, src []byte, edits int) []byte {
	r := rand.New(rand.NewPCG(seed, 7))
	out := append([]byte{}, src...)
	for e := 0; e < edits && len(out) > 64; e++ {
		at := r.IntN(len(out) - 32)
		switch r.IntN(4) {
		case 0:
			ins := rnd(r.Uint64(), 1+r.IntN(200))
			out = append(out[:at], append(ins, out[at:]...)...)
		case 1:
			n := 1 + r.IntN(100)
			out = append(out[:at], out[min(at+n, len(out)):]...)
		case 2:
			copy(out[at:], rnd(r.Uint64(), 1+r.IntN(30)))
		case 3: // move a block
			n := 16 + r.IntN(500)
			if at+n < len(out) {
				blk := append([]byte{}, out[at:at+n]...)
				out = append(out[:at], out[at+n:]...)
				to := r.IntN(len(out))
				out = append(out[:to], append(blk, out[to:]...)...)
			}
		}
	}
	return out
}

func checkExact(t *testing.T, bases [][]byte, tgt []byte, ops []Op) {
	t.Helper()
	got, err := Apply(bases, ops, int64(len(tgt)))
	if err != nil || !bytes.Equal(got, tgt) {
		t.Fatalf("reconstruction mismatch: err=%v lens %d %d", err, len(got), len(tgt))
	}
	var pos int64
	for _, o := range ops {
		if o.TPos != pos || o.N <= 0 {
			t.Fatalf("op layout: pos %d tpos %d n %d", pos, o.TPos, o.N)
		}
		pos += o.N
	}
}

func TestMatchRoundTripRandomized(t *testing.T) {
	for seed := uint64(1); seed <= 120; seed++ {
		r := rand.New(rand.NewPCG(seed, 3))
		nb := 1 + r.IntN(3)
		var bases [][]byte
		for i := 0; i < nb; i++ {
			bases = append(bases, rnd(seed*10+uint64(i), r.IntN(60000)))
		}
		var tgt []byte
		if len(bases[0]) > 0 {
			tgt = mutate(seed, bases[0], r.IntN(30))
		}
		if nb > 1 && len(bases[1]) > 100 {
			tgt = append(tgt, bases[1][50:len(bases[1])/2]...)
		}
		p := DefaultParams()
		if seed%3 == 0 {
			p.MaxAnchors = 64 // forces coarse sampling
		}
		if seed%5 == 0 {
			p.Window, p.MinCopy = 8, 8
		}
		ops, err := Match(bases, tgt, p)
		if err != nil {
			t.Fatal(err)
		}
		checkExact(t, bases, tgt, ops)
	}
}

func TestMatchAdversarial(t *testing.T) {
	zeros := make([]byte, 100000)
	per := bytes.Repeat([]byte("abcdefgh"), 20000)
	cases := map[string][2][]byte{
		"zeros":      {zeros, zeros[:70000]},
		"periodic":   {per, append(append([]byte{}, per[3:50000]...), per[100:]...)},
		"empty-base": {nil, []byte("hello world hello world hello world")},
		"empty-tgt":  {[]byte("hello"), nil},
		"tiny":       {[]byte("abc"), []byte("abc")},
		"short-win":  {rnd(1, 15), rnd(1, 15)},
		"exact":      {rnd(2, 5000), rnd(2, 5000)},
		"unrelated":  {rnd(3, 5000), rnd(4, 5000)},
	}
	for name, c := range cases {
		for _, ma := range []int{16, 1 << 20} {
			p := DefaultParams()
			p.MaxAnchors = ma
			ops, err := Match([][]byte{c[0]}, c[1], p)
			if err != nil {
				t.Fatal(name, err)
			}
			checkExact(t, [][]byte{c[0]}, c[1], ops)
		}
	}
}

// Anchors are filters: a base where many positions share a hash (collisions
// forced by Slots=1, MaxProbe=1) must still never produce a wrong COPY.
func TestMatchCollisionBounds(t *testing.T) {
	b := rnd(9, 40000)
	tgt := mutate(10, b, 20)
	p := DefaultParams()
	p.Slots, p.MaxProbe = 1, 1
	ops, _ := Match([][]byte{b}, tgt, p)
	checkExact(t, [][]byte{b}, tgt, ops)
}

func TestExactSimilarIsCompact(t *testing.T) {
	b := rnd(5, 1<<20)
	tgt := mutate(6, b, 40)
	ops, _ := Match([][]byte{b}, tgt, DefaultParams())
	checkExact(t, [][]byte{b}, tgt, ops)
	var lit int64
	for _, o := range ops {
		if o.Base < 0 {
			lit += o.N
		}
	}
	if lit > int64(len(tgt))/8 {
		t.Fatalf("too many literal bytes: %d of %d (ops %d)", lit, len(tgt), len(ops))
	}
}

func TestLayoutChecked(t *testing.T) {
	if _, _, err := layout([]int64{1 << 39, 1 << 39}); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge got %v", err)
	}
	if _, _, err := layout([]int64{-1}); err != ErrTooLarge {
		t.Fatal("negative size accepted")
	}
	if s, tot, err := layout([]int64{5, 7}); err != nil || tot != 12 || s[1] != 5 {
		t.Fatal("layout", s, tot, err)
	}
}

func TestCommonPrefixSuffix(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for k := 0; k < 2000; k++ {
		n := r.IntN(70)
		a := rnd(uint64(k), n)
		b := append([]byte{}, a...)
		if n > 0 {
			b[r.IntN(n)] ^= 1 << r.IntN(8)
		}
		wp, ws := 0, 0
		for wp < n && a[wp] == b[wp] {
			wp++
		}
		for ws < n && a[n-1-ws] == b[n-1-ws] {
			ws++
		}
		if commonPrefix(a, b) != wp || commonSuffix(a, b) != ws {
			t.Fatalf("prefix/suffix mismatch n=%d", n)
		}
	}
}

func FuzzMatchExact(f *testing.F) {
	f.Add([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), []byte("xxabcdefghijklmnopqrstuvwxyz0123456789yy"))
	f.Fuzz(func(t *testing.T, base, tgt []byte) {
		p := DefaultParams()
		p.MaxAnchors = 32
		ops, err := Match([][]byte{base}, tgt, p)
		if err != nil {
			t.Fatal(err)
		}
		checkExact(t, [][]byte{base}, tgt, ops)
	})
}
