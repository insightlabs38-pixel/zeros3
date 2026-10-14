package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"zeros3-z3/binref"
	"zeros3-z3/elfz3"
	"zeros3-z3/gopcln"
	"zeros3-z3/lab"
	"zeros3-z3/pez3"
)

const chunkRef = 96 // the laboratory's per-chunk reference cost (as in Z3-11)

type obj struct {
	key  [32]byte
	size int
}

// Rep is one representation of one binary: content-addressed objects plus
// accounting. Exact reconstruction is verified when Streams is set.
type Rep struct {
	Name    string
	Objs    []obj
	Meta    int // bytes of non-payload streams (references, tables, region table)
	Raw     int // bytes of opaque raw regions (informational)
	Refs    int // normalized references
	Stat    binref.Stats
	Exact   bool // reconstruction verified byte for byte (false for baselines/oracles that carry no recipe)
	Oracle  bool
	ParseNS int64
	NormNS  int64
	RecNS   int64
}

// cdcObjsP is the same gear CDC with scaled parameters (sensitivity only).
func cdcObjsP(b []byte, min, target, max int) []obj {
	bits := 0
	for 1<<bits < target {
		bits++
	}
	maskS, maskL := uint64(1)<<bits-1, uint64(1)<<(bits-1)-1
	var out []obj
	for len(b) > 0 {
		lim := len(b)
		if lim > max {
			lim = max
		}
		n := lim
		var h uint64
		for i := 0; i < lim; i++ {
			h = (h << 1) + lab.GearAt(b[i])
			sz := i + 1
			if sz < min {
				continue
			}
			m := maskL
			if sz < target {
				m = maskS
			}
			if h&m == 0 {
				n = sz
				break
			}
		}
		out = append(out, one(b[:n])...)
		b = b[n:]
	}
	return out
}

func cdcObjs(b []byte) []obj {
	var out []obj
	lab.ChunkCDC(bytes.NewReader(b), func(_ int64, ch []byte) error {
		out = append(out, obj{sha256.Sum256(ch), len(ch)})
		return nil
	})
	return out
}

func one(b []byte) []obj { return []obj{{sha256.Sum256(b), len(b)}} }

func fixedObjs(b []byte, n int) []obj {
	var out []obj
	for i := 0; i < len(b); i += n {
		out = append(out, one(b[i:min(i+n, len(b))])...)
	}
	return out
}

// Cost returns the bytes of cur's objects that base does not hold (each
// distinct object once) and the number of object references cur needs.
func Cost(base, cur []obj) (newBytes int, refs int) {
	have := map[[32]byte]bool{}
	for _, o := range base {
		have[o.key] = true
	}
	for _, o := range cur {
		if !have[o.key] {
			have[o.key] = true
			newBytes += o.size
		}
	}
	return newBytes, len(cur)
}

// bin is a loaded, parsed binary.
type bin struct {
	Name   string
	Data   []byte
	Im     *binref.Image
	Elf    *elfz3.Info
	Pcln   *gopcln.Table
	Format string
	Parse  int64 // ns
}

func loadBin(name string, data []byte) (*bin, error) {
	b := &bin{Name: name, Data: data}
	t0 := nowNS()
	var err error
	switch {
	case len(data) > 4 && string(data[:4]) == "\x7fELF":
		b.Format = "elf"
		b.Im, b.Elf, err = elfz3.Parse(data)
	case len(data) > 2 && string(data[:2]) == "MZ":
		b.Format = "pe"
		b.Im, err = pez3.Parse(data)
	default:
		err = fmt.Errorf("unknown binary format")
	}
	if err != nil {
		return nil, err
	}
	b.Pcln, _ = gopcln.Find(b.Im)
	b.Parse = nowNS() - t0
	return b, nil
}

// slotSet selects which candidate slots feed Normalize.
type slotSet struct {
	Name                               string
	Reloc, Table, Branch, RIP, Ptr, Go bool
}

func (b *bin) slots(s slotSet) []binref.Slot {
	var out []binref.Slot
	for _, sl := range b.Im.Slots {
		if (sl.Class == binref.ClassReloc && s.Reloc) || (sl.Class == binref.ClassTable && s.Table) {
			out = append(out, sl)
		}
	}
	if s.Go && b.Pcln != nil {
		out = append(out, b.Pcln.Slots...)
	}
	if s.Branch || s.RIP {
		for _, sl := range binref.ScanX86(b.Im) {
			if (sl.Class == binref.ClassBranch && s.Branch) || (sl.Class == binref.ClassRIP && s.RIP) {
				out = append(out, sl)
			}
		}
	}
	if s.Ptr {
		out = append(out, binref.ScanPtr(b.Im)...)
	}
	return out
}

