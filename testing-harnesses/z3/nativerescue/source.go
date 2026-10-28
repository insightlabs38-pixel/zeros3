// Package nativerescue packages the existing binary normalization machinery
// into grouped exact research components. It defines no production kind.
package nativerescue

import (
	"fmt"
	"time"
	"zeros3-z3/binref"
	"zeros3-z3/elfz3"
	"zeros3-z3/gopcln"
	"zeros3-z3/pez3"
)

func nowNS() int64 { return time.Now().UnixNano() }

// Mechanically extracted from cmd/z3bin/rep.go; Mach-O remains optional.
type bin struct {
	Name   string
	Data   []byte
	Im     *binref.Image
	Elf    *elfz3.Info
	Pe     *pez3.Info
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
		b.Im, b.Pe, err = pez3.Parse(data)
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
		case b.Pe != nil && len(b.Pe.Funcs) > 0:
			mode = "pdata"
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
	case "pdata":
		if b.Pe != nil && len(b.Pe.Funcs) > 0 {
			return b.Im.CleanUnits(b.Pe.Funcs)
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
