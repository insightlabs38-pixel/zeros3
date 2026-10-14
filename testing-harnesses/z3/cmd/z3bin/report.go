package main

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"zeros3-z3/binref"
)

// memo caches per-binary work so the pair matrix does not redo it.
type memo struct {
	bins  map[string]*bin
	reps  map[string][]*Rep
	norms map[string]*binref.Norm
	objs  map[string][]obj
	reprs map[string]*Rep
}

func newMemo() *memo {
	return &memo{map[string]*bin{}, map[string][]*Rep{}, map[string]*binref.Norm{}, map[string][]obj{}, map[string]*Rep{}}
}

func (m *memo) bin(path string) *bin {
	if b, ok := m.bins[path]; ok {
		return b
	}
	b := mustLoad(path)
	m.bins[path] = b
	return b
}

func (m *memo) norm(b *bin, c cfg) *binref.Norm {
	k := b.Name + "|" + c.String()
	if n, ok := m.norms[k]; ok {
		return n
	}
	n, _, err := normalize(b, c)
	if err != nil {
		fatal(err)
	}
	m.norms[k] = n
	return n
}

func (m *memo) rep(b *bin, c cfg, oracle bool) *Rep {
	k := fmt.Sprint(b.Name, "|", c, "|", oracle)
	if r, ok := m.reprs[k]; ok {
		return r
	}
	r, err := normRep(b, c, oracle)
	if err != nil {
		fatal(err)
	}
	m.reprs[k] = r
	return r
}

func (m *memo) base(b *bin) []*Rep {
	if r, ok := m.reps[b.Name]; ok {
		return r
	}
	r := baselineReps(b)
	// 4 KiB-average CDC sensitivity row (min 1 KiB, max 16 KiB)
	r = append(r, &Rep{Name: "CDC 4K-avg", Objs: cdcObjsP(b.Data, 1024, 4096, 16384), Exact: true})
	m.reps[b.Name] = r
	return r
}

func repByName(rs []*Rep, n string) *Rep {
	for _, r := range rs {
		if r.Name == n {
			return r
		}
	}
	return nil
}

func (m *memo) normObjs4k(b *bin, c cfg) []obj {
	k := b.Name + "|4k|" + c.String()
	if o, ok := m.objs[k]; ok {
		return o
	}
	var out []obj
	for _, s := range m.norm(b, c).Streams {
		out = append(out, cdcObjsP(s.Data, 1024, 4096, 16384)...)
	}
	m.objs[k] = out
	return out
}

type pairSpec struct{ label, base, cur string }

var pairSpecs = []pairSpec{
	{"const+string r0→r1", "r0", "r1"},
	{"add function r1→r2", "r1", "r2"},
	{"change body r2→r3", "r2", "r3"},
	{"remove function r2→r1", "r2", "r1"},
	{"early insert r2→early", "r2", "early"},
	{"reorder r2→swap", "r2", "swap"},
	{"data table r2→data", "r2", "data"},
	{"new packages r2→deps", "r2", "deps"},
}

func have(dir, goos, arch, flags string, revs ...string) bool {
	for _, r := range revs {
		if _, err := os.Stat(filepath.Join(dir, goos+"-"+arch, flags, r+ext(goos))); err != nil {
			return false
		}
	}
	return true
}

