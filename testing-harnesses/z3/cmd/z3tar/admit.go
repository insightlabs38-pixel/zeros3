package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

// features are cheap per-pair measurements a chooser can use without encoding anything.
type features struct {
	tarReuse, cdcReuse  float64 // fraction of the new revision's bytes already present in the base
	members, compressed int     // members and stored (compressed) bytes of the new revision
}

func pairFeatures(prev, cur *rev) (features, bool) {
	if prev.ar == nil || cur.ar == nil {
		return features{}, false
	}
	P := int64(len(cur.plain))
	have := prev.cdcChunks()
	var cdcOld int64
	lab.ChunkCDC(bytesReader(cur.plain), func(_ int64, c []byte) error {
		if _, ok := have[sha256.Sum256(c)]; ok {
			cdcOld += int64(len(c))
		}
		return nil
	})
	pd := prev.payloadDigests()
	var payload, oldPay int64
	for i := range cur.ar.Members {
		m := &cur.ar.Members[i]
		payload += m.PayLen
		if m.PayLen > 0 {
			if _, ok := pd[sha256.Sum256(cur.plain[m.PayOff:m.PayOff+m.PayLen])]; ok {
				oldPay += m.PayLen
			}
		}
	}
	// headers whose bytes also exist in the base count as reused structure
	return features{tarReuse: float64(oldPay) / float64(P), cdcReuse: float64(cdcOld) / float64(P), members: len(cur.ar.Members), compressed: len(cur.raw)}, true
}

