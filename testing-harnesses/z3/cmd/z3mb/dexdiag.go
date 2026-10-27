package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"zeros3-z3/dexz3"
)

func init() { cmds["dexdiag"] = dexDiag }

func dexDiag(args []string) error {
	enc := func(p string) (*dexz3.Rep, error) {
		f, b, err := loadDex(p)
		if err != nil {
			return nil, err
		}
		return dexz3.Encode(f, b, dexz3.Config{Cut: cdcScaled(4).Cut})
	}
	a, err := enc(args[0])
	if err != nil {
		return err
	}
	b, err := enc(args[1])
	if err != nil {
		return err
	}
	pay := map[[32]byte]bool{}
	whole := map[[32]byte]bool{}
	var str [6]map[[32]byte]bool
	for i := range str {
		str[i] = map[[32]byte]bool{}
	}
	for _, c := range a.Comps {
		whole[sha256.Sum256(c)] = true
		p, ss, _ := dexz3.SplitComp(c)
		pay[sha256.Sum256(p)] = true
		for i, s := range ss {
			str[i][sha256.Sum256(s)] = true
		}
	}
	var nw, payNew, payOldStrNew int
	var strNew [6]int
	for _, c := range b.Comps {
		if whole[sha256.Sum256(c)] {
			continue
		}
		nw++
		p, ss, _ := dexz3.SplitComp(c)
		if !pay[sha256.Sum256(p)] {
			payNew++
		} else {
			payOldStrNew++
			for i, s := range ss {
				if !str[i][sha256.Sum256(s)] {
					strNew[i]++
				}
			}
		}
	}
	fmt.Printf("comps %d -> %d; new %d; payload new %d; payload known but streams changed %d (per stream %v)\n", len(a.Comps), len(b.Comps), nw, payNew, payOldStrNew, strNew)
	_ = bytes.Equal
	return nil
}
