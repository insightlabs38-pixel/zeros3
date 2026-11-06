package simdelta

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
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

func TestEmptyAndMaximum(t *testing.T) {
	for _, a := range Algorithms {
		for _, b := range [][]byte{nil, []byte("small")} {
			w, e := Encode(a, [][]byte{b}, nil)
			if e != nil {
				t.Fatal(a, e)
			}
			out, e := Decode(w, func([32]byte) ([]byte, error) { return b, nil })
			if e != nil || len(out) != 0 {
				t.Fatal(a, e)
			}
		}
	}
	base := bytes.Repeat([]byte("large adversarial repeated pattern"), MaxBytes/34+1)
	base = base[:MaxBytes]
	target := append([]byte{}, base...)
	target[MaxBytes-1] ^= 1
	w, e := Encode("adaptive", [][]byte{base, base, base, base}, target)
	if e != nil {
		t.Fatal(e)
	}
	out, e := Decode(w, func([32]byte) ([]byte, error) { return base, nil })
	if e != nil || !bytes.Equal(out, target) {
		t.Fatal(e)
	}
}

type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestShortWriter(t *testing.T) {
	if e := WriteTable(shortWriter{}, []Posting{{1, 2}}); e == nil {
		t.Fatal("short write accepted")
	}
}
func TestFeaturesDisplacement(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	b := make([]byte, 65536)
	rng.Read(b)
	shifted := append([]byte("prefix"), b...)
	x := NewIndex()
	x.Add(3, Features(b, 16))
	q := x.Query(Features(shifted, 16), 1)
	if len(q) != 1 || q[0].ID != 3 {
		t.Fatal("boundary displacement recall", q)
	}
}

func TestRepeatedIdentityAndDepthFour(t *testing.T) {
	x := NewIndex()
	x.Add(1, []uint64{9})
	x.Add(2, []uint64{9})
	x.Add(1, []uint64{9})
	for _, c := range x.Query([]uint64{9}, 2) {
		if c.Hits != 1 {
			t.Fatal("duplicate biased score", c)
		}
	}
	store := map[[32]byte][]byte{}
	base := bytes.Repeat([]byte("chain"), 100)
	w, _ := Encode("direct", nil, base)
	store[sha256.Sum256(base)] = w
	for i := 0; i < 4; i++ {
		target := append([]byte{}, base...)
		target[i] ^= byte(i + 1)
		w, e := Encode("xor", [][]byte{base}, target)
		if e != nil {
			t.Fatal(e)
		}
		store[sha256.Sum256(target)] = w
		base = target
	}
	root := sha256.Sum256(base)
	get := func(h [32]byte) ([]byte, error) {
		b, ok := store[h]
		if !ok {
			return nil, fmt.Errorf("missing")
		}
		return b, nil
	}
	if _, e := Resolve(root, get, 4, int64(5*len(base))); e != nil {
		t.Fatal(e)
	}
	if _, e := Resolve(root, get, 3, int64(5*len(base))); e == nil {
		t.Fatal("depth 3")
	}
	if _, e := Resolve(root, get, 4, int64(5*len(base)-1)); e == nil {
		t.Fatal("repeated work")
	}
	// Explicit flattening removes all historical dependencies and tolerates GC of them.
	direct, _ := Encode("direct", nil, base)
	store = map[[32]byte][]byte{root: direct}
	if out, e := Resolve(root, get, 0, int64(len(base))); e != nil || !bytes.Equal(out, base) {
		t.Fatal("flatten", e)
	}
}

func TestPooledCompressionWireIdentity(t *testing.T) {
	for _, b := range [][]byte{nil, bytes.Repeat([]byte("compressor reuse preserves immutable serialized bytes"), 1000), []byte("tiny")} {
		var out bytes.Buffer
		w, _ := flate.NewWriter(&out, 6)
		w.Write(b)
		w.Close()
		for i := 0; i < 3; i++ {
			if !bytes.Equal(zip(b), out.Bytes()) {
				t.Fatal("compressor reuse changed wire")
			}
		}
	}
}

func TestConcurrentCompressorReuse(t *testing.T) {
	base := bytes.Repeat([]byte("shared immutable base"), 1200)
	target := append([]byte("inserted"), base...)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 12; n++ {
				for _, a := range []string{"direct", "adaptive", "plane"} {
					w, e := Encode(a, [][]byte{base}, target)
					if e != nil {
						errors <- e
						return
					}
					out, e := Decode(w, func([32]byte) ([]byte, error) { return base, nil })
					if e != nil || !bytes.Equal(out, target) {
						errors <- fmt.Errorf("concurrent %s: %v", a, e)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		t.Fatal(e)
	}
}
