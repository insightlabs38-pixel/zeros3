package admission

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func TestCaptureRejectsHiddenObjects(t *testing.T) {
	c := NewCapture()
	data := bytes.Repeat([]byte("exact capture"), 100)
	bundle, e := CDC(data)
	if e != nil {
		t.Fatal(e)
	}
	bundle.Objects = append(bundle.Objects, []byte("unreachable hidden write"))
	if _, e = c.Variant("bad", data, bundle, time.Now()); e == nil {
		t.Fatal("hidden storage uncharged")
	}
}
func TestPhysicalCaptureReloadAndCost(t *testing.T) {
	c := NewCapture()
	data := bytes.Repeat([]byte("physical frontier"), 1000)
	bundle, e := CDC(data)
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.Variant("cdc", data, bundle, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	f := Case{Name: "proof", Class: "concat", Evidence: "SYNTHETIC_FIXTURE", Stages: []Stage{{Label: "0", InputSHA: p.ProofSHA, Size: int64(len(data)), Candidates: []Candidate{p}}}}
	c.Corpus.Cases = []Case{f}
	if e = c.Corpus.Validate(); e != nil {
		t.Fatal(e)
	}
	r, e := c.Corpus.Run(f, "greedy")
	if e != nil {
		t.Fatal(e)
	}
	want := c.Corpus.cost(p, makeBits(len(c.Corpus.Objects)))
	if r.Total != want || !r.OracleExact {
		t.Fatal("physical cost drift")
	}
	// A generic DERIVED child shares exactly its stored closure and adds no
	// mutable root/catalog primitive.
	outer := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data)), Members: []repz3.Member{{Role: repz3.Derived, SHA: sha256.Sum256(data), Size: int64(len(data)), Child: bundle.ID}}}
	b, e := kinds.NewBundle(outer, nil, bundle)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Variant("derived", data, b, time.Now()); e != nil {
		t.Fatal(e)
	}
}
