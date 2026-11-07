package planner2

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"zeros3-z3/admission"
)

// LView is the read-only store state a lineage may consult when enumerating forms.
type LView interface {
	// StoredPrimary returns the live attached object of this lineage at stage k.
	StoredPrimary(lineage, stage int) (Cand, int, bool)
	Fanout(obj int) int
	DepthCap() int
	FanoutCap() int
}

// GroupAction is a measured whole-group reoptimisation (B3 optimize).
type GroupAction struct {
	Name, Scheme string
	BeforeTotal  int64
	AfterTotal   int64
	CPUMS        float64
	PeakRSSMiB   float64
}

// Lineage is a chronological family of related objects. Forms enumerates the exact
// candidates of one stage; Reuse/Sketch are the hidden truth behind simulated or
// real probes (reachable only through Env.Probe, never by policies directly).
type Lineage interface {
	Name() string
	Class() string
	Evidence() string
	Layer() string // "A" object frontier (Z3-36) | "B" captured Z4 series
	Len() int
	Logical(t int) int64
	Schemes() []SchemeInfo
	Forms(id, t int, scheme string, v LView) []Cand
	Reuse(t int, scheme string) float64
	Sketch(t int, scheme string) []string
	GroupActions() []GroupAction
	KeyHint() string
	SetKeyHint(string)
}

// ---------------------------------------------------------------- Layer A

// Z3Lineage adapts one Z3-36 frontier case (real object-level closures).
type Z3Lineage struct {
	C    admission.Case
	Corp *admission.Corpus
	hint string
}

func (z *Z3Lineage) Name() string                { return z.C.Name }
func (z *Z3Lineage) Class() string               { return z.C.Class }
func (z *Z3Lineage) Evidence() string            { return z.C.Evidence }
func (z *Z3Lineage) Layer() string               { return "A" }
func (z *Z3Lineage) Len() int                    { return len(z.C.Stages) }
func (z *Z3Lineage) KeyHint() string             { return z.hint }
func (z *Z3Lineage) SetKeyHint(h string)         { z.hint = h }
func (z *Z3Lineage) Logical(t int) int64         { return z.C.Stages[t].Size }
func (z *Z3Lineage) GroupActions() []GroupAction { return nil }

func (z *Z3Lineage) stage(t int, scheme string) (admission.Candidate, bool) {
	for _, p := range z.C.Stages[t].Candidates {
		if p.Name == scheme {
			return p, true
		}
	}
	return admission.Candidate{}, false
}

func (z *Z3Lineage) Schemes() []SchemeInfo {
	seen := map[string]bool{}
	var out []SchemeInfo
	for _, s := range z.C.Stages {
		for _, p := range s.Candidates {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			kind := "rep"
			if p.Name == CDC {
				kind = CDC
			}
			a4, a64, a1 := 12.0, 1.5, 0.8
			if kind == "rep" {
				a4, a64, a1 = 6.0, 1.6, 1.0
			}
			out = append(out, SchemeInfo{Name: p.Name, Family: "z3", Kind: kind, Dep: "none", AnalysisMiBs: 10, DecodeMiBs: 100,
				Amp4K: a4, Amp64K: a64, Amp1M: a1, Ready: true, AmpSrc: "modeled(class default)", StdSrc: "measured", IncSrc: "measured"})
		}
	}
	return out
}

func (z *Z3Lineage) Forms(id, t int, scheme string, _ LView) []Cand {
	p, ok := z.stage(t, scheme)
	if !ok {
		return nil
	}
	c := Cand{Scheme: scheme, Form: "z3", Family: "z3", Base: -1, Sample: p.Sample, LogicalBytes: z.C.Stages[t].Size,
		AnalysisMS: float64(p.AnalysisNS) / 1e6, ReadBytes: p.ReadBytes, DecodeMiBs: 100, Physical: "measured",
		SrcSHA: z.C.Stages[t].InputSHA, RootSHA: p.Root, Ready: true}
	for _, i := range p.Objects {
		o := z.Corp.Objects[i]
		c.Comps = append(c.Comps, CompRef{ID: CompID(o.SHA), Bytes: o.Packed})
	}
	for _, si := range z.Schemes() {
		if si.Name == scheme {
			c.Amp4K, c.Amp64K, c.Amp1M = si.Amp4K, si.Amp64K, si.Amp1M
		}
	}
	return []Cand{c}
}

