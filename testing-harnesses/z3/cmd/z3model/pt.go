package main

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"zeros3-z3/modelz3"
)

func ptRels() []rel {
	var r []rel
	for _, p := range [][2]string{{"step1000", "step2000"}, {"step2000", "step4000"}, {"step4000", "step8000"}, {"step8000", "main"}, {"step1000", "main"}} {
		r = append(r, rel{"Pythia-70M checkpoints (.bin)", p[0] + ">" + p[1], "pt-pythia70m/" + p[0], "pt-pythia70m/" + p[1]})
	}
	for _, p := range [][2]string{{"step1000", "step2000"}, {"step2000", "step3000"}, {"step3000", "step4000"}, {"step4000", "main"}, {"step1000", "main"}} {
		r = append(r, rel{"Pythia-14M checkpoints (.bin)", p[0] + ">" + p[1], "pt-pythia14m/" + p[0], "pt-pythia14m/" + p[1]})
	}
	r = append(r,
		rel{"bert-tiny star (.bin, different producers)", "base>ft-conll03", "pt-bert-tiny/base", "pt-bert-tiny/ft-conll03"},
		rel{"bert-tiny star (.bin, different producers)", "base>ft-fewnerd", "pt-bert-tiny/base", "pt-bert-tiny/ft-fewnerd"},
		rel{"bert-tiny star (.bin, different producers)", "ft-conll03>ft-fewnerd (siblings)", "pt-bert-tiny/ft-conll03", "pt-bert-tiny/ft-fewnerd"},
		rel{"unrelated", "pythia-70m vs pythia-70m-deduped", "pt-pythia70m/main", "pt-unrelated/pythia70m-deduped"},
		rel{"cross-format (same Pythia-70M main state)", ".bin storages vs model.safetensors tensors", "pt-pythia70m/main", "pt-pythia70m-st/main"},
	)
	return r
}

func (s *store) oracle(f *file, c *modelz3.Corpus) *modelz3.Oracle {
	sha := f.Path[strings.LastIndex(f.Path, "/")+1:]
	o, err := modelz3.LoadOracle("/home/user/z3cache/oracle/" + sha + ".json")
	check(err)
	return o
}

func runPT(s *store, filter string) {
	pk := modelz3.NewPacker()
	fmt.Printf("%-44s | %6s %6s %6s | %6s %6s | %6s %6s %6s %6s | %s\n", "relationship (% of target bytes; B stored after A)", "cdc", "member", "str+cd", "stoSHA", "frame%", "dName", "dSize", "dOrac", "sOrac", "notes")
	last := ""
	for _, r := range ptRels() {
		if filter != "" && !strings.Contains(r.Class+r.Label, filter) {
			continue
		}
		if r.Class != last {
			fmt.Println("##", r.Class)
			last = r.Class
		}
		if strings.HasPrefix(r.Class, "cross-format") {
			runCross(s, r)
			continue
		}
		a, b := s.get(r.A), s.get(r.B)
		var lays [2][2]*modelz3.Layout // [file][member|oracle]
		for i, f := range []*file{a, b} {
			l, _, err := modelz3.PTLayout(f.f, f.Size, nil)
			check(err)
			f.lay = l
			lo, _, err := modelz3.PTLayout(f.f, f.Size, s.oracle(f, nil))
			check(err)
			lays[i] = [2]*modelz3.Layout{l, lo}
		}
		var cs [3]float64
		for i, m := range []string{modelz3.CutCDC, modelz3.CutStruct, modelz3.CutStructCDC} {
			seen := modelz3.Seen{}
			seen.Add(a.cut(m))
			c, err := modelz3.CostOf(b.cut(m), seen, pk)
			check(err)
			cs[i] = pct(c.Total(true), b.Size)
		}
		// storages that exist anywhere in the base, by SHA, regardless of member name
		seen := map[[32]byte]bool{}
		for _, t := range lays[0][0].Tensors() {
			buf := make([]byte, t.Size())
			check(readAtFull(a, buf, t.Begin))
			seen[sha256.Sum256(buf)] = true
		}
		var sto, stoSame int64
		for _, t := range lays[1][0].Tensors() {
			buf := make([]byte, t.Size())
			check(readAtFull(b, buf, t.Begin))
			sto += t.Size()
			if seen[sha256.Sum256(buf)] {
				stoSame += t.Size()
			}
		}
		var dd [3]float64
		var stand float64
		for k := 0; k < 3; k++ {
			la, lb := lays[0][min(k, 1)], lays[1][min(k, 1)]
			if k == 1 {
				la, lb = la.SizeRank(), lb.SizeRank()
			}
			if k == 2 {
				la, lb = lays[0][1], lays[1][1]
			}
			dr, err := modelz3.DeltaOf(a.src, la, b.src, lb)
			check(err)
			raw, bs := dr.Raw, dr.Best
			dd[k] = pct(bs, raw)
			if k == 2 {
				stand = pct(dr.Standalone, raw)
			}
		}
		fmt.Printf("%-44s | %6.1f %6.1f %6.1f | %6.1f %6.1f | %6.1f %6.1f %6.1f %6.1f | storages %d pickle %dB\n", r.Label, cs[0], cs[1], cs[2], pct(stoSame, sto),
			pct(lays[1][0].FramingBytes(), b.Size), dd[0], dd[1], dd[2], stand, len(lays[1][0].Tensors()), pickleBytes(lays[1][0]))
	}
}

