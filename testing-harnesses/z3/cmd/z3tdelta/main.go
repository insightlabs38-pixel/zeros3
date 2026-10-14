// z3tdelta measures the Z3-10 tensor-pair delta representation on real
// Safetensors checkpoint families laid out by `z3fetch -out`:
//
//	pairs   adjacent and anchor pairs: CDC vs standalone vs DEFLATE plane-XOR (T0) vs native PXR (T1) vs hybrid rows
//	family  base-selection / delta-chain-depth policies over a whole family
//	sweep   row-reuse run-length threshold sweep
//	perf    encode/decode throughput and allocation of the codecs
//
// Every encoded file is decoded again and compared by length, SHA-256 and bytes.
// Laboratory code: it prints Markdown tables and writes nothing.
package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"zeros3-z3/corpus"
	"zeros3-z3/lab"
	"zeros3-z3/st"
	"zeros3-z3/tdelta"
)

type ckpt struct {
	label string
	data  []byte
	f     *st.File
	chunk map[[32]byte]int
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3tdelta:", err)
	os.Exit(1)
}

func load(dir string, families []string) (map[string][]*ckpt, []string) {
	m, err := corpus.ReadManifest(dir)
	if err != nil {
		fatal(err)
	}
	names, by := m.Group()
	out := map[string][]*ckpt{}
	var order []string
	for _, fam := range names {
		ok := len(families) == 0
		for _, f := range families {
			if f == fam || "rw-"+f == fam {
				ok = true
			}
		}
		if !ok || by[fam][0].Format != "safetensors" {
			continue
		}
		for _, mem := range by[fam] {
			p, err := corpus.MemberPath(dir, mem)
			if err != nil {
				fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				fatal(err)
			}
			f, err := st.ParseBytes(b, st.Options{})
			if err != nil {
				fatal(fmt.Errorf("%s: %w", p, err))
			}
			label := mem.Mutation
			if label == "" {
				label = mem.Revision
			}
			out[fam] = append(out[fam], &ckpt{label: label, data: b, f: f})
		}
		order = append(order, fam)
	}
	return out, order
}

func (c *ckpt) chunks() map[[32]byte]int {
	if c.chunk == nil {
		c.chunk = map[[32]byte]int{}
		lab.ChunkCDC(bytes.NewReader(c.data), func(_ int64, ch []byte) error {
			c.chunk[sha256.Sum256(ch)] = len(ch)
			return nil
		})
	}
	return c.chunk
}

var stepRe = regexp.MustCompile(`^step(\d+)$`)

func step(l string) (int, bool) {
	if m := stepRe.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	return 0, false
}

func pct(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*float64(a)/float64(b))
}

