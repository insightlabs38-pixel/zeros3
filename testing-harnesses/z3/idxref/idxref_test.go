package idxref

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func ids(n int, seed int64) []Identity {
	r := rand.New(rand.NewSource(seed))
	out := make([]Identity, n)
	for i := range out {
		r.Read(out[i][:])
	}
	return out
}

// With K0 = 1 and 2000 entries almost every prefix collides: the implied extra bytes must still
// resolve every reference exactly, in a stream of mixed lengths.
func TestPrefixIndexResolvesCollisions(t *testing.T) {
	tab := ids(2000, 1)
	for _, k0 := range []int{1, 2, 3, 4} {
		p := NewPrefixIndex(tab, k0)
		var stream []byte
		order := rand.New(rand.NewSource(2)).Perm(len(tab))
		for _, i := range order {
			var err error
			if stream, err = p.Put(stream, uint32(i)); err != nil {
				t.Fatal(err)
			}
		}
		rest := stream
		for n, want := range order {
			got, r, err := p.Get(rest)
			if err != nil || got != uint32(want) {
				t.Fatalf("K0=%d ref %d: got %d err %v, want %d", k0, n, got, err, want)
			}
			rest = r
		}
		if len(rest) != 0 {
			t.Fatalf("K0=%d: %d trailing bytes", k0, len(rest))
		}
		if k0 == 1 && p.Collisions() == 0 {
			t.Fatal("no collisions at K0=1")
		}
	}
	p := NewPrefixIndex(tab, 3)
	if _, err := p.Put(nil, 5000); !errors.Is(err, ErrFormat) {
		t.Fatal("out of range accepted")
	}
	if _, _, err := p.Get([]byte{1}); !errors.Is(err, ErrFormat) {
		t.Fatal("short stream accepted")
	}
	if _, _, err := NewPrefixIndex(nil, 3).Get([]byte{1, 2, 3}); !errors.Is(err, ErrFormat) {
		t.Fatal("unknown identity accepted")
	}
}

func TestClassIndexDuplicates(t *testing.T) {
	base := ids(50, 3)
	var hs []int
	var tab []Identity
	for i := 0; i < 200; i++ { // handles share identities: each identity appears 4 times
		hs = append(hs, 1000+i)
		tab = append(tab, base[i%50])
	}
	c := NewClassIndex(hs, tab, 2)
	if c.Duplicates() != 150 {
		t.Fatalf("duplicates %d", c.Duplicates())
	}
	var s []byte
	for _, h := range hs {
		var err error
		if s, err = c.Put(s, h); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range hs {
		got, r, err := c.Get(s)
		if err != nil || got != want {
			t.Fatalf("got %d err %v want %d", got, err, want)
		}
		s = r
	}
	if _, err := c.Put(nil, 5); !errors.Is(err, ErrFormat) {
		t.Fatal("unknown handle accepted")
	}
}

func TestComponentPackingIsExactAndBounded(t *testing.T) {
	payload := []byte("payload bytes")
	streams := [][]byte{{1, 2}, nil, {3}}
	c := PackComp(payload, streams)
	p, ss, err := SplitComp(c, 3)
	if err != nil || !bytes.Equal(p, payload) || len(ss) != 3 || !bytes.Equal(ss[0], []byte{1, 2}) || len(ss[1]) != 0 || !bytes.Equal(ss[2], []byte{3}) {
		t.Fatalf("round trip: %v %q %v", err, p, ss)
	}
	for _, bad := range [][]byte{nil, c[:len(c)-1], append(append([]byte(nil), c...), 0), {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1}, {5, 0, 0, 0, 1}} {
		if _, _, err := SplitComp(bad, 3); !errors.Is(err, ErrFormat) {
			t.Errorf("%x accepted (%v)", bad, err)
		}
	}
	for _, v := range []int64{0, 1, -1, 1 << 40, -(1 << 40)} {
		if Unzigzag(Zigzag(v)) != v {
			t.Errorf("zigzag %d", v)
		}
	}
}