func ext(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

func fp(x float64) string { return fmt.Sprintf("%.1f", x) }

// matrixTable prints, for every pair, the new bytes as % of cur for each representation.
func (m *memo) matrixTable(dir, goos, arch, flags, units string, pairs []pairSpec) {
	p := func(rev string) *bin { return m.bin(filepath.Join(dir, goos+"-"+arch, flags, rev+ext(goos))) }
	full := cfg{setFull, binref.IdentSymbol, units}
	reloc := cfg{setStruct, binref.IdentSymbol, units}
	x86 := cfg{setX86, binref.IdentSymbol, units}
	hasFR := len(p("r0").Im.UnitsFromSyms(true)) > 0
	hdr := "| pair | raw CDC | fixed 4K | section | sect+CDC |"
	sep := "|---|--:|--:|--:|--:|"
	if hasFR {
		hdr += " func ranges |"
		sep += "--:|"
	}
	hdr += " reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |"
	sep += "--:|--:|--:|--:|--:|--:|--:|--:|"
	fmt.Println(hdr)
	fmt.Println(sep)
	for _, ps := range pairs {
		if !have(dir, goos, arch, flags, ps.base, ps.cur) {
			continue
		}
		b, c := p(ps.base), p(ps.cur)
		n := len(c.Data)
		bb, cb := m.base(b), m.base(c)
		cell := func(name string) string {
			nb, _ := Cost(repByName(bb, name).Objs, repByName(cb, name).Objs)
			return fp(pct(nb, n))
		}
		nc := func(cf cfg, oracle bool) string {
			nb, _ := Cost(m.rep(b, cf, oracle).Objs, m.rep(c, cf, oracle).Objs)
			return fp(pct(nb, n))
		}
		row := fmt.Sprintf("| %s | %s | %s | %s | %s |", ps.label, cell("raw CDC"), cell("fixed 4 KiB"), cell("section"), cell("section + CDC"))
		if hasFR {
			row += " " + cell("function ranges") + " |"
		}
		nb4, _ := Cost(repByName(bb, "CDC 4K-avg").Objs, repByName(cb, "CDC 4K-avg").Objs)
		f4, _ := Cost(m.normObjs4k(b, full), m.normObjs4k(c, full))
		dr := deltaCost(b.Data, c.Data)
		dp, dm := normDelta(m.norm(b, full), m.norm(c, full))
		row += fmt.Sprintf(" %s | %s | %s | %s | %s | %s | %s | %s |", nc(reloc, false), nc(x86, false), nc(full, false), nc(full, true), fp(pct(nb4, n)), fp(pct(f4, n)), fp(pct(dr.Cost, n)), fp(pct(dp+dm, n)))
		fmt.Println(row)
	}
}

// identTable compares target identities on selected pairs.
func (m *memo) identTable(dir, goos, arch, flags string, units []string, pairs []pairSpec) {
	p := func(rev string) *bin { return m.bin(filepath.Join(dir, goos+"-"+arch, flags, rev+ext(goos))) }
	fmt.Println("| pair | units | ordinal | region-rel | symbol | content |")
	fmt.Println("|---|---|--:|--:|--:|--:|")
	for _, ps := range pairs {
		if !have(dir, goos, arch, flags, ps.base, ps.cur) {
			continue
		}
		b, c := p(ps.base), p(ps.cur)
		for _, um := range units {
			row := fmt.Sprintf("| %s | %s |", ps.label, um)
			for _, id := range []binref.Ident{binref.IdentOrdinal, binref.IdentRegionRel, binref.IdentSymbol, binref.IdentContent} {
				cf := cfg{setFull, id, um}
				if id == binref.IdentSymbol && um == "call" {
					row += " n/a (no names) |"
					continue
				}
				nb, _ := Cost(m.rep(b, cf, false).Objs, m.rep(c, cf, false).Objs)
				dp, dm := normDelta(m.norm(b, cf), m.norm(c, cf))
				row += fmt.Sprintf(" %s / %s |", fp(pct(nb, len(c.Data))), fp(pct(dp+dm, len(c.Data))))
			}
			fmt.Println(row)
		}
	}
}

// attrTable: per region literal bytes of cur vs the whole of base (raw vs normalized).
func (m *memo) attrTable(b, c *bin, cf cfg) {
	nb, nc := m.norm(b, cf), m.norm(c, cf)
	fmt.Println("| region | bytes | raw literal | normalized payload literal | refs stream cost | explained by references |")
	fmt.Println("|---|--:|--:|--:|--:|--:|")
	var tr, tp, tm int
	for i, r := range c.Im.Regions {
		if r.Len < 512 {
			continue
		}
		raw := deltaCost(b.Data, c.Data[r.Off:r.Off+r.Len]).Literal
		np := deltaCost(nb.Masked, nc.Masked[r.Off:r.Off+r.Len]).Literal
		var rc []byte
		var bc []byte
		for _, s := range nc.Streams {
			if s.Kind == "refs" && s.Region == i {
				rc = s.Data
			}
		}
		for _, s := range nb.Streams {
			if s.Kind == "refs" {
				bc = append(bc, s.Data...)
			}
		}
		rm := 0
		if rc != nil {
			rm = deltaCost(bc, rc).Cost
		}
		tr += raw
		tp += np
		tm += rm
		if raw == 0 && np == 0 && rm == 0 {
			continue
		}
		fmt.Printf("| %s | %d | %d | %d | %d | %d |\n", r.Name, r.Len, raw, np, rm, max(raw-np-rm, 0))
	}
	fmt.Printf("| **total** | %d | %d | %d | %d | %d |\n", len(c.Data), tr, tp, tm, max(tr-tp-tm, 0))
}

func deflateLen(b []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(b)
	w.Close()
	return buf.Len()
}

// debugTable: Go's compressed DWARF is the largest remaining unnormalized churn.
func (m *memo) debugTable(b, c *bin) {
	fmt.Println("| section | stored | inflated | stored literal vs base | inflated literal vs base inflated | zlib level that re-creates the bytes |")
	fmt.Println("|---|--:|--:|--:|--:|---|")
	infl := func(x *bin, s [2]uint64) []byte {
		raw := x.Data[s[0] : s[0]+s[1]]
		if len(raw) < 24 || binary.LittleEndian.Uint32(raw) != 1 {
			return nil
		}
		zr, err := zlib.NewReader(bytes.NewReader(raw[24:]))
		if err != nil {
			return nil
		}
		out, _ := io.ReadAll(zr)
		return out
	}
	var bAll, cAll []byte
	type sec struct {
		name string
		loc  [2]uint64
	}
	var cs []sec
	bm := map[string][2]uint64{}
	for _, s := range b.Elf.Sections {
		if s.Flags&0x800 != 0 {
			bm[s.Name] = [2]uint64{s.Off, s.Size}
			bAll = append(bAll, infl(b, [2]uint64{s.Off, s.Size})...)
		}
	}
	for _, s := range c.Elf.Sections {
		if s.Flags&0x800 != 0 {
			cs = append(cs, sec{s.Name, [2]uint64{s.Off, s.Size}})
			cAll = append(cAll, infl(c, [2]uint64{s.Off, s.Size})...)
		}
	}
	var tStored, tInfl, tLit, tILit int
	for _, s := range cs {
		d := infl(c, s.loc)
		level := "none"
		raw := c.Data[s.loc[0] : s.loc[0]+s.loc[1]]
		for lv := -1; lv <= 9; lv++ {
			var buf bytes.Buffer
			buf.Write(raw[:24])
			zw, _ := zlib.NewWriterLevel(&buf, lv)
			zw.Write(d)
			zw.Close()
			if bytes.Equal(buf.Bytes(), raw) {
				level = fmt.Sprint(lv)
				break
			}
		}
		lit := deltaCost(b.Data, raw).Literal
		ilit := deltaCost(bAll, d).Literal
		tStored += int(s.loc[1])
		tInfl += len(d)
		tLit += lit
		tILit += ilit
		fmt.Printf("| %s | %d | %d | %d | %d | %s |\n", s.name, s.loc[1], len(d), lit, ilit, level)
	}
	fmt.Printf("| **total** | %d | %d | %d | %d | |\n", tStored, tInfl, tLit, tILit)
	_ = cAll
}

func runReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("corp", "", "corpus directory")
	goos := fs.String("goos", "linux", "")
	arch := fs.String("arch", "amd64", "")
	section := fs.String("s", "all", "matrix|ident|attr|debug|meta|perf|pie|meta-flags|all")
	fs.Parse(args)
	m := newMemo()
	want := func(s string) bool { return *section == "all" || *section == s }
	bp := func(flags, rev string) *bin {
		return m.bin(filepath.Join(*dir, *goos+"-"+*arch, flags, rev+ext(*goos)))
	}
	if want("matrix") {
		for _, fl := range []string{"std", "strip"} {
			fmt.Printf("\n#### %s/%s %s: new bytes as %% of the current build\n\n", *goos, *arch, fl)
			m.matrixTable(*dir, *goos, *arch, fl, "auto", pairSpecs)
		}
	}
	if want("ident") {
		sel := []pairSpec{pairSpecs[1], pairSpecs[4], pairSpecs[5]}
		for _, fl := range []string{"std", "strip"} {
			us := []string{"auto"}
			if fl == "strip" {
				us = []string{"pcln", "call"}
			}
			fmt.Printf("\n#### target identities, %s (CDC %% / pairwise-delta %%)\n\n", fl)
			m.identTable(*dir, *goos, *arch, fl, us, sel)
		}
	}
	if want("attr") {
		for _, fl := range []string{"std", "strip"} {
			fmt.Printf("\n#### attribution, add function r1→r2, %s (literal bytes vs the whole base)\n\n", fl)
			m.attrTable(bp(fl, "r1"), bp(fl, "r2"), cfg{setFull, binref.IdentSymbol, "auto"})
		}
	}
	if want("debug") && bp("std", "r1").Elf != nil {
		fmt.Print("\n#### compressed DWARF, add function r1→r2, std\n\n")
		m.debugTable(bp("std", "r1"), bp("std", "r2"))
	}
	if want("meta") {
		fmt.Print("\n#### representation size of one build (r2)\n\n")
		fmt.Println("| flags | file | payload | meta (refs+units+regions) | meta DEFLATEd | refs | units | meta % of file | objects (CDC) |")
		fmt.Println("|---|--:|--:|--:|--:|--:|--:|--:|--:|")
		for _, fl := range []string{"std", "strip"} {
			b := bp(fl, "r2")
			cf := cfg{setFull, binref.IdentSymbol, "auto"}
			n := m.norm(b, cf)
			var meta []byte
			pay := 0
			for _, s := range n.Streams {
				if s.Kind == "payload" {
					pay += len(s.Data)
				} else {
					meta = append(meta, s.Data...)
				}
			}
			r := m.rep(b, cf, false)
			var refs int
			for _, c := range n.Stat.Accepted {
				refs += c
			}
			fmt.Printf("| %s | %d | %d | %d | %d | %d | %d | %.1f%% | %d |\n", fl, len(b.Data), pay, len(meta), deflateLen(meta), refs, n.Stat.Units, pct(len(meta), len(b.Data)), len(r.Objs))
		}
	}
	if want("perf") {
		fmt.Print("\n#### performance, r2 std (best of 3)\n\n")
		fmt.Println("| step | MiB/s |")
		fmt.Println("|---|--:|")
		d := bp("std", "r2").Data
		best := func(f func()) float64 {
			bs := int64(1 << 62)
			for i := 0; i < 3; i++ {
				t := nowNS()
				f()
				if e := nowNS() - t; e < bs {
					bs = e
				}
			}
			return float64(len(d)) / (1 << 20) / (float64(bs) / 1e9)
		}
		fmt.Printf("| parse (container + pclntab) | %.0f |\n", best(func() { loadBin("x", d) }))
		b := bp("std", "r2")
		cf := cfg{setFull, binref.IdentSymbol, "auto"}
		var sl []binref.Slot
		fmt.Printf("| x86 + pointer scan | %.0f |\n", best(func() { sl = b.slots(setFull) }))
		un := b.units("auto")
		var n *binref.Norm
		fmt.Printf("| normalize (given slots, units) | %.0f |\n", best(func() { n, _ = binref.Normalize(b.Im, sl, un, cf.Ident) }))
		fmt.Printf("| reconstruct | %.0f |\n", best(func() { binref.Reconstruct(n.Streams) }))
		fmt.Printf("| SHA-256 verify (reference) | %.0f |\n", best(func() { sha256.Sum256(d) }))
		var ms0, ms1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms0)
		n2, _ := binref.Normalize(b.Im, sl, un, cf.Ident)
		runtime.ReadMemStats(&ms1)
		fmt.Printf("\nnormalize allocates %.1f MiB for a %.1f MiB image (%d streams)\n", float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<20), float64(len(d))/(1<<20), len(n2.Streams))
	}
	if want("pie") && *goos == "linux" && *arch == "amd64" {
		for _, fl := range []string{"pie", "piestrip"} {
			for _, ps := range []pairSpec{pairSpecs[1], pairSpecs[4]} {
				if !have(*dir, *goos, *arch, fl, ps.base, ps.cur) {
					continue
				}
				b, c := bp(fl, ps.base), bp(fl, ps.cur)
				fmt.Printf("\n#### -buildmode=pie (%s), %s: %d ELF relocations (%d usable slots)\n\n", fl, ps.label, c.Elf.Relocs, c.Elf.RelocSlot)
				fmt.Println("| representation | CDC % | Δ % | meta B | refs |")
				fmt.Println("|---|--:|--:|--:|--:|")
				bb, cb := m.base(b), m.base(c)
				nb, _ := Cost(repByName(bb, "raw CDC").Objs, repByName(cb, "raw CDC").Objs)
				dr := deltaCost(b.Data, c.Data)
				fmt.Printf("| raw | %s | %s | 0 | 0 |\n", fp(pct(nb, len(c.Data))), fp(pct(dr.Cost, len(c.Data))))
				for _, set := range []slotSet{setReloc, setX86, setFull} {
					cf := cfg{set, binref.IdentSymbol, "auto"}
					nbb, _ := Cost(m.rep(b, cf, false).Objs, m.rep(c, cf, false).Objs)
					dp, dm := normDelta(m.norm(b, cf), m.norm(c, cf))
					r := m.rep(c, cf, false)
					fmt.Printf("| %s | %s | %s | %d | %d |\n", set.Name, fp(pct(nbb, len(c.Data))), fp(pct(dp+dm, len(c.Data))), r.Meta, r.Refs)
				}
			}
		}
	}
	if want("xor") {
		fmt.Print("\n#### XOR control (equal-size pairs only), DEFLATE of base XOR cur as % of cur\n\n")
		fmt.Println("| pair | flags | xor+DEFLATE % | Δ raw % |")
		fmt.Println("|---|---|--:|--:|")
		for _, fl := range []string{"strip"} {
			for _, ps := range pairSpecs {
				if !have(*dir, *goos, *arch, fl, ps.base, ps.cur) {
					continue
				}
				b, c := bp(fl, ps.base), bp(fl, ps.cur)
				if len(b.Data) != len(c.Data) {
					continue
				}
				x := make([]byte, len(b.Data))
				for i := range x {
					x[i] = b.Data[i] ^ c.Data[i]
				}
				fmt.Printf("| %s | %s | %s | %s |\n", ps.label, fl, fp(pct(deflateLen(x), len(c.Data))), fp(pct(deltaCost(b.Data, c.Data).Cost, len(c.Data))))
			}
		}
	}
	if want("classes") {
		for _, fl := range []string{"strip", "std"} {
			for _, ps := range []pairSpec{pairSpecs[1], pairSpecs[4]} {
				m.classTable(*dir, *goos, *arch, fl, ps)
			}
		}
	}
	if want("xformat") {
		for _, fl := range []string{"strip", "std"} {
			fmt.Printf("\n#### same source mutation, ELF versus PE, %s (new bytes as %% of the current build)\n\n", fl)
			m.xformat(*dir, fl)
		}
	}
	if want("admit") {
		for _, fl := range []string{"strip", "std"} {
			fmt.Printf("\n#### admission by measured cost, %s/%s %s (new bytes + 96 B per reference, %% of current build)\n\n", *goos, *arch, fl)
			m.admit(*dir, *goos, *arch, fl, pairSpecs)
		}
	}
	if want("meta-flags") && *goos == "linux" && *arch == "amd64" {
		fmt.Print("\n#### build metadata: r2 → r3 under different flags (raw CDC %, section+CDC %, raw Δ %)\n\n")
		fmt.Println("| flags | file | raw CDC | sect+CDC | raw Δ | full-norm Δ | regions that differ most (literal bytes) |")
		fmt.Println("|---|--:|--:|--:|--:|--:|---|")
		for _, fl := range []string{"std", "notrim", "nobid", "strip"} {
			b, c := bp(fl, "r2"), bp(fl, "r3")
			bb, cb := m.base(b), m.base(c)
			a1, _ := Cost(repByName(bb, "raw CDC").Objs, repByName(cb, "raw CDC").Objs)
			a2, _ := Cost(repByName(bb, "section + CDC").Objs, repByName(cb, "section + CDC").Objs)
			dr := deltaCost(b.Data, c.Data)
			cf := cfg{setFull, binref.IdentSymbol, "auto"}
			dp, dm := normDelta(m.norm(b, cf), m.norm(c, cf))
			type kv struct {
				n string
				v int
			}
			var kvs []kv
			for _, r := range c.Im.Regions {
				if r.Len < 64 {
					continue
				}
				kvs = append(kvs, kv{r.Name, deltaCost(b.Data, c.Data[r.Off:r.Off+r.Len]).Literal})
			}
			sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
			var top []string
			for i := 0; i < len(kvs) && i < 3; i++ {
				top = append(top, fmt.Sprintf("%s %d", kvs[i].n, kvs[i].v))
			}
			fmt.Printf("| %s | %d | %s | %s | %s | %s | %s |\n", fl, len(c.Data), fp(pct(a1, len(c.Data))), fp(pct(a2, len(c.Data))), fp(pct(dr.Cost, len(c.Data))), fp(pct(dp+dm, len(c.Data))), strings.Join(top, ", "))
		}
	}
	_ = os.Stdout
}

