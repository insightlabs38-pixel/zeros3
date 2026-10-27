package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/dexz3"
	"zeros3-z3/lab"
)

// Cutters choose the physical grouping of the masked payload (or of the raw file for baselines).
type cutter struct {
	Name string
	Cut  func(payload []byte, secs []dexz3.Sec) []int
}

func cutsOfChunks(chunks [][]byte, base int) []int {
	var out []int
	p := base
	for _, c := range chunks {
		p += len(c)
		out = append(out, p)
	}
	return out
}

func cdcProd() cutter {
	return cutter{"cdc-prod", func(p []byte, _ []dexz3.Sec) []int {
		var out []int
		lab.ChunkCDC(bytes.NewReader(p), func(off int64, c []byte) error { out = append(out, int(off)+len(c)); return nil })
		return out
	}}
}

func cdcScaled(kib int) cutter {
	return cutter{fmt.Sprintf("cdc-%dk", kib), func(p []byte, _ []dexz3.Sec) []int {
		t := kib << 10
		return cutsOfChunks(lab.ChunkScaled(p, t/4, t, t*4), 0)
	}}
}

// sectionCDC restarts the scaled CDC at every section boundary.
func sectionCDC(kib int) cutter {
	return cutter{fmt.Sprintf("seccdc-%dk", kib), func(p []byte, secs []dexz3.Sec) []int {
		t := kib << 10
		var out []int
		for _, s := range secs {
			out = append(out, cutsOfChunks(lab.ChunkScaled(p[s.Off:s.End], t/4, t, t*4), s.Off)...)
		}
		return out
	}}
}

func sections() cutter {
	return cutter{"sections", func(p []byte, secs []dexz3.Sec) []int {
		var out []int
		for _, s := range secs {
			out = append(out, s.End)
		}
		return out
	}}
}

func fnv(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

// unitCut groups consecutive items of an item section by a content-defined rule (cut after an
// item whose hash is 0 mod M, M = target / mean item size); sections without item boundaries
// (ID tables, gaps) use scaled CDC.
func unitCut(kib int) cutter {
	return cutter{fmt.Sprintf("unit-%dk", kib), func(p []byte, secs []dexz3.Sec) []int {
		t := kib << 10
		var out []int
		for _, s := range secs {
			if len(s.Items) < 2 {
				out = append(out, cutsOfChunks(lab.ChunkScaled(p[s.Off:s.End], t/4, t, t*4), s.Off)...)
				continue
			}
			m := uint64(t) * uint64(len(s.Items)) / uint64(s.End-s.Off)
			if m < 1 {
				m = 1
			}
			start := s.Off
			for i, st := range s.Items {
				end := s.End
				if i+1 < len(s.Items) {
					end = s.Items[i+1]
				}
				h := uint64(0)
				if s.Hash != nil {
					h = s.Hash[i]
				} else {
					h = fnv(p[st:end])
				}
				if h%m == 0 || end-start >= 4*t {
					out = append(out, end)
					start = end
				}
			}
			if start < s.End {
				out = append(out, s.End)
			}
		}
		return out
	}}
}

// perItem cuts at every item start (one component per code item / string / class_data...).
func perItem(types ...uint16) cutter {
	return cutter{"per-item", func(p []byte, secs []dexz3.Sec) []int {
		var out []int
		for _, s := range secs {
			use := false
			for _, t := range types {
				use = use || t == s.Type
			}
			if !use || len(s.Items) == 0 {
				out = append(out, s.End)
				continue
			}
			for i := range s.Items {
				if i+1 < len(s.Items) {
					out = append(out, s.Items[i+1])
				} else {
					out = append(out, s.End)
				}
			}
		}
		return out
	}}
}

func sum(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

func mkComp(b []byte, class string) *lab.Comp {
	return &lab.Comp{ID: sum(b), Size: int64(len(b)), Class: class, Data: func() ([]byte, error) { return b, nil }}
}

func isFallback(err error) bool {
	return errors.Is(err, dexz3.ErrFormat) || errors.Is(err, dexz3.ErrUnsupported) || errors.Is(err, dexz3.ErrLimit)
}

// rawCutPlan is a structure-only baseline: the raw file cut at the cutter's points (exact concat).
func rawCutPlan(c cutter, class string) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		df, err := dexz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), dexz3.Limits{})
		if err == nil {
			var w *dexz3.Walked
			if w, err = dexz3.Walk(df, f.Data, dexz3.WalkOpts{Offsets: true, Debug: true}); err == nil {
				cuts := c.Cut(f.Data, df.Sections(w, false))
				p := &lab.Plan{Concat: true}
				prev := 0
				for _, e := range cuts {
					if e > prev {
						p.Pieces = append(p.Pieces, lab.Piece{Comp: mkComp(f.Data[prev:e], class), Len: int64(e - prev)})
					}
					prev = e
				}
				if prev < len(f.Data) {
					p.Pieces = append(p.Pieces, lab.Piece{Comp: mkComp(f.Data[prev:], class), Len: int64(len(f.Data) - prev)})
				}
				p.Rebuild = lab.ConcatRebuild(p)
				return p, false, nil
			}
		}
		if !isFallback(err) {
			return nil, false, err
		}
		pl, err := lab.CDCPlan(f, "dex")
		return pl, true, err
	}
}

