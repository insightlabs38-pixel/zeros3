package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
)

func init() { cmds["rt"] = rt }

func printStats(st clrz3.Stats) {
	fmt.Printf("  payload %d comps %d marked %d/%d blobs, opaque bodies %d/%d, dups str/blob/row/us %v\n", st.Payload, st.Comps, st.Marked, st.Blobs, st.OpaqueBody, st.Bodies, st.Dups)
	fmt.Printf("  method RVAs: %d predicted from the previous body, %d null, %d explicit\n", st.RVA[0], st.RVA[1], st.RVA[2])
	for c := 0; c < clrz3.NumClasses; c++ {
		fmt.Printf("    %-7s slots %7d refbytes %8d\n", clrz3.ClassName[c], st.Slots[c], st.RefBytes[c])
	}
	for s := 0; s < clrz3.NumStreams; s++ {
		fmt.Printf("    stream %-9s %8d\n", clrz3.StreamName[s], st.StreamLen[s])
	}
}

// rt FILE... : encode with a verifying decode, report statistics.
func rt(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			fmt.Printf("%s: parse: %v\n", p, err)
			continue
		}
		t0 := time.Now()
		rep, err := clrz3.Encode(f, d, clrz3.Config{Cut: econx.UnitCut(4).Cut})
		if err != nil {
			fmt.Printf("%s: encode: %v\n", p, err)
			continue
		}
		fmt.Printf("%s: %d B -> meta %d, %d comps, %v\n", p, len(d), len(rep.Meta), len(rep.Comps), time.Since(t0))
		if os.Getenv("V") != "" {
			printStats(rep.Stats)
		}
	}
	return nil
}
