package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/gitz3"
)

func mib(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }

func buildScale(w, name string, a gitz3.Algo, kind string, nFiles, size, revs int, seed uint64, spec gitz3.RepackSpec) (*gitz3.Repo, error) {
	r, err := gitz3.NewRepo(filepath.Join(w, name), a)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := gitz3.WriteScaleStream(&b, kind, nFiles, size, revs, seed); err != nil {
		return nil, err
	}
	if err := r.Import(b.Bytes()); err != nil {
		return nil, err
	}
	if err := r.Repack(spec); err != nil {
		return nil, err
	}
	return r, nil
}

func cmdScale(args []string) error {
	var c common
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	c.flags(fs)
	sweep := fs.String("sweep", "all", "size|count|depth|all")
	maxMiB := fs.Int("max-mib", 160, "skip points whose pack exceeds this many MiB")
	irMax := fs.Int("ir-max-mib", 64, "skip the exact-DEFLATE analysis above this pack size")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	a := algoOf(c.algo)
	fmt.Printf("%s, object format %s\n\n", gitz3.GitVersion(), a)
	hdr := func() {
		fmt.Println("| point | pack MiB | entries | deltas | max depth | resolved MiB | parse ms (MiB/s) | resolve ms (MiB/s) | peak heap parse/resolve MiB | analyze ms (MiB/s) | peak analyze MiB | set recipe KiB | rebuild ms (MiB/s) | peak rebuild MiB | exact |")
		fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|")
	}
	row := func(sp gitz3.ScalePoint) {
		tp := func(ms float64) string {
			if ms == 0 {
				return "-"
			}
			return fmt.Sprintf("%.0f (%.0f)", ms, float64(sp.PackBytes)/(1<<20)/(ms/1000))
		}
		fmt.Printf("| %s | %s | %d | %d | %d | %s | %s | %s | %.0f / %.0f | %s | %.0f | %.1f | %s | %.0f | %v |\n", sp.Label, mib(sp.PackBytes), sp.Entries, sp.Deltas, sp.MaxDepth, mib(sp.ResolvedBytes), tp(sp.ParseMs), tp(sp.ResolveMs), sp.PeakParseMiB, sp.PeakResolveMiB, tp(sp.AnalyzeMs), sp.PeakAnalyzeMiB, float64(sp.RecipeSet)/1024, tp(sp.RebuildMs), sp.PeakRebuildMiB, sp.ExactOK)
		if sp.Err != "" {
			fmt.Println("|  | error:", sp.Err, "|")
		}
	}
	type pt struct {
		label              string
		kind               string
		nFiles, size, revs int
		spec               gitz3.RepackSpec
	}
	var pts []pt
	if *sweep == "size" || *sweep == "all" {
		for _, s := range []int{8 << 10, 32 << 10, 128 << 10, 512 << 10} {
			pts = append(pts, pt{fmt.Sprintf("size 300x%dK", s>>10), "size", 300, s, 4, gitz3.SpecDefault})
		}
	}
	if *sweep == "count" || *sweep == "all" {
		for _, n := range []int{2000, 8000, 32000, 96000} {
			pts = append(pts, pt{fmt.Sprintf("count %d tiny", n), "count", n, 400, 4, gitz3.SpecDefault})
		}
	}
	if *sweep == "depth" || *sweep == "all" {
		for _, d := range []int{10, 50, 250, 1000} {
			sp := gitz3.RepackSpec{Label: fmt.Sprintf("depth%d", d), Force: true, Window: 20, Depth: d}
			pts = append(pts, pt{fmt.Sprintf("depth<=%d (1 file 64K x600 revs)", d), "depth", 1, 64 << 10, 600, sp})
		}
	}
	hdr()
	for _, p := range pts {
		t0 := time.Now()
		r, err := buildScale(w, "scale", a, p.kind, p.nFiles, p.size, p.revs, c.seed, p.spec)
		if err != nil {
			return err
		}
		pp, err := r.PackPairs()
		if err != nil || len(pp) != 1 {
			return fmt.Errorf("packs: %v %d", err, len(pp))
		}
		if int(len(pp[0].Pack)>>20) > *maxMiB {
			fmt.Printf("| %s | %s | skipped (-max-mib) |\n", p.label, mib(int64(len(pp[0].Pack))))
			continue
		}
		sp := gitz3.MeasureScale(p.label, a, pp[0].Pack, pp[0].Idx, int(len(pp[0].Pack)>>20) <= *irMax)
		row(sp)
		_ = t0
		os.RemoveAll(filepath.Join(w, "scale"))
	}
	return nil
}

