// z4p7 is the Z4-P7 research driver (model/numeric compression and hard codecs).
package main

import (
	"fmt"
	"os"
)

var cmds = map[string]func([]string){}

func main() {
	if len(os.Args) < 2 || cmds[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "usage: z4p7 <command> [args]; commands:")
		for k := range cmds {
			fmt.Fprintln(os.Stderr, "  "+k)
		}
		os.Exit(2)
	}
	cmds[os.Args[1]](os.Args[2:])
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4p7:", err)
		os.Exit(1)
	}
}