// Reuse is unknown for Layer A (probes use real sketches instead).
func (z *Z3Lineage) Reuse(int, string) float64 { return -1 }
func (z *Z3Lineage) Sketch(t int, scheme string) []string {
	if p, ok := z.stage(t, scheme); ok {
		return p.Sample
	}
	return nil
}

// ---------------------------------------------------------------- Layer B

type seriesScheme struct {
	Name, Kind, Dep string
	DepthCap        int `json:"depth_cap"`
	Inc, Std        []int64
	AnalysisMiBs    float64 `json:"analysis_mibs"`
	DecodeMiBs      float64 `json:"decode_mibs"`
	Amp4K           float64 `json:"amp4k"`
	Amp64K          float64 `json:"amp64k"`
	Amp1M           float64 `json:"amp1m"`
	AmpSrc          string  `json:"amp_src"`
	StdSrc          string  `json:"std_src"`
	IncSrc          string  `json:"inc_src"`
}

type seriesHist struct {
	Name, Class, Evidence, Note string
	Source                      []string
	Stages                      []struct {
		Label   string
		Logical int64
	}
	Schemes      []seriesScheme
	GroupActions []struct {
		Name, Scheme string
		AfterTotal   int64   `json:"after_total"`
		BeforeTotal  int64   `json:"before_total"`
		CPUMS        float64 `json:"cpu_ms"`
		PeakRSSMiB   float64 `json:"peak_rss_mib"`
		Applies      bool
	} `json:"group_actions"`
}

// SeriesLineage is a captured Z4 stage-series history (B1, B3-B6). Forms are
// built from measured per-stage marginal bytes; a base must be a stored object.
type SeriesLineage struct {
	H    seriesHist
	hint string
}

type Opportunity struct {
	Name, Reason, Source string
	Ready                bool
}

type SeriesFile struct {
	Version       int
	Histories     []seriesHist
	Opportunities []Opportunity
}

func LoadSeries(r io.Reader) (*SeriesFile, []*SeriesLineage, error) {
	var f SeriesFile
	dec := json.NewDecoder(r)
	if err := dec.Decode(&f); err != nil {
		return nil, nil, err
	}
	if dec.More() {
		return nil, nil, fmt.Errorf("planner2: trailing data after series document")
	}
	if f.Version != 1 {
		return nil, nil, fmt.Errorf("planner2: series version %d", f.Version)
	}
	var out []*SeriesLineage
	seen := map[string]bool{}
	for _, h := range f.Histories {
		if seen[h.Name] || len(h.Stages) == 0 || len(h.Schemes) == 0 {
			return nil, nil, fmt.Errorf("planner2: malformed/duplicate history %q", h.Name)
		}
		seen[h.Name] = true
		hasCDC := false
		for _, s := range h.Schemes {
			if len(s.Inc) != len(h.Stages) || len(s.Std) != len(h.Stages) {
				return nil, nil, fmt.Errorf("planner2: %s/%s series length", h.Name, s.Name)
			}
			hasCDC = hasCDC || s.Name == CDC
			for t := range s.Inc {
				if s.Inc[t] < 0 || s.Std[t] < s.Inc[t] || s.Std[t] < 0 {
					return nil, nil, fmt.Errorf("planner2: %s/%s stage %d negative or inc>std", h.Name, s.Name, t)
				}
			}
		}
		if !hasCDC {
			return nil, nil, fmt.Errorf("planner2: %s lacks authoritative cdc scheme", h.Name)
		}
		hh := h
		out = append(out, &SeriesLineage{H: hh})
	}
	for _, o := range f.Opportunities {
		if o.Ready {
			return nil, nil, fmt.Errorf("planner2: opportunity %q must not be ready", o.Name)
		}
	}
	return &f, out, nil
}

