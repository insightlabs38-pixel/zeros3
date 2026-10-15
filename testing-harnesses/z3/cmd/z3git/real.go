package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/gitz3"
)

type realRepo struct {
	Name, URL, Tip, License, Mix string
	Depth                        int
}

func loadRepos(path string) ([]realRepo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct{ Repos []realRepo }
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Repos, nil
}

func cmdReal(args []string) error {
	var c common
	fs := flag.NewFlagSet("real", flag.ExitOnError)
	c.flags(fs)
	cfg := fs.String("config", "realworld/git.json", "pinned repositories")
	repos := fs.String("repos", "lua,imgui,codicons,zeros3", "repositories")
	maxMiB := fs.Int("max-mib", 64, "hard cap on a fetched cache pack")
	local := fs.String("local", "", "path of the local zeros3 checkout for the zeros3 entry (default: git rev-parse --show-toplevel)")
	which := fs.String("scenarios", "small-rev,gap10,settings,level,loose-pack,chain", "scenarios")
	detail := fs.String("detail", "", "comma-separated representations to break down")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	rs, err := loadRepos(*cfg)
	if err != nil {
		return err
	}
	l := gitz3.NewLab(c.ref, c.packed)
	l.SectionK, l.Slack, l.InlineMax = c.sectionK, c.slack, c.inline
	sel := map[string]bool{}
	for _, n := range strings.Split(*repos, ",") {
		sel[n] = true
	}
	fmt.Printf("%s; packs produced locally with `git pack-objects --threads=1`; fetch date %s\n", gitz3.GitVersion(), os.Getenv("Z3_FETCH_DATE"))
	for _, r := range rs {
		if !sel[r.Name] {
			continue
		}
		cache := gitz3.OpenRealRepo(filepath.Join(gitz3.CacheRoot(), r.Name+".git"))
		url := r.URL
		if url == "local" {
			url = *local
			if url == "" {
				url = os.Getenv("Z3_ZEROS3_REPO")
			}
			if url == "" {
				return fmt.Errorf("zeros3 entry needs -local or Z3_ZEROS3_REPO")
			}
			url = "file://" + url
		}
		if err := cache.Fetch(url, r.Tip, r.Depth, *maxMiB); err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
		revs, err := cache.RevList(r.Tip, 12)
		if err != nil {
			return err
		}
		if len(revs) < 12 {
			return fmt.Errorf("%s: only %d first-parent commits", r.Name, len(revs))
		}
		cnt, _ := cache.Git(nil, "rev-list", "--count", r.Tip)
		fmt.Printf("\n## real: %s — %s\n\nmix: %s; license %s; tip `%s`; shallow depth %d (%s commits reachable); first-parent tips used: T=%s T~1=%s T~10=%s\n", r.Name, r.URL, r.Mix, r.License, r.Tip, r.Depth, strings.TrimSpace(string(cnt)), revs[0][:10], revs[1][:10], revs[10][:10])
		if err := runScenarios(&c, l, cache, w, r.Name, revs, *which, *detail); err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
	}
	return nil
}

