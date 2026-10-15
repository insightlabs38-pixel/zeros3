package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"zeros3-z3/gitz3"
)

// cmdAdmit prints, per pack, the dry-run first-ingest premium of each layout over
// CDC and the layout the chooser picks for several q (the probability that the
// stored pack is later superseded by a rewrite of mostly the same objects).
func cmdAdmit(args []string) error {
	var c common
	fs := flag.NewFlagSet("admit", flag.ExitOnError)
	c.flags(fs)
	qs := fs.String("q", "0.005,0.02,0.05,0.1,0.2,0.5", "comma-separated probabilities")
	repos := fs.String("repos", "lua,imgui,codicons,zeros3", "cached real repositories to include (skipped if not cached)")
	cfg := fs.String("config", "realworld/git.json", "pinned repositories")
	apk := fs.String("apk", "", "corpus directory with rw-fdroid-apk (z3fetch -group fdroid-apk -out)")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	var qv []float64
	for _, s := range strings.Split(*qs, ",") {
		v, _ := strconv.ParseFloat(s, 64)
		qv = append(qv, v)
	}
	fmt.Printf("%s\n\nFirst-ingest premium over CDC (production pack rule, empty store; pack + .idx) and the chosen layout per q. q* = premium / (0.7 x CDC cost): the layout pays when the chance of a same-object rewrite exceeds q*.\n\n", gitz3.GitVersion())
	fmt.Print("| pack | entries | median zlib B | CDC KiB | seq-zlib | seq-auto | set-auto | set-plain |")
	for _, q := range qv {
		fmt.Printf(" q=%g |", q)
	}
	fmt.Print("\n|---|---:|---:|---:|---:|---:|---:|---:|")
	for range qv {
		fmt.Print("---|")
	}
	fmt.Println()
	row := func(label string, a gitz3.Algo, pack, idx []byte) error {
		p, err := gitz3.ParsePack(pack, a, gitz3.Limits{})
		if err != nil {
			return err
		}
		if _, err := p.Resolve(); err != nil {
			return err
		}
		an, err := gitz3.AnalyzePack(p)
		if err != nil {
			return err
		}
		pr, err := gitz3.DryRun(an, pack, idx)
		if err != nil {
			return err
		}
		pc := func(v int64) string {
			return fmt.Sprintf("%+.1f%% (q*=%.1f%%)", 100*float64(v-pr.CDC)/float64(pr.CDC), 100*float64(v-pr.CDC)/(gitz3.ExpectedSaving*float64(pr.CDC)))
		}
		fmt.Printf("| %s | %d | %d | %d | %s | %s | %s | %s |", label, pr.Entries, pr.EntriesMedianZlib, pr.CDC>>10, pc(pr.SeqZlib), pc(pr.SeqAuto), pc(pr.SetAuto), pc(pr.SetPlain))
		for _, q := range qv {
			ad, err := gitz3.Admit(pack, idx, a, q)
			if err != nil {
				return err
			}
			fmt.Printf(" %s |", ad.Layout)
		}
		fmt.Println()
		return nil
	}
	var fam gitz3.Family
	for _, f := range gitz3.Families() {
		if f.Name == "add-small" {
			fam = f
		}
	}
	h := gitz3.FamilyHistory(fam, c.seed, c.scale, c.prefix)
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		r, err := gitz3.Build(w, "ad", a, h, gitz3.SpecDefault)
		if err != nil {
			return err
		}
		pp, _ := r.PackPairs()
		if err := row("synthetic mixed history ("+a.String()+")", a, pp[0].Pack, pp[0].Idx); err != nil {
			return err
		}
	}
	if rs, err := loadRepos(*cfg); err == nil {
		sel := map[string]bool{}
		for _, n := range strings.Split(*repos, ",") {
			sel[n] = true
		}
		for _, rp := range rs {
			dir := filepath.Join(gitz3.CacheRoot(), rp.Name+".git")
			if _, err := os.Stat(dir); err != nil || !sel[rp.Name] {
				continue
			}
			cache := gitz3.OpenRealRepo(dir)
			st, err := cache.PackState("T", filepath.Join(w, "ad-"+rp.Name), []string{rp.Tip}, gitz3.SpecDefault)
			if err != nil {
				return err
			}
			if err := row("real "+rp.Name+" @T", gitz3.SHA1, st.Files[0].Data, st.Files[1].Data); err != nil {
				return err
			}
		}
	}
	if *apk != "" {
		// the APK pack: five artifacts, history to T
		if err := admitAPK(*apk, w, row); err != nil {
			return err
		}
	}
	return nil
}