// ---- Z3-13 sections ----

// classTable: which reference classes pay, for one format and flag set.
func (m *memo) classTable(dir, goos, arch, flags string, ps pairSpec) {
	p := func(rev string) *bin { return m.bin(filepath.Join(dir, goos+"-"+arch, flags, rev+ext(goos))) }
	if !have(dir, goos, arch, flags, ps.base, ps.cur) {
		return
	}
	b, c := p(ps.base), p(ps.cur)
	n := len(c.Data)
	fmt.Printf("\n##### %s/%s %s, %s\n\n", goos, arch, flags, ps.label)
	fmt.Println("| reference classes masked | CDC % | CDC-4K % | Δ % | reference metadata B | references |")
	fmt.Println("|---|--:|--:|--:|--:|--:|")
	bb, cb := m.base(b), m.base(c)
	nb, _ := Cost(repByName(bb, "raw CDC").Objs, repByName(cb, "raw CDC").Objs)
	n4, _ := Cost(repByName(bb, "CDC 4K-avg").Objs, repByName(cb, "CDC 4K-avg").Objs)
	dr := deltaCost(b.Data, c.Data)
	fmt.Printf("| none (raw) | %s | %s | %s | 0 | 0 |\n", fp(pct(nb, n)), fp(pct(n4, n)), fp(pct(dr.Cost, n)))
	tableOnly := slotSet{Name: "format tables (no Go)", Table: true}
	sets := []slotSet{
		{Name: "relocations only", Reloc: true},
		tableOnly,
		{Name: "relocations + format tables", Reloc: true, Table: true},
		{Name: "Go function table only", Go: true},
		{Name: "x86 branch + RIP only", Branch: true, RIP: true},
		{Name: "x86 + relocations", Reloc: true, Branch: true, RIP: true},
		{Name: "x86 + relocations + tables + Go table", Reloc: true, Table: true, Branch: true, RIP: true, Go: true},
		{Name: "all (+ pointer scan)", Reloc: true, Table: true, Branch: true, RIP: true, Ptr: true, Go: true},
	}
	for _, s := range sets {
		cf := cfg{s, binref.IdentSymbol, "auto"}
		r := m.rep(c, cf, false)
		nbb, _ := Cost(m.rep(b, cf, false).Objs, r.Objs)
		n4b, _ := Cost(m.normObjs4k(b, cf), m.normObjs4k(c, cf))
		dp, dm := normDelta(m.norm(b, cf), m.norm(c, cf))
		fmt.Printf("| %s | %s | %s | %s | %d | %d |\n", s.Name, fp(pct(nbb, n)), fp(pct(n4b, n)), fp(pct(dp+dm, n)), r.Meta, r.Refs)
	}
}

