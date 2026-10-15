package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/gitz3"
)

// cmdBinhist re-exports real binary artifacts (fetched by cmd/z3fetch, SHA-256
// verified) as consecutive commits of one path. The artifact bytes are real; the
// repository history is synthetic, so this is a "synthetic re-export of real
// components", not evidence about an independent Git producer.
func cmdBinhist(args []string) error {
	var c common
	fs := flag.NewFlagSet("binhist", flag.ExitOnError)
	c.flags(fs)
	dir := fs.String("corpus", "", "directory written by z3fetch -group G -out DIR")
	group := fs.String("group", "rw-fdroid-apk", "family directory inside the corpus (e.g. rw-fdroid-apk, rw-pythia-14m)")
	n := fs.Int("n", 5, "number of revisions (first n artifacts, oldest first)")
	which := fs.String("scenarios", "small-rev,settings,level,chain", "scenarios")
	detail := fs.String("detail", "", "representations to break down")
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("-corpus required")
	}
	w, err := c.setup()
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(*dir, *group, "r*"))
	sort.Slice(files, func(i, j int) bool {
		a, b := filepath.Base(files[i]), filepath.Base(files[j])
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	if len(files) < *n {
		return fmt.Errorf("only %d artifacts in %s/%s", len(files), *dir, *group)
	}
	h := &gitz3.History{}
	prev := -1
	var total int64
	for _, f := range files[:*n] {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		total += int64(len(b))
		t := gitz3.Snap{"artifact" + filepath.Ext(f): b}
		if prev < 0 {
			prev = h.Add(t)
		} else {
			prev = h.Add(t, prev)
		}
	}
	r, err := gitz3.Build(w, "bh-"+*group, gitz3.SHA1, h)
	if err != nil {
		return err
	}
	cache := &gitz3.RealRepo{Repo: r}
	revs, err := cache.RevList("main", *n)
	if err != nil {
		return err
	}
	l := gitz3.NewLab(c.ref, c.packed)
	l.SectionK, l.Slack, l.InlineMax = c.sectionK, c.slack, c.inline
	fmt.Printf("%s\n\n## binary re-export: %s (%d artifacts, %.1f MiB total, oldest first; synthetic history of real artifacts)\n", gitz3.GitVersion(), *group, *n, float64(total)/(1<<20))
	return runScenarios(&c, l, cache, w, "bin-"+strings.TrimPrefix(*group, "rw-"), revs, *which, *detail)
}

func admitAPK(dir, w string, row func(string, gitz3.Algo, []byte, []byte) error) error {
	files, _ := filepath.Glob(filepath.Join(dir, "rw-fdroid-apk", "r*"))
	sort.Strings(files)
	h := &gitz3.History{}
	prev := -1
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		t := gitz3.Snap{"artifact" + filepath.Ext(f): b}
		if prev < 0 {
			prev = h.Add(t)
		} else {
			prev = h.Add(t, prev)
		}
	}
	r, err := gitz3.Build(w, "ad-apk", gitz3.SHA1, h, gitz3.SpecDefault)
	if err != nil {
		return err
	}
	pp, _ := r.PackPairs()
	return row("binary re-export: 5 F-Droid APKs", gitz3.SHA1, pp[0].Pack, pp[0].Idx)
}
