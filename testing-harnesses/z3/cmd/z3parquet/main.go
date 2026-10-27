// Command z3parquet is the Z3-31 Parquet re-encoding study: audit the producers' files,
// measure the structural baselines and the logical-value exact representations.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3parquet audit|econ|... <args>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "audit":
		err = cmdAudit(os.Args[2:])
	case "econ":
		err = cmdEcon(os.Args[2:])
	case "rt":
		err = cmdRT(os.Args[2:])
	case "chain":
		err = cmdChain(os.Args[2:])
	case "reads":
		err = cmdReads(os.Args[2:])
	case "perf":
		err = cmdPerf(os.Args[2:])
	case "isolation":
		err = cmdIsolation(os.Args[2:])
	case "contract":
		err = cmdContract(os.Args[2:])
	case "dump":
		err = cmdDump(os.Args[2:])
	case "overhead":
		err = cmdOverhead(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3parquet:", err)
		os.Exit(1)
	}
}
