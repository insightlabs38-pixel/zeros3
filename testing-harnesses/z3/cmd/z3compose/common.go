package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"zeros3-z3/composez3"
	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/ociz3"
	"zeros3-z3/sqlitez3"
	"zeros3-z3/tarz3"
)

func cacheRoot() string {
	if p := os.Getenv("Z3_COMPOSE_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "compose")
}

func kib(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.0fK", float64(n)/1024)
	}
	return fmt.Sprintf("%d", n)
}

// ---- representations over a layer-like file (Kind layer-gz) ----

func cdcRep() fstate.RepDef {
	return fstate.RepDef{Name: "cdc", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) { return fstate.Baseline("cdc", f) }}
}

// outerRep is the Z3-11/17 tar+gzip representation ("tarmem-s": member payloads whole;
// cdc: payloads of 64 KiB and more cut with CDC). Anything it cannot transform is CDC.
func outerRep(name string, cdc bool) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		if !ociz3.Guard(f) {
			return fstate.FallbackCDC(f)
		}
		p, err := ociz3.TransformPlan(f, cdc)
		if err != nil || p.Rebuild == nil {
			c.Counts["outer_fallback"]++
			return fstate.FallbackCDC(f)
		}
		return p, nil
	}}
}

// composedRep is the outer tar+gzip plan with the detected member payloads of the allowed
// kinds replaced by their inner representations.
func composedRep(name string, kinds ...composez3.Kind) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		p, rep, err := composePlan(f, kinds)
		if err != nil {
			c.Counts["compose_fallback"]++
			return fstate.FallbackCDC(f)
		}
		c.Counts["inner_substituted"] += int64(rep.Substituted)
		c.Counts["inner_fallback"] += int64(rep.Fallback)
		return p, nil
	}}
}

func composePlan(f fstate.File, kinds []composez3.Kind) (*lab.Plan, *composez3.Report, error) {
	if !ociz3.Guard(f) {
		return nil, nil, fmt.Errorf("guard")
	}
	outer, err := ociz3.TransformPlan(f, false)
	if err != nil || outer.Rebuild == nil {
		return nil, nil, fmt.Errorf("outer: %v", err)
	}
	p, rep, err := composez3.Compose(outer, composez3.Options{MinSize: 64 << 10, Kinds: kinds})
	if err != nil {
		return nil, nil, err
	}
	if a := composez3.Audit(p); len(a) > 0 {
		return nil, nil, fmt.Errorf("duplicate charging: %v", a)
	}
	return p, rep, nil
}

// innerRep stores a standalone object with the inner transform alone (the object is
// not inside any container); anything it does not apply to is CDC.
func innerRep(name string, k composez3.Kind) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		p, _, err := composez3.InnerPlan(k, f.Data)
		if err != nil {
			c.Counts["inner_fallback"]++
			return fstate.FallbackCDC(f)
		}
		return p, nil
	}}
}

// planCost estimates what a plan adds to the current store (production-packed new
// components, 34 B per reference, recipe/meta/index bytes): the per-object cheapest
// valid plan oracle uses it.
func planCost(has func(lab.ID) bool, pl *lab.Plan) int64 {
	var n int64
	seen := map[lab.ID]bool{}
	for _, pc := range pl.Pieces {
		switch {
		case pc.Comp == nil:
			n += int64(len(pc.Inline)) + 1
		case pc.Derived:
			n += 34
		case has(pc.Comp.ID) || seen[pc.Comp.ID]:
			if !pc.Via {
				n += 34
			}
		default:
			seen[pc.Comp.ID] = true
			sz := pc.Comp.Size
			if pc.Comp.Data != nil {
				if b, err := pc.Comp.Data(); err == nil {
					sz = lab.PackedLen(b)
				}
			}
			n += sz
			if !pc.Via {
				n += 34
			}
		}
	}
	return n + pl.MetaBytes + pl.IndexBytes
}

// cheapest is the per-object cheapest-valid-plan oracle over exact candidates.
func cheapest(name string, cands ...fstate.RepDef) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		var best *lab.Plan
		var bestName string
		var bestCost int64
		for _, cd := range cands {
			pl, err := cd.Plan(c, f)
			if err != nil || pl == nil || (pl.Rebuild == nil && !pl.Concat) {
				continue
			}
			if cost := planCost(c.Acct.Has, pl); best == nil || cost < bestCost {
				best, bestName, bestCost = pl, cd.Name, cost
			}
		}
		if best == nil {
			return nil, fmt.Errorf("no valid plan")
		}
		c.Counts["pick_"+bestName]++
		return best, nil
	}}
}

