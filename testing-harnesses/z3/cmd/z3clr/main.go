// z3clr is the Z3-33 CLR/.NET driver: corpus inventory, attribution, structural baselines,
// exact-representation economics and read/performance measurements over clrz3.
package main

import (
	"fmt"
	"os"
	"sort"
)

var cmds = map[string]func([]string) error{}

func main() {
	if len(os.Args) < 2 {
		var names []string
		for n := range cmds {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintln(os.Stderr, "usage: z3clr", names)
		os.Exit(2)
	}
	f, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
	if err := f(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "z3clr:", err)
		os.Exit(1)
	}
}
