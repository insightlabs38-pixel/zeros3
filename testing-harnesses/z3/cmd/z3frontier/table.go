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