// admit is the implementable per-object heuristic: transform when it is cheaper under the
// current store, or when its first-sight cost (empty store) is within slack of CDC's (the
// premium for being able to find the plaintext when the next revision arrives).
func admit(name string, slack float64, plain, transformed fstate.RepDef) fstate.RepDef {
	none := func(lab.ID) bool { return false }
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		pc, err := plain.Plan(c, f)
		if err != nil {
			return nil, err
		}
		pt, err := transformed.Plan(c, f)
		if err != nil || pt == nil || pt.Rebuild == nil {
			c.Counts["pick_"+plain.Name]++
			return pc, nil
		}
		if planCost(c.Acct.Has, pt) < planCost(c.Acct.Has, pc) || float64(planCost(none, pt)) <= (1+slack)*float64(planCost(none, pc)) {
			c.Counts["pick_"+transformed.Name]++
			return pt, nil
		}
		c.Counts["pick_"+plain.Name]++
		return pc, nil
	}}
}

// ---- accounting split ----

type Split struct {
	Raw, Total                               int64
	Payload, OuterRecipe, InnerRecipe, Index int64
	Exact                                    bool
	Err                                      string           `json:",omitempty"`
	Picks                                    map[string]int64 `json:",omitempty"`
	Inner, InnerFallback, Deltas, Entries    int64
}

// splitOf separates a packed row cost into new payload/component bytes, the outer recipe
// (digest table + outer meta), the inner recipe (recipe objects + inner metadata streams)
// and indexes (the composition record); it must add up to the charged total.
func splitOf(r fstate.Row) Split {
	s := Split{Raw: r.RawBytes, Total: r.CostPacked, Exact: r.Exact && r.Err == "", Err: r.Err, Picks: map[string]int64{}}
	s.OuterRecipe, s.Index = r.Recipe+r.Meta, r.Index
	s.InnerRecipe = r.Detail["packed_"+composez3.ClassRecipe] + r.Detail["packed_"+composez3.ClassMeta]
	s.Payload = r.CostPacked - s.OuterRecipe - s.Index - s.InnerRecipe
	for k, v := range r.Detail {
		if len(k) > 5 && k[:5] == "pick_" {
			s.Picks[k[5:]] = v
		}
	}
	s.Inner, s.InnerFallback = r.Detail["inner_substituted"], r.Detail["inner_fallback"]
	s.Deltas, s.Entries = r.Detail["deltas"], r.Detail["entries"]
	return s
}

// Matrix holds Split per [rep][state].
type Matrix map[string]map[string]Split

func runMatrix(r *fstate.Runner, states []*fstate.State, reps []fstate.RepDef) (Matrix, error) {
	m := Matrix{}
	for _, row := range r.RunStates(states, reps) {
		if m[row.Rep] == nil {
			m[row.Rep] = map[string]Split{}
		}
		sp := splitOf(row)
		if row.Err != "" && !row.Oracle {
			fmt.Fprintf(os.Stderr, "ERR %s/%s: %s\n", row.State, row.Rep, row.Err)
		}
		m[row.Rep][row.State] = sp
	}
	return m, nil
}

func printCosts(title string, states []*fstate.State, reps []fstate.RepDef, m Matrix) {
	fmt.Printf("\n== %s (cost of each state added to a store holding the earlier ones; production pack rule; K=KiB M=MiB; ! = inexact)\n", title)
	fmt.Printf("%-22s %9s", "state", "raw")
	for _, rp := range reps {
		fmt.Printf(" %14s", rp.Name)
	}
	fmt.Println()
	for _, st := range states {
		fmt.Printf("%-22.22s %9s", st.Label, kib(st.Bytes()))
		for _, rp := range reps {
			x := m[rp.Name][st.Label]
			mark := ""
			if !x.Exact {
				mark = "!"
			}
			fmt.Printf(" %13s%1s", kib(x.Total), mark)
		}
		fmt.Println()
	}
}

