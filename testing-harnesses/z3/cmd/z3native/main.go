// z3native compares fixed, fully charged grouped native representations.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
	"zeros3-z3/debugsym"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/nativerescue"
	"zeros3-z3/pdelta"
)

type spec struct {
	name     string
	o        nativerescue.Options
	baseline int
}

func main() {
	only := flag.String("only", "", "optional preselected representation name")
	flag.Parse()
	if flag.NArg() == 0 {
		panic("usage: z3native [-only NAME] REVISION_FILES...")
	}
	var revs []econx.Rev
	var paths []map[string]any
	var pairwise []pdelta.Stat
	var previous []byte
	for _, path := range flag.Args() {
		data, e := os.ReadFile(path)
		if e != nil {
			panic(e)
		}
		if len(data) > nativerescue.MaxNative {
			panic("source size bound")
		}
		if previous != nil {
			pairwise = append(pairwise, pdelta.Cost(previous, data))
		}
		previous = data
		paths = append(paths, map[string]any{"path": path, "bytes": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
		revs = append(revs, econx.Rev{Name: path, Files: []lab.EconFile{{Name: "object", Data: data}}})
	}
	specs := []spec{{name: "raw-CDC", baseline: 1}, {name: "raw-section-repz3", baseline: 2}, {name: "old-normalized-production-CDC", o: nativerescue.Options{Target: -1, Selection: "full", Separate: true}}, {name: "normalized-64K", o: nativerescue.Options{Target: 65536, Selection: "full", Compact: true}}, {name: "normalized-16K", o: nativerescue.Options{Target: 16384, Selection: "full", Compact: true}}, {name: "normalized-4K", o: nativerescue.Options{Target: 4096, Selection: "full", Compact: true}}, {name: "normalized-2K", o: nativerescue.Options{Target: 2048, Selection: "full", Compact: true}}, {name: "function-4K", o: nativerescue.Options{Target: 4096, Selection: "full", Compact: true, Functions: true}}, {name: "function-2K", o: nativerescue.Options{Target: 2048, Selection: "full", Compact: true, Functions: true}}}
	specs = append(specs, spec{name: "function-4K/content", o: nativerescue.Options{Target: 4096, Selection: "full", Identity: "content", Compact: true, Functions: true}})
	specs = append(specs, spec{name: "old-normalized+nodes", o: nativerescue.Options{Target: -1, Selection: "full", Separate: true}})
	for _, class := range []string{"reloc", "table", "branch", "rip", "ptr", "go", "struct", "x86"} {
		specs = append(specs, spec{name: "function-4K/" + class, o: nativerescue.Options{Target: 4096, Selection: class, Compact: true, Functions: true}})
	}
	enc := json.NewEncoder(os.Stdout)
	for _, sp := range specs {
		if *only != "" && sp.name != *only {
			continue
		}
		var stats []nativerescue.Statistics
		start := time.Now()
		rep := econx.Rep{Name: sp.name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
			if sp.baseline == 1 {
				p, e := lab.CDCPlan(f, "native")
				return p, false, e
			}
			if sp.baseline == 2 {
				p, e := nativerescue.Structure(f.Data)
				if e == nil {
					e = debugsym.Proof(p, f.Data)
				}
				return p, false, e
			}
			p, s, e := nativerescue.Plan(f.Data, sp.o)
			stats = append(stats, s)
			return p, false, e
		}}
		fan := 128
		if sp.baseline == 1 || sp.name == "old-normalized-production-CDC" {
			fan = 0
		}
		result := econx.Run(revs, rep, econx.Opts{TreeFan: fan})
		if !result.Exact || result.Err != "" {
			panic(fmt.Sprintf("%s failed: %+v", sp.name, result))
		}
		enc.Encode(map[string]any{"representation": sp.name, "tree_fan": fan, "result": result, "sources": paths, "stats": stats, "elapsed_ms": time.Since(start).Milliseconds(), "pairwise_sensitivity_only": pairwise})
	}
}
