// Command z4b5 is the Z4-B5 model-delta 2.0 laboratory driver.
package main

import (
	"fmt"
	"os"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4b5:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z4b5 explore|...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "explore":
		cmdExplore(os.Args[2:])
	case "quant-repro":
		cmdQuantRepro(os.Args[2:])
	case "inv":
		cmdInv(os.Args[2:])
	case "discover":
		cmdDiscover(os.Args[2:])
	case "controls":
		cmdControls(os.Args[2:])
	case "coderbench":
		cmdCoderBench(os.Args[2:])
	case "sparse":
		cmdSparse(os.Args[2:])
	case "family":
		cmdFamily(os.Args[2:])
	case "cdc":
		cmdCDCBase(os.Args[2:])
	case "model8":
		cmdModel8(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