func printSplit(title string, states []*fstate.State, rep string, m Matrix) {
	fmt.Printf("\n-- accounting split of %s: payload+component bytes | outer recipe | inner recipe | indexes/mapping | total\n", title)
	for _, st := range states {
		x := m[rep][st.Label]
		ok := ""
		if x.Payload+x.OuterRecipe+x.InnerRecipe+x.Index != x.Total {
			ok = "  SPLIT MISMATCH"
		}
		fmt.Printf("%-22.22s %10d | %8d | %8d | %6d | %10d  (inner objects %d, inner fallbacks %d)%s\n", st.Label, x.Payload, x.OuterRecipe, x.InnerRecipe, x.Index, x.Total, x.Inner, x.InnerFallback, ok)
	}
}

// classify says, per state after the first, which plan won and how the gains of the two
// transforms relate, all against plain CDC of the same top-level object: So (outer alone),
// Si (the inner transform on the loose artifact) and Sc (composed). Where the outer alone
// loses, the composition is complementary (the outer exposes the payload the inner reuses).
func classify(states []*fstate.State, m Matrix, outer, composed string, innerSave map[string]int64) []string {
	var out []string
	for i, st := range states {
		if i == 0 {
			continue
		}
		cdc, o, c := m["cdc"][st.Label].Total, m[outer][st.Label].Total, m[composed][st.Label].Total
		winner, best := "CDC", cdc
		if o < best {
			winner, best = "outer-only", o
		}
		if c < best {
			winner = "outer+inner"
		}
		so, si, sc := cdc-o, innerSave[st.Label], cdc-c
		rel := ""
		switch {
		case winner != "outer+inner":
			rel = "composition does not pay"
		case so <= 0:
			rel = "complementary: the outer alone loses, the inner alone gains, together they gain more"
		case sc*10 >= (so+si)*9:
			rel = "additive"
		case sc > max(so, si):
			rel = "overlapping"
		default:
			rel = "destructive"
		}
		out = append(out, fmt.Sprintf("%-22s wins: %-11s vs CDC: outer %s, inner-alone %s, composed %s -> %s", st.Label, winner, kib(so), kib(si), kib(sc), rel))
	}
	return out
}

// ---- layer helpers ----

func gzipLayer(members []ociz3.Member) (fstate.File, error) {
	tb, err := ociz3.WriteTar(members, tar.FormatPAX)
	if err != nil {
		return fstate.File{}, err
	}
	gz, err := ociz3.Compress(tb, ociz3.GzipOpts{})
	return fstate.File{Kind: string(ociz3.KindLayerGz), Data: gz}, err
}

// layerMembers inflates a gzip layer and returns the tar archive and its members.
func layerMembers(f fstate.File) ([]byte, *tarz3.Archive, error) {
	zr, err := gzip.NewReader(bytes.NewReader(f.Data))
	if err != nil {
		return nil, nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(zr, 512<<20))
	if err != nil {
		return nil, nil, err
	}
	a, err := tarz3.Parse(plain, tarz3.Options{})
	return plain, a, err
}

// ---- reads / execution cost ----

type ReadStats struct {
	Label                                 string
	Raw                                   int64
	AnalysisMBps, RebuildMBps             float64
	WholeComponents                       int
	WholeBytes                            int64
	MemberComponents                      int
	MemberBytes                           int64
	MemberMs                              float64
	CDCMemberBytes                        int64
	RangeNote                             string
	RecipeBytes, InnerRecipe, RecordBytes int64
	PeakRebuildMiB                        float64
}

type countingStore struct {
	get   func(lab.ID) ([]byte, error)
	seen  map[lab.ID]bool
	bytes int64
}

func newCounting(get func(lab.ID) ([]byte, error)) *countingStore {
	return &countingStore{get: get, seen: map[lab.ID]bool{}}
}

func (c *countingStore) Get(id lab.ID) ([]byte, error) {
	b, err := c.get(id)
	if err == nil && !c.seen[id] {
		c.seen[id] = true
		c.bytes += int64(len(b))
	}
	return b, err
}

func peakHeapDelta(fn func()) float64 {
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return float64(int64(peak.Load())-int64(m0.HeapInuse)) / (1 << 20)
}

