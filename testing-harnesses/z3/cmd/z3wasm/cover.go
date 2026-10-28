package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"zeros3-z3/econx"
	"zeros3-z3/wasmz3"
)

func init() { cmds["cover"] = cover }

// cover FILE... : exact-coverage report (modules, functions, sections, bytes, opaque, fallbacks).
func cover(args []string) error {
	var files, fb, bytesTot, funcs, secs, opaque, custom, customBytes int64
	var slots [wasmz3.NumSpaces]int64
	reasons := map[string]int{}
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		var m *wasmz3.Module
		if err == nil {
			m, err = wasmz3.Analyze(dir, d, wasmz3.Limits{})
		}
		if err == nil {
			var rep *wasmz3.Rep
			if rep, err = wasmz3.Encode(m, d, wasmz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut}); err == nil {
				bytesTot += int64(len(d))
				funcs += int64(len(m.Funcs))
				secs += int64(len(dir.Secs))
				opaque += int64(m.Opaque)
				for _, s := range dir.Secs {
					if s.ID == 0 {
						custom++
						customBytes += s.Len
					}
				}
				for i, n := range rep.Stats.Slots {
					slots[i] += int64(n)
				}
				continue
			}
		}
		fb++
		reasons[err.Error()]++
	}
	fmt.Printf("modules %d (fallback %d) | bytes represented %d | functions %d (opaque bodies %d) | sections %d (custom %d, %d bytes kept exact) | index slots by space %v\n",
		files, fb, bytesTot, funcs, opaque, secs, custom, customBytes, slots[1:])
	var rs []string
	for r, n := range reasons {
		rs = append(rs, fmt.Sprintf("%dx %s", n, r))
	}
	sort.Strings(rs)
	fmt.Println("fallback reasons:", rs)
	return nil
}
