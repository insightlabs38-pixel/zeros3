package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/clrz3"
)

func init() { cmds["locals"] = localsCmd }

// locals FILE... : how LocalVarSig tokens relate to body order and StandAloneSig duplicates.
func localsCmd(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			return err
		}
		bodies := clrz3.FindBodies(f, d, false)
		maxRow, nul, hit, reuse, other, fat := uint32(0), 0, 0, 0, 0, 0
		seen := map[uint32]int{}
		for _, b := range bodies {
			if b.Opaque || !b.Body.Fat {
				continue
			}
			fat++
			r := b.Body.LocalTok & 0xffffff
			switch {
			case b.Body.LocalTok == 0:
				nul++
			case r == maxRow+1:
				hit++
				maxRow = r
			case r <= maxRow:
				reuse++
			default:
				other++
				maxRow = r
			}
			seen[r]++
		}
		sas := map[string]int{}
		hv, _ := clrz3.NewHeapView(f, d)
		for r := uint32(1); r <= f.Rows(clrz3.TStandAloneSig); r++ {
			b, _ := hv.BlobBytes(f.Col(d, clrz3.TStandAloneSig, r, 0))
			sas[string(b)]++
		}
		dup := 0
		for _, n := range sas {
			dup += n - 1
		}
		fmt.Printf("%s: bodies %d fat %d | null %d next %d reuse %d jump %d | StandAloneSig %d rows, %d distinct blobs, %d duplicate rows, rows used by >1 body: ", p, len(bodies), fat, nul, hit, reuse, other, f.Rows(clrz3.TStandAloneSig), len(sas), dup)
		m := 0
		for _, n := range seen {
			if n > 1 {
				m++
			}
		}
		fmt.Println(m)
	}
	return nil
}
