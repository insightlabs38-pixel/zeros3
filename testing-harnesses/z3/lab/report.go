package lab

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// WriteJSON writes the machine-readable report.
func WriteJSON(path string, r *Report) error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Sum aggregates the update revisions (index >= 1) of one representation.
type Sum struct {
	Family, Format, Rep string
	Oracle              bool
	CostNA              bool // experimental: no physical cost defined
	Revs                int
	Logical             int64
	Incr                int64
	Packed              int64
	PackedKnown         bool
	Invalid, Errors     int
	Exact               int
	Reuse, New          int64
}

func (s Sum) pctLogical(v int64) float64 {
	if s.Logical == 0 {
		return 0
	}
	return 100 * float64(v) / float64(s.Logical)
}

// Summarize aggregates rows by key(row). Base revisions are excluded: every
// representation pays roughly the object size for r0, which says nothing
// about reuse.
func Summarize(rows []Row, key func(Row) (family, format string)) []Sum {
	idx := map[string]*Sum{}
	var order []string
	for _, r := range rows {
		if r.Index == 0 {
			continue
		}
		fam, fmtName := key(r)
		k := fam + "\x00" + fmtName + "\x00" + r.Rep
		s := idx[k]
		if s == nil {
			s = &Sum{Family: fam, Format: fmtName, Rep: r.Rep, Oracle: r.Oracle, CostNA: r.CostNA, PackedKnown: !r.CostNA}
			idx[k] = s
			order = append(order, k)
		}
		switch r.Status {
		case "error":
			s.Errors++
			continue
		case "invalid":
			s.Invalid++
		}
		s.Revs++
		s.Logical += r.LogicalSize
		if r.RoundtripExact == "yes" {
			s.Exact++
		}
		if r.CostNA {
			continue
		}
		s.Incr += r.Incremental
		s.Reuse += r.ReuseBytes
		s.New += r.NewBytes
		if r.IncrementalPacked != nil {
			s.Packed += *r.IncrementalPacked
		} else {
			s.PackedKnown = false
		}
	}
	out := make([]Sum, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	return out
}

// cost returns the comparison cost: packed when both sides know it.
func cost(s, base Sum) (int64, int64) {
	if s.PackedKnown && base.PackedKnown {
		return s.Packed, base.Packed
	}
	return s.Incr, base.Incr
}

func mib(v int64) string { return fmt.Sprintf("%.2f", float64(v)/(1<<20)) }

func rtLabel(s Sum) string {
	switch {
	case s.Invalid > 0:
		return fmt.Sprintf("INVALID(%d)", s.Invalid)
	case s.Exact == s.Revs && s.Revs > 0:
		return "exact"
	case s.Oracle:
		return "oracle"
	}
	return "N/A"
}

// WriteTable prints a concise human-readable report.
func WriteTable(w io.Writer, r *Report, perFamily bool) {
	fmt.Fprintf(w, "Z3 lab (%s) refBytes=%d pack=%v  corpus: size=%dKiB revisions=%d seed=%d\n",
		r.Schema, r.RefBytes, r.Pack, r.Corpus.SizeKiB, r.Corpus.Revisions, r.Corpus.Seed)
	fmt.Fprintln(w, "Costs are for update revisions (r1..rN) only: bytes added beyond what the family already stores.")
	fmt.Fprintln(w, "'incr%' = incremental physical bytes / logical bytes; 'packed%' applies the production pack rule; 'vsCDC' = cost relative to current ZeroS3 CDC (<100% beats it).")

	byFormat := Summarize(r.Rows, func(x Row) (string, string) { return x.Format, x.Format })
	formats := orderedFormats(byFormat)
	for _, f := range formats {
		fmt.Fprintf(w, "\n== %s ==\n", f)
		writeSums(w, filter(byFormat, f), false)
	}
	if perFamily {
		perFam := Summarize(r.Rows, func(x Row) (string, string) { return x.Family, x.Format })
		var fams []string
		seen := map[string]bool{}
		for _, s := range perFam {
			if !seen[s.Family] {
				seen[s.Family] = true
				fams = append(fams, s.Family)
			}
		}
		for _, fam := range fams {
			fmt.Fprintf(w, "\n-- %s --\n", fam)
			var ss []Sum
			for _, s := range perFam {
				if s.Family == fam {
					ss = append(ss, s)
				}
			}
			writeSums(w, ss, true)
		}
	}
	writeRanking(w, Summarize(r.Rows, func(x Row) (string, string) { return x.Family, x.Format }))
}

func orderedFormats(ss []Sum) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s.Format] {
			seen[s.Format] = true
			out = append(out, s.Format)
		}
	}
	sort.Strings(out)
	return out
}

