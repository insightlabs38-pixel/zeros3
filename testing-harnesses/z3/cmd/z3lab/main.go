// z3lab runs the Z3 representation laboratory over a corpus.
//
//	z3lab -corpus DIR            measure an existing corpus (z3corpus gen)
//	z3lab -gen -size-kib 1024    generate a temporary corpus, measure, delete it
//
// Output: a table on stdout and, with -json, a machine-readable report.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/corpus"
	"zeros3-z3/lab"
)

func main() {
	cc := corpus.DefaultConfig()
	dir := flag.String("corpus", "", "existing corpus directory")
	gen := flag.Bool("gen", false, "generate a temporary corpus instead of -corpus")
	flag.Uint64Var(&cc.Seed, "seed", cc.Seed, "with -gen: corpus seed")
	flag.IntVar(&cc.SizeKiB, "size-kib", cc.SizeKiB, "with -gen: base artifact size")
	flag.IntVar(&cc.Revisions, "revisions", cc.Revisions, "with -gen: revisions after r0")
	fams := flag.String("families", "", "comma-separated family globs")
	reps := flag.String("reps", "", "comma-separated representation names")
	jsonOut := flag.String("json", "", "write the machine-readable report here")
	refBytes := flag.Int64("ref-bytes", lab.DefaultRefBytes, "recipe bytes charged per component reference")
	noPack := flag.Bool("no-pack", false, "skip pack-compression estimation")
	ranges := flag.Int("range-reads", 3, "sampled range reads per revision")
	detail := flag.Bool("per-family", false, "print per-family tables")
	flag.Parse()

	root := *dir
	if *gen {
		tmp, err := os.MkdirTemp("", "z3lab-corpus-")
		if err != nil {
			fatal(err)
		}
		defer os.RemoveAll(tmp)
		root = tmp
		if *fams != "" {
			cc.Families = strings.Split(*fams, ",")
		}
		if _, err := corpus.WriteDir(cc, root); err != nil {
			fatal(err)
		}
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "z3lab: need -corpus DIR or -gen")
		os.Exit(2)
	}
	cfg := lab.DefaultRunConfig(root)
	cfg.RefBytes, cfg.Pack, cfg.RangeReads = *refBytes, !*noPack, *ranges
	if *fams != "" {
		cfg.Families = strings.Split(*fams, ",")
	}
	if *reps != "" {
		cfg.Reps = strings.Split(*reps, ",")
	}
	rep, err := lab.Run(cfg)
	if err != nil {
		fatal(err)
	}
	lab.WriteTable(os.Stdout, rep, *detail)
	if *jsonOut != "" {
		if err := lab.WriteJSON(*jsonOut, rep); err != nil {
			fatal(err)
		}
	}
	if p := lab.Problems(rep); len(p) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d rows not ok:\n", len(p))
		for i, s := range p {
			if i == 20 {
				fmt.Fprintln(os.Stderr, "  ...")
				break
			}
			fmt.Fprintln(os.Stderr, " ", s)
		}
		for _, x := range rep.Rows {
			if x.Status == "invalid" {
				os.Exit(1)
			}
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