// xformat: the same source mutation, ELF versus PE.
func (m *memo) xformat(dir, flags string) {
	fmt.Println("| mutation | ELF CDC raw → full | ELF Δ raw → full | PE CDC raw → full | PE Δ raw → full |")
	fmt.Println("|---|--:|--:|--:|--:|")
	cf := cfg{setFull, binref.IdentSymbol, "auto"}
	for _, ps := range pairSpecs {
		row := fmt.Sprintf("| %s |", ps.label)
		ok := true
		for _, f := range []struct{ goos string }{{"linux"}, {"windows"}} {
			if !have(dir, f.goos, "amd64", flags, ps.base, ps.cur) {
				ok = false
				break
			}
			b := m.bin(filepath.Join(dir, f.goos+"-amd64", flags, ps.base+ext(f.goos)))
			c := m.bin(filepath.Join(dir, f.goos+"-amd64", flags, ps.cur+ext(f.goos)))
			n := len(c.Data)
			bb, cb := m.base(b), m.base(c)
			nb, _ := Cost(repByName(bb, "raw CDC").Objs, repByName(cb, "raw CDC").Objs)
			nf, _ := Cost(m.rep(b, cf, false).Objs, m.rep(c, cf, false).Objs)
			dr := deltaCost(b.Data, c.Data)
			dp, dm := normDelta(m.norm(b, cf), m.norm(c, cf))
			row += fmt.Sprintf(" %s → %s | %s → %s |", fp(pct(nb, n)), fp(pct(nf, n)), fp(pct(dr.Cost, n)), fp(pct(dp+dm, n)))
		}
		if ok {
			fmt.Println(row)
		}
	}
}

