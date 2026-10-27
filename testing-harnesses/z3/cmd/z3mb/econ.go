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
	return cutter{fmt.Sprintf("unit-%dk", kib), unitCutBytes(kib << 10)}
}

func unitCutBytes(t int) func(p []byte, secs []dexz3.Sec) []int {
	return func(p []byte, secs []dexz3.Sec) []int {
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
	}
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

// treeFan > 0 packs the component list of every plan into content-defined index nodes of about
// that many 32-byte digests: the children are reachable through their node (Via, no recipe
// reference of their own) and an unchanged run of children is one unchanged node.
var treeFan = 0

// starMode runs every revision after the first against a store holding only the first.
var starMode = os.Getenv("Z3MB_STAR") != ""

// refBytes is the recipe cost of one component reference (lab convention 96).
var refBytes int64 = lab.EconRef

func newStore() *lab.EconStore {
	return &lab.EconStore{Ac: lab.NewAcct(refBytes, true), Seen: map[string][32]byte{}}
}

// packPieces turns a meta record and its components into plan pieces (flat).
func packPieces(meta []byte, comps [][]byte, class string) []lab.Piece {
	ps := []lab.Piece{{Inline: meta}}
	for _, c := range comps {
		ps = append(ps, lab.Piece{Comp: mkComp(c, class), Len: int64(len(c))})
	}
	return ps
}

// unpackPieces fetches the meta record and component bytes back from a flat piece list.
func unpackPieces(pieces []lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, [][]byte, error) {
	if len(pieces) == 0 || pieces[0].Comp != nil {
		return nil, nil, fmt.Errorf("missing meta piece")
	}
	var cs [][]byte
	for _, pc := range pieces[1:] {
		if pc.Comp == nil {
			return nil, nil, fmt.Errorf("unexpected inline piece")
		}
		b, err := get(pc.Comp.ID)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, b)
	}
	return pieces[0].Inline, cs, nil
}

func repPlan(rep *dexz3.Rep) *lab.Plan {
	pieces := packPieces(rep.Meta, rep.Comps, "dexn")
	p := &lab.Plan{Pieces: pieces}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		meta, cs, err := unpackPieces(pieces, get)
		if err != nil {
			return err
		}
		out, err := dexz3.Decode(meta, cs)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
	return p
}

// treeify returns p with its component pieces indexed by nodes (see treeFan). Rebuild first checks
// that every node lists exactly the digests its children have, then runs the original rebuild.
func treeify(p *lab.Plan, fan int) *lab.Plan {
	if fan <= 0 || p == nil {
		return p
	}
	np := *p
	np.Concat = false
	np.Pieces = append([]lab.Piece(nil), p.Pieces...)
	var elig []int
	for i, pc := range np.Pieces {
		if pc.Comp != nil && !pc.Via && !pc.Derived {
			elig = append(elig, i)
		}
	}
	if len(elig) < 8 {
		return p
	}
	type node struct {
		comp *lab.Comp
		ids  []byte
	}
	var nodes []node
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			nodes = append(nodes, node{mkComp(cur, "node"), cur})
			cur = nil
		}
	}
	n := 0
	for _, i := range elig {
		id := np.Pieces[i].Comp.ID
		cur = append(cur, id[:]...)
		np.Pieces[i].Via = true
		n++
		if (int(id[0])<<8|int(id[1]))%fan == 0 || n >= 4*fan {
			flush()
			n = 0
		}
	}
	flush()
	for _, nd := range nodes {
		np.Pieces = append(np.Pieces, lab.Piece{Comp: nd.comp, Len: nd.comp.Size})
	}
	inner := p.Rebuild
	np.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		for _, nd := range nodes {
			b, err := get(nd.comp.ID)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, nd.ids) {
				return fmt.Errorf("index node differs")
			}
		}
		return inner(w, get)
	}
	return &np
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
	all = append(all,
		econRep{"n-unit-4k-k2", dexPlan(dexz3.Config{K0: 2, Cut: unitCut(4).Cut})},
		econRep{"n-unit-4k-k4", dexPlan(dexz3.Config{K0: 4, Cut: unitCut(4).Cut})},
		econRep{"oracle-payload-unit-4k", payloadOracle(unitCut(4))})
	for _, c := range []cutter{cdcProd(), cdcScaled(64), cdcScaled(16), cdcScaled(4), sectionCDC(16), sectionCDC(4), unitCut(64), unitCut(16), unitCut(8), unitCut(4), unitCut(2), unitCut(1), perItem(dexz3.TypeCode)} {
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
		var base *lab.EconStore
		fmt.Printf("%-16s", rp.Name)
		var tot lab.EconAgg
		var bad string
		for ri, rv := range revs {
			cur := st
			if starMode && ri > 0 {
				cur = base.Clone()
			}
			a := cur.Run(lab.EconRep{Name: rp.Name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
				pl, fb, err := rp.Plan(f)
				return treeify(pl, treeFan), fb, err
			}}, rv.Files)
			if starMode && ri == 0 {
				base = st
			}
			if !a.Exact {
				bad = a.Err
			}
			fmt.Printf(" %9s", kib(a.Pack))
			tot.Add(a)
			if os.Getenv("Z3MB_V") != "" {
				defer func(n string, a lab.EconAgg) {
					fmt.Printf("      [%s] newComps %d refs %d meta %d packedNew %d classes %v refsBy %v\n", n, a.NewComps, a.Refs, a.Meta, a.PackedNew, a.Classes, a.ClassRefs)
				}(rv.Name, a)
			}
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

func errorsIs(err, target error) bool { return errors.Is(err, target) }

// payloadOracle prices only the masked payload groups (no reference streams, no recipe): a lower
// bound that cannot be decoded.
func payloadOracle(c cutter) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		df, err := dexz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), dexz3.Limits{})
		if err != nil {
			pl, e := lab.CDCPlan(f, "dex")
			return pl, true, e
		}
		rep, err := dexz3.Encode(df, f.Data, dexz3.Config{Cut: c.Cut})
		if err != nil {
			pl, e := lab.CDCPlan(f, "dex")
			return pl, true, e
		}
		p := &lab.Plan{Oracle: true}
		for _, cm := range rep.Comps {
			pay, _, _ := dexz3.SplitComp(cm)
			p.Pieces = append(p.Pieces, lab.Piece{Comp: mkComp(pay, "payload")})
		}
		return p, false, nil
	}
}
