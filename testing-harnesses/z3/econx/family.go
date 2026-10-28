package econx

import (
	"bytes"
	"fmt"
	"io"

	"zeros3-z3/lab"
)

// Rep is one representation under test: a name and a planner for one file. The planner reports a
// fallback when the file was kept as plain CDC.
type Rep struct {
	Name string
	Plan func(f lab.EconFile) (*lab.Plan, bool, error)
}

// Rev is one revision of a family: its files in order.
type Rev struct {
	Name  string
	Files []lab.EconFile
}

// Opts are the accounting parameters of a run.
type Opts struct {
	TreeFan  int   // >0: component lists packed into index nodes of about that many digests
	RefBytes int64 // recipe cost of one component reference (default lab.EconRef = 96)
	Star     bool  // every revision after the first runs against a store holding only the first
}

// Result is the accounting of one representation over a family.
type Result struct {
	Name   string
	PerRev []lab.EconAgg
	Total  lab.EconAgg
	Exact  bool
	Err    string
}

// Run adds the revisions to a fresh store in order and returns the per-revision accounting. Every
// non-oracle plan is verified by rebuilding the file from its stored components (lab.EconStore).
func Run(revs []Rev, rep Rep, o Opts) Result {
	rb := o.RefBytes
	if rb == 0 {
		rb = lab.EconRef
	}
	st := &lab.EconStore{Ac: lab.NewAcct(rb, true), Seen: map[string][32]byte{}}
	var base *lab.EconStore
	res := Result{Name: rep.Name, Exact: true}
	for ri, rv := range revs {
		cur := st
		if o.Star && ri > 0 {
			cur = base.Clone()
		}
		a := cur.Run(lab.EconRep{Name: rep.Name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
			pl, fb, err := rep.Plan(f)
			return Treeify(pl, o.TreeFan), fb, err
		}}, rv.Files)
		if o.Star && ri == 0 {
			base = st
		}
		if !a.Exact {
			res.Exact, res.Err = false, a.Err
		}
		res.PerRev = append(res.PerRev, a)
		res.Total.Add(a)
	}
	return res
}

// ConcatPlan is a structure-only baseline: data cut at the given exclusive end offsets, rebuilt by
// concatenation.
func ConcatPlan(data []byte, cuts []int, class string) *lab.Plan {
	p := &lab.Plan{Concat: true}
	prev := 0
	for _, e := range cuts {
		if e > prev && e <= len(data) {
			p.Pieces = append(p.Pieces, lab.Piece{Comp: MkComp(data[prev:e], class), Len: int64(e - prev)})
			prev = e
		}
	}
	if prev < len(data) {
		p.Pieces = append(p.Pieces, lab.Piece{Comp: MkComp(data[prev:], class), Len: int64(len(data) - prev)})
	}
	p.Rebuild = lab.ConcatRebuild(p)
	return p
}

// RepPlan stores a representation as its meta record (inline) and its component bytes; the
// rebuild runs decode over the stored bytes only.
func RepPlan(meta []byte, comps [][]byte, class string, decode func(meta []byte, comps [][]byte) ([]byte, error)) *lab.Plan {
	pieces := PackPieces(meta, comps, class)
	p := &lab.Plan{Pieces: pieces}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		m, cs, err := UnpackPieces(pieces, get)
		if err != nil {
			return err
		}
		out, err := decode(m, cs)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
	return p
}

// Kib formats a byte count compactly.
func Kib(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<20:
		return fmt.Sprintf("%.2fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%dK", n>>10)
	default:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	}
}

// Same reports byte equality (used by self-checks).
func Same(a, b []byte) bool { return bytes.Equal(a, b) }