// admit: choose the cheapest representation by measured cost (new bytes + 96 B per object reference).
func (m *memo) admit(dir, goos, arch, flags string, pairs []pairSpec) {
	p := func(rev string) *bin { return m.bin(filepath.Join(dir, goos+"-"+arch, flags, rev+ext(goos))) }
	cands := []struct {
		name string
		cf   *cfg
	}{{"raw CDC", nil}, {"section + CDC", nil}, {"x86 refs", &cfg{setX86, binref.IdentSymbol, "auto"}}, {"full", &cfg{setFull, binref.IdentSymbol, "auto"}}}
	fmt.Println("| pair | raw CDC | section+CDC | x86 refs | full | chosen | saving vs raw (points) |")
	fmt.Println("|---|--:|--:|--:|--:|---|--:|")
	one := func(label string, b, c *bin) {
		n := len(c.Data)
		bb, cb := m.base(b), m.base(c)
		costs := make([]float64, len(cands))
		row := fmt.Sprintf("| %s |", label)
		best := 0
		for i, cd := range cands {
			var nb, objs int
			switch {
			case cd.cf != nil:
				nb, objs = Cost(m.rep(b, *cd.cf, false).Objs, m.rep(c, *cd.cf, false).Objs)
			default:
				nb, objs = Cost(repByName(bb, cd.name).Objs, repByName(cb, cd.name).Objs)
			}
			costs[i] = pct(nb+objs*chunkRef, n)
			if costs[i] < costs[best]-0.5 { // a candidate must win by half a point to displace raw CDC
				best = i
			}
			row += " " + fp(costs[i]) + " |"
		}
		fmt.Printf("%s %s | %s |\n", row, cands[best].name, fp(costs[0]-costs[best]))
	}
	for _, ps := range pairs {
		if have(dir, goos, arch, flags, ps.base, ps.cur) {
			one(ps.label, p(ps.base), p(ps.cur))
		}
	}
	if have(dir, goos, arch, flags, "r2", "other") {
		one("unrelated program (r2 → other)", p("r2"), p("other"))
	}
}
