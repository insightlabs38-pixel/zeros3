package simdelta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2049))
	for trial := 0; trial < 80; trial++ {
		b := make([]byte, rng.Intn(14000))
		rng.Read(b)
		target := append([]byte{}, b...)
		switch trial % 5 {
		case 0:
			target = append([]byte("displaced boundaries"), target...)
		case 1:
			for i := 0; i < len(target); i += 73 {
				target[i] ^= 7
			}
		case 2:
			rng.Read(target)
		case 3:
			target = bytes.Repeat([]byte{byte(trial)}, len(b))
		case 4:
			target = target[:len(target)/2]
		}
		for _, a := range Algorithms {
			w, e := Encode(a, [][]byte{b}, target)
			if e != nil {
				t.Fatal(e)
			}
			out, e := Decode(w, func(s [32]byte) ([]byte, error) {
				if s != sha256.Sum256(b) {
					t.Fatal("wrong base")
				}
				return b, nil
			})
			if e != nil || !bytes.Equal(out, target) {
				t.Fatalf("%s trial%d: %v", a, trial, e)
			}
			for _, cut := range []int{0, 1, 4, len(w) - 1} {
				if _, e := Decode(w[:cut], nil); e == nil {
					t.Fatalf("accepted truncated %s", a)
				}
			}
			if _, e := Decode(append(w, 0), nil); e == nil {
				t.Fatal("trailing wire")
			}
		}
	}
}
func TestMultiBaseAndGraph(t *testing.T) {
	rng := rand.New(rand.NewSource(81))
	a, b := make([]byte, 8192), make([]byte, 8192)
	rng.Read(a)
	rng.Read(b)
	target := append(append([]byte{}, a...), b...)
	w, e := Encode("adaptive", [][]byte{a, b}, target)
	if e != nil {
		t.Fatal(e)
	}
	h, e := Inspect(w)
	if e != nil || len(h.Bases) != 2 {
		t.Fatalf("multi: %+v %v", h, e)
	}
	store := map[[32]byte][]byte{h.Hash: w}
	for _, v := range [][]byte{a, b} {
		d, _ := Encode("direct", nil, v)
		store[sha256.Sum256(v)] = d
	}
	get := func(s [32]byte) ([]byte, error) {
		w, ok := store[s]
		if !ok {
			return nil, fmt.Errorf("missing")
		}
		return w, nil
	}
	out, e := Resolve(h.Hash, get, 2, 1<<20)
	if e != nil || !bytes.Equal(out, target) {
		t.Fatal(e)
	}
	for _, depth := range []int{0, 5} {
		if _, e = Resolve(h.Hash, get, depth, 1<<20); e == nil {
			t.Fatal("depth")
		}
	}
	if _, e = Resolve(h.Hash, get, 2, int64(len(target))); e == nil {
		t.Fatal("work")
	}
	delete(store, sha256.Sum256(a))
	if _, e = Resolve(h.Hash, get, 2, 1<<20); e == nil {
		t.Fatal("missing")
	}
	r := append([]byte("B1D1"), byte(4))
	r = uv(r, 1)
	hash := sha256.Sum256([]byte("x"))
	r = append(r, hash[:]...)
	r = append(r, 1)
	r = append(r, hash[:]...)
	r = uv(r, 1)
	r = append(r, 0)
	r = uv(r, 4)
	r = append(r, 1, 0, 0, 1)
	if _, e = Resolve(hash, func([32]byte) ([]byte, error) { return r, nil }, 4, 1<<20); e == nil {
		t.Fatal("cycle")
	}
}
func TestRejectCorrupt(t *testing.T) {
	base := bytes.Repeat([]byte("base-content"), 100)
	w, _ := Encode("xor", [][]byte{base}, base)
	if _, e := Decode(w, func([32]byte) ([]byte, error) { return []byte("wrong"), nil }); e == nil {
		t.Fatal("corrupt base")
	}
	w[len(w)-1] ^= 255
	if _, e := Decode(w, func([32]byte) ([]byte, error) { return base, nil }); e == nil {
		t.Fatal("corrupt payload")
	}
}
func FuzzDecode(f *testing.F) {
	for _, a := range Algorithms {
		w, _ := Encode(a, [][]byte{[]byte("base")}, []byte("target"))
		f.Add(w)
	}
	f.Fuzz(func(t *testing.T, w []byte) {
		out, e := Decode(w, func([32]byte) ([]byte, error) { return []byte("base"), nil })
		if e == nil {
			h, e := Inspect(w)
			if e != nil || len(out) != h.Size || sha256.Sum256(out) != h.Hash {
				t.Fatal("authentication")
			}
		}
	})
}
func TestIndex(t *testing.T) {
	x := NewIndex()
	var p []Posting
	for i := 0; i < 130; i++ {
		x.Add(uint32(i), []uint64{7, 7, uint64(i + 9)})
		p = append(p, Posting{7, uint32(i)})
	}
	q := x.Query([]uint64{7, 7}, 4)
	if len(q) != 4 || q[0] != (Candidate{129, 1}) {
		t.Fatal(q)
	}
	x.Delete(129)
	x.Rebuild()
	if x.Query([]uint64{7}, 1)[0].ID != 128 {
		t.Fatal("delete")
	}
	Sort(p)
	var b bytes.Buffer
	if e := WriteTable(&b, p); e != nil {
		t.Fatal(e)
	}
	ids, e := DiskLookup(bytes.NewReader(b.Bytes()), int64(len(p)), 7)
	if e != nil || len(ids) != 64 || ids[0] != 129 {
		t.Fatal(ids, e)
	}
	if _, e = DiskLookup(bytes.NewReader(nil), 10, 7); e == nil {
		t.Fatal("truncated index")
	}
	if got := Features(bytes.Repeat([]byte{1}, 10000), 16); len(got) != 1 {
		t.Fatal(got)
	}
}
func TestMalformedBounds(t *testing.T) {
	w := append([]byte("B1D1"), byte(0))
	w = binary.AppendUvarint(w, ^uint64(0))
	if _, e := Decode(w, nil); e == nil {
		t.Fatal("overflow")
	}
}
