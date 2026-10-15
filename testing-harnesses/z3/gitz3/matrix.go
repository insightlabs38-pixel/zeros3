package gitz3

import (
	"crypto/sha256"
	"strings"
	"zeros3-z3/fstate"

	"zeros3-z3/lab"
)

// File, State, RepDef, Row, Ctx, Scenario and Result are the shared file-state
// laboratory types (package fstate); a Git state's object format is State.Tag.
type (
	File     = fstate.File
	State    = fstate.State
	RepDef   = fstate.RepDef
	Row      = fstate.Row
	Ctx      = fstate.Ctx
	Scenario = fstate.Scenario
	Result   = fstate.Result
)

// AlgoOf is the object format of a state.
func AlgoOf(s *State) Algo { return Algo(s.Tag) }

// gctx is the Git adapter's per-(state, representation) context (Ctx.X).
type gctx struct {
	Lab       *Lab
	PackPlans map[string]*lab.Plan // by pack base name, for the idx files that follow
}

func gc(c *Ctx) *gctx { return c.X.(*gctx) }

// StateOf captures a repository's pack and loose files.
func StateOf(r *Repo, label string) (*State, error) {
	st := &State{Label: label, Tag: int(r.Algo)}
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

// Lab caches parses and analyses across representations.
type Lab struct {
	fstate.Runner
	SectionK  int     // set layout section size (entries), 0 = default
	InlineMax int     // pieces shorter than this are inline in the recipe/table, 0 = default
	Slack     float64 // auto mode slack, 0 = default
	packs     map[[32]byte]*Pack
	ans       map[[32]byte]*PackAnalysis
}

func NewLab(refBytes int64, pack bool) *Lab {
	l := &Lab{packs: map[[32]byte]*Pack{}, ans: map[[32]byte]*PackAnalysis{}}
	l.RefBytes, l.Pack = refBytes, pack
	l.Order = kindOrder
	l.Setup = func(c *Ctx, st *State) { c.X = &gctx{Lab: l, PackPlans: map[string]*lab.Plan{}} }
	return l
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

func opts(c *Ctx, mode string) RepOpts {
	l := gc(c).Lab
	return RepOpts{Mode: mode, Has: c.Acct.Has, SectionK: l.SectionK, Slack: l.Slack, InlineMax: l.InlineMax}
}

func fallbackCDC(f File) (*lab.Plan, error)           { return fstate.FallbackCDC(f) }
func baseline(name string, f File) (*lab.Plan, error) { return fstate.Baseline(name, f) }

// BaselineReps are the whole-file baselines and the whole-state CDC.
func BaselineReps() []RepDef { return fstate.BaselineReps() }

// exactRep builds a representation: packs via build, idx derived when
// possible, loose via LoosePlan, anything else (or any failure) CDC.
func exactRep(name, mode string, build func(an *PackAnalysis, o RepOpts) *lab.Plan) RepDef {
	return RepDef{Name: name, Plan: func(c *Ctx, f File) (*lab.Plan, error) {
		switch f.Kind {
		case "pack":
			an, err := gc(c).Lab.Analysis(f, Algo(c.Tag))
			if err != nil {
				c.Counts["pack_fallback"]++
				c.Counts["fb_"+FallbackReason(err)]++
				return fallbackCDC(f) // unsupported / malformed / thin: ordinary bytes
			}
			c.Counts["pack_exact"]++
			pl := build(an, opts(c, mode))
			gc(c).PackPlans[strings.TrimSuffix(f.Name, ".pack")] = pl
			return pl, nil
		case "idx":
			pp := gc(c).PackPlans[strings.TrimSuffix(f.Name, ".idx")]
			if pp != nil {
				for _, sf := range c.Files {
					if sf.Name == strings.TrimSuffix(f.Name, ".idx")+".pack" {
						if p, err := gc(c).Lab.ParsedPack(sf, Algo(c.Tag)); err == nil {
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
			pl, err := LoosePlan(Algo(c.Tag), LooseFile{Path: f.Name, Data: f.Data}, opts(c, mode))
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
			p, err := gc(c).Lab.ParsedPack(f, Algo(c.Tag))
			if err != nil {
				return fallbackCDC(f)
			}
			return EntryWholePlan(p, opts(c, ""))
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
				lo, err := ParseLoose(Algo(c.Tag), f.Data, Limits{})
				if err != nil {
					return nil, err
				}
				pl := &lab.Plan{Oracle: true, Pieces: mkPieces(lo.Canon, 1, "native")}
				for k := range pl.Pieces {
					pl.Pieces[k].Via = true
				}
				if Algo(c.Tag) == SHA1 && len(pl.Pieces) > 0 && pl.Pieces[0].Comp != nil && !c.Acct.Has(pl.Pieces[0].Comp.ID) {
					pl.IndexBytes = 52
				}
				return pl, nil
			}
			p, err := gc(c).Lab.ParsedPack(f, Algo(c.Tag))
			if err != nil {
				return nil, err
			}
			return NativeObjPlan(p, opts(c, ""))
		}},
	}
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
