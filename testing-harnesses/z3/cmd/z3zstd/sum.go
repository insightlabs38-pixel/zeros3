package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/frontier"
)

// row is one reported representation: a sum of dotted-path fields of each revision record.
type row struct {
	label string
	parts []string
}

func loadJSONL(path string) map[string]map[string]any {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	out := map[string]map[string]any{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r map[string]any
		must(json.Unmarshal(sc.Bytes(), &r))
		out[r["rev"].(string)] = r
	}
	return out
}

func getp(r map[string]any, path string) (float64, bool) {
	var cur any = r
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		if cur, ok = m[p]; !ok {
			return 0, false
		}
	}
	v, ok := cur.(float64)
	return v, ok
}

func hum(v float64) string {
	switch {
	case v >= 10<<20:
		return fmt.Sprintf("%.1fM", v/(1<<20))
	case v >= 10<<10:
		return fmt.Sprintf("%.0fK", v/(1<<10))
	}
	return fmt.Sprintf("%.0f", v)
}

// series returns the per-revision value of a row (nil entries are missing).
func (rw row) series(eco, floor map[string]map[string]any, revs []string) []float64 {
	out := make([]float64, len(revs))
	for i, rev := range revs {
		for _, p := range rw.parts {
			var v float64
			var ok bool
			if strings.HasPrefix(p, "floor:") {
				v, ok = getp(floor[rev], strings.TrimPrefix(p, "floor:"))
			} else {
				v, ok = getp(eco[rev], p)
			}
			if !ok {
				out[i] = -1
				break
			}
			out[i] += v
		}
	}
	return out
}

// stdRows are the Z3-25 candidate representations.
func stdRows() []row {
	return []row{
		{"FLOOR (oracle)", []string{"floor:total"}},
		{"A  CDC of tar.zst", []string{"A_cdc.incr"}},
		{"B  native zstd blocks", []string{"B_native.Incr"}},
		{"C  exact+tar CDC / units CDC", []string{"C_plain.incr", "IR_skel.incr", "IR_useq.incr"}},
		{"C' exact+tar CDC / units native", []string{"C_plain.incr", "IR_skel.incr", "IR_unative.Incr"}},
		{"D  exact+structural tar / units CDC", []string{"D_struct.incr", "D_pay.Incr", "IR_skel.incr", "IR_useq.incr"}},
		{"F  IR alone (units CDC)", []string{"IR_skel.incr", "IR_useq.incr"}},
		{"   plain tar CDC", []string{"C_plain.incr"}},
		{"   structural tar", []string{"D_struct.incr", "D_pay.Incr"}},
		{"   IR skeleton", []string{"IR_skel.incr"}},
		{"   IR units CDC", []string{"IR_useq.incr"}},
		{"   IR units native", []string{"IR_unative.Incr"}},
	}
}

func cmdEcoSum(args []string) {
	fs := flag.NewFlagSet("ecosum", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "gen output directory")
	tag := fs.String("tag", "z155l3", "producer tag")
	fs.Parse(args)
	eco := loadJSONL(filepath.Join(*dir, "eco."+*tag+".jsonl"))
	floor := loadJSONL(filepath.Join(*dir, "floor.jsonl"))
	var revs []string
	for _, r := range frontier.Revisions {
		if _, ok := eco[r]; ok {
			revs = append(revs, r)
		}
	}
	fmt.Printf("%-38s", "")
	for _, r := range revs {
		fmt.Printf("%8s", r)
	}
	fmt.Printf("%9s%9s%9s\n", "R0", "R1-R10", "cum")
	rows := stdRows()
	var floorS []float64
	for i, rw := range rows {
		s := rw.series(eco, floor, revs)
		if i == 0 {
			floorS = s
		}
		fmt.Printf("%-38s", rw.label)
		var cum, mid float64
		for j, v := range s {
			if v < 0 {
				fmt.Printf("%8s", "-")
				continue
			}
			fmt.Printf("%8s", hum(v))
			cum += v
			if revs[j] != "R0" && revs[j] != "R11" {
				mid += v
			}
		}
		fmt.Printf("%9s%9s%9s", hum(s[0]), hum(mid), hum(cum))
		if i > 0 {
			var fm, fc float64
			for j, v := range floorS {
				fc += v
				if revs[j] != "R0" && revs[j] != "R11" {
					fm += v
				}
			}
			fmt.Printf("   x floor: R1-10 %.2f cum %.2f", mid/fm, cum/fc)
		}
		fmt.Println()
	}
}
