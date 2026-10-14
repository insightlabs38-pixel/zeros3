package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

// lifecycleCmd: Z3-08I. For every family with several revisions, the cumulative
// physical cost of keeping CDC, transforming at once, or transforming only
// after the 1st/2nd update, with the break-even revision count.
func lifecycleCmd(args []string) {
	fs := flag.NewFlagSet("lifecycle", flag.ExitOnError)
	from := fs.String("from", "", "comma-separated lab reports (with recipe_layout_bytes)")
	zrep := fs.String("rep", "irp-tok16k", "transformed representation")
	layout := fs.String("layout", "bin32", "recipe layout used for both (cdc and the transform)")
	minRevs := fs.Int("min-revs", 4, "families with fewer revisions are skipped")
	fams := fs.String("families", "", "family globs")
	verbose := fs.Bool("v", false, "print the cumulative curves")
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
	type rev struct{ size, c, z int64 }
	byFam := map[string]map[int]*rev{}
	var order []string
	for _, r := range rep.Rows {
		if r.Status != "ok" || r.Layouts == nil || r.IncrementalPacked == nil || (len(*fams) > 0 && !matchFam(split(*fams), r.Family)) {
			continue
		}
		if r.Rep != "cdc" && r.Rep != *zrep {
			continue
		}
		if byFam[r.Family] == nil {
			byFam[r.Family] = map[int]*rev{}
			order = append(order, r.Family)
		}
		rv := byFam[r.Family][r.Index]
		if rv == nil {
			rv = &rev{size: r.LogicalSize}
			byFam[r.Family][r.Index] = rv
		}
		cost := *r.IncrementalPacked - r.MetaBytes - r.IndexBytes + r.Layouts[*layout] + r.PlanMetaBytes + r.IndexBytes
		if r.Rep == "cdc" {
			rv.c = cost
		} else {
			rv.z = cost
		}
	}
	sort.Strings(order)
	fmt.Printf("Cumulative physical bytes as %% of the compressed sizes of r0..rk (recipe layout %s, transform %s).\n", *layout, *zrep)
	fmt.Println("Policies: CDC = never transform; Z = transform every revision; Z@1 / Z@2 = CDC for r0..r1 / r0..r2, then the transform (its first object pays the entry cost, later ones the measured incremental cost).")
	fmt.Println()
	fmt.Println("| family | revs | all-CDC % | Z-immediately % | Z after 1st update % | Z after 2nd update % | break-even k: Z / Z@1 / Z@2 | r0 entry Z % | steady Z incr % |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---|---:|---:|")
	var sumC, sumZ, sumZ1, sumZ2, sumSize int64
	for _, fam := range order {
		rv := byFam[fam]
		n := len(rv)
		if n < *minRevs {
			continue
		}
		var size, c, z, z1, z2 []int64
		var cc, zc, z1c, z2c, sc int64
		ok := true
		entry0 := float64(rv[0].z) / float64(rv[0].size)
		for i := 0; i < n; i++ {
			x := rv[i]
			if x == nil || x.c == 0 && x.z == 0 {
				ok = false
				break
			}
			cc += x.c
			zc += x.z
			sc += x.size
			// Z after the 1st update: r0, r1 are CDC; r2 pays a transform entry; later ones the measured incremental cost
			switch {
			case i < 2:
				z1c += x.c
			case i == 2:
				z1c += int64(entry0 * float64(x.size))
			default:
				z1c += x.z
			}
			switch {
			case i < 3:
				z2c += x.c
			case i == 3:
				z2c += int64(entry0 * float64(x.size))
			default:
				z2c += x.z
			}
			size, c, z, z1, z2 = append(size, sc), append(c, cc), append(z, zc), append(z1, z1c), append(z2, z2c)
		}
		if !ok {
			continue
		}
		be := func(a []int64) string {
			for k := range a {
				if a[k] < c[k] {
					return fmt.Sprint(k)
				}
			}
			return "never"
		}
		last := n - 1
		fmt.Printf("| %s | %d | %s | %s | %s | %s | %s / %s / %s | %.0f | %s |\n", strings.TrimPrefix(fam, "extreal-"), n, pct(c[last], size[last]), pct(z[last], size[last]),
			pct(z1[last], size[last]), pct(z2[last], size[last]), be(z), be(z1), be(z2), 100*entry0, pct(rv[last].z, rv[last].size))
		sumC += c[last]
		sumZ += z[last]
		sumZ1 += z1[last]
		sumZ2 += z2[last]
		sumSize += size[last]
		if *verbose {
			for k := range c {
				fmt.Printf("|  | k=%d | %s | %s | %s | %s |||\n", k, pct(c[k], size[k]), pct(z[k], size[k]), pct(z1[k], size[k]), pct(z2[k], size[k]))
			}
		}
	}
	fmt.Printf("\nByte-weighted over these families: CDC %s, Z %s, Z@1 %s, Z@2 %s (percent of all revisions' compressed bytes).\n",
		pct(sumC, sumSize), pct(sumZ, sumSize), pct(sumZ1, sumSize), pct(sumZ2, sumSize))
}
