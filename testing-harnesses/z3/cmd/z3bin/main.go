// z3bin is the Z3-12/13 laboratory for reversible binary reference
// normalization (ELF, PE). Laboratory code: prints Markdown, writes nothing
// into the repository.
//
//	corpus   build deterministic Go ELF/PE revision families (go build -overlay)
package main

import (
	"fmt"
	"os"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3bin:", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: z3bin corpus|... [flags]"))
	}
	switch os.Args[1] {
	case "corpus":
		runCorpus(os.Args[2:])
	case "attr":
		runAttr(os.Args[2:])
	case "delta":
		runDelta(os.Args[2:])
	case "runs":
		runRuns(os.Args[2:])
	case "report":
		runReport(os.Args[2:])
	case "idstab":
		runIdStab(os.Args[2:])
	case "dbg":
		runDbg(os.Args[2:])
	case "ccorpus":
		runCCorpus(os.Args[2:])
	case "pair":
		runPair(os.Args[2:])
	default:
		fatal(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}
