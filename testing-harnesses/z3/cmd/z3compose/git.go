package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/composez3"
	"zeros3-z3/fstate"
	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
)

// C: Git pack -> blob. The outer representation is the Z3-16 exact pack recipe (DEFLATE
// IR + entry plaintext); the inner transform represents the resolved blob. Two producer
// settings are compared because they answer different questions:
//
//	delta pack     `git repack -a -d -f`: Git's own delta compression is in the pack and the
//	               Git-aware recipe stores the delta programs (outer only)
//	no-delta pack  window=0: every blob is a full object; the large blobs are kept whole
//	               (RepOpts.WholeMin) and composed with the inner transform
type gitCtx struct{ packPlans map[string]*lab.Plan }

func gitRep(l *gitz3.Lab, name string, wholeMin int, kinds []composez3.Kind) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		gx := c.X.(*gitCtx)
		switch f.Kind {
		case "pack":
			an, err := l.Analysis(f, gitz3.Algo(c.Tag))
			if err != nil {
				c.Counts["pack_fallback"]++
				return fstate.FallbackCDC(f)
			}
			pl, st := an.SeqPlan(gitz3.RepOpts{Mode: "auto", Has: c.Acct.Has, WholeMin: wholeMin})
			c.Counts["deltas"] += int64(st.Deltas)
			c.Counts["entries"] += int64(st.Entries)
			if kinds != nil {
				cp, rep, err := composez3.Compose(pl, composez3.Options{MinSize: 64 << 10, Kinds: kinds})
				if err != nil {
					return nil, err
				}
				if a := composez3.Audit(cp); len(a) > 0 {
					return nil, fmt.Errorf("duplicate charging: %v", a)
				}
				c.Counts["inner_substituted"] += int64(rep.Substituted)
				c.Counts["inner_fallback"] += int64(rep.Fallback)
				pl = cp
			}
			gx.packPlans[strings.TrimSuffix(f.Name, ".pack")] = pl
			return pl, nil
		case "idx":
			base := strings.TrimSuffix(f.Name, ".idx")
			if pp := gx.packPlans[base]; pp != nil {
				for _, sf := range c.Files {
					if sf.Name == base+".pack" {
						if p, err := l.ParsedPack(sf, gitz3.Algo(c.Tag)); err == nil {
							if pl, ok := gitz3.IdxPlan(p, f.Data, pp); ok {
								return pl, nil
							}
						}
					}
				}
			}
		}
		return fstate.FallbackCDC(f)
	}}
}

func gitStates(work, name string, versions [][]byte, path string, spec gitz3.RepackSpec) ([]*fstate.State, error) {
	var states []*fstate.State
	for k := 1; k <= len(versions); k++ {
		h := &gitz3.History{}
		prev := -1
		for i := 0; i < k; i++ {
			t := gitz3.Snap{path: versions[i], "README": []byte("z3 compound workload\n")}
			if prev < 0 {
				prev = h.Add(t)
			} else {
				prev = h.Add(t, prev)
			}
		}
		r, err := gitz3.Build(work, fmt.Sprintf("%s-%s-%d", name, spec.Label, k), gitz3.SHA1, h, spec)
		if err != nil {
			return nil, err
		}
		pp, err := r.PackPairs()
		if err != nil || len(pp) != 1 {
			return nil, fmt.Errorf("%s k=%d: %d packs (%v)", name, k, len(pp), err)
		}
		states = append(states, &fstate.State{Label: fmt.Sprintf("commit %d", k), Tag: int(gitz3.SHA1), Files: []fstate.File{
			{Name: "pack/p.pack", Kind: "pack", Data: pp[0].Pack}, {Name: "pack/p.idx", Kind: "idx", Data: pp[0].Idx}}})
	}
	return states, nil
}

