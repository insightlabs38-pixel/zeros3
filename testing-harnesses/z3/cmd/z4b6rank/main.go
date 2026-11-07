// z4b6rank: cumulative retained-history cost of a directory tree OUT/<family>/<version>/<view>/<files>
// under several chunkers (B6 ranking). Versions are sorted naturally by the order of versions.txt if present.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"zeros3-z3/b6cost"
)

type row struct {
	Family, View, Chunker string
	Versions              int
	Files                 int
	Raw, Objs, Pack       int64
	FirstSight            int64
	PerVersion            []int64
}

func natLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, e1 := strconv.Atoi(pa[i])
		y, e2 := strconv.Atoi(pb[i])
		if e1 == nil && e2 == nil && x != y {
			return x < y
		}
		if pa[i] != pb[i] && (e1 != nil || e2 != nil) {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func main() {
	root := os.Args[1]
	var rows []row
	fams, _ := os.ReadDir(root)
	for _, f := range fams {
		if !f.IsDir() {
			continue
		}
		vdirs, _ := os.ReadDir(filepath.Join(root, f.Name()))
		var vers []string
		for _, v := range vdirs {
			if v.IsDir() {
				vers = append(vers, v.Name())
			}
		}
		sort.Slice(vers, func(i, j int) bool { return natLess(vers[i], vers[j]) })
		views := map[string]bool{}
		if len(vers) > 0 {
			vs, _ := os.ReadDir(filepath.Join(root, f.Name(), vers[0]))
			for _, v := range vs {
				views[v.Name()] = true
			}
		}
		for view := range views {
			for _, ch := range []b6cost.Chunker{b6cost.Whole, b6cost.ProdCDC, b6cost.FineCDC, b6cost.Fine2CDC} {
				st := b6cost.NewStore()
				r := row{Family: f.Name(), View: view, Chunker: ch.Name, Versions: len(vers)}
				for i, v := range vers {
					files, _ := os.ReadDir(filepath.Join(root, f.Name(), v, view))
					var inc int64
					for _, fe := range files {
						b, err := os.ReadFile(filepath.Join(root, f.Name(), v, view, fe.Name()))
						if err != nil {
							panic(err)
						}
						inc += st.Add(ch, b)
						if i == 0 {
							r.Files++
						}
					}
					if i == 0 {
						r.FirstSight = inc
					}
					r.PerVersion = append(r.PerVersion, inc)
				}
				r.Raw, r.Objs, r.Pack = st.Raw, int64(st.Objs), st.Pack
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.View != b.View {
			return a.View < b.View
		}
		return a.Chunker < b.Chunker
	})
	if len(os.Args) > 2 && os.Args[2] == "json" {
		json.NewEncoder(os.Stdout).Encode(rows)
		return
	}
	for _, r := range rows {
		fmt.Printf("%-32s %-5s %-14s v=%2d files=%3d raw=%10d pack=%10d (%.3f of raw) objs=%6d first=%d\n", r.Family, r.View, r.Chunker, r.Versions, r.Files, r.Raw, r.Pack, float64(r.Pack)/float64(r.Raw), r.Objs, r.FirstSight)
	}
}
