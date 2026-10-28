// z3debug measures hash-pinned debug build histories. Rows are evidence, not promotion.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"zeros3-z3/debugsym"
	"zeros3-z3/lab"
)

func main() {
	target := flag.Int("target", 4096, "grouped structural target; zero means exact rows/blocks")
	flag.Parse()
	stores := []*lab.EconStore{lab.NewEconStore(), lab.NewEconStore()}
	enc := json.NewEncoder(os.Stdout)
	for _, path := range flag.Args() {
		data, e := os.ReadFile(path)
		if e != nil {
			panic(e)
		}
		start := time.Now()
		p, in, fallback, e := debugsym.Analyze(data, *target)
		if e != nil {
			panic(fmt.Errorf("%s: %w", path, e))
		}
		if e = debugsym.Proof(p, data); e != nil {
			panic(e)
		}
		f := lab.EconFile{Name: filepath.Base(path), Data: data}
		reps := []lab.EconRep{{Name: "cdc", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { p, e := lab.CDCPlan(f, "debug"); return p, false, e }}, {Name: "debug-struct", Plan: func(lab.EconFile) (*lab.Plan, bool, error) { return p, fallback, nil }}}
		a := stores[0].Run(reps[0], []lab.EconFile{f})
		b := stores[1].Run(reps[1], []lab.EconFile{f})
		enc.Encode(map[string]any{"path": path, "sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "bytes": len(data), "format": in.Format, "streams": in.Streams, "rows": in.Rows, "spans": len(in.Spans), "target": *target, "elapsed_ms": time.Since(start).Milliseconds(), "cdc": a, "candidate": b})
	}
}
