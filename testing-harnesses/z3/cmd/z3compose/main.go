// Command z3compose runs the Z3-19 compound-representation workloads.
//
//	z3compose oci      A: real node-exporter OCI layers (outer tar+gzip, inner Go ELF)
//	z3compose sqlite   B: OCI-like layers carrying SQLite revisions (inner sqlitez3)
//	z3compose git      C: Git packs holding versions of an executable (positive) and a SQLite database (negative)
//	z3compose car      D: tar.gz archives of CAR exports (inner carz3)
//
// Corpora live outside Git (see the Z3-18/17 caches and $Z3_COMPOSE_CACHE); nothing is committed or fetched by tests.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3compose <oci|sqlite|git|car> [flags]")
		os.Exit(2)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "oci":
		err = cmdOCI(args)
	case "sqlite":
		err = cmdSQLite(args)
	case "git":
		err = cmdGit(args)
	case "car":
		err = cmdCAR(args)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3compose:", err)
		os.Exit(1)
	}
}
