package fstate

import (
	"bytes"
	"io"
	"testing"

	"zeros3-z3/lab"
)

func TestRunnerSkipsUnchangedAndDetectsBadRebuild(t *testing.T) {
	a := File{Name: "a", Kind: "x", Data: bytes.Repeat([]byte("hello world "), 5000)}
	b := File{Name: "b", Kind: "x", Data: bytes.Repeat([]byte("other bytes "), 5000)}
	states := []*State{{Label: "one", Files: []File{a, b}}, {Label: "two", Files: []File{a, b}}}
	rows := (&Runner{RefBytes: 96, Pack: true}).RunStates(states, BaselineReps())
	for _, r := range rows {
		if r.Rep == "cdc" {
			if r.State == "one" && (r.NewBytes == 0 || !r.Exact) {
				t.Errorf("first state: %+v", r)
			}
			if r.State == "two" && (r.NewBytes != 0 || r.Cost != 0) {
				t.Errorf("an unchanged file name and content must cost nothing: %+v", r)
			}
		}
	}
	// a representation whose rebuild is wrong is flagged, not trusted
	bad := RepDef{Name: "bad", Plan: func(c *Ctx, f File) (*lab.Plan, error) {
		pl, err := FallbackCDC(f)
		if err != nil {
			return nil, err
		}
		pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
			_, err := w.Write(f.Data[:len(f.Data)-1])
			return err
		}
		return pl, nil
	}}
	rows = (&Runner{RefBytes: 96}).RunStates(states[:1], []RepDef{bad})
	if rows[0].Exact || rows[0].Err == "" {
		t.Errorf("wrong rebuild accepted: %+v", rows[0])
	}
}

func TestPiecesRoundTrip(t *testing.T) {
	pl, err := FallbackCDC(File{Name: "x", Data: bytes.Repeat([]byte("abc"), 100000)})
	if err != nil {
		t.Fatal(err)
	}
	enc := MarshalPieces(pl.Pieces)
	dec, err := UnmarshalPieces(enc)
	if err != nil || len(dec) != len(pl.Pieces) {
		t.Fatalf("%v %d/%d", err, len(dec), len(pl.Pieces))
	}
	for i := range dec {
		if dec[i].Comp.ID != pl.Pieces[i].Comp.ID {
			t.Fatalf("piece %d differs", i)
		}
	}
	for n := 0; n < len(enc); n++ {
		if _, err := UnmarshalPieces(enc[:n]); err == nil && n != len(enc) {
			// a prefix may decode only if it is a complete shorter list; it must never panic
			continue
		}
	}
}