// runAdmit compares the measured update cost of the candidates with a rule
// that sees only cheap features of the pair (no encoding):
//
//	raw CDC       compressed bytes cut by CDC                       predicted cost 1
//	DEFLATE-only  irp-tok16k, plaintext CDC                         predicted (1 - plaintext CDC reuse) + 2%
//	tar-aware     irp-tok16k+tarmem*, best variant                  predicted (1 - exact member reuse) + 2 refs per member / stored bytes + 1%
//
// The rule takes the smallest prediction; tar semantics are never chosen because
// the magic says "tar". Reuse fractions are of the new plaintext, so the
// predictions are fractions of the stored bytes only when the layer does not
// compress; the comparison between candidates is what matters.
func runAdmit(revs map[string][]*rev, order []string, args []string) {
	fs := flag.NewFlagSet("admit", flag.ExitOnError)
	from := fs.String("from", "", "lab reports (-json of z3ir family), comma separated")
	refB := fs.Float64("ref-bytes", 96, "recipe bytes per component reference")
	fs.Parse(args)
	var rep *lab.Report
	for _, f := range strings.Split(*from, ",") {
		r, err := lab.ReadJSON(f)
		if err != nil {
			fatal(err)
		}
		if rep == nil {
			rep = r
		} else {
			rep.Rows = append(rep.Rows, r.Rows...)
		}
	}
	type key struct{ fam, rep string }
	cost := map[key]int64{}
	logical := map[string]int64{}
	exact := map[key]bool{}
	for _, r := range rep.Rows {
		if r.Index == 0 || r.Status != "ok" {
			continue
		}
		inc := r.Incremental
		if r.IncrementalPacked != nil {
			inc = *r.IncrementalPacked
		}
		k := key{r.Family, r.Rep}
		cost[k] += inc
		if r.Rep == "cdc" {
			logical[r.Family] += r.LogicalSize
		}
		exact[k] = r.Oracle || r.RoundtripExact == "yes"
	}
	tarVariants := []string{"irp-tok16k+tarmem", "irp-tok16k+tarmem-s", "irp-tok16k+tarmem-cdc64k"}
	fmt.Println("| family | raw CDC % | DEFLATE-only % | tar-aware % (best) | winner | member reuse % | plaintext CDC reuse % | est. tar | est. DEFLATE | rule picks | rule cost % | best cost % | regret (points) |")
	fmt.Println("|---|---:|---:|---:|---|---:|---:|---:|---:|---|---:|---:|---:|")
	var regret, n float64
	for _, f := range order {
		list := revs[f]
		l := logical[f]
		if l == 0 {
			continue
		}
		cdc, irp := cost[key{f, "cdc"}], cost[key{f, "irp-tok16k"}]
		var bestTar int64 = -1
		for _, v := range tarVariants {
			if c, ok := cost[key{f, v}]; ok && exact[key{f, v}] && (bestTar < 0 || c < bestTar) {
				bestTar = c
			}
		}
		if bestTar < 0 {
			continue
		}
		var tr, cr, refs float64
		var cnt int
		for j := 1; j < len(list); j++ {
			if ft, ok := pairFeatures(list[j-1], list[j]); ok {
				tr += ft.tarReuse
				cr += ft.cdcReuse
				refs += 2 * float64(ft.members) * *refB / float64(ft.compressed)
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		tr, cr, refs = tr/float64(cnt), cr/float64(cnt), refs/float64(cnt)
		estTar, estDefl := (1-tr)+refs+0.01, (1-cr)+0.02
		pick, pc := "raw CDC", cdc
		switch {
		case estTar < estDefl && estTar < 1:
			pick, pc = "tar-aware", bestTar
		case estDefl < 1:
			pick, pc = "DEFLATE-only", irp
		}
		best, bn := cdc, "raw CDC"
		if irp < best {
			best, bn = irp, "DEFLATE-only"
		}
		if bestTar < best {
			best, bn = bestTar, "tar-aware"
		}
		rg := 100 * float64(pc-best) / float64(l)
		regret += rg
		n++
		fmt.Printf("| %s | %s | %s | %s | %s | %.0f | %.0f | %.2f | %.2f | %s | %s | %s | %.1f |\n", short(f), pct(cdc, l), pct(irp, l), pct(bestTar, l), bn, 100*tr, 100*cr, estTar, estDefl, pick, pct(pc, l), pct(best, l), rg)
	}
	if n > 0 {
		fmt.Printf("\nmean regret of the rule: %.2f points of the compressed update bytes over %d families\n", regret/n, int(n))
	}
	_ = sort.Strings
	_ = os.Stdout
}

// runCost breaks the update cost of tar representations down by what is paid for:
// new payload, new structure (headers, small members), unit blobs, references,
// inline bytes and skeleton / recipe metadata, as a percentage of the stored bytes.
func runCost(args []string) {
	fs := flag.NewFlagSet("cost", flag.ExitOnError)
	from := fs.String("from", "", "lab reports (-json of z3ir family), comma separated")
	reps := fs.String("reps", "tarmem,oracle-tarmem,irp-tok16k+tarmem,irp-tok16k+tarmem-cdc64k", "representations to break down")
	refB := fs.Int64("ref-bytes", 96, "recipe bytes per component reference")
	fs.Parse(args)
	var rep *lab.Report
	for _, f := range strings.Split(*from, ",") {
		r, err := lab.ReadJSON(f)
		if err != nil {
			fatal(err)
		}
		if rep == nil {
			rep = r
		} else {
			rep.Rows = append(rep.Rows, r.Rows...)
		}
	}
	type agg struct{ logical, total, pay, hdr, unit, plain, refs, inline, meta int64 }
	by := map[[2]string]*agg{}
	var fams []string
	seen := map[string]bool{}
	want := map[string]bool{}
	for _, r := range strings.Split(*reps, ",") {
		want[r] = true
	}
	for _, r := range rep.Rows {
		if r.Index == 0 || r.Status != "ok" || !want[r.Rep] {
			continue
		}
		if !seen[r.Family] {
			seen[r.Family] = true
			fams = append(fams, r.Family)
		}
		k := [2]string{r.Family, r.Rep}
		a := by[k]
		if a == nil {
			a = &agg{}
			by[k] = a
		}
		inc := r.Incremental
		if r.IncrementalPacked != nil {
			inc = *r.IncrementalPacked
		}
		a.logical += r.LogicalSize
		a.total += inc + int64(r.Components)*(*refB-rep.RefBytes)
		a.pay += r.ClassPackedNew["tarpay"]
		a.hdr += r.ClassPackedNew["tarhdr"]
		a.unit += r.ClassPackedNew["unit"]
		a.plain += r.ClassPackedNew["plain"]
		a.refs += int64(r.Components) * *refB
		a.inline += r.InlineBytes
		a.meta += r.PlanMetaBytes
	}
	fmt.Println("| family | representation | total % | new payload % | new header/small-member % | new units % | other new % | references % | inline % | skeleton/recipe % |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, f := range fams {
		for _, r := range strings.Split(*reps, ",") {
			a := by[[2]string{f, r}]
			if a == nil {
				continue
			}
			other := a.total - a.pay - a.hdr - a.unit - a.refs - a.inline - a.meta
			fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", short(f), r, pct(a.total, a.logical), pct(a.pay, a.logical), pct(a.hdr, a.logical), pct(a.unit, a.logical),
				pct(other, a.logical), pct(a.refs, a.logical), pct(a.inline, a.logical), pct(a.meta, a.logical))
		}
	}
}
