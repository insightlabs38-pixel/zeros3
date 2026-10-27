// Command z3sst is the Z3-30 SST re-encoding study: audit the producers' tables,
// measure the structural baselines and the entry-level exact representations.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3sst audit|... <args>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen-ops":
		err = cmdGenOps(os.Args[2:])
	case "rt":
		err = cmdRT(os.Args[2:])
	case "snappy":
		err = cmdSnappy(os.Args[2:])
	case "econ":
		err = cmdEcon(os.Args[2:])
	case "logical":
		err = cmdLogical(os.Args[2:])
	case "audit":
		err = cmdAudit(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3sst:", err)
		os.Exit(1)
	}
}