// dexPlan is the indexed-reference representation with the given grouping.
func dexPlan(cfg dexz3.Config) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		df, err := dexz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), dexz3.Limits{})
		if err == nil {
			var rep *dexz3.Rep
			if rep, err = dexz3.Encode(df, f.Data, cfg); err == nil {
				return repPlan(rep), false, nil
			}
		}
		if !isFallback(err) {
			return nil, false, err
		}
		pl, err := lab.CDCPlan(f, "dex")
		return pl, true, err
	}
}

// treeFan > 0 packs the component list into content-defined index nodes of about that many
// 32-byte digests: the children are reachable through their node (Via, no recipe reference of
// their own) and an unchanged run of children is one unchanged node.
var treeFan = 0

// refBytes is the recipe cost of one component reference (lab convention 96).
var refBytes int64 = lab.EconRef

func newStore() *lab.EconStore {
	return &lab.EconStore{Ac: lab.NewAcct(refBytes, true), Seen: map[string][32]byte{}}
}

func repPlan(rep *dexz3.Rep) *lab.Plan {
	return compPlan(rep.Meta, rep.Comps, "dexn", func(meta []byte, cs [][]byte) ([]byte, error) { return dexz3.Decode(meta, cs) })
}

func compPlan(meta []byte, comps [][]byte, class string, decode func(meta []byte, cs [][]byte) ([]byte, error)) *lab.Plan {
	p := &lab.Plan{}
	p.Pieces = append(p.Pieces, lab.Piece{Inline: meta})
	ids := make([]lab.ID, len(comps))
	for i, c := range comps {
		ids[i] = sum(c)
	}
	var top []lab.ID
	if treeFan > 0 {
		var cur []byte
		var members []int
		flush := func() {
			if len(members) == 0 {
				return
			}
			nd := mkComp(cur, "node")
			top = append(top, nd.ID)
			p.Pieces = append(p.Pieces, lab.Piece{Comp: nd, Len: nd.Size})
			for _, m := range members {
				p.Pieces = append(p.Pieces, lab.Piece{Comp: mkComp(comps[m], class), Len: int64(len(comps[m])), Via: true})
			}
			cur, members = nil, nil
		}
		for i, id := range ids {
			cur = append(cur, id[:]...)
			members = append(members, i)
			if (int(id[0])<<8|int(id[1]))%treeFan == 0 || len(members) >= 4*treeFan {
				flush()
			}
		}
		flush()
	} else {
		for i, c := range comps {
			p.Pieces = append(p.Pieces, lab.Piece{Comp: mkComp(c, class), Len: int64(len(c))})
			_ = i
		}
	}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		list := ids
		if treeFan > 0 {
			list = nil
			for _, t := range top {
				nb, err := get(t)
				if err != nil {
					return err
				}
				if len(nb)%32 != 0 {
					return fmt.Errorf("bad index node")
				}
				for o := 0; o < len(nb); o += 32 {
					var id lab.ID
					copy(id[:], nb[o:])
					list = append(list, id)
				}
			}
		}
		cs := make([][]byte, len(list))
		for i, id := range list {
			b, err := get(id)
			if err != nil {
				return err
			}
			cs[i] = b
		}
		out, err := decode(meta, cs)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
	return p
}

