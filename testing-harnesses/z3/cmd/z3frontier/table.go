package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/frontier"
)

type results map[string]map[string]map[string]any // lane -> rev -> record

func loadResults(path string) results {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	res := results{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r map[string]any
		must(json.Unmarshal(sc.Bytes(), &r))
		lane, rev := r["lane"].(string), r["rev"].(string)
		if res[lane] == nil {
			res[lane] = map[string]map[string]any{}
		}
		res[lane][rev] = r
	}
	return res
}

// get follows a dotted path ("cdc.incr") through a decoded record.
func get(r map[string]any, path string) (float64, bool) {
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

func human(v float64) string {
	switch {
	case v >= 10<<20:
		return fmt.Sprintf("%.1fM", v/(1<<20))
	case v >= 10<<10:
		return fmt.Sprintf("%.0fK", v/(1<<10))
	}
	return fmt.Sprintf("%.0f", v)
}

// printRows prints one line per "label=lane:path" with per-revision values and the chain sum.
func printRows(res results, revs []string, rows []string) {
	fmt.Printf("%-26s", "")
	for _, r := range revs {
		fmt.Printf("%8s", r)
	}
	fmt.Printf("%9s\n", "cum")
	for _, row := range rows {
		label, spec, _ := strings.Cut(row, "=")
		lane, path, _ := strings.Cut(spec, ":")
		fmt.Printf("%-26s", label)
		var sum float64
		for _, r := range revs {
			v, ok := get(res[lane][r], path)
			if !ok {
				fmt.Printf("%8s", "-")
				continue
			}
			sum += v
			fmt.Printf("%8s", human(v))
		}
		fmt.Printf("%9s\n", human(sum))
	}
}

func cmdC23Sum(args []string) {
	fs := flag.NewFlagSet("c23sum", flag.ExitOnError)
	in := fs.String("in", "/root/z3scratch/c23.jsonl", "results")
	fs.Parse(args)
	res := loadResults(*in)
	printRows(res, frontier.Revisions, []string{
		"FLOOR total (oracle)=floor:total", "  floor file chunks=floor:file_packed", "  floor manifest=floor:manifest", "whole-file objects=floor:obj_bytes",
		"tar CDC=tar:cdc.incr", "tar structure CDC=tar:struct.incr",
		"tar.gz size=targz:size", "tar.gz CDC=targz:cdc.incr",
		"tar.zst size=tarzst:size", "tar.zst CDC=tarzst:cdc.incr", "  native blocks new B=tarzst:blockset.NewBytes",
		"estargz size=estargz:size", "estargz CDC=estargz:cdc.incr", "  gzip members new B=estargz:memberset.NewBytes", "  chunk-digest new B=estargz:chunk_new_bytes", "  TOC bytes=estargz:toc_bytes",
		"zstd:chunked size=zstdchunked:size", "zstd:chunked CDC=zstdchunked:cdc.incr", "  frames new B=zstdchunked:frameset.NewBytes", "  frameset incr=zstdchunked:frameset.Incr", "  chunk-digest new B=zstdchunked:chunk_new_bytes", "  TOC bytes=zstdchunked:toc_bytes",
		"nydus blob=nydus:size", "nydus blob CDC=nydus:cdc.incr", "  bootstrap CDC=nydus:boot_cdc.incr", "  chunk new B=nydus:chunk_new_bytes",
	})
	fmt.Println("\nreproducible R0 (two producer runs):")
	for _, l := range []string{"tarzst", "estargz", "zstdchunked", "nydus"} {
		fmt.Printf("  %-12s %v\n", l, res[l]["R0"]["repro"])
	}
	fmt.Println("\npoint reads at R0 (native physical bytes / regions / production-CDC chunk cover):")
	for _, l := range []string{"tar", "tarzst", "estargz", "zstdchunked", "nydus"} {
		for _, k := range []string{"read_tiny", "read_large"} {
			if m, ok := res[l]["R0"][k].(map[string]any); ok {
				fmt.Printf("  %-12s %-10s native=%-9.0f regions=%.0f cdc_cover=%-9.0f", l, k, m["native"], m["regions"], m["cdc_cover"])
				if b, ok := m["boot_est"]; ok {
					fmt.Printf(" bootstrap~%.0f of %.0f", b, m["boot_total"])
				}
				fmt.Println()
			}
		}
	}
}

func fnum(m map[string]any, k string) float64 { v, _ := m[k].(float64); return v }

func sub(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }

// cmdI24Sum prints the Z3-24 tables from the JSONL produced by `i24`.
func cmdI24Sum(args []string) {
	fs := flag.NewFlagSet("i24sum", flag.ExitOnError)
	in := fs.String("in", "/root/z3scratch/i24.jsonl", "results")
	fs.Parse(args)
	res := loadResults(*in)
	// derived composefs rows
	for rev, r := range res["cfs"] {
		r["cfs_combined"] = fnum(sub(r, "cdc"), "incr") + fnum(r, "obj_new_bytes")
		r["cfs_combined_cdc"] = fnum(sub(r, "cdc"), "incr") + fnum(r, "obj_cdc_packed") + frontier.RefBytes*fnum(r, "obj_chunks")
		res["cfs"][rev] = r
	}
	revs := frontier.Revisions
	fmt.Println("== per-revision incremental bytes (K/M) ==")
	printRows(res, revs, []string{
		"FLOOR file+manifest (oracle)=floor:total", "whole-file objects (oracle)=floor:obj_bytes",
		"squashfs image=sq:size", "sq CDC raw image=sq:cdc.incr", "sq native blocks=sq:native.incr", "  sq native data=sq:native.new_by_class.data", "  sq native tail/frag=sq:native.new_by_class.tail", "  sq native inode=sq:native.new_by_class.inode", "  sq native dir=sq:native.new_by_class.dir", "  sq native index+super=sq:native.new_by_class.index", "  sq native pad=sq:native.new_by_class.pad",
		"erofs image=er:size", "er CDC raw image=er:cdc.incr", "er native units=er:native.incr", "  er native data=er:native.new_by_class.data", "  er native tail/frag=er:native.new_by_class.tail", "  er native inode=er:native.new_by_class.inode", "  er native dir=er:native.new_by_class.dir", "  er native index+super=er:native.new_by_class.index", "  er native pad=er:native.new_by_class.pad",
		"sqraw CDC=sqraw:cdc.incr", "sqraw native=sqraw:native.incr", "erraw CDC=erraw:cdc.incr", "erraw native=erraw:native.incr", "erchunk CDC=erchunk:cdc.incr", "erchunk native=erchunk:native.incr",
		"cfs metadata image CDC=cfs:cdc.incr", "cfs new objects (raw)=cfs:obj_new_bytes", "cfs new objects (count)=cfs:obj_new", "cfs combined native=cfs:cfs_combined", "cfs combined, objects via CDC=cfs:cfs_combined_cdc",
	})
	fmt.Println("\n== CDC cost attribution by image region (packed bytes of new chunks) ==")
	for _, l := range []string{"sq", "er"} {
		printRows(res, revs, []string{
			l + " CDC data=" + l + ":cdc_class.data", l + " CDC tail/frag=" + l + ":cdc_class.tail", l + " CDC inode=" + l + ":cdc_class.inode", l + " CDC dir=" + l + ":cdc_class.dir", l + " CDC index+super=" + l + ":cdc_class.index", l + " CDC pad=" + l + ":cdc_class.pad",
		})
	}
	fmt.Println("\n== tail / fragment variants (chains R0,R6,R8): image size, native incr, CDC incr ==")
	for _, l := range []string{"sq", "sqnf", "sqtail", "er", "ernf", "erall"} {
		fmt.Printf("  %-7s", l)
		for _, r := range []string{"R0", "R6", "R8"} {
			m := res[l][r]
			if m == nil {
				fmt.Printf("  %s: -", r)
				continue
			}
			fmt.Printf("  %s: size=%s native=%s cdc=%s tail-new=%s", r, human(fnum(m, "size")), human(fnum(sub(m, "native"), "incr")), human(fnum(sub(m, "cdc"), "incr")), human(fnum(sub(sub(m, "native"), "new_by_class"), "tail")))
		}
		fmt.Println()
	}
	fmt.Println("\n== threshold files (R0 -> R8) ==")
	for _, l := range []string{"sq", "sqnf", "er", "ernf"} {
		for _, r := range []string{"R0", "R8"} {
			if t := sub(res[l][r], "thr"); t != nil {
				fmt.Printf("  %-5s %s", l, r)
				for _, n := range []string{"thr/t3_0.dat", "thr/t4_0.dat"} {
					fmt.Printf("  %s=%v", strings.TrimPrefix(n, "thr/"), t[n])
				}
				fmt.Println()
			}
		}
	}
	fmt.Println("\n== sparse file (big/sparse.img, 72 MiB logical, 5.25 MiB data), R0 and R9 ==")
	for _, l := range []string{"sq", "sqraw", "er", "erraw", "erchunk"} {
		for _, r := range []string{"R0", "R9"} {
			fmt.Printf("  %-8s %s sparse=%v  cdc_incr=%s\n", l, r, sub(res[l][r], "sparse"), human(fnum(sub(res[l][r], "cdc"), "incr")))
		}
	}
	fmt.Println("\n== point reads at R0 (native bytes: meta+data, regions, CDC chunk cover) ==")
	for _, l := range []string{"sq", "sqraw", "er", "erraw", "erchunk"} {
		for _, k := range []string{"read_tiny", "read_large", "read_dir"} {
			if m := sub(res[l]["R0"], k); m != nil {
				fmt.Printf("  %-8s %-10s native=%-8.0f (meta %-6.0f data %-8.0f) regions=%-3.0f cdc_cover=%.0f\n", l, k, m["native"], m["meta_bytes"], m["data_bytes"], m["regions"], m["cdc_cover"])
			}
		}
	}
	fmt.Println("\n== reproducibility (R0 twice) / verification (extract + compare, all revisions) ==")
	for _, l := range []string{"sq", "sqraw", "er", "erraw", "erchunk", "cfs", "sqnf", "sqtail", "ernf", "erall", "sq9", "er9", "ertar"} {
		bad, tot, patched := 0, 0, 0.0
		for _, r := range revs {
			if m := res[l][r]; m != nil {
				tot++
				if v := sub(m, "verify"); v == nil || v["ok"] != true {
					bad++
				} else {
					patched += fnum(v, "cat_verified")
				}
			}
		}
		fmt.Printf("  %-7s repro=%v verified %d/%d revisions (cat-verified files: %.0f)\n", l, res[l]["R0"]["repro"], tot-bad, tot, patched)
	}
	fmt.Println("\n== chain totals (R1-R10 excludes the first sight R0 and the unrelated control R11) ==")
	mid := revs[1:11]
	sum := func(lane, path string, rs []string) float64 {
		var t float64
		for _, r := range rs {
			v, _ := get(res[lane][r], path)
			t += v
		}
		return t
	}
	flAll, flMid := sum("floor", "total", revs), sum("floor", "total", mid)
	for _, row := range [][3]string{{"sq CDC", "sq", "cdc.incr"}, {"sq native", "sq", "native.incr"}, {"er CDC", "er", "cdc.incr"}, {"er native", "er", "native.incr"},
		{"sqraw CDC", "sqraw", "cdc.incr"}, {"erraw CDC", "erraw", "cdc.incr"}, {"erchunk CDC", "erchunk", "cdc.incr"},
		{"cfs metadata image CDC", "cfs", "cdc.incr"}, {"cfs combined native (raw objects)", "cfs", "cfs_combined"}, {"cfs combined (objects via CDC)", "cfs", "cfs_combined_cdc"}} {
		all, m := sum(row[1], row[2], revs), sum(row[1], row[2], mid)
		fmt.Printf("  %-36s cum=%-8s x%.2f   R1-R10=%-8s x%.2f\n", row[0], human(all), all/flAll, human(m), m/flMid)
	}
	fmt.Printf("  FLOOR cum=%s  R1-R10=%s\n", human(flAll), human(flMid))
	for _, l := range []string{"sq", "er"} {
		fmt.Printf("  %s native new bytes by class over R1-R10:", l)
		for _, c := range []string{"data", "tail", "inode", "dir", "index", "pad"} {
			fmt.Printf(" %s=%s", c, human(sum(l, "native.new_by_class."+c, mid)))
		}
		fmt.Println()
	}
}
