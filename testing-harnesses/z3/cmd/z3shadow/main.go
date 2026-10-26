// Command z3shadow runs the Z3-22 production shadow-integration measurements
// against the real ZeroS3 store (see prodshadow). Nothing it builds is committed:
// the unmodified zeros3.go and a one-method-renamed bridge copy are compiled into
// the OS temp directory.
//
//	z3shadow [-work DIR] [-pack-mib N] [-scale small|full] chains|mapping|reads|tiers|restart|failures|lifecycle|all
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/prodshadow"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3shadow:", err)
		os.Exit(1)
	}
}

func main() {
	work := flag.String("work", "", "scratch directory (default: a new temp dir)")
	packMiB := flag.Int("pack-mib", 4, "compaction pack target (MiB; production default is 64)")
	scale := flag.String("scale", "small", "small|full")
	only := flag.String("only", "", "comma-separated chain names to run")
	flag.Parse()
	if *work == "" {
		d, err := os.MkdirTemp("", "z3shadow-work-")
		must(err)
		*work = d
	}
	b, err := prodshadow.Build()
	must(err)
	cmd := "all"
	if flag.NArg() > 0 {
		cmd = flag.Arg(0)
	}
	cfg := config{b: b, work: *work, packMiB: *packMiB, full: *scale == "full", only: strings.Split(*only, ",")}
	switch cmd {
	case "chains":
		cfg.chains()
	case "mapping":
		cfg.mapping()
	case "reads":
		cfg.reads()
	case "restart":
		cfg.restart()
	case "scenarios":
		cfg.scenarios()
	case "all":
		cfg.mapping()
		cfg.chains()
		cfg.reads()
		cfg.restart()
		cfg.scenarios()
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}
