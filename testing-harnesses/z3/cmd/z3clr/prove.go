package main

import (
	"fmt"
	"os"
	"time"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/repz3/kinds"
)

func init() { cmds["prove"] = prove }

// prove FILE... : every assembly is encoded, persisted as a KindCLR representation (recipe, index
// nodes, groups), the analysis state is dropped and the file is rebuilt through the repz3
// contract (length, SHA-256 and bytes checked; every object read must be reachable).
func prove(args []string) error {
	ok, fb := 0, 0
	t0 := time.Now()
	var bytes int64
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b, _, err := kinds.EncodeCLR(d, clrz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			fb++
			fmt.Printf("fallback %s: %v\n", p, err)
			continue
		}
		if err := kinds.Prove(nil, b, d); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		ok++
		bytes += int64(len(d))
	}
	fmt.Printf("%d assemblies proved through KindCLR (%d MiB), %d kept as CDC, %v\n", ok, bytes>>20, fb, time.Since(t0).Round(time.Millisecond))
	return nil
}
