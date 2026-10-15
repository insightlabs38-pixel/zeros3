// Command z3git runs the Z3-16 Git pack / loose-object experiments.
//
//	z3git probe    producer provenance and byte-reproducibility
//	z3git matrix   repack matrix over synthetic families
//
// Scratch repositories live under -work (default $Z3_WORK or the temp dir) and
// are never committed.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"

	"zeros3-z3/gitz3"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: z3git <probe|matrix> [flags]")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	if pf := os.Getenv("Z3_CPUPROFILE"); pf != "" {
		f, ferr := os.Create(pf)
		if ferr == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}
	var err error
	switch cmd {
	case "probe":
		err = cmdProbe(args)
	case "matrix":
		err = cmdMatrix(args)
	case "admit":
		err = cmdAdmit(args)
	case "mapping":
		err = cmdMapping(args)
	case "binhist":
		err = cmdBinhist(args)
	case "real":
		err = cmdReal(args)
	case "scale":
		err = cmdScale(args)
	case "reads":
		err = cmdReads(args)
	case "derive":
		err = cmdDerive(args)
	case "fixtures":
		err = cmdFixtures(args)
	default:
		usage()
	}
	if err != nil {
		pprof.StopCPUProfile()
		fmt.Fprintln(os.Stderr, "z3git:", err)
		os.Exit(1)
	}
}

func algoOf(s string) gitz3.Algo {
	if s == "sha256" {
		return gitz3.SHA256
	}
	return gitz3.SHA1
}

type common struct {
	work     string
	algo     string
	seed     uint64
	scale    int
	prefix   int
	ref      int64
	packed   bool
	sectionK int
	inline   int
	slack    float64
}

func (c *common) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.work, "work", "", "scratch directory (default $Z3_WORK or temp)")
	fs.StringVar(&c.algo, "algo", "sha1", "object format: sha1|sha256")
	fs.Uint64Var(&c.seed, "seed", 20260616, "corpus seed")
	fs.IntVar(&c.scale, "scale", 1, "content scale (big text 256 KiB x scale, binary 1 MiB x scale)")
	fs.IntVar(&c.prefix, "prefix", 24, "common history length before the family's final revision")
	fs.Int64Var(&c.ref, "ref-bytes", 96, "recipe cost of one component reference in the JSON-layout cost (table layout is exact)")
	fs.BoolVar(&c.packed, "packed", true, "also report the production pack-rule estimate")
	fs.IntVar(&c.sectionK, "section", 0, "set layout: mean entries per table section (default 8)")
	fs.IntVar(&c.inline, "inline", 0, "pieces shorter than this many bytes are inline in the recipe/table (default 40)")
	fs.Float64Var(&c.slack, "slack", 0, "auto mode: accept plaintext+recon up to (1+slack) x the zlib cost (default 0.10)")
}

func (c *common) setup() (string, error) {
	w, err := gitz3.Scratch(c.work)
	if err != nil {
		return "", err
	}
	return w, nil
}

func cmdProbe(args []string) error {
	var c common
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	c.flags(fs)
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	fmt.Println(gitz3.GitVersion())
	fam := gitz3.Families()[0]
	h := gitz3.FamilyHistory(fam, c.seed, c.scale, c.prefix)
	specs := []gitz3.RepackSpec{gitz3.SpecDefault, gitz3.SpecReuse, gitz3.SpecW3D3, gitz3.SpecNoDelta, gitz3.SpecRefDelta, gitz3.SpecLevel(1), gitz3.SpecReverse}
	fmt.Println("| algo | steps | pack size | pack sha256 | idx size | pack repeat identical | idx repeat identical | repository tip |")
	fmt.Println("|---|---|---:|---|---:|---|---|---|")
	var cmds []string
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		for _, sp := range specs {
			st, err := gitz3.Determinism(w, a, h, sp)
			if err != nil {
				return err
			}
			fmt.Printf("| %v | import + %s | %d | %s | %d | %v | %v | %s |\n", a, sp.Label, st.PackSize, st.PackSHA, st.IdxSize, st.Repeat, st.RepeatIdx, st.Tip[:12])
			if a == gitz3.SHA1 {
				cmds = append(cmds, fmt.Sprintf("%s: %s", sp.Label, strings.Join(st.Commands, " ; ")))
			}
		}
	}
	fmt.Println("\nCommand lines (every run is prefixed by `git -c pack.threads=1 -c gc.auto=0 -c pack.writeReverseIndex=false -c core.logAllRefUpdates=false`; environment: HOME=<repo>, LC_ALL=C, TZ=UTC, GIT_CONFIG_NOSYSTEM=1, GIT_CONFIG_GLOBAL=/dev/null, fixed author/committer name, email and date):")
	for _, c := range cmds {
		fmt.Println("-", c)
	}
	return nil
}

