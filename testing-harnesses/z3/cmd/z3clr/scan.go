package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/clrz3"
)

func init() { cmds["scan"] = scan }

// scan FILE... : table stream form, widths, heap sizes, body parse and signature coverage.
func scan(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			fmt.Printf("%s: %v\n", p, err)
			continue
		}
		sw, gw, bw := f.HeapWidths()
		fmt.Printf("%s: %d B pe32+=%v tab=#%c heapW=%d/%d/%d str=%d blob=%d us=%d guid=%d ptr=%v streams=%d\n", p, len(d), f.PE32Plus, f.TabKind, sw, gw, bw, f.Str.Size, f.Blob.Size, f.US.Size, f.GUID.Size, f.HasPtr, len(f.Streams))
		var rows []string
		for t := 0; t < clrz3.NumTables; t++ {
			if f.Rows(t) > 0 {
				rows = append(rows, fmt.Sprintf("%s=%d", clrz3.TableName(t), f.Rows(t)))
			}
		}
		fmt.Println("  rows:", rows)
		var cw []string
		for c := 0; c < clrz3.NumCoded; c++ {
			cw = append(cw, fmt.Sprint(f.CodedWidth(c)))
		}
		fmt.Println("  coded widths (TypeDefOrRef HasConst HasCA HasFM HasDS MRP HasSem MDOR MemFwd Impl CAType ResScope TOMD):", cw)
		ok, bad, toks, clauses := 0, 0, 0, 0
		for r := uint32(1); r <= f.Rows(clrz3.TMethodDef); r++ {
			rva := f.Col(d, clrz3.TMethodDef, r, 0)
			if rva == 0 {
				continue
			}
			off, _, mapped := f.RVAToOff(rva)
			if !mapped {
				bad++
				continue
			}
			b, good := clrz3.ParseBody(d, int64(off), true)
			if !good {
				bad++
				continue
			}
			ok++
			toks += len(b.Toks)
			clauses += len(b.Clauses)
		}
		fmt.Printf("  bodies ok=%d bad=%d tokens=%d clauses=%d\n", ok, bad, toks, clauses)
	}
	return nil
}
