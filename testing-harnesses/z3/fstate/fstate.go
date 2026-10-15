// Package fstate is the Z3-16/17 file-state laboratory runner shared by the Git
// and OCI adapters. A State is a set of named files at one moment (a repository's
// object files, an image's blobs); a RepDef plans each file under a
// representation; Runner feeds a sequence of states through one component store
// per representation and reports, per state, the incremental physical cost.
//
// Every accepted non-oracle plan is checked the same way: its piece list is
// serialised in the lab's table layout (the bytes must equal the accounted
// recipe size), deserialised, and the original file is rebuilt from stored
// components only and compared by length, SHA-256 and bytes. A non-oracle plan
// with no reconstruction path is a row failure, never an exact row.
package fstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"zeros3-z3/lab"
)

// File is one file of a state.
type File struct {
	Name string // identity within the store; an unchanged name+content costs nothing
	Kind string // adapter-defined class ("pack", "idx", "layer", ...)
	Data []byte
}

// State is a labelled set of files. Tag is adapter data (e.g. object format).
type State struct {
	Label string
	Tag   int
	Files []File
}

// Bytes is the total size of the state's files.
func (s *State) Bytes() int64 {
	var n int64
	for _, f := range s.Files {
		n += int64(len(f.Data))
	}
	return n
}

// Ctx is what a representation sees while planning one file.
type Ctx struct {
	Acct   *lab.Acct
	Counts map[string]int64 // per-state counters copied into Row.Detail
	Files  []File           // every file of the state being planned
	Tag    int              // State.Tag
	X      any              // adapter per-(state, representation) context
}

// RepDef plans one file under a representation.
type RepDef struct {
	Name string
	Plan func(c *Ctx, f File) (*lab.Plan, error)
	// Oracle marks an estimate with no reconstruction path.
	Oracle bool
	// Snapshot, if set, plans the whole state as one object instead of file by file.
	Snapshot func(c *Ctx, st *State) (*lab.Plan, error)
	// Object returns the exact bytes a Snapshot plan represents; a non-oracle
	// snapshot representation without it cannot be verified and is a row failure.
	Object func(st *State) []byte
}

// Row is one (state, representation) measurement summed over the state's files.
type Row struct {
	State, Rep string
	RawBytes   int64
	NewBytes   int64 // new component bytes
	Recipe     int64 // table-layout recipe bytes
	Meta       int64 // plan meta bytes
	Index      int64
	Cost       int64 // lab.TableCost: new + recipe + meta + index
	CostPacked int64 // with the production pack rule on new components
	CostJSON   int64 // production manifest (chunkRef JSON) layout
	NewComps   int
	Comps      int
	Reused     int
	Exact      bool
	Oracle     bool
	Err        string
	BuildMs    float64
	Detail     map[string]int64
}

// Runner holds the accounting parameters and the adapter hooks.
type Runner struct {
	RefBytes int64
	Pack     bool // estimate production pack compression of new components
	// Order sorts files within a state (e.g. packs before their indexes); nil = given order.
	Order func(kind string) int
	// Setup creates the adapter context of one (state, representation).
	Setup func(c *Ctx, st *State)
}

// RunStates feeds the states in order through one component store per
// representation (cumulative family accounting) and returns a row per
// (state, representation).
func (r *Runner) RunStates(states []*State, reps []RepDef) []Row {
	var rows []Row
	for _, rp := range reps {
		ac := lab.NewAcct(r.RefBytes, r.Pack)
		seen := map[string][32]byte{} // file name -> content hash already represented
		for _, st := range states {
			row := Row{State: st.Label, Rep: rp.Name, RawBytes: st.Bytes(), Exact: true, Oracle: rp.Oracle, Detail: map[string]int64{}}
			c := &Ctx{Acct: ac, Counts: map[string]int64{}, Files: st.Files, Tag: st.Tag}
			if r.Setup != nil {
				r.Setup(c, st)
			}
			files := append([]File(nil), st.Files...)
			if r.Order != nil {
				sort.SliceStable(files, func(i, j int) bool { return r.Order(files[i].Kind) < r.Order(files[j].Kind) })
			}
			t0 := time.Now()
			if rp.Snapshot != nil {
				pl, err := rp.Snapshot(c, st)
				if err == nil {
					var acc lab.Account
					if acc, err = ac.Add(pl); err == nil {
						row.NewBytes, row.Recipe, row.Meta = acc.NewBytes, acc.Layouts["table"], acc.PlanMetaBytes
						row.Cost, row.CostJSON = lab.TableCost(acc), acc.NewBytes+acc.Layouts["json"]+acc.PlanMetaBytes
						if acc.PackedKnown {
							row.CostPacked = acc.PackedNew + acc.Layouts["table"] + acc.PlanMetaBytes
						}
						row.NewComps, row.Comps, row.Reused = acc.NewComponents, acc.Components, acc.ReusedComponents
						if !rp.Oracle {
							if rp.Object == nil {
								err = errors.New("snapshot representation has no Object to verify against")
							} else {
								err = verifyPlan(ac, pl, acc, rp.Object(st), "snapshot")
							}
						}
					}
				}
				if err != nil {
					row.Err, row.Exact = err.Error(), false
				}
				row.BuildMs = ms(time.Since(t0))
				rows = append(rows, row)
				continue
			}
			for _, f := range files {
				fh := sha256.Sum256(f.Data)
				if prev, ok := seen[f.Name]; ok && prev == fh {
					continue
				}
				seen[f.Name] = fh
				pl, err := rp.Plan(c, f)
				if err != nil {
					row.Err, row.Exact = err.Error(), false
					continue
				}
				acc, err := ac.Add(pl)
				if err != nil {
					row.Err, row.Exact = err.Error(), false
					continue
				}
				row.NewBytes += acc.NewBytes
				row.Recipe += acc.Layouts["table"]
				row.Meta += acc.PlanMetaBytes
				row.Index += acc.IndexBytes
				row.Cost += lab.TableCost(acc)
				row.CostJSON += acc.NewBytes + acc.Layouts["json"] + acc.PlanMetaBytes + acc.IndexBytes
				if acc.PackedKnown {
					row.CostPacked += acc.PackedNew + acc.Layouts["table"] + acc.PlanMetaBytes + acc.IndexBytes
				}
				row.NewComps += acc.NewComponents
				row.Comps += acc.Components
				row.Reused += acc.ReusedComponents
				for k, v := range acc.ClassNew {
					row.Detail["new_"+k] += v
				}
				for k, v := range acc.ClassPackedNew {
					row.Detail["packed_"+k] += v
				}
				if !rp.Oracle {
					if verr := verifyPlan(ac, pl, acc, f.Data, f.Name); verr != nil {
						row.Exact, row.Err = false, verr.Error()
					}
				}
			}
			for k, v := range c.Counts {
				row.Detail[k] = v
			}
			row.BuildMs = ms(time.Since(t0))
			rows = append(rows, row)
		}
	}
	return rows
}