func cmdGit(args []string) error {
	fs := flag.NewFlagSet("git", flag.ExitOnError)
	bindir := fs.String("bin", "/tmp/claude-0/z3bin/linux-amd64/std", "directory of Go executables r0..r3 (z3bin corpus -flags std)")
	work := fs.String("work", "", "scratch directory for the Git repositories (default: a temp dir)")
	n := fs.Int("n", 4, "commits per family")
	jsonOut := fs.String("json", "", "write results as JSON")
	fs.Parse(args)
	if *work == "" {
		d, err := os.MkdirTemp("", "z3compose-git")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)
		*work = d
	}
	type family struct {
		name, path string
		kind       composez3.Kind
		versions   [][]byte
		fkind      string
	}
	var fams []family
	var elf [][]byte
	for _, r := range []string{"r0", "r1", "r2", "r3"}[:min(*n, 4)] {
		b, err := os.ReadFile(filepath.Join(*bindir, r))
		if err != nil {
			return fmt.Errorf("%w (build with: z3bin corpus -out DIR -goos linux -flags std)", err)
		}
		elf = append(elf, b)
	}
	fams = append(fams, family{"elf-history", "bin/app", composez3.KindExec, elf, "elf"})
	var dbs [][]byte
	for i := 0; i < min(*n, 6); i++ {
		b, err := os.ReadFile(filepath.Join(structCache(), "sqlite", "hist10", fmt.Sprintf("r%d", i), "app.sqlite"))
		if err != nil {
			return fmt.Errorf("%w (generate with oracle/structured/gen_sqlite.py)", err)
		}
		dbs = append(dbs, b)
	}
	fams = append(fams, family{"sqlite-history", "data/app.db", composez3.KindSQLite, dbs, "sqlite"})
	all := map[string]any{}
	for _, fm := range fams {
		dst, err := gitStates(*work, fm.name, fm.versions, fm.path, gitz3.SpecDefault)
		if err != nil {
			return err
		}
		nst, err := gitStates(*work, fm.name, fm.versions, fm.path, gitz3.SpecNoDelta)
		if err != nil {
			return err
		}
		l := gitz3.NewLab(96, true)
		l.Setup = func(c *fstate.Ctx, st *fstate.State) { c.X = &gitCtx{packPlans: map[string]*lab.Plan{}} }
		cdc := cdcRep()
		reps1 := []fstate.RepDef{cdc, gitRep(l, "git-aware(delta)", 0, nil)}
		m1, err := runMatrix(&l.Runner, dst, reps1)
		if err != nil {
			return err
		}
		l2 := gitz3.NewLab(96, true)
		l2.Setup = l.Setup
		reps2 := []fstate.RepDef{cdc, gitRep(l2, "git-aware(nodelta)", 0, nil), gitRep(l2, "composed(nodelta)", 64<<10, []composez3.Kind{fm.kind})}
		m2, err := runMatrix(&l2.Runner, nst, reps2)
		if err != nil {
			return err
		}
		var arts []*fstate.State
		for i, b := range fm.versions {
			arts = append(arts, &fstate.State{Label: fmt.Sprintf("commit %d", i+1), Files: []fstate.File{{Name: fmt.Sprintf("artifact-%d", i), Kind: fm.fkind, Data: b}}})
		}
		ireps := []fstate.RepDef{cdcRep(), innerRep("inner-alone", fm.kind)}
		im, err := runMatrix(&fstate.Runner{RefBytes: 96, Pack: true}, arts, ireps)
		if err != nil {
			return err
		}
		fmt.Printf("\n== C. Git pack -> blob: %s (%d commits of %s; SHA-1; git %s)\n", fm.name, len(fm.versions), fm.path, gitz3.GitVersion())
		fmt.Printf("%-10s %9s %11s %14s %16s %18s %18s %13s\n", "state", "raw delta", "cdc(delta)", "git-aware(Δ)", "raw no-delta", "git-aware(no-Δ)", "composed(no-Δ)", "inner alone")
		for i := range dst {
			a, b := dst[i], nst[i]
			fmt.Printf("%-10s %9s %11s %14s %16s %18s %18s %13s\n", a.Label, kib(a.Bytes()), kib(m1["cdc"][a.Label].Total), kib(m1["git-aware(delta)"][a.Label].Total),
				kib(b.Bytes()), kib(m2["git-aware(nodelta)"][b.Label].Total), kib(m2["composed(nodelta)"][b.Label].Total), kib(im["inner-alone"][a.Label].Total))
		}
		printSplit("composed(nodelta)", nst, "composed(nodelta)", m2)
		for i := range dst {
			fmt.Printf("commit %d: delta entries in the delta pack: %d of %d; inner objects substituted in the no-delta pack: %d (fallbacks %d)\n", i+1, m1["git-aware(delta)"][dst[i].Label].Deltas, m1["git-aware(delta)"][dst[i].Label].Entries, m2["composed(nodelta)"][nst[i].Label].Inner, m2["composed(nodelta)"][nst[i].Label].InnerFallback)
		}
		all[fm.name] = map[string]any{"delta": m1, "nodelta": m2, "inner_alone": im}
	}
	return writeJSON(*jsonOut, all)
}
