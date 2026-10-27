// z3mb is the Z3-32 managed-bytecode laboratory driver.
package main

import (
	"fmt"
	"os"
)

var cmds = map[string]func(args []string) error{}

func main() {
	if len(os.Args) < 2 || cmds[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "usage: z3mb <command> ...")
		for k := range cmds {
			fmt.Fprintln(os.Stderr, "  ", k)
		}
		os.Exit(2)
	}
	if err := cmds[os.Args[1]](os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "z3mb:", err)
		os.Exit(1)
	}
}
