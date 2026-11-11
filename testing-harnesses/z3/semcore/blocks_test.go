package semcore

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func blockStore(t *testing.T, bs int) (*Store, *MemBackend) {
	t.Helper()
	be := NewMemBackend()
	s, err := NewStore(be)
	if err != nil {
		t.Fatal(err)
	}
	s.BlockSize = bs
	return s, be
}

// placeFile ingests a safetensors-like document through the planner and returns the recipe.
func placeTensorFile(t *testing.T, s *Store, name string, data []byte) *Recipe {
	t.Helper()
	p := NewPlanner(s, DefaultOptions())
	doc, err := DetectAt(name, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := p.Ingest(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRecipe(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func stFile(ts ...vt) []byte {
	var b []buildTensor
	for _, x := range ts {
		b = append(b, buildTensor{x.name, x.dtype, x.shape, x.data})
	}
	return buildSafetensors(b)
}

type vt struct {
	name, dtype string
	shape       []int64
	data        []byte
}

type buildTensor struct {
	Name, DType string
	Shape       []int64
	Data        []byte
}

// minimal safetensors writer (tests only; avoids depending on st.Build from this package's tests)
func buildSafetensors(ts []buildTensor) []byte {
	var h []byte
	h = append(h, '{')
	off := 0
	for i, t := range ts {
		if i > 0 {
			h = append(h, ',')
		}
		h = append(h, []byte(`"`+t.Name+`":{"dtype":"`+t.DType+`","shape":[`)...)
		for j, d := range t.Shape {
			if j > 0 {
				h = append(h, ',')
			}
			h = append(h, []byte(itoa(d))...)
		}
		h = append(h, []byte(`],"data_offsets":[`+itoa(int64(off))+`,`+itoa(int64(off+len(t.Data)))+`]}`)...)
		off += len(t.Data)
	}
	h = append(h, '}')
	for len(h)%8 != 0 {
		h = append(h, ' ')
	}
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[i] = byte(len(h) >> (8 * i))
	}
	out = append(out, h...)
	for _, t := range ts {
		out = append(out, t.Data...)
	}
	return out
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// Every range of every recipe equals the replayed original with verified blocks on, for plain,
// converted, transposed and gathered components, at several block sizes.
func TestVerifiedRangesEqualReplay(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	base := make([]byte, 2*96*64)
	r.Read(base)
	// transposed copy of base (a layout relation) and a widened copy (a convert relation)
	tr, _ := permute(base, 2, []uint64{96, 64}, []uint8{1, 0})
	wide, _ := convert(base, DBF16, DF32)
	f1 := stFile(vt{"w", "BF16", []int64{96, 64}, base})
	f2 := stFile(vt{"wT", "BF16", []int64{64, 96}, tr})
	f3 := stFile(vt{"w32", "F32", []int64{96, 64}, wide})
	for _, bs := range []int{4096, 16384, 65536} {
		s, _ := blockStore(t, bs)
		var recs []*Recipe
		var files [][]byte
		for i, f := range [][]byte{f1, f2, f3} {
			recs = append(recs, placeTensorFile(t, s, string(rune('a'+i))+".safetensors", f))
			files = append(files, f)
		}
		derived := 0
		for _, rc := range s.All() {
			if rc.Derived() {
				derived++
			}
		}
		if derived == 0 {
			t.Fatal("fixture did not produce derived components")
		}
		for i, rc := range recs {
			for k := 0; k < 120; k++ {
				off := int64(r.Intn(len(files[i])))
				n := int64(1 + r.Intn(min(9000, len(files[i])-int(off))))
				var io IO
				got, err := s.ReadRange(rc, off, n, &io)
				if err != nil {
					t.Fatalf("bs=%d file %d range %d+%d: %v", bs, i, off, n, err)
				}
				if !bytes.Equal(got, files[i][off:off+n]) {
					t.Fatalf("bs=%d file %d range %d+%d: wrong bytes", bs, i, off, n)
				}
			}
		}
	}
}

// Blob tampering: before first touch the flat digest catches it; after first touch the
// per-block check catches a flipped block; an untouched block still verifies.
func TestVerifiedBlocksFailClosed(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	data := make([]byte, 2*256*128)
	r.Read(data)
	f := stFile(vt{"w", "BF16", []int64{256, 128}, data})
	s, be := blockStore(t, 4096)
	rc := placeTensorFile(t, s, "a.safetensors", f)
	// find the raw blob
	var payload ID
	for _, x := range s.All() {
		if !x.Derived() && x.PayloadLen == int64(len(data)) {
			payload = x.Payload
		}
	}
	// 1. corrupt before first touch
	be.Corrupt(payload, 20000)
	var io IO
	hdr := int64(len(f) - len(data))
	if _, err := s.ReadRange(rc, hdr+100, 50, &io); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered blob accepted on first touch: %v", err)
	}
	be.Corrupt(payload, 20000) // restore
	// 2. first touch ok, then corrupt one block
	if _, err := s.ReadRange(rc, hdr+100, 50, &io); err != nil {
		t.Fatal(err)
	}
	if io.FirstTouches != 1 || io.FirstTouchBytes != int64(len(data)) {
		t.Fatalf("first-touch accounting: %+v", io)
	}
	be.Corrupt(payload, 40000)
	var io2 IO
	if _, err := s.ReadRange(rc, hdr+40000, 10, &io2); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered block accepted: %v", err)
	}
	var io3 IO
	got, err := s.ReadRange(rc, hdr+100, 50, &io3)
	if err != nil || !bytes.Equal(got, f[hdr+100:hdr+150]) {
		t.Fatalf("an untouched verified block must still be served: %v", err)
	}
	if io3.FirstTouches != 0 {
		t.Fatal("verified table was not reused")
	}
	// 3. a fresh store (new process) re-verifies and rejects the tampered blob
	s2, _ := NewStore(be)
	s2.BlockSize = 4096
	if _, err := s2.ReadRange(rc, hdr+100, 50, &IO{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("fresh store accepted a tampered blob: %v", err)
	}
}

// A wrong-length or truncated blob never yields bytes.
func TestVerifiedBlocksTruncatedBlob(t *testing.T) {
	data := make([]byte, 2*64*64)
	rand.New(rand.NewSource(1)).Read(data)
	f := stFile(vt{"w", "BF16", []int64{64, 64}, data})
	s, be := blockStore(t, 4096)
	rc := placeTensorFile(t, s, "a.safetensors", f)
	for id, b := range be.blobs {
		if len(b) == len(data) {
			be.blobs[id] = b[:len(b)-1]
		}
	}
	if _, err := s.ReadRange(rc, int64(len(f)-100), 50, &IO{}); err == nil {
		t.Fatal("truncated blob served")
	}
}
