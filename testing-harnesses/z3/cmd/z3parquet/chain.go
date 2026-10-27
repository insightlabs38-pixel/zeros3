package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// cmdChain measures the coherent table history: every revision is a full rewrite of the
// table. For each representation: the incremental cost of each revision against everything
// retained so far, the cost against the previous revision only (the rolling live state),
// the cumulative retained total, and the first-sight lock-in comparison (all CDC, the
// representation from first sight, greedy per-file admission, hindsight).
func cmdChain(args []string) error {
	fs := flag.NewFlagSet("chain", flag.ExitOnError)
	repNames := fs.String("reps", "cdc,dpages,logical,lg-page", "representations")
	fam := fs.String("fam", "pyarrow-chain", "family")
	fs.Parse(args)
	sts, _, err := loadFamily(*fam)
	if err != nil {
		return err
	}
	var files []lab.EconFile
	for _, s := range sts {
		f, err := file(s)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	reps := pick(allReps(repOptions{}), *repNames)
	h := fstate.Human
	type res struct{ incr, roll []int64 }
	all := map[string]*res{}
	for _, rd := range reps {
		rep := rd.mk()
		r := &res{}
		st := lab.NewEconStore()
		for i, f := range files {
			a := st.Run(rep, []lab.EconFile{f})
			if !a.Exact {
				fmt.Fprintf(os.Stderr, "%s %s: not exact: %s\n", rd.name, sts[i].Rev, a.Err)
			}
			r.incr = append(r.incr, a.Pack)
			if i == 0 {
				r.roll = append(r.roll, a.Pack)
				continue
			}
			rs := lab.NewEconStore()
			rs.Run(rep, []lab.EconFile{files[i-1]})
			r.roll = append(r.roll, rs.Run(rep, []lab.EconFile{f}).Pack)
		}
		all[rd.name] = r
		fmt.Fprintln(os.Stderr, "chain", rd.name, "done")
	}
	for _, kind := range []string{"incremental", "rolling"} {
		fmt.Printf("\n%s chain, %s cost per revision (packed)\n| revision |", *fam, kind)
		for _, rd := range reps {
			fmt.Printf(" %s |", rd.name)
		}
		fmt.Print("\n|---|")
		for range reps {
			fmt.Print("---:|")
		}
		fmt.Println()
		tot := map[string]int64{}
		for i, s := range sts {
			fmt.Printf("| %s: %s |", s.Rev, s.Mutation)
			for _, rd := range reps {
				v := all[rd.name].incr[i]
				if kind == "rolling" {
					v = all[rd.name].roll[i]
				}
				tot[rd.name] += v
				fmt.Printf(" %s |", h(v))
			}
			fmt.Println()
		}
		fmt.Printf("| **total (%s)** |", map[string]string{"incremental": "cumulative retained", "rolling": "live state only"}[kind])
		for _, rd := range reps {
			fmt.Printf(" **%s** |", h(tot[rd.name]))
		}
		fmt.Println()
	}
	// lock-in: files admitted to CDC or to the representation by their own incremental cost
	var logical, cdc repDef
	for _, rd := range reps {
		switch rd.name {
		case "logical":
			logical = rd
		case "cdc":
			cdc = rd
		}
	}
	if logical.mk == nil || cdc.mk == nil {
		return nil
	}
	cr, lr := cdc.mk(), logical.mk()
	sc, sl := lab.NewEconStore(), lab.NewEconStore()
	var greedy int64
	var picks []string
	for i, f := range files {
		cc := sc.Clone().Run(cr, []lab.EconFile{f}).Pack
		cl := sl.Clone().Run(lr, []lab.EconFile{f}).Pack
		if cc <= cl {
			greedy += sc.Run(cr, []lab.EconFile{f}).Pack
			picks = append(picks, "cdc")
		} else {
			greedy += sl.Run(lr, []lab.EconFile{f}).Pack
			picks = append(picks, "logical")
		}
		_ = i
	}
	var allc, alll int64
	for i := range files {
		allc += all["cdc"].incr[i]
		alll += all["logical"].incr[i]
	}
	fmt.Printf("\nlock-in (cumulative retained): all CDC %s | logical from first sight %s | greedy per-file %s (%s) | hindsight %s\n",
		h(allc), h(alll), h(greedy), strings.Join(picks, ","), h(min(allc, alll)))
	return nil
}