func pickleBytes(l *modelz3.Layout) (n int64) {
	for _, s := range l.Segs {
		if s.Name == "data.pkl" {
			n += s.Size()
		}
	}
	return
}

func readAtFull(f *file, b []byte, off int64) error {
	_, err := f.f.ReadAt(b, off)
	return err
}

// runCross compares the exact bytes of the same model state stored as PyTorch storages and as
// Safetensors tensors (REAL: both published by the same repository at the same revision).
func runCross(s *store, r rel) {
	a, b := s.get(r.A), s.get(r.B)
	pl, _, err := modelz3.PTLayout(a.f, a.Size, s.oracle(a, nil))
	check(err)
	sl, _, err := modelz3.STLayout(b.f, b.Size)
	check(err)
	byHash := map[[32]byte]string{}
	for _, t := range pl.Tensors() {
		buf := make([]byte, t.Size())
		check(readAtFull(a, buf, t.Begin))
		byHash[sha256.Sum256(buf)] = t.Name
	}
	var tot, same, byName int64
	nameMatch := map[string]bool{}
	for _, t := range pl.Tensors() {
		nameMatch[t.Name] = true
	}
	for _, t := range sl.Tensors() {
		buf := make([]byte, t.Size())
		check(readAtFull(b, buf, t.Begin))
		tot += t.Size()
		if _, ok := byHash[sha256.Sum256(buf)]; ok {
			same += t.Size()
		}
		if nameMatch[t.Name] {
			byName += t.Size()
		}
	}
	pk := modelz3.NewPacker()
	sl.Format = "safetensors"
	var cs [3]float64
	for i, m := range []string{modelz3.CutCDC, modelz3.CutStruct, modelz3.CutStructCDC} {
		a.lay, b.lay = pl, sl
		seen := modelz3.Seen{}
		seen.Add(a.cut(m))
		c, err := modelz3.CostOf(b.cut(m), seen, pk)
		check(err)
		cs[i] = pct(c.Total(true), b.Size)
	}
	fmt.Printf("cross-format ST cost given the PT file stored: cdc %.1f%% struct %.1f%% struct+cdc %.1f%%\n", cs[0], cs[1], cs[2])
	fmt.Printf("%-44s | PT %d B / ST %d B; ST tensor bytes %d; exact-identical to some PT storage: %.1f%%; same-named tensors (by oracle): %.1f%%; ST framing %.3f%%, PT framing %.3f%%\n",
		r.Label, a.Size, b.Size, tot, pct(same, tot), pct(byName, tot), pct(sl.FramingBytes(), b.Size), pct(pl.FramingBytes(), a.Size))
}