func singlePack(l *gitz3.Lab, st *gitz3.State) *gitz3.Pack {
	var pk *gitz3.Pack
	for _, f := range st.Files {
		if f.Kind != "pack" {
			continue
		}
		if pk != nil {
			return nil
		}
		p, err := l.ParsedPack(f, st.Algo)
		if err != nil {
			return nil
		}
		pk = p
	}
	return pk
}

func printIR(l *gitz3.Lab, sc *gitz3.Scenario) {
	for _, s := range sc.States {
		for _, f := range s.Files {
			if f.Kind != "pack" || singlePack(l, s) == nil {
				continue
			}
			if an, err := l.Analysis(f, s.Algo); err == nil {
				ir := an.IRSummary()
				fmt.Printf("exact DEFLATE of `%s`: %d/%d entries reconstruct; zlib %d B <- plaintext %d B + skeleton %d B + units %d B (%.1f B/entry)\n", s.Label, ir.OK, ir.Entries, ir.Zlib, ir.Plain, ir.Skel, ir.Units, float64(ir.Skel+ir.Units)/float64(max(ir.OK, 1)))
			}
		}
	}
}

func printChurn(l *gitz3.Lab, sc *gitz3.Scenario) {
	var ps []*gitz3.Pack
	for _, s := range sc.States {
		ps = append(ps, singlePack(l, s))
	}
	if len(ps) < 2 || ps[0] == nil {
		return
	}
	var any bool
	for i := 1; i < len(ps); i++ {
		if ps[i] == nil {
			continue
		}
		if !any {
			gitz3.ChurnHeader(os.Stdout)
			any = true
		}
		gitz3.WriteChurn(os.Stdout, sc.States[0].Label+" -> "+sc.States[i].Label, gitz3.Attribute(ps[0], ps[i]))
	}
}

func stateOfBuild(w, name string, a gitz3.Algo, h *gitz3.History, label string, specs ...gitz3.RepackSpec) (*gitz3.State, error) {
	r, err := gitz3.Build(w, name, a, h, specs...)
	if err != nil {
		return nil, err
	}
	return gitz3.StateOf(r, label)
}

