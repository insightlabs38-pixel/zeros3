package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"zeros3-z3/dexz3"
)

func init() { cmds["dexstat"] = dexStat }

func loadDex(p string) (*dexz3.File, []byte, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	f, err := dexz3.Parse(bytes.NewReader(b), int64(len(b)), dexz3.Limits{})
	return f, b, err
}

func dexStat(args []string) error {
	for _, p := range args {
		t0 := time.Now()
		f, b, err := loadDex(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		t1 := time.Now()
		w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		fmt.Printf("%s: %d bytes v%d parse %v walk %v slots %d items %d\n", p, len(b), f.Version, t1.Sub(t0), time.Since(t1), len(w.Slots), len(w.Items))
		type key struct {
			c dexz3.Class
			s dexz3.Space
		}
		cnt, by := map[key]int{}, map[key]int{}
		for _, s := range w.Slots {
			k := key{s.Cls, s.Space}
			cnt[k]++
			by[k] += int(s.Len)
		}
		tot := 0
		for k, n := range cnt {
			fmt.Printf("   %-9s %-7s slots %8d bytes %8d (%.2f%%)\n", k.c, k.s, n, by[k], 100*float64(by[k])/float64(len(b)))
			tot += by[k]
		}
		fmt.Printf("   total slot bytes %d (%.1f%%)\n", tot, 100*float64(tot)/float64(len(b)))
	}
	return nil
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
