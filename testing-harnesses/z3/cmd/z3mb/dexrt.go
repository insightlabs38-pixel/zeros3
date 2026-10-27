package main

import (
	"bytes"
	"fmt"
	"time"

	"zeros3-z3/dexz3"
)

func init() { cmds["dexrt"] = dexRT }

func dexRT(args []string) error {
	for _, p := range args {
		f, b, err := loadDex(p)
		if err != nil {
			return err
		}
		t0 := time.Now()
		rep, err := dexz3.Encode(f, b, dexz3.Config{})
		if err != nil {
			fmt.Printf("%s: %v\n", p, err)
			continue
		}
		t1 := time.Now()
		out, err := dexz3.Decode(rep.Meta, rep.Comps)
		if err != nil || !bytes.Equal(out, b) {
			return fmt.Errorf("%s: decode: %v", p, err)
		}
		fmt.Printf("%s: %d B  enc %v dec %v  comps %d  streams %v slots %v payload %d\n", p, len(b), t1.Sub(t0), time.Since(t1), len(rep.Comps), rep.Stats.StreamLen, rep.Stats.Slots, rep.Stats.Payload)
	}
	return nil
}
