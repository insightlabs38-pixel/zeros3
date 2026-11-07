// Command z4b7 drives the Z4-B7 planner study.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	pl "zeros3-z3/planner2"
)

var (
	series   = flag.String("series", "../z4/b7/series.json", "captured Z4 series")
	frontier = flag.String("frontier", "../testdata/q6-admission/frontier.json.gz", "Z3-36 frontier")
	mode     = flag.String("mode", "explore", "explore")
	only     = flag.String("only", "", "lineage name substring")
)

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4b7:", err)
		os.Exit(1)
	}
}

func loadAll() (lins []pl.Lineage, corp float64) {
	_, sl, err := pl.LoadSeriesFile(*series)
	die(err)
	for _, s := range sl {
		lins = append(lins, s)
	}
	c, zl, err := pl.LoadFrontier(*frontier)
	die(err)
	_ = c
	for _, z := range zl {
		if len(z.Name()) > 3 && (z.Name()[:3] == "no-" || z.Name()[:4] == "one-") {
			continue
		}
		lins = append(lins, z)
	}
	return lins, 0
}

func main() {
	flag.Parse()
	lins, _ := loadAll()
	switch *mode {
	case "explore":
		explore(lins)
	default:
		die(fmt.Errorf("unknown mode"))
	}
}

func explore(lins []pl.Lineage) {
	cfg := pl.DefaultConfig()
	cfg.Overhead = 64
	pr := pl.Calibrate(lins)
	fmt.Printf("pooled prior (all lineages, exploration only): %+v\n", pr)
	names := pl.PolicyNames
	fmt.Printf("%-28s", "lineage")
	for _, n := range names {
		fmt.Printf(" %9.9s", n)
	}
	fmt.Println()
	tot := map[string]float64{}
	var keys []string
	for _, l := range lins {
		if *only != "" && !contains(l.Name(), *only) {
			continue
		}
		keys = append(keys, l.Name())
	}
	sort.Strings(keys)
	for _, l := range lins {
		if *only != "" && !contains(l.Name(), *only) {
			continue
		}
		sp := pl.DefaultSpec(l.Name())
		w := pl.Build(sp, []pl.Lineage{l})
		var base float64
		fmt.Printf("%-28.28s", l.Name())
		for i, n := range names {
			p, err := pl.Preset(n, pr)
			die(err)
			led, err := pl.Run(w, p, cfg)
			die(err)
			v := float64(led.FinalPhys)
			if i == 0 {
				base = v
			}
			tot[n] += v / base
			fmt.Printf(" %9.3f", v/base)
		}
		fmt.Println()
	}
	fmt.Printf("%-28s", "MEAN ratio vs cdc")
	for _, n := range names {
		fmt.Printf(" %9.3f", tot[n]/float64(len(keys)))
	}
	fmt.Println()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
