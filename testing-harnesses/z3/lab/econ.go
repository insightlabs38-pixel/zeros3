package lab

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// Z3-31: the family-economics glue that z3struct and z3sst each carried privately (agg /
// store / runFiles), extracted here so later representation studies share one copy. It
// adds no accounting of its own: every number comes from Acct.Add, and every non-oracle
// plan is verified by rebuilding the file from its serialized recipe and stored components.

// EconFile is one named file of a state.
type EconFile struct {
	Name string
	Data []byte
}

// EconRef is the Z3-18/29 convention for one component reference in a manifest.
const EconRef = 96

// EconAgg is the accounting of one state under one representation.
type EconAgg struct {
	Raw       int64            `json:"raw"`
	Pack      int64            `json:"pack"`      // packed new components + references + recipe (the cost)
	New       int64            `json:"new"`       // unpacked new component bytes
	PackedNew int64            `json:"packedNew"` // packed new component bytes
	Meta      int64            `json:"meta"`      // recipe bytes charged (head, no references)
	Refs      int              `json:"refs"`      // component references
	NewComps  int              `json:"newComps"`
	Fallback  int              `json:"fallback"` // files that fell back to CDC
	Files     int              `json:"files"`
	Classes   map[string]int64 `json:"classes,omitempty"`   // packed new bytes by component class
	ClassRefs map[string]int   `json:"classRefs,omitempty"` // references by component class
	Exact     bool             `json:"exact"`
	Err       string           `json:"err,omitempty"`
}

// Add accumulates o into a.
func (a *EconAgg) Add(o EconAgg) {
	a.Raw += o.Raw
	a.Pack += o.Pack
	a.New += o.New
	a.PackedNew += o.PackedNew
	a.Meta += o.Meta
	a.Refs += o.Refs
	a.NewComps += o.NewComps
	a.Fallback += o.Fallback
	a.Files += o.Files
	for k, v := range o.Classes {
		if a.Classes == nil {
			a.Classes = map[string]int64{}
		}
		a.Classes[k] += v
	}
	for k, v := range o.ClassRefs {
		if a.ClassRefs == nil {
			a.ClassRefs = map[string]int{}
		}
		a.ClassRefs[k] += v
	}
}

// EconRep plans one file under one representation. fallback is set when the representation
// could not express the file and the plan is ordinary CDC.
type EconRep struct {
	Name   string
	Oracle bool
	Plan   func(f EconFile) (pl *Plan, fallback bool, err error)
}

// EconStore is a component store plus the files (name and content) it already holds: an
// unchanged file costs nothing under every representation (the Z3-16/17 runner rule).
type EconStore struct {
	Ac   *Acct
	Seen map[string][32]byte
}

// NewEconStore returns an empty store with the production pack rule and 96 B references.
func NewEconStore() *EconStore {
	return &EconStore{NewAcct(EconRef, true), map[string][32]byte{}}
}

// Clone returns an independent copy.
func (s *EconStore) Clone() *EconStore {
	m := make(map[string][32]byte, len(s.Seen))
	for k, v := range s.Seen {
		m[k] = v
	}
	return &EconStore{s.Ac.Clone(), m}
}

// Run adds the plans of the files to the store.
func (st *EconStore) Run(rep EconRep, files []EconFile) EconAgg {
	ac := st.Ac
	a := EconAgg{Exact: true}
	for _, f := range files {
		a.Files++
		a.Raw += int64(len(f.Data))
		if h := sha256.Sum256(f.Data); st.Seen[f.Name] == h {
			continue
		} else {
			st.Seen[f.Name] = h
		}
		pl, fb, err := rep.Plan(f)
		if err != nil || pl == nil {
			a.Exact, a.Err = false, fmt.Sprint(err)
			continue
		}
		if fb {
			a.Fallback++
		}
		acc, err := ac.Add(pl)
		if err != nil {
			a.Exact, a.Err = false, err.Error()
			continue
		}
		a.Pack += acc.IncrementalPack
		a.New += acc.NewBytes
		a.PackedNew += acc.PackedNew
		a.Meta += acc.MetaBytes - EconRef*int64(acc.Components)
		a.Refs += acc.Components
		a.NewComps += acc.NewComponents
		for k, v := range acc.ClassPackedNew {
			if a.Classes == nil {
				a.Classes = map[string]int64{}
			}
			a.Classes[k] += v
		}
		for k, v := range acc.ClassRefs {
			if a.ClassRefs == nil {
				a.ClassRefs = map[string]int{}
			}
			a.ClassRefs[k] += v
		}
		if rep.Oracle || pl.Oracle {
			continue
		}
		if pl.Rebuild == nil {
			a.Exact, a.Err = false, "no reconstruction path"
			continue
		}
		var out bytes.Buffer
		if err := pl.Rebuild(&out, ac.Get); err != nil {
			a.Exact, a.Err = false, err.Error()
		} else if out.Len() != len(f.Data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(f.Data) || !bytes.Equal(out.Bytes(), f.Data) {
			a.Exact, a.Err = false, "rebuild differs: "+f.Name
		}
	}
	return a
}

// CDCPlan is the production CDC baseline plan of a file.
func CDCPlan(f EconFile, family string) (*Plan, error) {
	for _, r := range Reps() {
		if r.Name == "cdc" {
			return r.Analyze(&Input{Family: family, Format: family, Revision: f.Name, Src: BytesSource(f.Name, f.Data), Opts: DefaultOptions()})
		}
	}
	return nil, fmt.Errorf("lab: no cdc baseline")
}

// PackedSize is the production pack rule applied to b (DEFLATE, kept only if it saves 1/16).
func PackedSize(b []byte) int64 { return packedLen(b) }
