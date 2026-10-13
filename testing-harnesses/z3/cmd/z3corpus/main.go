// z3corpus generates and verifies the deterministic Z3 research corpus.
//
//	z3corpus gen    -out DIR [-seed N] [-size-kib N] [-revisions N] [-families glob,glob] [-max-mib N]
//	z3corpus verify -dir DIR [-regen]
//	z3corpus ext    -out DIR [-seed N] [-size-kib N] [-revisions N]   optional external DEFLATE producers
//	z3corpus list
//
// Generated corpora are scratch data: write them to a temporary directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/corpus"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "gen":
		gen(os.Args[2:])
	case "verify":
		verify(os.Args[2:])
	case "ext":
		ext(os.Args[2:])
	case "list":
		for _, f := range corpus.Families() {
			fmt.Printf("%-28s %-12s %s\n", f.Name, f.Format, f.Workload)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: z3corpus gen|verify|list [flags]")
	os.Exit(2)
}

func gen(args []string) {
	cfg := corpus.DefaultConfig()
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fs.String("out", "", "output directory (required)")
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "generator seed")
	fs.IntVar(&cfg.SizeKiB, "size-kib", cfg.SizeKiB, "approximate base artifact size")
	fs.IntVar(&cfg.Revisions, "revisions", cfg.Revisions, "revisions after r0")
	fs.IntVar(&cfg.MaxTotalMiB, "max-mib", cfg.MaxTotalMiB, "refuse corpora larger than this")
	fams := fs.String("families", "", "comma-separated family globs (default all)")
	fs.Parse(args)
	if *out == "" {
		fmt.Fprintln(os.Stderr, "z3corpus gen: -out is required")
		os.Exit(2)
	}
	if *fams != "" {
		cfg.Families = strings.Split(*fams, ",")
	}
	m, err := corpus.WriteDir(cfg, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var total int64
	for _, mem := range m.Members {
		total += mem.Size
	}
	fmt.Printf("wrote %d members, %.1f MiB, to %s\n", len(m.Members), float64(total)/(1<<20), *out)
}

func verify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "", "corpus directory (required)")
	regen := fs.Bool("regen", false, "also regenerate in memory and compare")
	fs.Parse(args)
	m, err := corpus.ReadManifest(*dir)
	if err == nil {
		err = corpus.VerifyFiles(*dir, m)
	}
	if err == nil && *regen {
		var warns []string
		warns, err = corpus.VerifyRegen(m)
		for _, w := range warns {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("ok: %d members\n", len(m.Members))
}

// ext writes the optional external-producer corpus; missing tools are skipped.
func ext(args []string) {
	cfg := corpus.DefaultConfig()
	fs := flag.NewFlagSet("ext", flag.ExitOnError)
	out := fs.String("out", "", "output directory (required)")
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "generator seed")
	fs.IntVar(&cfg.SizeKiB, "size-kib", 256, "approximate base artifact size")
	fs.IntVar(&cfg.Revisions, "revisions", 2, "revisions after r0")
	fs.Parse(args)
	if *out == "" {
		fmt.Fprintln(os.Stderr, "z3corpus ext: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, skipped, err := corpus.GenerateExt(cfg, *out)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, "skipped:", s)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d external-producer members to %s\n", len(m.Members), *out)
}
