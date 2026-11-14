// z4b6: Z4-B6 hard-codec laboratory driver.
package main

import (
	"fmt"
	"os"
	_ "zeros3-z3/brotliz3/brdict"
)

var cmds = map[string]func(args []string) error{}

func main() {
	if len(os.Args) < 2 || cmds[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "usage: z4b6 <command> ...")
		for k := range cmds {
			fmt.Fprintln(os.Stderr, "  ", k)
		}
		os.Exit(2)
	}
	if err := cmds[os.Args[1]](os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "z4b6:", err)
		os.Exit(1)
	}
}
