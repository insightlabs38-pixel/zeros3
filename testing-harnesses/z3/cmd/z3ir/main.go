// z3ir reports the Z3-06/07 reconstruction-IR experiments.
//
//	z3ir units   -corpus DIR [-families g,..] [-policies p,..]   unit survival across revisions
//	z3ir entropy -corpus DIR [-families g,..]                    IR size / entropy per codec
//	z3ir family  -corpus DIR [-families g,..] [-reps r,..] [-ref-bytes N]   family cost with breakdown
//
// Everything is laboratory evidence over scratch corpora (z3corpus gen/ext).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3ir units|entropy|family|predict|chooser -corpus DIR [flags]")
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "units":
		unitsCmd(args)
	case "entropy":
		entropyCmd(args)
	case "family":
		familyCmd(args)
	case "predict":
		predictCmd(args)
	case "chooser":
		chooserCmd(args)
	case "matrix":
		matrixCmd(args)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand", os.Args[1])
		os.Exit(2)
	}
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

type common struct {
	dir      string
	families []string
}

func commonFlags(fs *flag.FlagSet) (*string, *string) {
	d := fs.String("corpus", "", "corpus directory (z3corpus gen/ext)")
	f := fs.String("families", "", "comma-separated family globs")
	return d, f
}

func pct(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*float64(a)/float64(b))
}