func cmdMatrix(args []string) error {
	var c common
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	c.flags(fs)
	fams := fs.String("families", "add-small,edit-text-mid,edit-bin-sparse,tiny-churn", "comma-separated families, or all")
	detail := fs.String("detail", "", "comma-separated representations to break down")
	which := fs.String("scenarios", "loose-pack,repeat,reorder,depth,level,refdelta,small-rev", "scenarios")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	a := algoOf(c.algo)
	l := gitz3.NewLab(c.ref, c.packed)
	l.SectionK, l.Slack, l.InlineMax = c.sectionK, c.slack, c.inline
	reps := append(gitz3.BaselineReps(), gitz3.ExactReps()...)
	famSel := map[string]bool{}
	for _, f := range strings.Split(*fams, ",") {
		famSel[f] = true
	}
	fmt.Printf("%s, object format %s, seed %d\n", gitz3.GitVersion(), a, c.seed)
	for _, fam := range gitz3.Families() {
		if !famSel["all"] && !famSel[fam.Name] {
			continue
		}
		h := gitz3.FamilyHistory(fam, c.seed, c.scale, c.prefix)
		n := len(h.Trees)
		hA := h.Prefix(n - 1)
		if fam.Shape == "branchy" {
			hA = h.Prefix(c.prefix + 1)
		}
		fmt.Printf("\n## family %s (%s)\n", fam.Name, a)
		for _, sc := range strings.Split(*which, ",") {
			var states []*gitz3.State
			var desc string
			switch sc {
			case "loose-pack":
				desc = "loose objects -> first pack"
				s0, err := stateOfBuild(w, "lp0", a, hA, "loose", gitz3.SpecUnpack)
				if err != nil {
					return err
				}
				s1, err := stateOfBuild(w, "lp1", a, hA, "pack", gitz3.SpecDefault)
				if err != nil {
					return err
				}
				states = []*gitz3.State{s0, s1}
			case "repeat":
				desc = "same command twice"
				s0, _ := stateOfBuild(w, "rp0", a, hA, "run1", gitz3.SpecDefault)
				s1, _ := stateOfBuild(w, "rp1", a, hA, "run2", gitz3.SpecDefault)
				states = []*gitz3.State{s0, s1}
			case "reorder":
				desc = "same object set, reversed pack-objects input order"
				s0, _ := stateOfBuild(w, "ro0", a, hA, "default", gitz3.SpecDefault)
				s1, err := stateOfBuild(w, "ro1", a, hA, "reversed", gitz3.SpecReverse)
				if err != nil {
					return err
				}
				states = []*gitz3.State{s0, s1}
			case "depth":
				desc = "delta window/depth variations (cumulative chain from default)"
				for _, sp := range []gitz3.RepackSpec{gitz3.SpecDefault, gitz3.SpecW3D3, gitz3.SpecW50D250, gitz3.SpecNoDelta} {
					s, err := stateOfBuild(w, "dp-"+sp.Label, a, hA, sp.Label, sp)
					if err != nil {
						return err
					}
					states = append(states, s)
				}
			case "level":
				desc = "compression level only (-F recompress, same object set)"
				for _, n := range []int{6, 1, 9} {
					sp := gitz3.SpecLevel(n)
					s, err := stateOfBuild(w, "lv-"+sp.Label, a, hA, sp.Label, sp)
					if err != nil {
						return err
					}
					states = append(states, s)
				}
			case "refdelta":
				desc = "OFS_DELTA -> REF_DELTA"
				s0, _ := stateOfBuild(w, "rd0", a, hA, "ofs", gitz3.SpecDefault)
				s1, err := stateOfBuild(w, "rd1", a, hA, "ref", gitz3.SpecRefDelta)
				if err != nil {
					return err
				}
				states = []*gitz3.State{s0, s1}
			case "chain":
				desc = "cumulative family: full repack (gc) after each of the last 5 revisions"
				for k := n - 4; k <= n; k++ {
					hk := h.Prefix(k)
					if fam.Shape == "branchy" {
						hk = h.Prefix(c.prefix + 1 + (k - (n - 4)) + 3)
					}
					s, err := stateOfBuild(w, fmt.Sprintf("ch-%d", k), a, hk, fmt.Sprintf("gc@%d", k), gitz3.SpecDefault)
					if err != nil {
						return err
					}
					states = append(states, s)
				}
			case "small-rev":
				desc = "one small revision: A = full pack(H); B variants"
				sA, err := stateOfBuild(w, "sr-a", a, hA, "A:pack(H)", gitz3.SpecDefault)
				if err != nil {
					return err
				}
				sFull, err := stateOfBuild(w, "sr-b1", a, h, "B:full -f", gitz3.SpecDefault)
				if err != nil {
					return err
				}
				// growth: pack(H), then import H+1 as a second pack
				rg, err := gitz3.Build(w, "sr-b2", a, hA, gitz3.SpecDefault)
				if err != nil {
					return err
				}
				if err := rg.ImportHistory(h); err != nil {
					return err
				}
				sGrow, _ := gitz3.StateOf(rg, "B:two packs")
				if err := rg.Repack(gitz3.SpecReuse); err != nil {
					return err
				}
				sCons, _ := gitz3.StateOf(rg, "B:consolidated(reuse)")
				for _, b := range []*gitz3.State{sFull, sGrow, sCons} {
					scen := &gitz3.Scenario{Name: sc + " " + fam.Name, Desc: desc, States: []*gitz3.State{sA, b}}
					res := l.Run(scen, reps)
					res.WriteMD(os.Stdout, c.packed)
					printChurn(l, scen)
					if *detail != "" {
						res.WriteDetail(os.Stdout, strings.Split(*detail, ",")...)
					}
				}
				continue
			}
			scen := &gitz3.Scenario{Name: sc + " " + fam.Name, Desc: desc, States: states}
			res := l.Run(scen, reps)
			res.WriteMD(os.Stdout, c.packed)
			printChurn(l, scen)
			printIR(l, scen)
			if *detail != "" {
				res.WriteDetail(os.Stdout, strings.Split(*detail, ",")...)
			}
		}
	}
	return nil
}

func cmdFixtures(args []string) error {
	fs := flag.NewFlagSet("fixtures", flag.ExitOnError)
	out := fs.String("out", "gitz3/testdata", "output directory")
	work := fs.String("work", "", "scratch directory")
	fs.Parse(args)
	w, err := gitz3.Scratch(*work)
	if err != nil {
		return err
	}
	fx, err := gitz3.BuildFixtures(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for name, b := range fx {
		if err := os.WriteFile(*out+"/"+name, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("%-28s %6d B  sha256 %s\n", name, len(b), gitz3.SHA256Hex(b)[:16])
	}
	return nil
}
