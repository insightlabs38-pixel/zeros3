package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/wasmz3"
)

func init() { cmds["oracle"] = oracleCmd }

// oracle FILE : compare the output of oracle/clrwasm/wasm_inventory.js (V8's WebAssembly, ORACLE
// only) with wasmz3's own parse: validity and the import / export count of every kind.
func oracleCmd(args []string) error {
	fh, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer fh.Close()
	names := []string{"function", "table", "memory", "global", "tag"}
	n, bad, invalid, unsup := 0, 0, 0, 0
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) < 4 {
			continue
		}
		d, err := os.ReadFile(parts[0])
		if err != nil {
			return err
		}
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		var m *wasmz3.Module
		if err == nil {
			m, err = wasmz3.Analyze(dir, d, wasmz3.Limits{})
		}
		if parts[1] != "true" {
			invalid++ // the engine rejects it (the unsupported-feature fixtures): we must not represent it either
			if err == nil {
				fmt.Printf("%s: invalid for V8 but parsed\n", parts[0])
				bad++
			}
			continue
		}
		n++
		if err != nil {
			if isUnsupported(err) { // valid, but a feature this package deliberately does not represent (GC types ...)
				unsup++
				continue
			}
			fmt.Printf("%s: valid for V8 but %v\n", parts[0], err)
			bad++
			continue
		}
		var wi, we map[string]int
		json.Unmarshal([]byte(strings.TrimPrefix(parts[2], "imports=")), &wi)
		json.Unmarshal([]byte(strings.TrimPrefix(parts[3], "exports=")), &we)
		var gi, ge [5]int
		for _, im := range m.Imports {
			gi[im.Kind]++
		}
		for _, e := range m.Exports {
			ge[e.Kind]++
		}
		for k, nm := range names {
			if wi[nm] != gi[k] || we[nm] != ge[k] {
				fmt.Printf("%s: %s imports %d/%d exports %d/%d (ours/oracle)\n", parts[0], nm, gi[k], wi[nm], ge[k], we[nm])
				bad++
			}
		}
	}
	fmt.Printf("oracle: %d valid modules (%d compared, %d valid but deliberately unsupported), %d rejected by the engine, %d differ\n", n, n-unsup, unsup, invalid, bad)
	if bad > 0 {
		return fmt.Errorf("%d differences", bad)
	}
	return nil
}

func isUnsupported(err error) bool {
	return strings.Contains(err.Error(), wasmz3.ErrUnsupported.Error())
}