func filter(ss []Sum, format string) []Sum {
	var out []Sum
	for _, s := range ss {
		if s.Format == format {
			out = append(out, s)
		}
	}
	return out
}

func writeSums(w io.Writer, ss []Sum, withErr bool) {
	var cdc Sum
	for _, s := range ss {
		if s.Rep == "cdc" {
			cdc = s
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "representation\tlogicalMiB\tincrMiB\tincr%\tpackedMiB\tpacked%\tvsCDC\treuse%\troundtrip\terr")
	for _, s := range ss {
		packed, pct, vs := "N/A", "N/A", "N/A"
		if s.PackedKnown {
			packed, pct = mib(s.Packed), fmt.Sprintf("%.1f", s.pctLogical(s.Packed))
		}
		if cdc.Revs > 0 && s.Revs > 0 {
			a, b := cost(s, cdc)
			if b > 0 {
				vs = fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
			}
		}
		reuse := "N/A"
		if s.Reuse+s.New > 0 {
			reuse = fmt.Sprintf("%.1f", 100*float64(s.Reuse)/float64(s.Reuse+s.New))
		}
		name := s.Rep
		if s.Oracle {
			name += "*"
		}
		if s.CostNA {
			fmt.Fprintf(tw, "%s\t%s\tN/A\tN/A\tN/A\tN/A\tN/A\tN/A\t%s\t%d\n", name+"\u2020", mib(s.Logical), rtLabel(s), s.Errors)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%d\n", name, mib(s.Logical), mib(s.Incr), s.pctLogical(s.Incr),
			packed, pct, vs, reuse, rtLabel(s), s.Errors)
	}
	tw.Flush()
}

// writeRanking lists workloads by how much of the update cost CDC leaves on
// the table relative to the best valid structural representation and to the
// best oracle.
func writeRanking(w io.Writer, fam []Sum) {
	type line struct {
		family, format string
		cdcPct         float64
		bestRep        string
		bestPct        float64
		oracleRep      string
		oraclePct      float64
	}
	byFam := map[string][]Sum{}
	var order []string
	for _, s := range fam {
		if _, ok := byFam[s.Family]; !ok {
			order = append(order, s.Family)
		}
		byFam[s.Family] = append(byFam[s.Family], s)
	}
	var lines []line
	for _, f := range order {
		ss := byFam[f]
		var cdc Sum
		for _, s := range ss {
			if s.Rep == "cdc" {
				cdc = s
			}
		}
		if cdc.Revs == 0 || cdc.Packed == 0 {
			continue
		}
		l := line{family: f, format: ss[0].Format, cdcPct: cdc.pctLogical(cdc.Packed), bestRep: "-", bestPct: cdc.pctLogical(cdc.Packed), oracleRep: "-", oraclePct: cdc.pctLogical(cdc.Packed)}
		for _, s := range ss {
			if s.CostNA || !s.PackedKnown || s.Revs == 0 || s.Rep == "cdc" || s.Rep == "raw" || s.Invalid > 0 || s.Errors > 0 {
				continue
			}
			p := s.pctLogical(s.Packed)
			if s.Oracle {
				if p < l.oraclePct {
					l.oracleRep, l.oraclePct = s.Rep, p
				}
			} else if p < l.bestPct {
				l.bestRep, l.bestPct = s.Rep, p
			}
		}
		if l.oraclePct > l.bestPct {
			l.oracleRep, l.oraclePct = l.bestRep, l.bestPct
		}
		lines = append(lines, l)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].cdcPct-lines[i].oraclePct > lines[j].cdcPct-lines[j].oraclePct })
	fmt.Fprintln(w, "\n== Workloads ranked by absolute reuse gap (CDC packed% minus best representation packed%) ==")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "family\tformat\tcdc packed%\tbest valid\tpacked%\tbest incl. oracle\tpacked%\tgap(pp)")
	for _, l := range lines {
		fmt.Fprintf(tw, "%s\t%s\t%.1f\t%s\t%.1f\t%s\t%.1f\t%.1f\n", l.family, l.format, l.cdcPct, l.bestRep, l.bestPct, l.oracleRep, l.oraclePct, l.cdcPct-l.oraclePct)
	}
	tw.Flush()
	fmt.Fprintln(w, "('*' and 'oracle' = estimate without a reconstruction path; 'best valid' reps are exactly reversible; '\u2020' = experimental, parsed and replayed but no physical cost defined)")
}

// Problems lists rows that failed exact reconstruction or errored.
func Problems(r *Report) []string {
	var out []string
	for _, x := range r.Rows {
		if x.Status != "ok" {
			out = append(out, fmt.Sprintf("%s/%s %s: %s %s", x.Family, x.Revision, x.Rep, x.Status, strings.TrimSpace(x.Error)))
		}
	}
	return out
}
