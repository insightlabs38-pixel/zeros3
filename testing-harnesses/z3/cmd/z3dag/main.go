// Command z3dag runs the Z3-28 representation-DAG measurements (laboratory only).
//
//	z3dag dag|econ|plan|scale|reads|budget|all
//
// Every number is computed from persisted repz3 objects; nothing is estimated.
package main

import (
	"fmt"
	"os"
	"time"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3dag:", err)
		os.Exit(1)
	}
}

func main() {
	sections := map[string]func(){"dag": dagSection, "econ": econSection, "plan": planSection, "scale": scaleSection, "reads": readsSection, "budget": budgetSection}
	order := []string{"dag", "econ", "plan", "scale", "reads", "budget"}
	arg := "all"
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	for _, name := range order {
		if arg == "all" || arg == name {
			t := time.Now()
			fmt.Printf("\n== %s ==\n", name)
			sections[name]()
			fmt.Printf("(%s %.1fs)\n", name, time.Since(t).Seconds())
		}
	}
}