// runScenarios measures the standard pack scenarios on a repository whose first-parent
// tips (newest first) are revs.
func runScenarios(c *common, l *gitz3.Lab, cache *gitz3.RealRepo, w, name string, revs []string, which, detail string) error {
	reps := append(gitz3.BaselineReps(), gitz3.ExactReps()...)
	gap := 10
	if len(revs) <= gap {
		gap = len(revs) - 1
	}
	state := func(label string, rev string, spec gitz3.RepackSpec) (*gitz3.State, error) {
		return cache.PackState(label, filepath.Join(w, "rl-"+name+"-"+label), []string{rev}, spec)
	}
	report := func(sname, desc string, states []*gitz3.State) {
		sc := &gitz3.Scenario{Name: sname + " " + name, Desc: desc, States: states}
		res := l.Run(sc, reps)
		res.WriteMD(os.Stdout, c.packed)
		if detail != "" {
			res.WriteDetail(os.Stdout, strings.Split(detail, ",")...)
		}
		printChurn(l, sc)
		for _, s := range states {
			if p := singlePack(l, s); p != nil {
				fmt.Printf("\npack `%s`: %s\n", s.Label, gitz3.Shape(p))
				for _, f := range s.Files {
					if f.Kind == "pack" {
						if an, err := l.Analysis(f, gitz3.AlgoOf(s)); err == nil {
							ir := an.IRSummary()
							fmt.Printf("exact DEFLATE of `%s`: %d/%d entries reconstruct; zlib %d B <- plaintext %d B + skeleton %d B + units %d B (%.1f B/entry)\n", s.Label, ir.OK, ir.Entries, ir.Zlib, ir.Plain, ir.Skel, ir.Units, float64(ir.Skel+ir.Units)/float64(max(ir.OK, 1)))
						}
					}
				}
			}
		}
	}
	for _, sc := range strings.Split(which, ",") {
		switch sc {
		case "small-rev":
			a, err := state("pack(T~1)", revs[1], gitz3.SpecDefault)
			if err != nil {
				return err
			}
			b, err := state("pack(T)", revs[0], gitz3.SpecDefault)
			if err != nil {
				return err
			}
			report("small-rev", "A = full repack of history to T~1, B = full repack to T (one revision later)", []*gitz3.State{a, b})
		case "gap10":
			a, err := state(fmt.Sprintf("pack(T~%d)", gap), revs[gap], gitz3.SpecDefault)
			if err != nil {
				return err
			}
			b, err := state("pack(T)", revs[0], gitz3.SpecDefault)
			if err != nil {
				return err
			}
			report("gap"+fmt.Sprint(gap), fmt.Sprintf("A = full repack to T~%d, B = full repack to T", gap), []*gitz3.State{a, b})
		case "settings":
			var st []*gitz3.State
			for _, sp := range []gitz3.RepackSpec{gitz3.SpecDefault, gitz3.SpecReverse, gitz3.SpecW3D3, gitz3.SpecW50D250, gitz3.SpecRefDelta, gitz3.SpecNoDelta} {
				s, err := state(sp.Label, revs[0], sp)
				if err != nil {
					return err
				}
				st = append(st, s)
			}
			report("settings", "same object set at T; producer settings (cumulative chain from default)", st)
		case "level":
			var st []*gitz3.State
			for _, n := range []int{6, 1, 9} {
				sp := gitz3.SpecLevel(n)
				s, err := state(sp.Label, revs[0], sp)
				if err != nil {
					return err
				}
				st = append(st, s)
			}
			report("level", "same object set at T; compression level only (-F)", st)
		case "loose-pack":
			a, err := cache.LooseState("loose(T)", filepath.Join(w, "rl-"+name+"-loose"), []string{revs[0]})
			if err != nil {
				return err
			}
			b, err := state("pack(T)", revs[0], gitz3.SpecDefault)
			if err != nil {
				return err
			}
			report("loose-pack", "loose objects of T -> first pack", []*gitz3.State{a, b})
		case "derive":
			o, err := gitz3.StartDeltaOracle()
			if err != nil {
				fmt.Println("\n(derive skipped:", err, ")")
				continue
			}
			a, err := state("pack(T~1)", revs[1], gitz3.SpecDefault)
			if err != nil {
				o.Close()
				return err
			}
			b, err := state("pack(T)", revs[0], gitz3.SpecDefault)
			if err != nil {
				o.Close()
				return err
			}
			l := gitz3.NewLab(96, false)
			pa, pb := singlePack(l, a), singlePack(l, b)
			if pa == nil || pb == nil {
				o.Close()
				return fmt.Errorf("derive: pack did not resolve")
			}
			st, err := gitz3.DeriveDeltas(pb, o)
			if err != nil {
				o.Close()
				return err
			}
			g, err := gitz3.DeriveGain(pa, pb, o)
			o.Close()
			if err != nil {
				return err
			}
			fmt.Printf("\n**derive %s** (oracle: Git v2.43.0 create_delta): pack(T) has %d delta entries; %d regenerate exactly from (base, target), %d differ, %d none; stored delta bytes %d, exactly regenerable %d (%.1f%%).\n", name, st.Deltas, st.Exact, st.Differ, st.NoDelta, st.ExactBytes+st.DifferBytes, st.ExactBytes, 100*float64(st.ExactBytes)/float64(max(st.ExactBytes+st.DifferBytes, 1)))
			fmt.Printf("T~1 -> T: %d delta entries in B, %d programs already in A, %d new (%d bytes), %d with base/target not in A, %d regenerable from A's objects (%d bytes).\n", g.BDeltas, g.ProgramsInA, g.NewPrograms, g.NewProgBytes, g.BaseOrTgtNotInA, g.Derivable, g.DerivableBytes)
		case "chain":
			var st []*gitz3.State
			for k := min(4, len(revs)-1); k >= 0; k-- {
				s, err := state(fmt.Sprintf("gc@T~%d", k), revs[k], gitz3.SpecDefault)
				if err != nil {
					return err
				}
				st = append(st, s)
			}
			report("chain", "cumulative family: full repack after each of the last revisions", st)
		}
	}
	return nil
}