type econRep struct {
	Name string
	Plan func(f lab.EconFile) (*lab.Plan, bool, error)
}

func dexReps(names ...string) []econRep {
	all := []econRep{
		{"cdc", func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "dex"); return p, false, err }},
		{"raw-sections", rawCutPlan(sections(), "raw")},
		{"raw-codeitems", rawCutPlan(perItem(dexz3.TypeCode), "raw")},
		{"raw-unit-16k", rawCutPlan(unitCut(16), "raw")},
		{"raw-unit-4k", rawCutPlan(unitCut(4), "raw")},
	}
	for _, c := range []cutter{cdcProd(), cdcScaled(64), cdcScaled(16), cdcScaled(4), sectionCDC(16), sectionCDC(4), unitCut(64), unitCut(16), unitCut(8), unitCut(4), unitCut(2), perItem(dexz3.TypeCode)} {
		c := c
		all = append(all, econRep{"n-" + c.Name, dexPlan(dexz3.Config{Cut: c.Cut})})
	}
	if len(names) == 0 {
		return all
	}
	var out []econRep
	for _, n := range names {
		for _, r := range all {
			if r.Name == n {
				out = append(out, r)
			}
		}
	}
	return out
}

type revFiles struct {
	Name  string
	Files []lab.EconFile
}

func readDexDir(dir string) ([]lab.EconFile, error) {
	fs, _ := filepath.Glob(filepath.Join(dir, "classes*.dex"))
	if len(fs) == 0 {
		fs, _ = filepath.Glob(filepath.Join(dir, "dex", "classes*.dex"))
	}
	sort.Strings(fs)
	var out []lab.EconFile
	for _, p := range fs {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, lab.EconFile{Name: filepath.Base(p), Data: b})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no dex in %s", dir)
	}
	return out, nil
}

func kib(n int64) string {
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

// runFamily runs every rep over the revisions in order (one store per rep) and prints a table.
func runFamily(title string, revs []revFiles, reps []econRep) error {
	fmt.Printf("== %s\n%-16s", title, "rep")
	for _, r := range revs {
		fmt.Printf(" %9s", r.Name)
	}
	fmt.Printf(" %9s %7s %6s\n", "total", "newComp", "fb")
	for _, rp := range reps {
		st := newStore()
		fmt.Printf("%-16s", rp.Name)
		var tot lab.EconAgg
		var bad string
		for _, rv := range revs {
			a := st.Run(lab.EconRep{Name: rp.Name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { return rp.Plan(f) }}, rv.Files)
			if !a.Exact {
				bad = a.Err
			}
			fmt.Printf(" %9s", kib(a.Pack))
			tot.Add(a)
		}
		fmt.Printf(" %9s %7d %6d", kib(tot.Pack), tot.NewComps, tot.Fallback)
		if bad != "" {
			fmt.Printf("  NOT EXACT: %s", strings.TrimSpace(bad))
		}
		fmt.Println()
	}
	return nil
}

func init() {
	if v := os.Getenv("Z3MB_TREE"); v != "" {
		fmt.Sscan(v, &treeFan)
	}
	if v := os.Getenv("Z3MB_REF"); v != "" {
		fmt.Sscan(v, &refBytes)
	}
	cmds["dexecon"] = func(args []string) error {
		// dexecon NAME[,NAME] DIR...   (each DIR is one revision, in order)
		if len(args) < 2 {
			return fmt.Errorf("usage: dexecon REPS DIR...")
		}
		var names []string
		if args[0] != "all" {
			names = strings.Split(args[0], ",")
		}
		var revs []revFiles
		for _, d := range args[1:] {
			fs, err := readDexDir(d)
			if err != nil {
				return err
			}
			revs = append(revs, revFiles{filepath.Base(d), fs})
		}
		return runFamily(strings.Join(args[1:], " "), revs, dexReps(names...))
	}
}
