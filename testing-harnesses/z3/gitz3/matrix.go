package gitz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"zeros3-z3/lab"
)

// File is one file of a repository snapshot.
type File struct {
	Name string // path relative to objects/ ("pack/pack-X.pack", "ab/cdef...")
	Kind string // pack | idx | loose
	Data []byte
}

// State is a repository snapshot (the set of object files at one moment).
type State struct {
	Label string
	Algo  Algo
	Files []File
}

// StateOf captures a repository's pack and loose files.
func StateOf(r *Repo, label string) (*State, error) {
	st := &State{Label: label, Algo: r.Algo}
	pp, err := r.PackPairs()
	if err != nil {
		return nil, err
	}
	for _, p := range pp {
		st.Files = append(st.Files, File{Name: "pack/" + p.Name + ".pack", Kind: "pack", Data: p.Pack}, File{Name: "pack/" + p.Name + ".idx", Kind: "idx", Data: p.Idx})
	}
	lo, err := r.Loose()
	if err != nil {
		return nil, err
	}
	for _, l := range lo {
		st.Files = append(st.Files, File{Name: l.Path, Kind: "loose", Data: l.Data})
	}
	return st, nil
}

// Bytes is the total size of the snapshot's files.
func (s *State) Bytes() int64 {
	var n int64
	for _, f := range s.Files {
		n += int64(len(f.Data))
	}
	return n
}

// Lab caches parses and analyses across representations.
type Lab struct {
	RefBytes  int64
	Pack      bool    // estimate production pack compression of new components
	SectionK  int     // set layout section size (entries), 0 = default
	InlineMax int     // pieces shorter than this are inline in the recipe/table, 0 = default
	Slack     float64 // auto mode slack, 0 = default
	packs     map[[32]byte]*Pack
	ans       map[[32]byte]*PackAnalysis
}

func NewLab(refBytes int64, pack bool) *Lab {
	return &Lab{RefBytes: refBytes, Pack: pack, packs: map[[32]byte]*Pack{}, ans: map[[32]byte]*PackAnalysis{}}
}

func (l *Lab) ParsedPack(f File, a Algo) (*Pack, error) {
	k := sha256.Sum256(f.Data)
	if p, ok := l.packs[k]; ok {
		return p, nil
	}
	p, err := ParsePack(f.Data, a, Limits{})
	if err != nil {
		return nil, err
	}
	if _, err := p.Resolve(); err != nil {
		return nil, err
	}
	l.packs[k] = p
	return p, nil
}

func (l *Lab) Analysis(f File, a Algo) (*PackAnalysis, error) {
	k := sha256.Sum256(f.Data)
	if an, ok := l.ans[k]; ok {
		return an, nil
	}
	p, err := l.ParsedPack(f, a)
	if err != nil {
		return nil, err
	}
	an, err := AnalyzePack(p)
	if err != nil {
		return nil, err
	}
	l.ans[k] = an
	return an, nil
}

// Ctx is what a representation sees while planning one file.
type Ctx struct {
	Lab        *Lab
	Acct       *lab.Acct
	Algo       Algo
	PackPlans  map[string]*lab.Plan // by pack base name, for the idx files that follow
	Counts     map[string]int64     // per-state counters (derived idx, fallbacks) copied into Row.Detail
	stateFiles []File
}

// RepDef plans one file under a representation. Returning lab.ErrNotApplicable
// means the representation leaves that file to the fallback.
type RepDef struct {
	Name string
	Plan func(c *Ctx, f File) (*lab.Plan, error)
	// Oracle marks an estimate with no reconstruction path.
	Oracle bool
	// Snapshot, if set, plans the whole state as one object instead of file by file.
	Snapshot func(c *Ctx, st *State) (*lab.Plan, error)
}