func cmdReads(args []string) error {
	var c common
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	c.flags(fs)
	sample := fs.Int("sample", 300, "objects sampled per pack")
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	a := algoOf(c.algo)
	fmt.Printf("%s, %s; mean bytes fetched to read one object by native id (sample %d, seed 1)\n\n", gitz3.GitVersion(), a, *sample)
	fmt.Println("| pack | entries | mean chain depth | mean object B | CDC pack+idx | (of which idx) | gp-entry (idx CDC + chain entries) | gp-set (sections + plain/delta) | floor (object's own packed bytes) | set directory B | deepest chain: depth / set / CDC |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|")
	type cfg struct {
		label string
		fam   string
		spec  gitz3.RepackSpec
		pre   int
	}
	cfgs := []cfg{
		{"mixed history, window10/depth50", "add-small", gitz3.SpecDefault, 60},
		{"mixed history, depth<=250", "add-small", gitz3.SpecW50D250, 60},
		{"mixed history, no deltas", "add-small", gitz3.SpecNoDelta, 60},
		{"tiny-file churn", "tiny-churn", gitz3.SpecDefault, 30},
	}
	for _, g := range cfgs {
		var fam gitz3.Family
		for _, f := range gitz3.Families() {
			if f.Name == g.fam {
				fam = f
			}
		}
		h := gitz3.FamilyHistory(fam, c.seed, c.scale, g.pre)
		r, err := gitz3.Build(w, "rd", a, h, g.spec)
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
		an, err := gitz3.AnalyzePack(p)
		if err != nil {
			return err
		}
		rc, err := gitz3.MeasureReads(an, pp[0].Pack, pp[0].Idx, *sample, 1, gitz3.RepOpts{Mode: "auto"})
		if err != nil {
			return err
		}
		fmt.Printf("| %s | %d | %.2f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %d | %d / %.0f / %.0f |\n", g.label, len(p.Entries), rc.MeanDepth, rc.ObjectBytes, rc.CDCPackIdx, rc.CDCIdxPart, rc.EntryComponents, rc.SetSections, rc.NativeObject, rc.SetDirectoryBytes, rc.DeepestChainDepth, rc.DeepestChainFetch, rc.CDCPackIdxAtDeepest)
	}
	// depth effect on a real parse: time to materialise objects from chains of different depth
	fmt.Println("\nObject materialisation time vs delta depth (1 file 64K edited 600 times, window 20; mean of 200 random objects, no cache):")
	fmt.Println("\n| max depth allowed | chain depth mean / max | entry bytes touched (mean) | materialise µs (mean) |")
	fmt.Println("|---:|---:|---:|---:|")
	for _, d := range []int{10, 50, 250, 1000} {
		sp := gitz3.RepackSpec{Label: "d", Force: true, Window: 20, Depth: d}
		r, err := buildScale(w, "rdd", a, "depth", 1, 64<<10, 600, c.seed, sp)
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
		rng := rand.New(rand.NewSource(1))
		var depthSum, bytesSum float64
		maxd := 0
		t0 := time.Now()
		for k := 0; k < 200; k++ {
			i := rng.Intn(len(p.Entries))
			depthSum += float64(p.Entries[i].Depth)
			if p.Entries[i].Depth > maxd {
				maxd = p.Entries[i].Depth
			}
			for j := i; ; j = p.Entries[j].Base {
				bytesSum += float64(p.Entries[j].End - p.Entries[j].Off)
				if !p.Entries[j].IsDelta() {
					break
				}
			}
			if _, err := p.Object(i, nil); err != nil {
				return err
			}
		}
		el := time.Since(t0)
		fmt.Printf("| %d | %.1f / %d | %.0f | %.0f |\n", d, depthSum/200, maxd, bytesSum/200, float64(el.Microseconds())/200)
		os.RemoveAll(filepath.Join(w, "rdd"))
	}
	_ = strings.TrimSpace
	return nil
}