// verifyPlan enforces the exactness invariant for one accepted non-oracle plan:
// the recipe that is charged is the recipe that is used. The piece list is
// serialised (table layout), its size checked against the accounting, deserialised,
// and the object rebuilt from the deserialised pieces and the stored components
// only; length, SHA-256 and bytes must equal want. A plan with no reconstruction
// path (no Rebuild and not a plain concatenation of its pieces) is a failure, never
// an exact row.
func verifyPlan(ac *lab.Acct, pl *lab.Plan, acc lab.Account, want []byte, name string) error {
	rebuild := pl.Rebuild
	if rebuild == nil && pl.Concat {
		rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error { return concatPieces(w, pl.Pieces, get) }
	}
	if rebuild == nil {
		return fmt.Errorf("no reconstruction path (%s)", name)
	}
	enc := MarshalPieces(pl.Pieces)
	if w := acc.Layouts["table"] + uvLen(uint64(CountListed(pl.Pieces))); int64(len(enc)) != w {
		return fmt.Errorf("recipe bytes %d, accounted %d (%s)", len(enc), w, name)
	}
	dec, err := UnmarshalPieces(enc)
	if err != nil {
		return err
	}
	pl.Pieces = dec
	var out bytes.Buffer
	if err := rebuild(&out, ac.Get); err != nil {
		return err
	}
	if out.Len() != len(want) || sha256.Sum256(out.Bytes()) != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		return fmt.Errorf("rebuild mismatch %s", name)
	}
	return nil
}

func concatPieces(w io.Writer, ps []lab.Piece, get func(lab.ID) ([]byte, error)) error {
	for _, p := range ps {
		b := p.Inline
		if p.Comp != nil {
			var err error
			if b, err = get(p.Comp.ID); err != nil {
				return err
			}
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// ---- baselines (the lab's own representations over a file's bytes) ----

// Baseline plans f with the named lab representation (raw, cdc, fixed-64k, ...).
func Baseline(name string, f File) (*lab.Plan, error) {
	for _, r := range lab.Reps() {
		if r.Name == name {
			return r.Analyze(&lab.Input{Family: "fstate", Format: "fstate-" + f.Kind, Revision: f.Name, Src: lab.BytesSource(f.Name, f.Data), Opts: lab.DefaultOptions()})
		}
	}
	return nil, fmt.Errorf("no baseline %q", name)
}

// FallbackCDC is ordinary production-shaped CDC of the file's bytes: the terminal
// answer for anything a representation cannot or should not transform.
func FallbackCDC(f File) (*lab.Plan, error) { return Baseline("cdc", f) }

// BaselineReps are the whole-file baselines plus the whole-state CDC.
func BaselineReps() []RepDef {
	var out []RepDef
	for _, n := range []string{"raw", "cdc", "fixed-64k", "fixed-4k"} {
		n := n
		out = append(out, RepDef{Name: n, Plan: func(c *Ctx, f File) (*lab.Plan, error) { return Baseline(n, f) }})
	}
	return append(out, SnapshotCDC())
}

// SnapshotCDC is the state-as-one-object baseline: the files sorted by name,
// framed (u16 name length, name, u64 size, bytes), chunked with production CDC.
func SnapshotCDC() RepDef {
	return RepDef{Name: "cdc-snapshot", Object: snapshotObject, Snapshot: func(c *Ctx, st *State) (*lab.Plan, error) {
		return Baseline("cdc", File{Name: "snapshot", Kind: "snapshot", Data: snapshotObject(st)})
	}}
}

func snapshotObject(st *State) []byte {
	files := append([]File(nil), st.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var b bytes.Buffer
	for _, f := range files {
		b.Write([]byte{byte(len(f.Name) >> 8), byte(len(f.Name))})
		b.WriteString(f.Name)
		var sz [8]byte
		binary.BigEndian.PutUint64(sz[:], uint64(len(f.Data)))
		b.Write(sz[:])
		b.Write(f.Data)
	}
	return b.Bytes()
}
