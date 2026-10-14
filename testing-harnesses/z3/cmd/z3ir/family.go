package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

// famRow aggregates one (family, representation) over the update revisions.
type famRow struct {
	rep                         string
	revs                        int
	logical                     int64 // compressed bytes of update revisions
	packed, raw                 int64
	plainNew, unitNew, otherNew int64 // packed new bytes by class
	refs, skel, recon, inl      int64 // reference, skeleton, recon-blob, inline bytes
	base                        int64 // r0 packed incremental
	baseLogical                 int64
	exact, bad                  int
	newUnits, unitRefs          int
	analyzeMs, replayMs         float64
	plainBytes                  int64
	alloc                       uint64
}

// aggregate sums update revisions per (family, rep). The lab charged runBytes
// per reference; refBytes re-costs references (a pure post-hoc adjustment).
func aggregate(rep *lab.Report, refBytes, runBytes int64) (map[string][]*famRow, []string) {
	out := map[string][]*famRow{}
	idx := map[string]*famRow{}
	var fams []string
	seenFam := map[string]bool{}
	for _, r := range rep.Rows {
		if !seenFam[r.Family] {
			seenFam[r.Family] = true
			fams = append(fams, r.Family)
		}
		k := r.Family + "\x00" + r.Rep
		a := idx[k]
		if a == nil {
			a = &famRow{rep: r.Rep}
			idx[k] = a
			out[r.Family] = append(out[r.Family], a)
		}
		if r.Status != "ok" || r.CostNA {
			a.bad++
			continue
		}
		inc := r.Incremental
		if r.IncrementalPacked != nil {
			inc = *r.IncrementalPacked
		}
		adj := int64(r.Components) * (refBytes - runBytes)
		inc += adj
		if r.Index == 0 {
			a.base += inc
			a.baseLogical += r.LogicalSize
			continue
		}
		a.revs++
		a.logical += r.LogicalSize
		a.packed += inc
		a.raw += r.Incremental + adj
		if r.RoundtripExact == "yes" {
			a.exact++
		}
		var cp int64
		for cl, v := range r.ClassPackedNew {
			switch cl {
			case "plain", "tarhdr", "tarpay":
				a.plainNew += v
			case "unit":
				a.unitNew += v
			default:
				a.otherNew += v
			}
			cp += v
		}
		// IncrementalPack = PackedNew + MetaBytes + IndexBytes (PackedNew is not serialised)
		packedNew := inc - adj
		if r.IncrementalPacked != nil {
			packedNew = *r.IncrementalPacked - r.MetaBytes - r.IndexBytes
		} else {
			packedNew = r.Incremental - r.MetaBytes - r.IndexBytes
		}
		a.otherNew += packedNew - cp
		a.refs += int64(r.Components) * refBytes
		a.inl += r.InlineBytes
		if r.Detail != nil {
			a.skel += detInt(r.Detail, "ir_skeleton_bytes")
			if b, _ := r.Detail["recon_in_store"].(bool); !b { // chunks in the store are already "other"
				a.recon += detInt(r.Detail, "recon_bytes")
			}
			a.plainBytes += detInt(r.Detail, "plain_bytes")
			a.analyzeMs += detFloat(r.Detail, "analyze_ms")
			a.replayMs += detFloat(r.Detail, "replay_ms")
		}
		a.newUnits += r.ClassNewComps["unit"]
		a.unitRefs += r.ClassRefs["unit"]
		a.alloc += r.AllocBytes
	}
	return out, fams
}