func short(f string) string { return strings.TrimPrefix(f, "rw-") }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3tdelta pairs|family|sweep|perf -corpus DIR [-families a,b] [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory (z3fetch -out)")
	fam := fs.String("families", "", "comma list of families")
	anchor := fs.Bool("anchor", true, "pairs: also report every file against the family's first file")
	minReuse := fs.Int("min-reuse", 1, "row recipe: shortest reuse run kept (rows)")
	maxFrac := fs.Float64("maxfrac", 0, "admission: a non-raw tensor mode must be <= this fraction of the raw tensor (0 = no limit)")
	oracle := fs.Bool("oracle", false, "family: also try every earlier file as a base (diagnostic upper bound)")
	i := fs.Int("i", 0, "perf: base file index")
	j := fs.Int("j", 1, "perf: target file index")
	fs.Parse(os.Args[2:])
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	var fl []string
	if *fam != "" {
		fl = strings.Split(*fam, ",")
	}
	cks, order := load(*dir, fl)
	switch os.Args[1] {
	case "pairs":
		runPairs(cks, order, *anchor, *minReuse, *maxFrac)
	case "family":
		runFamily(cks, order, *minReuse, *oracle, *maxFrac)
	case "sweep":
		runSweep(cks, order)
	case "perf":
		runPerf(cks, order, *i, *j, *minReuse)
	default:
		fatal(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

// ---- one pair ----

type pairRes struct {
	raw                                 int64
	cdc                                 int64 // new CDC chunk bytes of the target given the base
	standalone, t0, t1, hybrid          int64 // total container-equivalent bytes
	fixed, hdr, records, rowMeta        int64
	rowsChosen, deltaChosen, sameChosen int
	modeBytes                           [8]int64 // tensor bytes by chosen mode
	tensors                             int
	byDType                             map[string]*dt
	enc, dec                            time.Duration
	exact                               bool
	contLen                             int
	rep                                 *tdelta.FileRep
}

type dt struct{ raw, t0, hybrid, delta int64 }

func evalPair(base, tgt *ckpt, minReuse int, maxFrac float64) *pairRes {
	r := &pairRes{raw: int64(len(tgt.data)), byDType: map[string]*dt{}}
	t0 := time.Now()
	opt := tdelta.Options{Compare: true, MinReuse: minReuse, MaxFrac: maxFrac}
	var bd []byte
	var bf *st.File
	if base != nil {
		bd, bf = base.data, base.f
	}
	cont, rep, err := tdelta.EncodeFile(tgt.data, tgt.f, bd, bf, opt)
	if err != nil {
		fatal(err)
	}
	r.enc = time.Since(t0) // includes the DEFLATE comparison work; perf measures codecs separately
	t1 := time.Now()
	got, err := tdelta.DecodeFile(cont, bd, bf)
	r.dec = time.Since(t1)
	r.exact = err == nil && len(got) == len(tgt.data) && sha256.Sum256(got) == sha256.Sum256(tgt.data) && bytes.Equal(got, tgt.data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
	}
	r.contLen, r.rep = len(cont), rep
	r.fixed, r.hdr = int64(rep.Fixed), int64(rep.Header)
	base0 := int64(rep.Fixed + rep.Header)
	r.standalone, r.t0, r.t1, r.hybrid = base0, base0, base0, base0
	for _, t := range rep.Tensors {
		r.tensors++
		var raw, stand, same, delta, flD, flS int
		for _, c := range t.Cands {
			switch c.Mode {
			case tdelta.ModeRaw:
				raw = c.Wire
			case tdelta.ModeStandalone:
				stand, flS = c.Wire, tdelta.WireLen(tdelta.ModeStandalone, c.Flate)
			case tdelta.ModeSame:
				same = c.Wire
			case tdelta.ModeDelta:
				delta = c.Wire
				flD = tdelta.WireLen(tdelta.ModeDelta, c.Flate)
			}
		}
		min := func(xs ...int) int { // smallest positive; 0 marks an absent candidate
			m := 0
			for _, x := range xs {
				if x > 0 && (m == 0 || x < m) {
					m = x
				}
			}
			return m
		}
		sa := min(raw, stand)
		fl := min(raw, flS, flD)
		bt := min(raw, stand, same, delta)
		fl2 := fl
		fl2 = min(fl, same)
		hy := t.Wire
		r.standalone += int64(sa)
		r.t0 += int64(fl2)
		r.t1 += int64(bt)
		r.hybrid += int64(hy)
		d := r.byDType[t.DType]
		if d == nil {
			d = &dt{}
			r.byDType[t.DType] = d
		}
		d.raw += int64(t.Size)
		d.t0 += int64(fl2)
		d.hybrid += int64(hy)
		d.delta += int64(bt)
		r.modeBytes[t.Mode] += int64(t.Size)
		switch t.Mode {
		case tdelta.ModeRows:
			r.rowsChosen++
		case tdelta.ModeDelta:
			r.deltaChosen++
		case tdelta.ModeSame:
			r.sameChosen++
		}
		for _, c := range t.Cands {
			if c.Mode == tdelta.ModeRows && t.Mode == tdelta.ModeRows {
				r.rowMeta += int64(c.Meta)
			}
		}
		pay := 0
		for _, c := range t.Cands {
			if c.Mode == t.Mode {
				pay = c.Pay
			}
		}
		r.records += int64(t.Wire - pay)
	}
	// raw CDC against the base
	if base != nil {
		have := base.chunks()
		lab.ChunkCDC(bytes.NewReader(tgt.data), func(_ int64, ch []byte) error {
			if _, ok := have[sha256.Sum256(ch)]; !ok {
				r.cdc += int64(len(ch))
			}
			return nil
		})
	} else {
		r.cdc = r.raw
	}
	return r
}

func runPairs(cks map[string][]*ckpt, order []string, anchor bool, minReuse int, maxFrac float64) {
	fmt.Println("| family | base -> target | dist | MiB | CDC % | standalone % | T0 plane-XOR+DEFLATE % | T1 native whole-tensor % | hybrid rows % | meta B | row recipe B | tensors same/delta/rows | bytes by mode: raw/stand/same/delta/rows % | exact | enc MiB/s | dec MiB/s |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|---|---:|---:|")
	type agg struct{ raw, cdc, sa, t0, t1, hy int64 }
	tot := map[string]*agg{}
	dts := map[string]map[string]*dt{}
	for _, f := range order {
		c := cks[f]
		for j := 1; j < len(c); j++ {
			cand := []int{j - 1}
			if anchor && j > 1 {
				cand = append(cand, 0)
			}
			for _, i := range cand {
				r := evalPair(c[i], c[j], minReuse, maxFrac)
				dist := "-"
				if a, ok := step(c[i].label); ok {
					if b, ok := step(c[j].label); ok {
						dist = strconv.Itoa(b - a)
					}
				}
				kind := "adj"
				if i != j-1 {
					kind = "anchor"
				}
				fmt.Printf("| %s %s | %s -> %s | %s | %.1f | %s | %s | %s | %s | %s | %d | %d | %d/%d/%d | %s | %v | %.0f | %.0f |\n", short(f), kind, c[i].label, c[j].label, dist, float64(r.raw)/(1<<20),
					pct(r.cdc, r.raw), pct(r.standalone, r.raw), pct(r.t0, r.raw), pct(r.t1, r.raw), pct(r.hybrid, r.raw),
					r.fixed+r.hdr+r.records, r.rowMeta, r.sameChosen, r.deltaChosen, r.rowsChosen, modeMix(r), r.exact,
					float64(r.raw)/(1<<20)/r.enc.Seconds(), float64(r.raw)/(1<<20)/r.dec.Seconds())
				if !r.exact {
					fatal(fmt.Errorf("RECONSTRUCTION MISMATCH %s %s->%s", f, c[i].label, c[j].label))
				}
				key := short(f) + " " + kind
				a := tot[key]
				if a == nil {
					a = &agg{}
					tot[key] = a
					dts[key] = map[string]*dt{}
				}
				a.raw += r.raw
				a.cdc += r.cdc
				a.sa += r.standalone
				a.t0 += r.t0
				a.t1 += r.t1
				a.hy += r.hybrid
				for k, v := range r.byDType {
					d := dts[key][k]
					if d == nil {
						d = &dt{}
						dts[key][k] = d
					}
					d.raw += v.raw
					d.t0 += v.t0
					d.hybrid += v.hybrid
					d.delta += v.delta
				}
			}
		}
	}
	fmt.Println("\nByte-weighted totals per family and pair kind (adj = previous file, anchor = family's first file):")
	fmt.Println()
	fmt.Println("| family / kind | CDC % | standalone % | T0 % | T1 whole-tensor % | hybrid % |")
	fmt.Println("|---|---:|---:|---:|---:|---:|")
	var keys []string
	for k := range tot {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := tot[k]
		fmt.Printf("| %s | %s | %s | %s | %s | %s |\n", k, pct(a.cdc, a.raw), pct(a.sa, a.raw), pct(a.t0, a.raw), pct(a.t1, a.raw), pct(a.hy, a.raw))
	}
	fmt.Println("\nBy dtype (tensor bytes only, same pairs; T0 = DEFLATE plane-XOR best-of, T1 = native whole-tensor best-of, hybrid = native with rows):")
	fmt.Println()
	fmt.Println("| family / kind | dtype | MiB | T0 % | T1 % | hybrid % |")
	fmt.Println("|---|---|---:|---:|---:|---:|")
	for _, k := range keys {
		var ds []string
		for d := range dts[k] {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		for _, d := range ds {
			v := dts[k][d]
			fmt.Printf("| %s | %s | %.1f | %s | %s | %s |\n", k, d, float64(v.raw)/(1<<20), pct(v.t0, v.raw), pct(v.delta, v.raw), pct(v.hybrid, v.raw))
		}
	}
}

// modeMix prints the share of tensor bytes per chosen mode: raw / standalone / same / delta / rows.
func modeMix(r *pairRes) string {
	var tot int64
	for _, v := range r.modeBytes {
		tot += v
	}
	m := []tdelta.Mode{tdelta.ModeRaw, tdelta.ModeStandalone, tdelta.ModeSame, tdelta.ModeDelta, tdelta.ModeRows}
	var parts []string
	for _, k := range m {
		parts = append(parts, fmt.Sprintf("%.0f", 100*float64(r.modeBytes[k])/float64(max(tot, 1))))
	}
	return strings.Join(parts, "/")
}
