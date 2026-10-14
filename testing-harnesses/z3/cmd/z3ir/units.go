package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

func parsePolicy(s string) (ir.Policy, error) {
	num := func(t string) int {
		mult := 1
		switch {
		case strings.HasSuffix(t, "K"):
			mult, t = 1<<10, strings.TrimSuffix(t, "K")
		case strings.HasSuffix(t, "M"):
			mult, t = 1<<20, strings.TrimSuffix(t, "M")
		}
		n, _ := strconv.Atoi(t)
		return n * mult
	}
	switch {
	case s == "stream":
		return ir.Policy{Kind: ir.PStream}, nil
	case s == "block":
		return ir.Policy{Kind: ir.PBlock}, nil
	case strings.HasPrefix(s, "group"):
		return ir.Policy{Kind: ir.PGroup, Target: num(s[5:])}, nil
	case strings.HasPrefix(s, "cblk"):
		return ir.Policy{Kind: ir.PContentBlocks, Target: num(s[4:])}, nil
	case strings.HasPrefix(s, "tok"):
		return ir.Policy{Kind: ir.PTokRun, Target: num(s[3:])}, nil
	}
	return ir.Policy{}, fmt.Errorf("bad policy %q", s)
}

type survAgg struct {
	revs                 int
	units                int64
	plain, ebytes        int64
	keptUnits            int64 // units found in the previous revision
	keptPlain, keptBytes int64 // vs previous revision
	anyPlain, anyBytes   int64 // vs any earlier revision
	cKeptPlain           int64 // header-coupled identity (block-ish policies)
	cUnits               int64
	maxDist              int64
	skel                 int64
	streams              int
	firstUnits           int
}

type survState struct {
	prev, any, prevC map[[32]byte]bool
}

func unitsCmd(args []string) {
	fs := flag.NewFlagSet("units", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	pols := fs.String("policies", "stream,block,group64K,group256K,group1M,cblk64K,cblk256K,cblk1M,tok1K,tok2K,tok4K,tok8K,tok16K,tok64K", "unit policies")
	fs.Parse(args)
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	var policies []ir.Policy
	for _, p := range split(*pols) {
		pp, err := parsePolicy(p)
		if err != nil {
			fatal(err)
		}
		policies = append(policies, pp)
	}
	ins, order, cl, err := lab.FamilyInputs(*dir, split(*fams), lab.DefaultOptions())
	if err != nil {
		fatal(err)
	}
	defer cl.Close()
	fmt.Println("| family | policy | units/rev | plain KiB/unit | E1 B/unit | units kept % | plaintext covered by kept units % (prev rev) | ... (any earlier rev) | header-coupled kept % | max dist KiB (avg) |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		aggs := make([]survAgg, len(policies))
		st := make([]survState, len(policies))
		for pi := range st {
			st[pi] = survState{any: map[[32]byte]bool{}}
		}
		for k, in := range ins[fam] {
			regs, err := in.StreamRegions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", fam, err)
				break
			}
			cur := make([]survState, len(policies))
			for pi := range cur {
				cur[pi] = survState{prev: map[[32]byte]bool{}, prevC: map[[32]byte]bool{}}
			}
			for _, rg := range regs {
				if !rg.Stream {
					continue
				}
				s, err := in.ParseStream(rg.Off, rg.Len)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s/%s parse: %v\n", fam, in.Revision, err)
					continue
				}
				for pi, pol := range policies {
					pk, err := ir.Encode(s, ir.Config{Codec: ir.E1, Policy: pol})
					if err != nil {
						fatal(err)
					}
					a := &aggs[pi]
					for ui, u := range pk.Units {
						key := sha256.Sum256(u)
						plain := int64(pk.Skel.Units[ui].PlainLen)
						// coupled identity: unit bytes plus the block entries it touches
						h := sha256.New()
						h.Write(u)
						for b := pk.UnitBlocks[ui][0]; b <= pk.UnitBlocks[ui][1]; b++ {
							h.Write(pk.BlockBytes[b])
						}
						var ck [32]byte
						copy(ck[:], h.Sum(nil))
						if k >= 1 {
							a.units++
							a.plain += plain
							a.ebytes += int64(len(u))
							a.maxDist += int64(pk.UnitMaxDist[ui])
							if st[pi].prev[key] {
								a.keptUnits++
								a.keptPlain += plain
								a.keptBytes += int64(len(u))
							}
							if st[pi].any[key] {
								a.anyPlain += plain
								a.anyBytes += int64(len(u))
							}
							if st[pi].prevC[ck] {
								a.cKeptPlain += plain
							}
						}
						cur[pi].prev[key] = true
						cur[pi].prevC[ck] = true
					}
					if k >= 1 {
						a.skel += int64(len(pk.Skeleton))
					}
				}
			}
			for pi := range policies {
				for key := range cur[pi].prev {
					st[pi].any[key] = true
				}
				st[pi].prev, st[pi].prevC = cur[pi].prev, cur[pi].prevC
				if k >= 1 {
					aggs[pi].revs++
				}
			}
			in.DropParsed()
		}
		for pi, pol := range policies {
			a := aggs[pi]
			if a.revs == 0 || a.units == 0 {
				continue
			}
			couple := "-"
			if pol.Kind != ir.PTokRun {
				couple = pct(a.cKeptPlain, a.plain)
			}
			fmt.Printf("| %s | %s | %d | %.1f | %.0f | %s | %s | %s | %s | %.1f |\n", fam, pol, a.units/int64(a.revs),
				float64(a.plain)/float64(a.units)/1024, float64(a.ebytes)/float64(a.units), pct(a.keptUnits, a.units),
				pct(a.keptPlain, a.plain), pct(a.anyPlain, a.plain), couple, float64(a.maxDist)/float64(a.units)/1024)
		}
	}
}
