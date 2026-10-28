package convergence

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"zeros3-z3/admission"
	"zeros3-z3/repz3"
)

func TestSpanProofAndReachability(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	cs, back, e := chunksOf(data)
	if e != nil {
		t.Fatal(e)
	}
	spans := []Span{}
	for _, c := range cs {
		spans = append(spans, Span{c.id, 0, c.end - c.start})
	}
	id := sha256.Sum256(data)
	got, e := Restore(id, len(data), spans, back)
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal(e)
	}
	// Marking only the logical SHA loses every backing object; marking each span
	// is sufficient even when the original object's CDC root is no longer live.
	live := map[[32]byte][]byte{}
	for _, s := range spans {
		live[s.SHA] = back[s.SHA]
	}
	if _, e := Restore(id, len(data), spans, live); e != nil {
		t.Fatal(e)
	}
	bad := append([]Span(nil), spans...)
	bad[0].Offset++
	if _, e := Restore(id, len(data), bad, back); e == nil {
		t.Fatal("accepted invalid offset")
	}
	bad = append([]Span(nil), spans...)
	bad[0].Length = -1
	if _, e := Restore(id, len(data), bad, back); e == nil {
		t.Fatal("accepted negative length")
	}
	wrong := id
	wrong[0] ^= 1
	if _, e := Restore(wrong, len(data), spans, back); e == nil {
		t.Fatal("accepted wrong logical digest")
	}
	first := spans[0].SHA
	corrupt := bytes.Clone(live[first])
	corrupt[0] ^= 1
	live[first] = corrupt
	if _, e := Restore(id, len(data), spans, live); e == nil {
		t.Fatal("accepted corrupt chunk")
	}
	delete(live, first)
	if _, e := Restore(id, len(data), spans, live); e == nil {
		t.Fatal("accepted missing chunk")
	}
}
func FuzzRestore(f *testing.F) {
	f.Add([]byte("hello world"), 0, 5)
	f.Add([]byte("abcdef"), -1, 7)
	f.Fuzz(func(t *testing.T, b []byte, off, n int) {
		if len(b) > 1<<20 {
			return
		}
		id := sha256.Sum256(b)
		spans := []Span{{id, off, n}}
		out, e := Restore(id, len(b), spans, map[[32]byte][]byte{id: b})
		if e == nil && !bytes.Equal(out, b) {
			t.Fatal("accepted wrong logical bytes")
		}
	})
}

func TestExactSHAAndRepeatedStageCosts(t *testing.T) {
	data := bytes.Repeat([]byte("abcdef"), 500)
	store := repz3.NewMemStore()
	id, _ := store.Put(data)
	o := admission.Observation{Case: "exact", Stage: "one", Class: "sqlite", Variant: "sqlite", Data: data, Store: store, Reach: &repz3.Reach{Order: []repz3.SHA{id}}}
	s := New()
	if e := s.Observe(o); e != nil {
		t.Fatal(e)
	}
	r := s.Rows[0]
	if r.ExactComponents != 1 || r.SpanDescriptors != 0 || r.CDC != r.Additive || r.CDC != r.Slices || r.CDC != r.Packs {
		t.Fatalf("exact duplicate charged: %+v", r)
	}
	o.Stage = "two"
	if e := s.Observe(o); e != nil {
		t.Fatal(e)
	}
	r2 := s.Rows[1]
	if r2.CDC != r.CDC || r2.Additive != r.Additive || r2.Slices != r.Slices || r2.Packs != r.Packs {
		t.Fatal("repeated costs grew")
	}
}
