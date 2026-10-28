package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
)

func init() { cmds["cover"] = cover }

// cover FILE... : exact-coverage report (assemblies, methods, rows, bytes, fallbacks, opaque parts).
func cover(args []string) error {
	var files, fb, bytesTot, methods, bodies, opaque, rows, marked, blobs, toks, slots int64
	tables := map[string]int64{}
	reasons := map[string]int{}
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			fb++
			reasons[strings.SplitN(err.Error(), ":", 3)[len(strings.SplitN(err.Error(), ":", 3))-1]]++
			continue
		}
		rep, err := clrz3.Encode(f, d, clrz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			fb++
			reasons[err.Error()]++
			continue
		}
		bytesTot += int64(len(d))
		methods += int64(f.Rows(clrz3.TMethodDef))
		for t := 0; t < clrz3.NumTables; t++ {
			rows += int64(f.Rows(t))
			if f.Rows(t) > 0 {
				tables[clrz3.TableName(t)] += int64(f.Rows(t))
			}
		}
		st := rep.Stats
		bodies += int64(st.Bodies)
		opaque += int64(st.OpaqueBody)
		marked += int64(st.Marked)
		blobs += int64(st.Blobs)
		toks += int64(st.Slots[clrz3.ClsToken])
		for c := 0; c < clrz3.NumClasses; c++ {
			slots += int64(st.Slots[c])
		}
	}
	fmt.Printf("assemblies %d (fallback %d) | bytes represented %d | methods %d, bodies %d (opaque %d) | rows %d in %d tables | signature blobs %d/%d normalized | token slots %d | all slots %d\n",
		files, fb, bytesTot, methods, bodies, opaque, rows, len(tables), marked, blobs, toks, slots)
	var rs []string
	for r, n := range reasons {
		rs = append(rs, fmt.Sprintf("%dx %s", n, r))
	}
	sort.Strings(rs)
	fmt.Println("fallback reasons:", rs)
	return nil
}
