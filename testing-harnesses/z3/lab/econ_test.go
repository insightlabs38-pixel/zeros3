package lab

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"
)

func fixedPlan(f EconFile, n int) *Plan {
	p := &Plan{Concat: true}
	for off := 0; off < len(f.Data); off += n {
		b := f.Data[off:min(off+n, len(f.Data))]
		bb := b
		p.Pieces = append(p.Pieces, Piece{Comp: &Comp{ID: ID(sha256.Sum256(b)), Size: int64(len(b)), Data: func() ([]byte, error) { return bb, nil }}, Len: int64(len(b))})
	}
	p.Rebuild = ConcatRebuild(p)
	return p
}

// The extracted family runner: shared components are paid once, an unchanged file is free,
// a broken rebuild is reported, and fallback plans are counted.
func TestEconStore(t *testing.T) {
	rep := EconRep{Name: "fixed", Plan: func(f EconFile) (*Plan, bool, error) { return fixedPlan(f, 1000), false, nil }}
	a := bytes.Repeat([]byte("abcdefghij"), 1000)
	b := append(append([]byte(nil), a...), bytes.Repeat([]byte("Z"), 1500)...)
	st := NewEconStore()
	first := st.Run(rep, []EconFile{{"a", a}})
	if !first.Exact || first.Refs != 10 || first.NewComps != 1 {
		t.Fatalf("first %+v", first) // the ten identical 1000-byte pieces are one component
	}
	if again := st.Run(rep, []EconFile{{"a", a}}); again.Pack != 0 || again.Refs != 0 {
		t.Fatalf("unchanged file cost %+v", again)
	}
	clone := st.Clone()
	second := clone.Run(rep, []EconFile{{"b", b}})
	if !second.Exact || second.NewComps != 2 || second.Refs != 12 {
		t.Fatalf("second %+v", second)
	}
	if st.Ac.Stored() != 1 {
		t.Fatal("Clone shares its store with the original")
	}
	bad := EconRep{Name: "bad", Plan: func(f EconFile) (*Plan, bool, error) {
		p := fixedPlan(f, 1000)
		p.Rebuild = func(w io.Writer, _ func(ID) ([]byte, error)) error { _, err := w.Write(f.Data[1:]); return err }
		return p, true, nil
	}}
	r := NewEconStore().Run(bad, []EconFile{{"c", a}})
	if r.Exact || r.Fallback != 1 {
		t.Fatalf("a rebuild that differs must be reported: %+v", r)
	}
}