func LoadSeriesFile(p string) (*SeriesFile, []*SeriesLineage, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer fh.Close()
	var r io.Reader = fh
	if strings.HasSuffix(p, ".gz") {
		g, err := gzip.NewReader(fh)
		if err != nil {
			return nil, nil, err
		}
		defer g.Close()
		r = g
	}
	return LoadSeries(r)
}

func (s *SeriesLineage) Name() string                { return s.H.Name }
func (s *SeriesLineage) Class() string               { return s.H.Class }
func (s *SeriesLineage) Evidence() string            { return s.H.Evidence }
func (s *SeriesLineage) Layer() string               { return "B" }
func (s *SeriesLineage) Len() int                    { return len(s.H.Stages) }
func (s *SeriesLineage) KeyHint() string             { return s.hint }
func (s *SeriesLineage) SetKeyHint(h string)         { s.hint = h }
func (s *SeriesLineage) Logical(t int) int64         { return s.H.Stages[t].Logical }
func (s *SeriesLineage) Sketch(int, string) []string { return nil }

func (s *SeriesLineage) scheme(name string) *seriesScheme {
	for i := range s.H.Schemes {
		if s.H.Schemes[i].Name == name {
			return &s.H.Schemes[i]
		}
	}
	return nil
}

func (s *SeriesLineage) Schemes() []SchemeInfo {
	var out []SchemeInfo
	for _, x := range s.H.Schemes {
		fam := "b1"
		switch {
		case strings.HasPrefix(x.Name, "b3"):
			fam = "b3"
		case strings.HasPrefix(x.Name, "b4"):
			fam = "b4"
		case strings.HasPrefix(x.Name, "b5"):
			fam = "b5"
		case strings.HasPrefix(x.Name, "b6"):
			fam = "b6"
		case x.Name == CDC:
			fam = "cdc"
		}
		out = append(out, SchemeInfo{Name: x.Name, Family: fam, Kind: x.Kind, Dep: x.Dep, DepthCap: x.DepthCap,
			AnalysisMiBs: x.AnalysisMiBs, DecodeMiBs: x.DecodeMiBs, Amp4K: x.Amp4K, Amp64K: x.Amp64K, Amp1M: x.Amp1M,
			Ready: true, AmpSrc: x.AmpSrc, StdSrc: x.StdSrc, IncSrc: x.IncSrc})
	}
	return out
}

func (s *SeriesLineage) GroupActions() []GroupAction {
	var out []GroupAction
	for _, g := range s.H.GroupActions {
		if g.Applies {
			out = append(out, GroupAction{Name: g.Name, Scheme: g.Scheme, BeforeTotal: g.BeforeTotal, AfterTotal: g.AfterTotal, CPUMS: g.CPUMS, PeakRSSMiB: g.PeakRSSMiB})
		}
	}
	return out
}

// Reuse is the hidden truth of how much of the std bytes the best earlier stage
// removes; it is exposed only through the (noisy, charged) probe model.
func (s *SeriesLineage) Reuse(t int, scheme string) float64 {
	x := s.scheme(scheme)
	if x == nil || t == 0 || x.Std[t] == 0 {
		return 0
	}
	return 1 - float64(x.Inc[t])/float64(x.Std[t])
}

func (s *SeriesLineage) comp(scheme string, t int, form string, bytes int64) CompRef {
	return CompRef{ID: CompID(fmt.Sprintf("%s|%s|%d|%s", s.H.Name, scheme, t, form)), Bytes: bytes}
}