// SnapshotCDC is the repository-snapshot baseline: the state's files, sorted by
// name, framed (u16 name length, name, u64 size, bytes) as one object and chunked
// with production CDC. Order and framing are part of the object.
func SnapshotCDC() RepDef {
	return RepDef{Name: "cdc-snapshot", Snapshot: func(c *Ctx, st *State) (*lab.Plan, error) {
		files := append([]File(nil), st.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		var b bytes.Buffer
		for _, f := range files {
			b.Write([]byte{byte(len(f.Name) >> 8), byte(len(f.Name))})
			b.WriteString(f.Name)
			var sz [8]byte
			for i := 0; i < 8; i++ {
				sz[7-i] = byte(len(f.Data) >> (8 * i))
			}
			b.Write(sz[:])
			b.Write(f.Data)
		}
		return baseline("cdc", File{Name: "snapshot", Kind: "snapshot", Data: b.Bytes()})
	}}
}

func (c *Ctx) opts(mode string) RepOpts {
	return RepOpts{Mode: mode, Has: c.Acct.Has, SectionK: c.Lab.SectionK, Slack: c.Lab.Slack, InlineMax: c.Lab.InlineMax}
}

// fallbackCDC is ordinary production-shaped CDC of the file's bytes: the
// terminal answer for anything a representation cannot or should not transform.
func fallbackCDC(f File) (*lab.Plan, error) { return baseline("cdc", f) }

func baseline(name string, f File) (*lab.Plan, error) {
	for _, r := range lab.Reps() {
		if r.Name == name {
			return r.Analyze(&lab.Input{Family: "git", Format: "git-" + f.Kind, Revision: f.Name, Src: lab.BytesSource(f.Name, f.Data), Opts: lab.DefaultOptions()})
		}
	}
	return nil, fmt.Errorf("no baseline %q", name)
}

// BaselineReps are the whole-file baselines.
func BaselineReps() []RepDef {
	var out []RepDef
	for _, n := range []string{"raw", "cdc", "fixed-64k", "fixed-4k"} {
		n := n
		out = append(out, RepDef{Name: n, Plan: func(c *Ctx, f File) (*lab.Plan, error) { return baseline(n, f) }})
	}
	return append(out, SnapshotCDC())
}

// exactRep builds a representation: packs via build, idx derived when
// possible, loose via LoosePlan, anything else (or any failure) CDC.
func exactRep(name, mode string, build func(an *PackAnalysis, o RepOpts) *lab.Plan) RepDef {
	return RepDef{Name: name, Plan: func(c *Ctx, f File) (*lab.Plan, error) {
		switch f.Kind {
		case "pack":
			an, err := c.Lab.Analysis(f, c.Algo)
			if err != nil {
				c.Counts["pack_fallback"]++
				c.Counts["fb_"+FallbackReason(err)]++
				return fallbackCDC(f) // unsupported / malformed / thin: ordinary bytes
			}
			c.Counts["pack_exact"]++
			pl := build(an, c.opts(mode))
			c.PackPlans[strings.TrimSuffix(f.Name, ".pack")] = pl
			return pl, nil
		case "idx":
			pp := c.PackPlans[strings.TrimSuffix(f.Name, ".idx")]
			if pp != nil {
				for _, sf := range c.stateFiles {
					if sf.Name == strings.TrimSuffix(f.Name, ".idx")+".pack" {
						if p, err := c.Lab.ParsedPack(sf, c.Algo); err == nil {
							if pl, ok := IdxPlan(p, f.Data, pp); ok {
								c.Counts["idx_derived"]++
								return pl, nil
							}
						}
					}
				}
			}
			c.Counts["idx_fallback"]++
			return fallbackCDC(f)
		case "loose":
			pl, err := LoosePlan(c.Algo, LooseFile{Path: f.Name, Data: f.Data}, c.opts(mode))
			if err != nil {
				return fallbackCDC(f)
			}
			return pl, nil
		}
		return fallbackCDC(f)
	}}
}

// ExactReps are the structural/exact representations in report order.
func ExactReps() []RepDef {
	return []RepDef{
		{Name: "gp-entry", Plan: func(c *Ctx, f File) (*lab.Plan, error) {
			if f.Kind != "pack" {
				return fallbackCDC(f)
			}
			p, err := c.Lab.ParsedPack(f, c.Algo)
			if err != nil {
				return fallbackCDC(f)
			}
			return EntryWholePlan(p, c.opts("")), nil
		}},
		exactRep("gp-zlib", "zlib", func(an *PackAnalysis, o RepOpts) *lab.Plan { pl, _ := an.SeqPlan(o); return pl }),
		exactRep("gp-plain", "plain", func(an *PackAnalysis, o RepOpts) *lab.Plan { pl, _ := an.SeqPlan(o); return pl }),
		exactRep("gp-auto", "auto", func(an *PackAnalysis, o RepOpts) *lab.Plan { pl, _ := an.SeqPlan(o); return pl }),
		exactRep("gp-set", "auto", func(an *PackAnalysis, o RepOpts) *lab.Plan { pl, _ := an.SetPlan(o); return pl }),
		exactRep("gp-set-plain", "plain", func(an *PackAnalysis, o RepOpts) *lab.Plan { pl, _ := an.SetPlan(o); return pl }),
		{Name: "floor-native-obj", Oracle: true, Plan: func(c *Ctx, f File) (*lab.Plan, error) {
			if f.Kind == "idx" {
				return &lab.Plan{Oracle: true}, nil
			}
			if f.Kind == "loose" {
				lo, err := ParseLoose(c.Algo, f.Data, Limits{})
				if err != nil {
					return nil, err
				}
				pl := &lab.Plan{Oracle: true, Pieces: mkPieces(lo.Canon, 1, "native")}
				for k := range pl.Pieces {
					pl.Pieces[k].Via = true
				}
				if c.Algo == SHA1 && len(pl.Pieces) > 0 && pl.Pieces[0].Comp != nil && !c.Acct.Has(pl.Pieces[0].Comp.ID) {
					pl.IndexBytes = 52
				}
				return pl, nil
			}
			p, err := c.Lab.ParsedPack(f, c.Algo)
			if err != nil {
				return nil, err
			}
			return NativeObjPlan(p, c.opts(""))
		}},
	}
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
	Packed     int64 // alias of CostPacked kept for table code
}

// RunStates feeds the states in order through one component store per
// representation (cumulative family accounting) and returns a row per
// (state, representation). Every accepted non-oracle plan is rebuilt from the
// store and compared by length and SHA-256 with the original file.
func (l *Lab) RunStates(states []*State, reps []RepDef) []Row {
	var rows []Row
	for _, rp := range reps {
		ac := lab.NewAcct(l.RefBytes, l.Pack)
		seen := map[string][32]byte{} // file name -> content hash already represented (unchanged files cost nothing)
		for _, st := range states {
			row := Row{State: st.Label, Rep: rp.Name, RawBytes: st.Bytes(), Exact: true, Oracle: rp.Oracle, Detail: map[string]int64{}}
			c := &Ctx{Lab: l, Acct: ac, Algo: st.Algo, PackPlans: map[string]*lab.Plan{}, Counts: map[string]int64{}, stateFiles: st.Files}
			files := append([]File(nil), st.Files...)
			sort.SliceStable(files, func(i, j int) bool { return kindOrder(files[i].Kind) < kindOrder(files[j].Kind) })
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
					}
				}
				if err != nil {
					row.Err, row.Exact = err.Error(), false
				}
				row.BuildMs = float64(time.Since(t0).Microseconds()) / 1000
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
					row.Err = err.Error()
					row.Exact = false
					continue
				}
				acc, err := ac.Add(pl)
				if err != nil {
					row.Err = err.Error()
					row.Exact = false
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
				if !rp.Oracle && pl.Rebuild != nil {
					// the recipe that is charged is the recipe that is used: serialise the piece
					// list (table layout), check its size against the accounting, deserialise it
					// and rebuild from the deserialised pieces only
					enc := MarshalPieces(pl.Pieces)
					if want := acc.Layouts["table"] + uvLen(uint64(CountListed(pl.Pieces))); int64(len(enc)) != want {
						row.Exact = false
						row.Err = fmt.Sprintf("recipe bytes %d, accounted %d (%s)", len(enc), want, f.Name)
					}
					dec, derr := UnmarshalPieces(enc)
					if derr != nil {
						row.Exact, row.Err = false, derr.Error()
					} else {
						pl.Pieces = dec
					}
					var out bytes.Buffer
					rerr := pl.Rebuild(&out, ac.Get)
					if rerr != nil || out.Len() != len(f.Data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(f.Data) {
						row.Exact = false
						if rerr != nil {
							row.Err = rerr.Error()
						} else {
							row.Err = "rebuild mismatch " + f.Name
						}
					}
				}
			}
			for k, v := range c.Counts {
				row.Detail[k] = v
			}
			row.BuildMs = float64(time.Since(t0).Microseconds()) / 1000
			rows = append(rows, row)
		}
	}
	return rows
}

func kindOrder(k string) int {
	switch k {
	case "pack":
		return 0
	case "idx":
		return 1
	}
	return 2
}

var _ = io.EOF
