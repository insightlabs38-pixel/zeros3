package main

import (
	"bytes"
	"fmt"
	"time"

	"zeros3-z3/jvmz3"
)

func init() { cmds["jvmrt"] = jvmRT }

// jvmrt JAR...: encode and decode every class of the jars, report exactness and fallbacks.
func jvmRT(args []string) error {
	for _, p := range args {
		_, data, err := jarClasses(p)
		if err != nil {
			return err
		}
		var ok, fb, bytesOK, bytesFB int
		var enc, dec time.Duration
		errs := map[string]int{}
		var refB, n int
		for _, b := range data {
			c, _, err := jvmz3.Parse(bytes.NewReader(b), int64(len(b)), jvmz3.Limits{})
			if err != nil {
				fb++
				bytesFB += len(b)
				errs[err.Error()[:min(60, len(err.Error()))]]++
				continue
			}
			t0 := time.Now()
			rep, err := jvmz3.Encode(c, b, jvmz3.Config{})
			enc += time.Since(t0)
			if err != nil {
				fb++
				bytesFB += len(b)
				errs[err.Error()[:min(60, len(err.Error()))]]++
				continue
			}
			t1 := time.Now()
			out, err := jvmz3.Decode(rep.Meta, rep.Comps)
			dec += time.Since(t1)
			if err != nil || !bytes.Equal(out, b) {
				return fmt.Errorf("%s: decode mismatch: %v", p, err)
			}
			ok++
			bytesOK += len(b)
			for _, l := range rep.Stats.StreamLen {
				refB += l
			}
			n += len(c.Slots)
		}
		fmt.Printf("%s: %d classes exact (%d B), %d fallback (%d B) enc %v dec %v ref bytes %d / %d slots\n", p, ok, bytesOK, fb, bytesFB, enc, dec, refB, n)
		for e, c := range errs {
			fmt.Printf("   %q x%d\n", e, c)
		}
	}
	return nil
}
