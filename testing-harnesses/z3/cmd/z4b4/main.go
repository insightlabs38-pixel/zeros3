// Command z4b4 is the Z4-B4 graph-normalization laboratory driver.
package main

import (
	"fmt"
	"os"

	"zeros3-z3/graphnorm"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4b4:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z4b4 llvm FILES...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "llvm":
		cmdLLVM(os.Args[2:])
	case "llvm-store":
		cmdStore(os.Args[2], graphnorm.PolicyAll, os.Args[3:])
	case "llvm-replay":
		cmdReplay(os.Args[2], os.Args[3])
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
