package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/wasmz3"
)

func init() { cmds["scan"] = scan }

// scan FILE... : section inventory, index-space sizes, slots, opaque bodies, custom share.
func scan(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		if err != nil {
			fmt.Printf("%s: %v\n", p, err)
			continue
		}
		var custom int64
		var cn []string
		for _, s := range dir.Secs {
			if s.ID == 0 {
				custom += s.Len
				cn = append(cn, fmt.Sprintf("%s:%d", s.Name, s.Len))
			}
		}
		m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
		if err != nil {
			fmt.Printf("%s: %d B, %d sections: analyze: %v\n", p, len(d), len(dir.Secs), err)
			continue
		}
		var per [wasmz3.NumSpaces]int
		for _, s := range m.Slots {
			per[s.Sp]++
		}
		code, data := int64(0), int64(0)
		for _, s := range dir.Secs {
			switch s.ID {
			case wasmz3.SecCode:
				code = s.Len
			case wasmz3.SecData:
				data = s.Len
			}
		}
		fmt.Printf("%s: %d B code %d data %d custom %d %v\n  types %d imports %d (f/t/m/g/e %v) funcs %d tables %d mems %d globals %d tags %d exports %d elems %d datas %d bodies %d opaque %d names %d\n  slots %d by space %v opaque-why %v\n",
			p, len(d), code, data, custom, cn, len(m.Types), len(m.Imports), m.NImport, len(m.Funcs), len(m.Tables), len(m.Mems), len(m.Globals), len(m.Tags), len(m.Exports), len(m.Elems), len(m.Datas), len(m.Bodies), m.Opaque, len(m.Names), len(m.Slots), per[1:], m.Why)
	}
	return nil
}