// measureReads builds the composed plan for f in a fresh store, rebuilds the whole top-level
// object and reads one inner object (the substituted member with the largest payload).
func measureReads(label string, f fstate.File, kinds []composez3.Kind) (*ReadStats, error) {
	rs := &ReadStats{Label: label, Raw: int64(len(f.Data))}
	plain, _, err := layerMembers(f)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	p, rep, err := composePlan(f, kinds)
	if err != nil {
		return nil, err
	}
	rs.AnalysisMBps = float64(len(plain)) / time.Since(t0).Seconds() / 1e6
	acct := lab.NewAcct(96, false)
	acc, err := acct.Add(p)
	if err != nil {
		return nil, err
	}
	rs.RecipeBytes = acc.Layouts["table"] + acc.PlanMetaBytes
	rs.RecordBytes = acc.IndexBytes
	rs.InnerRecipe = acc.ClassNew[composez3.ClassRecipe] + acc.ClassNew[composez3.ClassMeta]
	cs := newCounting(acct.Get)
	var out bytes.Buffer
	t0 = time.Now()
	rs.PeakRebuildMiB = peakHeapDelta(func() { err = p.Rebuild(&out, cs.Get) })
	if err != nil {
		return nil, err
	}
	d := time.Since(t0)
	if sha256.Sum256(out.Bytes()) != sha256.Sum256(f.Data) {
		return nil, fmt.Errorf("composed rebuild of the top-level object differs")
	}
	rs.RebuildMBps = float64(len(f.Data)) / d.Seconds() / 1e6
	rs.WholeComponents, rs.WholeBytes = len(cs.seen), cs.bytes
	// one inner object: its recipe and every component reachable through it
	var rec *lab.Piece
	for i := range p.Pieces {
		pc := &p.Pieces[i]
		if pc.Comp != nil && pc.Comp.Class == composez3.ClassRecipe {
			rec = pc
			break
		}
	}
	if rec != nil && rep.Substituted > 0 {
		one := newCounting(acct.Get)
		kind := firstKind(rep)
		recipe, _ := one.Get(rec.Comp.ID)
		t0 = time.Now()
		var buf bytes.Buffer
		if err := composez3.RebuildInner(kind, &buf, recipe, one.Get); err != nil {
			return nil, err
		}
		rs.MemberMs = float64(time.Since(t0).Microseconds()) / 1000
		rs.MemberComponents, rs.MemberBytes = len(one.seen), one.bytes
		if kind == composez3.KindSQLite { // one 4 KiB page of the database inside the gzip layer
			pg := newCounting(acct.Get)
			t0 = time.Now()
			if _, err := sqlitez3.ReadPage(recipe, 2000, func(id sqlitez3.ID) ([]byte, error) { return pg.Get(lab.ID(id)) }, sqlitez3.Limits{}); err != nil {
				return nil, err
			}
			rs.RangeNote = fmt.Sprintf("one 4 KiB database page: %d comps, %s (%.2f ms)", len(pg.seen), kib(pg.bytes), float64(time.Since(t0).Microseconds())/1000)
		}
	}
	// plain CDC of the compressed layer: reading a member needs the gzip stream up to its end
	sizes, _ := lab.CDCSizes(bytes.NewReader(f.Data))
	rs.CDCMemberBytes = int64(len(f.Data))
	_ = sizes
	return rs, nil
}

func firstKind(r *composez3.Report) composez3.Kind {
	var ks []int
	for k := range r.ByKind {
		ks = append(ks, int(k))
	}
	sort.Ints(ks)
	return composez3.Kind(ks[0])
}

func printReads(rs []*ReadStats) {
	fmt.Println("\n-- reads / execution cost of the composed representation")
	for _, r := range rs {
		fmt.Printf("%-18s raw %s | analysis %.0f MB/s, rebuild %.0f MB/s, rebuild heap +%.0f MiB | whole: %d comps %s | one inner object: %d comps %s in %.1f ms (CDC: gzip stream, %s) | recipes: outer %d B, inner %d B, record %d B %s\n",
			r.Label, kib(r.Raw), r.AnalysisMBps, r.RebuildMBps, r.PeakRebuildMiB, r.WholeComponents, kib(r.WholeBytes), r.MemberComponents, kib(r.MemberBytes), r.MemberMs, kib(r.CDCMemberBytes), r.RecipeBytes, r.InnerRecipe, r.RecordBytes, r.RangeNote)
	}
}

func writeJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(v, "", " ")
	return os.WriteFile(path, b, 0o644)
}
