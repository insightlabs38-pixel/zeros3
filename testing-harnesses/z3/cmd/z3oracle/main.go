// z3oracle reports the Preflate oracle experiment (Z3-04) over DEFLATE-bearing
// families. It needs the optional z3preflate binary (oracle/preflate/setup.sh,
// or Z3_PREFLATE) and skips cleanly without it.
//
//	z3oracle -corpus DIR [-ext DIR] [-json OUT] [-families glob,...]
//
// Costs are update revisions (r1..rN) only, in the lab's cumulative-family
// accounting, with the production pack rule applied to new CAS components.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

func main() {
	dir := flag.String("corpus", "", "stdlib-producer corpus (z3corpus gen)")
	ext := flag.String("ext", "", "optional external-producer corpus (z3corpus ext)")
	jsonOut := flag.String("json", "", "write the lab report of the stdlib corpus here")
	fams := flag.String("families", "", "comma-separated family globs")
	flag.Parse()
	bin, ok := lab.PreflateBinary()
	if !ok {
		fmt.Println("z3oracle: preflate oracle not built (run oracle/preflate/setup.sh or set Z3_PREFLATE); skipping")
		return
	}
	fmt.Println("oracle:", bin)
	if *dir == "" && *ext == "" {
		fmt.Fprintln(os.Stderr, "z3oracle: need -corpus and/or -ext")
		os.Exit(2)
	}
	for _, c := range []struct{ label, dir string }{{"stdlib guaranteed corpus", *dir}, {"optional external-producer corpus", *ext}} {
		if c.dir == "" {
			continue
		}
		cfg := lab.DefaultRunConfig(c.dir)
		cfg.Reps = []string{"cdc", "oracle-logical", "preflate", "preflate-rcdc"}
		cfg.RangeReads = 0
		if *fams != "" {
			cfg.Families = strings.Split(*fams, ",")
		}
		rep, err := lab.Run(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("\n## %s\n\n", c.label)
		table(rep)
		if *jsonOut != "" && c.dir == *dir {
			if err := lab.WriteJSON(*jsonOut, rep); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
}

type agg struct {
	n                                   int
	comp, plain                         int64
	incr, packed                        int64
	newPacked, meta                     int64
	recon, other                        int64
	analyzeMs, recreateMs, reconstructS float64
	streams, ok                         int
	r0Comp, r0Recon                     int64
	exact, bad                          int
	fails                               map[string]int
	params                              string
}

func table(rep *lab.Report) {
	type key struct{ fam, rep string }
	sums := map[key]*agg{}
	fmts := map[string]string{}
	var fams []string
	for _, r := range rep.Rows {
		fmts[r.Family] = r.Format
		k := key{r.Family, r.Rep}
		a := sums[k]
		if a == nil {
			a = &agg{fails: map[string]int{}}
			sums[k] = a
			if r.Rep == "cdc" {
				fams = append(fams, r.Family)
			}
		}
		d := r.Detail
		if r.Index == 0 {
			if v, ok := d["compressed_bytes"].(int64); ok {
				a.r0Comp += v
			}
			continue
		}
		a.n++
		if r.Status != "ok" {
			a.bad++
			continue
		}
		a.comp += r.LogicalSize
		if r.IncrementalPacked != nil {
			a.packed += *r.IncrementalPacked
		}
		a.incr += r.Incremental
		a.newPacked += r.PackedNew
		a.meta += r.MetaBytes + r.IndexBytes
		if r.RoundtripExact == "yes" {
			a.exact++
		}
		if r.ReconstructMs != nil {
			a.reconstructS += *r.ReconstructMs
		}
		if d != nil {
			a.plain += asInt(d["plain_bytes"])
			a.recon += asInt(d["recon_bytes"])
			a.streams += int(asInt(d["streams"]))
			a.ok += int(asInt(d["streams_ok"]))
			a.analyzeMs += asF(d["analyze_ms"])
			a.recreateMs += asF(d["recreate_ms"])
			if m, ok := d["fail_classes"].(map[string]int); ok {
				for c, n := range m {
					a.fails[c] += n
				}
			}
			if ps, ok := d["params"].([]string); ok && len(ps) > 0 && a.params == "" {
				a.params = ps[0]
			}
		}
	}
	sort.Strings(fams)
	mib := func(v int64) float64 { return float64(v) / (1 << 20) }
	pct := func(v, d int64) string {
		if d == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f", 100*float64(v)/float64(d))
	}
	fmt.Println("| family | fmt | comp MiB | plain MiB | cdc% | plain-oracle% | preflate% (new plain + recon + other) | preflate-rcdc% | vs cdc | recon/comp% | stream ok | analyze ms/MiB | recreate ms/MiB | exact |")
	fmt.Println("|---|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---|")
	for _, f := range fams {
		c, o, p, rc := sums[key{f, "cdc"}], sums[key{f, "oracle-logical"}], sums[key{f, "preflate"}], sums[key{f, "preflate-rcdc"}]
		if p == nil || c == nil {
			continue
		}
		plainOr := "-"
		if o != nil {
			plainOr = pct(o.packed, c.comp)
		}
		rcd := "-"
		if rc != nil && rc.bad == 0 {
			rcd = pct(rc.packed, c.comp)
		}
		other := p.meta - p.recon
		fmt.Printf("| %s | %s | %.2f | %.2f | %s | %s | %s (%s + %s + %s) | %s | %s | %s | %d/%d | %.0f | %.0f | %d/%d |\n",
			f, fmts[f], mib(c.comp), mib(p.plain), pct(c.packed, c.comp), plainOr,
			pct(p.packed, c.comp), pct(p.newPacked, c.comp), pct(p.recon, c.comp), pct(other, c.comp),
			rcd, vs(p.packed, c.packed), pct(p.recon, c.comp), p.ok, p.streams,
			p.analyzeMs/max(mib(p.comp), 1e-9), p.recreateMs/max(mib(p.comp), 1e-9), p.exact, p.n-p.bad)
	}
	var failed []string
	for _, f := range fams {
		if p := sums[key{f, "preflate"}]; p != nil && p.ok < p.streams {
			failed = append(failed, fmt.Sprintf("%s %v", f, p.fails))
		}
	}
	if len(failed) > 0 {
		fmt.Println("\nUnsupported streams (fell back to CDC of the compressed bytes):")
		for _, s := range failed {
			fmt.Println("-", s)
		}
	}
	fmt.Println("\nPercentages are of the family's update-revision compressed bytes (r1..rN). 'other' = recipe refs, inline wrapper bytes and header/trailer; vs cdc = preflate packed / cdc packed.")
}

func vs(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

func asF(v any) float64 {
	if x, ok := v.(float64); ok {
		return x
	}
	return 0
}
