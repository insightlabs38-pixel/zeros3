package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
)

func init() { cmds["diffc"] = diffc }

// diffc OLD NEW : which groups of NEW are not in OLD, by section type and stream.
func diffc(args []string) error {
	enc := func(p string) (*clrz3.Rep, error) {
		d, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			return nil, err
		}
		return clrz3.Encode(f, d, clrz3.Config{Cut: econx.UnitCut(4).Cut})
	}
	a, err := enc(args[0])
	if err != nil {
		return err
	}
	b, err := enc(args[1])
	if err != nil {
		return err
	}
	old := map[[32]byte]bool{}
	oldPay := map[[32]byte]bool{}
	oldStr := map[[32]byte]int{}
	for _, c := range a.Comps {
		old[sha256.Sum256(c)] = true
		pl, ss, _ := idxref.SplitComp(c, clrz3.NumStreams)
		oldPay[sha256.Sum256(pl)] = true
		for k, st := range ss {
			if len(st) > 0 {
				oldStr[sha256.Sum256(append([]byte{byte(k)}, st...))]++
			}
		}
	}
	secOf := func(r *clrz3.Rep, pos int) uint16 {
		for _, s := range r.Secs {
			if pos >= s.Off && pos < s.End {
				return s.Type
			}
		}
		return 0xffff
	}
	start := 0
	byType := map[uint16][2]int{}
	for i, c := range b.Comps {
		end := b.Cuts[i]
		if !old[sha256.Sum256(c)] {
			t := secOf(b, start)
			x := byType[t]
			x[0]++
			x[1] += len(c)
			byType[t] = x
			if len(args) > 2 {
				pl, ss, _ := idxref.SplitComp(c, clrz3.NumStreams)
				fmt.Printf("new comp %d payload [%d,%d) type %#x: payload %d (old payload %v) streams", i, start, end, t, len(pl), oldPay[sha256.Sum256(pl)])
				for k, s := range ss {
					fmt.Printf(" %s:%d(%v)", clrz3.StreamName[k][:2], len(s), oldStr[sha256.Sum256(append([]byte{byte(k)}, s...))] > 0)
				}
				fmt.Println()
			}
		}
		start = end
	}
	fmt.Printf("%d comps, new by section type (count, bytes):\n", len(b.Comps))
	for t, x := range byType {
		fmt.Printf("  %#05x  %4d comps  %7d B\n", t, x[0], x[1])
	}
	return nil
}
