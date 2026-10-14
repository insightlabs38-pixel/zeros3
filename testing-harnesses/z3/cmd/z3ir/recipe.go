package main

import (
	"flag"
	"fmt"
	path "path"
	"strings"

	"zeros3-z3/lab"
)

// recipeCmd: Z3-08H. Re-costs saved lab reports with real serialised recipe
// layouts (lab/recipe.go) instead of a flat per-reference constant.
func recipeCmd(args []string) {
	fs := flag.NewFlagSet("recipe", flag.ExitOnError)
	from := fs.String("from", "", "comma-separated lab reports written with -json (needs recipe_layout_bytes)")
	reps := fs.String("reps", "cdc,irp-tok4k,irp-tok16k,irp-tok64k", "representations")
	fams := fs.String("families", "", "family globs")
	perFam := fs.Bool("per-family", false, "also print one table per family group")
	fs.Parse(args)
	var rep *lab.Report
	for _, f := range split(*from) {
		r, err := lab.ReadJSON(f)
		if err != nil {
			fatal(err)
		}
		if rep == nil {
			rep = r
		} else {
			rep.Rows = append(rep.Rows, r.Rows...)
		}
	}
	if rep == nil {
		fatal(fmt.Errorf("-from is required"))
	}
	type key struct{ fam, rep string }
	incr := map[key]map[string]int64{}
	entry := map[key]map[string]int64{}
	logical := map[key]int64{}
	baseLogical := map[key]int64{}
	order := []string{}
	seen := map[string]bool{}
	for _, r := range rep.Rows {
		if r.Status != "ok" || r.Layouts == nil || r.IncrementalPacked == nil || (len(*fams) > 0 && !matchFam(split(*fams), r.Family)) {
			continue
		}
		k := key{r.Family, r.Rep}
		if !seen[r.Family] {
			seen[r.Family] = true
			order = append(order, r.Family)
		}
		packedNew := *r.IncrementalPacked - r.MetaBytes - r.IndexBytes
		dst := incr
		if r.Index == 0 {
			dst = entry
		}
		if dst[k] == nil {
			dst[k] = map[string]int64{}
		}
		for _, l := range lab.RecipeLayouts {
			dst[k][l] += packedNew + r.Layouts[l] + r.PlanMetaBytes + r.IndexBytes
		}
		if r.Index == 0 {
			baseLogical[k] += r.LogicalSize
		} else {
			logical[k] += r.LogicalSize
		}
	}
	print := func(title string, famSel func(string) bool) {
		fmt.Println(title)
		fmt.Printf("| representation |")
		for _, l := range lab.RecipeLayouts {
			fmt.Printf(" %s |", l)
		}
		fmt.Printf("\n|---|")
		for range lab.RecipeLayouts {
			fmt.Printf("---:|")
		}
		fmt.Println()
		for _, rp := range split(*reps) {
			var inc, ent map[string]int64 = map[string]int64{}, map[string]int64{}
			var lg, bl int64
			for _, f := range order {
				if !famSel(f) {
					continue
				}
				k := key{f, rp}
				for _, l := range lab.RecipeLayouts {
					inc[l] += incr[k][l]
					ent[l] += entry[k][l]
				}
				lg += logical[k]
				bl += baseLogical[k]
			}
			if lg == 0 {
				continue
			}
			fmt.Printf("| %s |", rp)
			for _, l := range lab.RecipeLayouts {
				fmt.Printf(" %s (entry %s) |", pct(inc[l], lg), pct(ent[l], bl))
			}
			fmt.Println()
		}
		fmt.Println()
	}
	print("Byte-weighted incremental cost over update revisions % of their compressed bytes (r0 entry cost in brackets), by recipe layout:", func(string) bool { return true })
	if *perFam {
		for _, f := range order {
			ff := f
			print("### "+strings.TrimPrefix(f, "extreal-"), func(s string) bool { return s == ff })
		}
	}
}

func matchFam(globs []string, f string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, f); ok {
			return true
		}
	}
	return false
}
