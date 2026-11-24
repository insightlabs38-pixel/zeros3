//go:build b10bench && b6bench && b5locality && b1rangeexperiment

package main

// B10 deep-stream campaign (not a regression test): go test -tags b10bench,b6bench,b5locality,b1rangeexperiment.
// Environment as B6-B9 (ZEROS3_B6_FX, ZEROS3_B6_CORPUS, ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestB10Probe: per-file deep-stream analysis of B10_FILES (units, refusals, deep-stream and recipe bytes).
func TestB10Probe(t *testing.T) {
	l := newAuthLab(t)
	for _, p := range strings.Fields(os.Getenv("B10_FILES")) {
		data, err := os.ReadFile(p)
		mustOK(t, err)
		key := fmt.Sprintf("probe/%x", len(l.want))
		l.put(key, data)
		mem := map[[32]byte][]byte{}
		st := &SpliceStats{}
		t0 := time.Now()
		root, canon, err := l.s.spliceConstruct(l.man(key), bytes.NewReader(data), 0, mem, splOpts{hard: true}, st)
		sec := time.Since(t0).Seconds()
		row := map[string]any{"file": p, "size": len(data), "seconds": sec, "stats": st}
		if err != nil {
			row["err"] = err.Error()
		} else {
			cl, _ := l.s.repClosureMem(root, mem)
			var rb int64
			for _, c := range cl.chunks {
				rb += c.Length
			}
			row["recipe_bytes"], row["deep"] = rb, canon.Size
			canon.close()
		}
		b, _ := json.Marshal(row)
		fmt.Println(string(b))
	}
}
