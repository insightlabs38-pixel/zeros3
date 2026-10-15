package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/gitz3"
)

func cmdDerive(args []string) error {
	var c common
	fs := flag.NewFlagSet("derive", flag.ExitOnError)
	c.flags(fs)
	pairs := fs.Bool("pairs", false, "also measure derivation gain between producer pairs")
	cross := fs.String("cross", "", "comma-separated tags built with Z3_GIT_TAG=<tag> oracle/gitdelta/setup.sh; compare their create_delta with the stored programs")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	o, err := gitz3.StartDeltaOracle()
	if err != nil {
		return err
	}
	defer o.Close()
	fmt.Printf("%s; oracle = Git v2.43.0 diff-delta.c create_delta(base, target) compiled by oracle/gitdelta/setup.sh\n\n", gitz3.GitVersion())
	fmt.Println("| object format | producer | delta entries | regenerated exactly | different program | oracle no-delta | stored delta B | exact B | exact share by bytes |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|")
	var fam gitz3.Family
	for _, f := range gitz3.Families() {
		if f.Name == "add-small" {
			fam = f
		}
	}
	h := gitz3.FamilyHistory(fam, c.seed, c.scale, 80)
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		for _, sp := range []gitz3.RepackSpec{gitz3.SpecDefault, gitz3.SpecW3D3, gitz3.SpecW50D250, gitz3.SpecRefDelta, gitz3.SpecReuse, gitz3.SpecReverse, gitz3.SpecLevel(1)} {
			r, err := gitz3.Build(w, "dv", a, h, sp)
			if err != nil {
				return err
			}
			pp, _ := r.PackPairs()
			p, err := gitz3.ParsePack(pp[0].Pack, a, gitz3.Limits{})
			if err != nil {
				return err
			}
			if _, err := p.Resolve(); err != nil {
				return err
			}
			st, err := gitz3.DeriveDeltas(p, o)
			if err != nil {
				return err
			}
			tot := st.ExactBytes + st.DifferBytes
			fmt.Printf("| %v | import+%s | %d | %d | %d | %d | %d | %d | %.1f%% |\n", a, sp.Label, st.Deltas, st.Exact, st.Differ, st.NoDelta, tot, st.ExactBytes, 100*float64(st.ExactBytes)/float64(max(tot, 1)))
		}
	}
	if *pairs {
		fmt.Println("\nDerivation gain A -> B (oracle bound): B's delta programs that A does not already hold, and how many Git's create_delta regenerates from objects A holds")
		fmt.Println("\n| pair | B delta entries | program already in A | new programs (bytes) | base/target not in A | regenerable by create_delta (bytes) |")
		fmt.Println("|---|---:|---:|---:|---:|---:|")
		prefix := c.prefix
		if prefix < 60 {
			prefix = 60
		}
		hh := gitz3.FamilyHistory(fam, c.seed, c.scale, prefix)
		hA := hh.Prefix(len(hh.Trees) - 1)
		type pr struct {
			label string
			a, b  func() (*gitz3.Repo, error)
		}
		build := func(name string, hist *gitz3.History, specs ...gitz3.RepackSpec) func() (*gitz3.Repo, error) {
			return func() (*gitz3.Repo, error) { return gitz3.Build(w, name, gitz3.SHA1, hist, specs...) }
		}
		grow := func() (*gitz3.Repo, error) {
			r, err := gitz3.Build(w, "pg", gitz3.SHA1, hA, gitz3.SpecDefault)
			if err != nil {
				return nil, err
			}
			if err := r.ImportHistory(hh); err != nil {
				return nil, err
			}
			return r, r.Repack(gitz3.SpecReuse)
		}
		prs := []pr{
			{"default -> window3/depth3", build("pa", hA, gitz3.SpecDefault), build("pb", hA, gitz3.SpecW3D3)},
			{"default -> window50/depth250", build("pa", hA, gitz3.SpecDefault), build("pb", hA, gitz3.SpecW50D250)},
			{"default -> reversed input", build("pa", hA, gitz3.SpecDefault), build("pb", hA, gitz3.SpecReverse)},
			{"default -> no delta search", build("pa", hA, gitz3.SpecDefault), build("pb", hA, gitz3.SpecNoDelta)},
			{"pack(H) -> pack(H+1) full -f", build("pa", hA, gitz3.SpecDefault), build("pb", hh, gitz3.SpecDefault)},
			{"pack(H) -> pack(H+1) consolidate (reuse)", build("pa", hA, gitz3.SpecDefault), grow},
		}
		for _, q := range prs {
			ra, err := q.a()
			if err != nil {
				return err
			}
			pa, _ := ra.PackPairs()
			A, err := gitz3.ParsePack(pa[0].Pack, gitz3.SHA1, gitz3.Limits{})
			if err != nil {
				return err
			}
			A.Resolve()
			rb, err := q.b()
			if err != nil {
				return err
			}
			pb, _ := rb.PackPairs()
			B, err := gitz3.ParsePack(pb[0].Pack, gitz3.SHA1, gitz3.Limits{})
			if err != nil {
				return err
			}
			B.Resolve()
			g, err := gitz3.DeriveGain(A, B, o)
			if err != nil {
				return err
			}
			fmt.Printf("| %s | %d | %d | %d (%d) | %d | %d (%d) |\n", q.label, g.BDeltas, g.ProgramsInA, g.NewPrograms, g.NewProgBytes, g.BaseOrTgtNotInA, g.Derivable, g.DerivableBytes)
		}
	}
	if *cross != "" {
		fmt.Println("\nCross-version: stored programs (made by the installed git) vs create_delta from other Git tags' diff-delta.c")
		fmt.Println("\n| diff-delta.c tag | delta entries | regenerated exactly | different | exact share by bytes |")
		fmt.Println("|---|---:|---:|---:|---:|")
		home, _ := os.UserHomeDir()
		r, err := gitz3.Build(w, "dv", gitz3.SHA1, h, gitz3.SpecDefault)
		if err != nil {
			return err
		}
		pp, _ := r.PackPairs()
		p, _ := gitz3.ParsePack(pp[0].Pack, gitz3.SHA1, gitz3.Limits{})
		p.Resolve()
		for _, tag := range strings.Split(*cross, ",") {
			bin := filepath.Join(home, ".cache", "z3-oracle", "gitdelta-"+tag, "gitdelta")
			if tag == "v2.43.0" {
				bin = gitz3.DeltaOraclePath()
			}
			ox, err := gitz3.StartDeltaOracleAt(bin)
			if err != nil {
				return err
			}
			st, err := gitz3.DeriveDeltas(p, ox)
			ox.Close()
			if err != nil {
				return err
			}
			tot := st.ExactBytes + st.DifferBytes
			fmt.Printf("| %s | %d | %d | %d | %.1f%% |\n", tag, st.Deltas, st.Exact, st.Differ+st.NoDelta, 100*float64(st.ExactBytes)/float64(max(tot, 1)))
		}
	}
	return nil
}