// Forms enumerates exact forms for (stage t, scheme): a standalone form plus a
// delta form against each of up to three nearest stored bases of this scheme.
func (s *SeriesLineage) Forms(id, t int, scheme string, v LView) []Cand {
	x := s.scheme(scheme)
	if x == nil {
		return nil
	}
	logical := s.H.Stages[t].Logical
	mk := func(form string, own CompRef, base int, depth int, baseComps []CompRef) Cand {
		c := Cand{Scheme: scheme, Form: form, Family: s.Schemes()[0].Family, Base: base, Depth: depth,
			LogicalBytes: logical, DecodeMiBs: x.DecodeMiBs, Amp4K: x.Amp4K, Amp64K: x.Amp64K, Amp1M: x.Amp1M,
			Physical: "measured", SrcSHA: fmt.Sprintf("%s#%d", s.H.Name, t), RootSHA: string(own.ID), Ready: true}
		for _, sc := range s.Schemes() {
			if sc.Name == scheme {
				c.Family = sc.Family
			}
		}
		if strings.Contains(x.StdSrc, "modeled") && form == "std" && t > 0 {
			c.Physical = "modeled"
		}
		if form != "std" && !strings.HasPrefix(x.IncSrc, "measured") {
			c.Physical = "modeled"
		}
		c.AnalysisMS = float64(logical) / (1 << 20) / x.AnalysisMiBs * 1000
		if scheme == CDC {
			c.AnalysisMS = 0
		}
		c.ReadBytes = logical
		c.Comps = append([]CompRef{own}, baseComps...)
		return c
	}
	out := []Cand{mk("std", s.comp(scheme, t, "std", x.Std[t]), -1, 0, nil)}
	if v == nil || t == 0 {
		return out
	}
	found := 0
	for k := t - 1; k >= 0 && found < 3; k-- {
		bc, bid, ok := v.StoredPrimary(id, k)
		if !ok || bc.Scheme != scheme {
			continue
		}
		found++
		if x.Dep == "chain" {
			if bc.Depth+1 > min(x.DepthCap, v.DepthCap()) || v.Fanout(bid) >= v.FanoutCap() {
				continue
			}
		}
		var gap int64
		for j := k + 1; j <= t; j++ {
			gap += x.Inc[j]
		}
		if gap >= x.Std[t] {
			continue // a delta no smaller than standalone is never offered
		}
		var baseComps []CompRef
		depth := 0
		if x.Dep == "chain" {
			baseComps = bc.Comps
			depth = bc.Depth + 1
		} else if len(bc.Comps) > 0 {
			baseComps = bc.Comps[:1] // CAS pin model: the base's own component only (non-transitive)
		}
		out = append(out, mk(fmt.Sprintf("d%d", k), s.comp(scheme, t, fmt.Sprintf("d%d", k), gap), bid, depth, baseComps))
	}
	return out
}

// ---------------------------------------------------------------- helpers

// LoadFrontier loads and validates the Z3-36 frontier into Layer-A lineages.
func LoadFrontier(p string) (*admission.Corpus, []*Z3Lineage, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer fh.Close()
	g, err := gzip.NewReader(fh)
	if err != nil {
		return nil, nil, err
	}
	defer g.Close()
	var c admission.Corpus
	if err := json.NewDecoder(g).Decode(&c); err != nil {
		return nil, nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	var out []*Z3Lineage
	for _, f := range c.Cases {
		out = append(out, &Z3Lineage{C: f, Corp: &c, hint: f.KeyHint})
	}
	return &c, out, nil
}

// FoldOf assigns a lineage to a protocol fold by glob (z3 cases are prefixed "z3:").
func FoldOf(folds map[string][]string, l Lineage) string {
	name := l.Name()
	if l.Layer() == "A" {
		name = "z3:" + strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name, "no-hint/"), "one-off/"), "")
	}
	var keys []string
	for k := range folds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, pat := range folds[k] {
			if ok, _ := path.Match(pat, name); ok {
				return k
			}
		}
	}
	return ""
}