var (
	setReloc  = slotSet{Name: "reloc", Reloc: true}
	setStruct = slotSet{Name: "reloc+struct", Reloc: true, Table: true, Go: true}
	setX86    = slotSet{Name: "x86", Branch: true, RIP: true}
	setFull   = slotSet{Name: "full", Reloc: true, Table: true, Branch: true, RIP: true, Ptr: true, Go: true}
)

// units chooses the naming scope: "sym" (symbol table), "pcln" (Go function
// table: names survive stripping), "call" (CALL targets only: no symbol
// information at all), or "auto" (first available in that order).
func (b *bin) units(mode string) []binref.Unit {
	if mode == "auto" {
		switch {
		case len(b.Im.Syms) > 0:
			mode = "sym"
		case b.Pcln != nil:
			mode = "pcln"
		default:
			mode = "call"
		}
	}
	switch mode {
	case "sym":
		return b.Im.UnitsFromSyms(false)
	case "pcln":
		if b.Pcln != nil {
			return b.Im.CleanUnits(b.Pcln.Units)
		}
	}
	return b.Im.UnitsFromBranches(binref.ScanX86(b.Im))
}

type cfg struct {
	Set   slotSet
	Ident binref.Ident
	Units string
}

func (c cfg) String() string { return c.Set.Name + "/" + c.Ident.String() + "/" + c.Units }

func normalize(b *bin, c cfg) (*binref.Norm, int64, error) {
	slots := b.slots(c.Set)
	units := b.units(c.Units)
	t0 := nowNS()
	n, err := binref.Normalize(b.Im, slots, units, c.Ident)
	return n, nowNS() - t0, err
}

func normRep(b *bin, c cfg, oracle bool) (*Rep, error) {
	n, ns, err := normalize(b, c)
	if err != nil {
		return nil, err
	}
	r := &Rep{Name: c.String(), Stat: n.Stat, Oracle: oracle, NormNS: ns}
	for _, s := range n.Streams {
		if s.Kind != "payload" {
			r.Meta += len(s.Data)
		}
		if oracle && s.Kind != "payload" {
			continue
		}
		r.Objs = append(r.Objs, cdcObjs(s.Data)...)
	}
	for _, c := range n.Stat.Accepted {
		r.Refs += c
	}
	if !oracle {
		t2 := nowNS()
		out, err := binref.Reconstruct(n.Streams)
		r.RecNS = nowNS() - t2
		if err != nil || len(out) != len(b.Data) || sha256.Sum256(out) != sha256.Sum256(b.Data) || !bytes.Equal(out, b.Data) {
			return nil, fmt.Errorf("%s: reconstruction mismatch (err=%v)", c, err)
		}
		r.Exact = true
	}
	return r, nil
}

// baselines over a parsed image.
func baselineReps(bn *bin) []*Rep {
	data, im := bn.Data, bn.Im
	reps := []*Rep{
		{Name: "raw CDC", Objs: cdcObjs(data)},
		{Name: "fixed 4 KiB", Objs: fixedObjs(data, 4096)},
	}
	if im != nil {
		var sec, secCDC []obj
		for _, r := range im.Regions {
			b := data[r.Off : r.Off+r.Len]
			sec = append(sec, one(b)...)
			secCDC = append(secCDC, cdcObjs(b)...)
		}
		reps = append(reps, &Rep{Name: "section", Objs: sec}, &Rep{Name: "section + CDC", Objs: secCDC})
		if us := im.UnitsFromSyms(true); len(us) > 0 {
			var fn []obj
			for _, r := range im.Regions {
				b := data[r.Off : r.Off+r.Len]
				if !r.Exec {
					fn = append(fn, cdcObjs(b)...)
					continue
				}
				cur := 0
				for _, u := range us {
					if u.VA < r.VA || u.VA >= r.VA+uint64(r.Len) {
						continue
					}
					o := int(u.VA - r.VA)
					if o > cur {
						fn = append(fn, one(b[cur:o])...)
					}
					e := min(o+int(u.Size), len(b))
					if e > cur {
						fn = append(fn, one(b[max(o, cur):e])...)
						cur = e
					}
				}
				if cur < len(b) {
					fn = append(fn, one(b[cur:])...)
				}
			}
			reps = append(reps, &Rep{Name: "function ranges", Objs: fn})
		}
	}
	for _, r := range reps {
		r.Exact = true // concatenation of the byte ranges
	}
	return reps
}
