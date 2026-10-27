package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"zeros3-z3/dexz3"
	"zeros3-z3/lab"
	"zeros3-z3/pdelta"
)

func init() { cmds["dexattr"] = dexAttr }

type dexRev struct {
	Name  string
	Files []string
	Raw   [][]byte
	F     []*dexz3.File
	W     []*dexz3.Walked
}

func loadRev(dir string) (*dexRev, error) {
	fs, err := filepath.Glob(filepath.Join(dir, "classes*.dex"))
	if len(fs) == 0 {
		fs, err = filepath.Glob(filepath.Join(dir, "dex", "classes*.dex"))
	}
	if err != nil || len(fs) == 0 {
		return nil, fmt.Errorf("no dex in %s", dir)
	}
	sort.Strings(fs)
	r := &dexRev{Name: filepath.Base(dir), Files: fs}
	for _, p := range fs {
		f, b, err := loadDex(p)
		if err != nil {
			return nil, err
		}
		w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		r.Raw, r.F, r.W = append(r.Raw, b), append(r.F, f), append(r.W, w)
	}
	return r, nil
}

// variant masks a copy of every dex of the revision.
func (r *dexRev) variant(sel func(*dexz3.Slot) bool) [][]byte {
	out := make([][]byte, len(r.Raw))
	for i, b := range r.Raw {
		c := append([]byte(nil), b...)
		if sel != nil {
			dexz3.Mask(c, r.W[i].Slots, sel)
		}
		out[i] = c
	}
	return out
}

type sel struct {
	name string
	f    func(*dexz3.Slot) bool
}

var isIdx = func(s *dexz3.Slot) bool { return s.Space != dexz3.SpOffset }
var isOff = func(s *dexz3.Slot) bool { return s.Space == dexz3.SpOffset }

var attrSels = []sel{
	{"raw", nil},
	{"idtab idx", func(s *dexz3.Slot) bool { return isIdx(s) && (s.Cls == dexz3.ClIDTab || s.Cls == dexz3.ClClassDef) }},
	{"insn idx", func(s *dexz3.Slot) bool { return isIdx(s) && s.Cls == dexz3.ClInsn }},
	{"classdata idx", func(s *dexz3.Slot) bool { return isIdx(s) && s.Cls == dexz3.ClClassData }},
	{"all idx", isIdx},
	{"offsets", isOff},
	{"idx+offsets", func(s *dexz3.Slot) bool { return true }},
}

// cdcNew returns the bytes of cur's chunks not present in base (each distinct chunk once).
func cdcNew(base, cur [][]byte, chunk func([]byte) [][]byte) (newB, total int) {
	have := map[[32]byte]bool{}
	for _, b := range base {
		for _, c := range chunk(b) {
			have[sha256.Sum256(c)] = true
		}
	}
	for _, b := range cur {
		total += len(b)
		for _, c := range chunk(b) {
			h := sha256.Sum256(c)
			if !have[h] {
				have[h] = true
				newB += len(c)
			}
		}
	}
	return
}

func prodChunks(b []byte) [][]byte {
	var out [][]byte
	lab.ChunkCDC(bytesReader(b), func(_ int64, c []byte) error { out = append(out, c); return nil })
	return out
}
func cdc4k(b []byte) [][]byte { return lab.ChunkScaled(b, 1024, 4096, 16384) }

func dexAttr(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dexattr OLDDIR NEWDIR [...]")
	}
	old, err := loadRev(args[0])
	if err != nil {
		return err
	}
	cur, err := loadRev(args[1])
	if err != nil {
		return err
	}
	fmt.Printf("pair %s -> %s (%d -> %d dex)\n", old.Name, cur.Name, len(old.Raw), len(cur.Raw))
	fmt.Printf("%-14s %9s %9s %9s %12s\n", "variant", "CDC %", "CDC-4K %", "delta %", "delta lit B")
	for _, s := range attrSels {
		ob, cb := old.variant(s.f), cur.variant(s.f)
		n1, t := cdcNew(ob, cb, prodChunks)
		n2, _ := cdcNew(ob, cb, cdc4k)
		var lit, cp int
		// pairwise delta against the concatenation of the old revision
		var base []byte
		for _, b := range ob {
			base = append(base, b...)
		}
		for _, b := range cb {
			st := pdelta.Cost(base, b)
			lit += st.Literal
			cp += st.Cost
		}
		fmt.Printf("%-14s %8.1f%% %8.1f%% %8.1f%% %12d\n", s.name, 100*float64(n1)/float64(t), 100*float64(n2)/float64(t), 100*float64(cp)/float64(t), lit)
	}
	return nil
}

var _ = os.Stdout
