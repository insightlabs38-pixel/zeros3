package gitz3

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Family is a revision family: a common linear prefix followed by a special
// final revision (or a branchy shape).
type Family struct {
	Name    string
	Special func(r *Rng, t Snap) Snap // final revision transform; nil = shape-only family
	Shape   string                    // "linear" (default) | "branchy"
}

func clonePath(t Snap, from, to string) Snap {
	n := t.Clone()
	n[to] = t[from]
	return n
}

// Families lists the Z3-16B revision families.
func Families() []Family {
	return []Family{
		{Name: "add-small", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			n["src/mod0/added.c"] = TextFile(r, 2<<10)
			return n
		}},
		{Name: "edit-text-begin", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			n["docs/big.txt"] = EditText(r, t["docs/big.txt"], 0.01, 3)
			return n
		}},
		{Name: "edit-text-mid", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			n["docs/big.txt"] = EditText(r, t["docs/big.txt"], 0.5, 3)
			return n
		}},
		{Name: "edit-text-end", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			n["docs/big.txt"] = EditText(r, t["docs/big.txt"], 0.99, 3)
			return n
		}},
		{Name: "edit-bin-sparse", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			n["assets/blob.bin"] = EditSparse(r, t["assets/blob.bin"], 40)
			return n
		}},
		{Name: "rename", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			delete(n, "src/mod0/file00.c")
			n["src/mod0/renamed.c"] = t["src/mod0/file00.c"]
			return n
		}},
		{Name: "dup-blob", Special: func(r *Rng, t Snap) Snap { return clonePath(t, "docs/big.txt", "docs/big-copy.txt") }},
		{Name: "tiny-churn", Special: func(r *Rng, t Snap) Snap {
			n := t.Clone()
			for i := 0; i < 300; i++ {
				n[fmt.Sprintf("data/new/%04d.txt", i)] = TextFile(r, 40+r.Intn(160))
			}
			for i := 0; i < 30; i++ {
				delete(n, fmt.Sprintf("data/tiny/%03d.txt", i))
			}
			return n
		}},
		{Name: "tree-restructure", Special: func(r *Rng, t Snap) Snap {
			n := Snap{}
			for p, b := range t {
				if len(p) > 4 && p[:4] == "src/" {
					n["lib/"+p[4:]] = b
				} else {
					n[p] = b
				}
			}
			return n
		}},
		{Name: "branchy", Shape: "branchy"},
	}
}

// FamilyHistory returns a history with `prefix` common revisions and then the
// family's final revision(s). The last commit index is len(Trees)-1.
func FamilyHistory(f Family, seed uint64, scale, prefix int) *History {
	h := LinearHistory(seed, scale, prefix)
	r := NewRng(seed ^ 0x5eed)
	if f.Shape == "branchy" {
		base := len(h.Trees) - 1
		bt := h.Trees[base]
		ta, tb := bt, bt
		ia, ib := base, base
		for i := 0; i < 5; i++ {
			ta = Evolve(r, ta)
			ia = h.Add(ta, ia)
			tb = Evolve(r, tb)
			ib = h.Add(tb, ib)
		}
		m := ta.Clone()
		for p, b := range tb {
			if _, same := bt[p]; !same || string(bt[p]) != string(b) {
				if string(ta[p]) == string(bt[p]) {
					m[p] = b
				}
			}
		}
		h.Add(m, ia, ib)
		return h
	}
	last := len(h.Trees) - 1
	h.Add(f.Special(r, h.Trees[last]), last)
	return h
}

// Prefix returns the history truncated to its first n commits.
func (h *History) Prefix(n int) *History {
	return &History{Trees: h.Trees[:n], Parents: h.Parents[:n]}
}

// ProvStep is one reproducibility measurement.
type ProvStep struct {
	Label     string
	Command   string
	PackSHA   string
	PackSize  int
	IdxSHA    string
	IdxSize   int
	Repeat    bool // identical command reproduced identical pack bytes
	RepeatIdx bool
	Tip       string   // refs/heads/main commit id of the imported history
	Commands  []string // git command lines run (after fixed -c pack.threads=1 -c gc.auto=0 -c pack.writeReverseIndex=false)
}

// Build creates a repository of the history prefix and applies repack specs in order.
func Build(work, name string, a Algo, h *History, specs ...RepackSpec) (*Repo, error) {
	r, err := NewRepo(filepath.Join(work, name), a)
	if err != nil {
		return nil, err
	}
	if err := r.ImportHistory(h); err != nil {
		return nil, err
	}
	for _, s := range specs {
		if s.Label == "unpack" {
			if err := r.Unpack(); err != nil {
				return nil, err
			}
			continue
		}
		if err := r.Repack(s); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Spec shorthands.
var (
	SpecDefault  = RepackSpec{Label: "default", Force: true}                   // git repack -a -d -f
	SpecReuse    = RepackSpec{Label: "reuse"}                                  // git repack -a -d (reuses existing deltas/objects)
	SpecIncr     = RepackSpec{Label: "incr", Incremental: true}                // pack only loose objects
	SpecUnpack   = RepackSpec{Label: "unpack"}                                 // explode to loose
	SpecReverse  = RepackSpec{Label: "reverse", Force: true, Reverse: true}    // reversed object list
	SpecNoDelta  = RepackSpec{Label: "nodelta", NoDelta: true}                 // window=0
	SpecW3D3     = RepackSpec{Label: "w3d3", Force: true, Window: 3, Depth: 3} //
	SpecW50D250  = RepackSpec{Label: "w50d250", Force: true, Window: 50, Depth: 250}
	SpecW10D10   = RepackSpec{Label: "w10d10", Force: true, Window: 10, Depth: 10}
	SpecRefDelta = RepackSpec{Label: "refdelta", Force: true, REFDelta: true}
)

func SpecLevel(n int) RepackSpec {
	return RepackSpec{Label: fmt.Sprintf("level%d", n), Force: true, NoReuseObject: true, Compression: n}
}

// Determinism repeats an identical build and records whether pack and index
// bytes reproduce under the same git, environment and command line.
func Determinism(work string, a Algo, h *History, spec ...RepackSpec) (ProvStep, error) {
	var st ProvStep
	var packs [2][]PackPair
	for i := 0; i < 2; i++ {
		r, err := Build(work, fmt.Sprintf("det%d", i), a, h, spec...)
		if err != nil {
			return st, err
		}
		if packs[i], err = r.PackPairs(); err != nil {
			return st, err
		}
		if i == 0 {
			st.Commands = append([]string(nil), r.Log...)
			if tip, err := r.Git(nil, "rev-parse", "refs/heads/main"); err == nil {
				st.Tip = strings.TrimSpace(string(tip))
			}
		}
	}
	if len(packs[0]) != 1 || len(packs[1]) != 1 {
		return st, fmt.Errorf("expected one pack, got %d/%d", len(packs[0]), len(packs[1]))
	}
	a0, b0 := packs[0][0], packs[1][0]
	st.PackSHA = fmt.Sprintf("%x", sha256.Sum256(a0.Pack))
	st.IdxSHA = fmt.Sprintf("%x", sha256.Sum256(a0.Idx))
	st.PackSize, st.IdxSize = len(a0.Pack), len(a0.Idx)
	st.Repeat = string(a0.Pack) == string(b0.Pack)
	st.RepeatIdx = string(a0.Idx) == string(b0.Idx)
	return st, nil
}

// Scratch returns the scratch directory (outside the repository).
func Scratch(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("Z3_WORK")
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "z3git")
	}
	return dir, os.MkdirAll(dir, 0o755)
}
