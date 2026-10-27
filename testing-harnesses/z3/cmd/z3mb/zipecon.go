package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/dexz3"
	"zeros3-z3/jvmz3"
	"zeros3-z3/lab"
)

func init() { cmds["zipecon"] = zipEcon }

func irSpec(name string) lab.IRSpec {
	for _, s := range lab.IRSpecs() {
		if s.Name == name {
			return s
		}
	}
	panic("no IR spec " + name)
}

// containerReps builds the APK/JAR representations (outer ZIP + exact DEFLATE, with or without the
// inner managed-bytecode transform).
func containerReps(names []string, st *innerStats) []econRep {
	ex := func(name string, in lab.Inner) econRep {
		spec := irSpec("irp-tok16k")
		spec.Name = name
		spec.Inner = in
		rep := lab.IRRep(spec)
		return econRep{name, func(f lab.EconFile) (*lab.Plan, bool, error) {
			in := &lab.Input{Family: "zip", Format: "zip", Revision: f.Name, Src: lab.BytesSource(f.Name, f.Data), Opts: lab.DefaultOptions()}
			p, err := rep.Analyze(in)
			if err != nil {
				pl, err2 := lab.CDCPlan(f, "zip")
				return pl, true, err2
			}
			return p, false, nil
		}}
	}
	all := []econRep{
		{"cdc", func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "zip"); return p, false, err }},
		{"struct-seg", func(f lab.EconFile) (*lab.Plan, bool, error) {
			for _, r := range lab.Reps() {
				if r.Name == "struct-seg" {
					p, err := r.Analyze(&lab.Input{Family: "zip", Format: "zip", Revision: f.Name, Src: lab.BytesSource(f.Name, f.Data), Opts: lab.DefaultOptions()})
					return p, false, err
				}
			}
			return nil, false, fmt.Errorf("no struct-seg")
		}},
		ex("outer", nil),
	}
	for _, c := range []cutter{cdcProd(), unitCut(16), unitCut(8), unitCut(4), unitCut(2)} {
		c := c
		all = append(all, ex("outer+dex-"+c.Name, dexInner{dexz3.Config{Cut: c.Cut}, st}))
	}
	for _, c := range []struct {
		name string
		cut  func([]byte, []dexz3.Sec) []int
	}{{"whole", nil}, {"cp+body", cpBody}, {"unit-2k", unitCut(2).Cut}, {"unit-1k", unitCutB(1024).Cut}, {"unit-512", unitCutB(512).Cut}} {
		c := c
		all = append(all, ex("outer+jvm-"+c.name, jvmInner{jvmz3.Config{Cut: c.cut}, st}))
	}
	all = append(all, ex("outer+both-unit4k", multiInner{dexInner{dexz3.Config{Cut: unitCut(4).Cut}, st}, jvmInner{jvmz3.Config{Cut: unitCut(2).Cut}, st}}))
	if len(names) == 0 {
		return all
	}
	var out []econRep
	for _, n := range names {
		for _, r := range all {
			if r.Name == n {
				out = append(out, r)
			}
		}
	}
	return out
}

// cpBody cuts a class file after the constant pool.
func cpBody(p []byte, secs []dexz3.Sec) []int {
	if len(secs) >= 2 {
		return []int{secs[0].End}
	}
	return nil
}

// unitCutB is unitCut with a byte target.
func unitCutB(target int) cutter {
	c := unitCut(1)
	c.Name = fmt.Sprintf("unit-%db", target)
	c.Cut = unitCutBytes(target)
	return c
}

func zipEcon(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: zipecon REPS|all FILE...")
	}
	var names []string
	if args[0] != "all" {
		names = strings.Split(args[0], ",")
	}
	var revs []revFiles
	for _, p := range args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		revs = append(revs, revFiles{filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p), []lab.EconFile{{Name: "pkg", Data: b}}})
	}
	for i := range revs {
		revs[i].Name = fmt.Sprintf("r%d", i)
	}
	st := &innerStats{}
	err := runFamily(strings.Join(args[1:], " "), revs, containerReps(names, st))
	fmt.Printf("inner transform: %d members exact, %d fell back (%d / %d bytes)\n", st.ok.Load(), st.fallback.Load(), st.okBytes.Load(), st.fbBytes.Load())
	return err
}