func detInt(d map[string]any, k string) int64 {
	switch v := d[k].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func detFloat(d map[string]any, k string) float64 {
	switch v := d[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func mibf(v int64) string { return fmt.Sprintf("%.3f", float64(v)/(1<<20)) }

func familyCmd(args []string) {
	fs := flag.NewFlagSet("family", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	reps := fs.String("reps", "cdc,preflate,preflate-rcdc,ir-mono-e2,ir-block-e2,ir-tok2k-e2,ir-tok4k-e2,ir-tok16k-e2", "representations")
	refB := fs.Int64("ref-bytes", lab.DefaultRefBytes, "recipe bytes per component reference")
	jsonOut := fs.String("json", "", "write the lab report here")
	from := fs.String("from", "", "read a lab report written earlier with -json instead of running")
	showRef := fs.Int64("show-ref-bytes", 0, "with -from: re-cost recipe references at this many bytes per reference")
	show := fs.String("show", "", "comma-separated representations to print (default all)")
	fs.Parse(args)
	var rep *lab.Report
	if *from != "" {
		var err error
		for _, f := range split(*from) { // several reports are merged
			r, e := lab.ReadJSON(f)
			if e != nil {
				fatal(e)
			}
			if rep == nil {
				rep = r
			} else {
				rep.Rows = append(rep.Rows, r.Rows...)
			}
		}
		_ = err
		if *showRef > 0 {
			*refB = *showRef
		} else {
			*refB = rep.RefBytes
		}
	} else {
		if *dir == "" {
			fatal(fmt.Errorf("-corpus is required"))
		}
		cfg := lab.DefaultRunConfig(*dir)
		cfg.Reps, cfg.RefBytes, cfg.RangeReads = split(*reps), *refB, 0
		cfg.Families = split(*fams)
		var err error
		if rep, err = lab.Run(cfg); err != nil {
			fatal(err)
		}
		if *jsonOut != "" {
			if err := lab.WriteJSON(*jsonOut, rep); err != nil {
				fatal(err)
			}
		}
	}
	agg, order := aggregate(rep, *refB, rep.RefBytes)
	fmt.Printf("ref bytes per component reference: %d. Costs: update revisions r1..rN, packed; %% of the update revisions' compressed bytes.\n\n", *refB)
	fmt.Println("| family | representation | incr % | vs CDC | new plaintext % | new units % | refs % | skeleton/recon % | other % | r0 entry % | cum. incl. r0 % | exact |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|")
	bad := false
	for _, fam := range order {
		var cdc *famRow
		for _, a := range agg[fam] {
			if a.rep == "cdc" {
				cdc = a
			}
		}
		for _, a := range agg[fam] {
			if len(*show) > 0 && !strings.Contains(","+*show+",", ","+a.rep+",") {
				continue
			}
			if a.revs == 0 {
				fmt.Printf("| %s | %s | n/a (%d rows not ok) |||||||||\n", fam, a.rep, a.bad)
				continue
			}
			vs := "-"
			if cdc != nil && cdc.packed > 0 {
				vs = fmt.Sprintf("%.0f%%", 100*float64(a.packed)/float64(cdc.packed))
			}
			l := a.logical
			baseAvg := float64(a.base) / float64(max(a.baseLogical, 1)) * 100
			cum := 100 * float64(a.base+a.packed) / float64(a.baseLogical+a.logical)
			ex := fmt.Sprintf("%d/%d", a.exact, a.revs)
			if a.exact != a.revs {
				bad = true
			}
			fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %.1f | %.1f | %s |\n", fam, a.rep, pct(a.packed, l), vs,
				pct(a.plainNew, l), pct(a.unitNew, l), pct(a.refs+a.inl, l), pct(a.skel+a.recon, l), pct(a.otherNew, l), baseAvg, cum, ex)
		}
	}
	_ = sort.Strings
	if bad {
		fmt.Fprintln(os.Stderr, "WARNING: some rows are not exact")
		os.Exit(1)
	}
}

// matrixCmd prints families x representations for one metric from saved reports.
func matrixCmd(args []string) {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	from := fs.String("from", "", "comma-separated lab reports")
	reps := fs.String("reps", "", "comma-separated representations (columns)")
	refB := fs.Int64("ref-bytes", 35, "recipe bytes per component reference")
	metric := fs.String("metric", "incr", "incr (update revisions), cum (r0..rN), entry (r0 only)")
	fams := fs.String("families", "", "family globs (default all)")
	fs.Parse(args)
	var rep *lab.Report
	for _, f := range split(*from) {
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
	if rep == nil {
		fatal(fmt.Errorf("-from is required"))
	}
	agg, order := aggregate(rep, *refB, rep.RefBytes)
	cols := split(*reps)
	fmt.Printf("| family (%s, %d B/ref) |", *metric, *refB)
	sep := "|---|"
	for _, c := range cols {
		fmt.Printf(" %s |", c)
		sep += "---:|"
	}
	fmt.Println()
	fmt.Println(sep)
	for _, fam := range order {
		if len(*fams) > 0 {
			ok := false
			for _, g := range split(*fams) {
				if m, _ := path.Match(g, fam); m {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		row := map[string]*famRow{}
		for _, a := range agg[fam] {
			row[a.rep] = a
		}
		fmt.Printf("| %s |", fam)
		for _, c := range cols {
			a := row[c]
			switch {
			case a == nil || a.revs == 0:
				fmt.Printf(" - |")
			case *metric == "incr":
				fmt.Printf(" %s |", pct(a.packed, a.logical))
			case *metric == "entry":
				fmt.Printf(" %.0f |", 100*float64(a.base)/float64(max(a.baseLogical, 1)))
			default:
				fmt.Printf(" %.1f |", 100*float64(a.base+a.packed)/float64(a.baseLogical+a.logical))
			}
		}
		fmt.Println()
	}
}
