// z3deflate characterizes the native exact DEFLATE parser/replayer (Z3-05)
// over every DEFLATE-bearing family of a corpus: parse success, bit-exact replay,
// structural counters and throughput. Costs are not reported: the explicit IR
// has no physical encoding yet.
//
//	z3deflate -corpus DIR [-families glob,...] [-json OUT]
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

type agg struct {
	format                            string
	revs, exact, bad                  int
	streams                           int
	comp, plain                       int64
	blocks, stored, fixed, dyn        int
	tokens, lits, matches, matchBytes int64
	hdr, tokB, naive                  int64
	parseMs, replayMs                 float64
	nonCanon                          int64
}

func geti(d map[string]any, k string) int64 {
	switch v := d[k].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func getf(d map[string]any, k string) float64 {
	if v, ok := d[k].(float64); ok {
		return v
	}
	return 0
}

func main() {
	dir := flag.String("corpus", "", "corpus directory (required)")
	fams := flag.String("families", "", "comma-separated family globs")
	jsonOut := flag.String("json", "", "write the lab report here")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "z3deflate: -corpus is required")
		os.Exit(2)
	}
	cfg := lab.DefaultRunConfig(*dir)
	cfg.Reps = []string{"deflate-ir"}
	cfg.RangeReads = 0
	if *fams != "" {
		cfg.Families = strings.Split(*fams, ",")
	}
	rep, err := lab.Run(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	by := map[string]*agg{}
	var names []string
	for _, r := range rep.Rows {
		a := by[r.Family]
		if a == nil {
			a = &agg{format: r.Format}
			by[r.Family] = a
			names = append(names, r.Family)
		}
		a.revs++
		if r.Status != "ok" {
			a.bad++
			fmt.Fprintf(os.Stderr, "%s/%s: %s %s\n", r.Family, r.Revision, r.Status, r.Error)
			continue
		}
		if r.RoundtripExact == "yes" {
			a.exact++
		}
		d := r.Detail
		a.streams += int(geti(d, "streams"))
		a.comp += geti(d, "compressed_bytes")
		a.plain += geti(d, "plain_bytes")
		a.blocks += int(geti(d, "blocks"))
		a.stored += int(geti(d, "stored_blocks"))
		a.fixed += int(geti(d, "fixed_blocks"))
		a.dyn += int(geti(d, "dynamic_blocks"))
		a.tokens += geti(d, "tokens")
		a.lits += geti(d, "literals")
		a.matches += geti(d, "matches")
		a.matchBytes += geti(d, "match_bytes")
		a.hdr += geti(d, "dyn_header_bytes")
		a.tokB += geti(d, "token_bytes")
		a.naive += geti(d, "naive_ir_bytes")
		a.nonCanon += geti(d, "noncanonical_lengths")
		a.parseMs += getf(d, "parse_ms")
		a.replayMs += getf(d, "replay_ms")
	}
	sort.Strings(names)
	pct := func(v, d int64) string {
		if d == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f", 100*float64(v)/float64(d))
	}
	rate := func(plain int64, ms float64) string {
		if ms <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.0f", float64(plain)/(1<<20)/(ms/1000))
	}
	fmt.Println("| family | fmt | streams | comp MiB | plain MiB | blocks S/F/D | tokens | lit% | avg match | dyn hdr % of comp | naive IR % of comp | parse MiB/s | replay MiB/s | exact |")
	fmt.Println("|---|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---|")
	var tc, tp int64
	var tpm, trm float64
	for _, n := range names {
		a := by[n]
		avg := "-"
		if a.matches > 0 {
			avg = fmt.Sprintf("%.1f", float64(a.matchBytes)/float64(a.matches))
		}
		fmt.Printf("| %s | %s | %d | %.2f | %.2f | %d/%d/%d | %d | %s | %s | %s | %s | %s | %s | %d/%d |\n",
			n, a.format, a.streams, float64(a.comp)/(1<<20), float64(a.plain)/(1<<20), a.stored, a.fixed, a.dyn, a.tokens,
			pct(a.lits, a.tokens), avg, pct(a.hdr, a.comp), pct(a.naive, a.comp), rate(a.plain, a.parseMs), rate(a.plain, a.replayMs), a.exact, a.revs)
		tc += a.comp
		tp += a.plain
		tpm += a.parseMs
		trm += a.replayMs
	}
	fmt.Printf("\nTotal: %.1f MiB compressed, %.1f MiB plaintext; parse %s MiB/s, replay %s MiB/s (plaintext, single-threaded, includes allocation)\n",
		float64(tc)/(1<<20), float64(tp)/(1<<20), rate(tp, tpm), rate(tp, trm))
	if *jsonOut != "" {
		if err := lab.WriteJSON(*jsonOut, rep); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, a := range by {
		if a.bad > 0 || a.exact != a.revs {
			os.Exit(1)
		}
	}
}
