package fstate

import (
	"bytes"
	"io"
	"strings"
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

// A non-oracle plan that cannot be reconstructed is a failed row, never an exact
// one; an oracle row needs no reconstruction; a plain concatenation of the pieces
// is a reconstruction path of its own.
func TestNonOracleWithoutRebuildFails(t *testing.T) {
	st := []*State{{Label: "s", Files: []File{{Name: "a", Kind: "x", Data: bytes.Repeat([]byte("hello world "), 5000)}}}}
	noRebuild := func(concat bool) func(*Ctx, File) (*lab.Plan, error) {
		return func(c *Ctx, f File) (*lab.Plan, error) {
			pl, err := FallbackCDC(f)
			if err != nil {
				return nil, err
			}
			pl.Rebuild, pl.Concat = nil, concat
			return pl, nil
		}
	}
	reps := []RepDef{
		{Name: "no-rebuild", Plan: noRebuild(false)},
		{Name: "oracle", Oracle: true, Plan: noRebuild(false)},
		{Name: "concat", Plan: noRebuild(true)},
		{Name: "snapshot-no-object", Snapshot: func(c *Ctx, st *State) (*lab.Plan, error) { return FallbackCDC(st.Files[0]) }},
	}
	got := map[string]Row{}
	for _, r := range (&Runner{RefBytes: 96}).RunStates(st, reps) {
		got[r.Rep] = r
	}
	if r := got["no-rebuild"]; r.Exact || !strings.Contains(r.Err, "no reconstruction path") {
		t.Errorf("non-oracle without Rebuild: %+v", r)
	}
	if r := got["oracle"]; !r.Exact || !r.Oracle || r.Err != "" {
		t.Errorf("oracle row: %+v", r)
	}
	if r := got["concat"]; !r.Exact || r.Err != "" {
		t.Errorf("concat plan: %+v", r)
	}
	if r := got["snapshot-no-object"]; r.Exact || r.Err == "" {
		t.Errorf("unverifiable snapshot accepted: %+v", r)
	}
}

// The baselines are reconstructed through their serialised recipe, and a corrupt
// recipe or a single wrong byte is caught.
func TestBaselinesRebuildThroughRecipe(t *testing.T) {
	data := bytes.Repeat([]byte("recipe round trip "), 9000)
	st := []*State{{Label: "s", Files: []File{{Name: "a", Kind: "x", Data: data}, {Name: "b", Kind: "x", Data: data[:777]}}}}
	for _, r := range (&Runner{RefBytes: 96}).RunStates(st, BaselineReps()) {
		if !r.Exact || r.Err != "" {
			t.Errorf("%s: %+v", r.Rep, r)
		}
	}
	flip := RepDef{Name: "flip", Plan: func(c *Ctx, f File) (*lab.Plan, error) {
		pl, err := FallbackCDC(f)
		if err != nil {
			return nil, err
		}
		pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
			b := append([]byte(nil), f.Data...)
			b[len(b)/2] ^= 1
			_, err := w.Write(b)
			return err
		}
		return pl, nil
	}}
	if r := (&Runner{RefBytes: 96}).RunStates(st, []RepDef{flip})[0]; r.Exact || !strings.Contains(r.Err, "mismatch") {
		t.Errorf("same-length corruption accepted: %+v", r)
	}
}
