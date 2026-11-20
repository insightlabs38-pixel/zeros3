package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"zeros3-z3/composez3"
	"zeros3-z3/fstate"
)

func cmdExecHistory(args []string) error {
	fs := flag.NewFlagSet("exec-history", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print exact per-revision accounting rows as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("exec-history requires at least two chronological ELF or PE release files")
	}
	start := time.Now()
	var states []*fstate.State
	for i, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if kind, ok := composez3.Detect(data); !ok || kind != composez3.KindExec {
			return fmt.Errorf("%s is not an ELF or PE input", path)
		}
		label := fmt.Sprintf("%d-%s", i+1, filepath.Base(path))
		states = append(states, &fstate.State{Label: label, Files: []fstate.File{
			{Name: "executable", Kind: "exec", Data: data},
		}})
	}
	runner := fstate.Runner{RefBytes: 34, Pack: true}
	scenario := &fstate.Scenario{Name: "Shipped executable history", Desc: "Exact standalone CDC and EXR1, production pack rule estimate", States: states}
	result := runner.Run(scenario, []fstate.RepDef{cdcRep(), innerRep("exec", composez3.KindExec)})
	if *jsonOut {
		record := struct {
			Rows   []fstate.Row `json:"rows"`
			WallMS int64        `json:"wall_ms"`
		}{Rows: result.Rows, WallMS: time.Since(start).Milliseconds()}
		out := json.NewEncoder(os.Stdout)
		out.SetIndent("", "  ")
		if err := out.Encode(record); err != nil {
			return err
		}
	} else {
		result.WriteMD(os.Stdout, true)
	}
	for _, row := range result.Rows {
		if !row.Exact || row.Detail["inner_fallback"] > 0 {
			return fmt.Errorf("%s %s failed normalization or replay: %s", row.State, row.Rep, row.Err)
		}
	}
	return nil
}
