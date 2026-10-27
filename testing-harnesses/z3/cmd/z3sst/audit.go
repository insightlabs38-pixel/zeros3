package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/sstz3"
)

// cmdAudit is the format/code audit: per producer it aggregates what the tables of a
// corpus actually contain (footer family/version, checksum, classified features,
// compression mix by block and byte, properties of interest) and, with -deep, parses
// every block and compares entry counts with the producer's own properties.
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	deep := fs.Bool("deep", false, "decode every block and compare entry counts with the table properties")
	codecs := fs.Bool("codecs", false, "include the codec controls (tables the representation rejects)")
	fs.Parse(args)
	type agg struct {
		files, blocks   int
		bytes           int64
		sigs            map[string]int
		feats           map[string]int
		compBlocks      map[uint8]int
		compBytes       map[uint8]int64
		tailBytes       int64
		entries, propN  int64
		mismatch, perr  int
		props           map[string]map[string]int
		maxRestartCount int
	}
	byProducer := map[string]*agg{}
	for _, root := range fs.Args() {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".ldb") || strings.HasSuffix(p, ".sst")) || strings.Contains(p, "_other") || (!*codecs && strings.Contains(p, "/codecs/")) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			prod := strings.Split(rel, string(filepath.Separator))[0]
			a := byProducer[prod]
			if a == nil {
				a = &agg{sigs: map[string]int{}, feats: map[string]int{}, compBlocks: map[uint8]int{}, compBytes: map[uint8]int64{}, props: map[string]map[string]int{}}
				byProducer[prod] = a
			}
			f, _ := os.Open(p)
			defer f.Close()
			a.files++
			a.bytes += info.Size()
			t, err := sstz3.Open(f, info.Size(), sstz3.Limits{})
			if err != nil {
				a.perr++
				fmt.Printf("%s: %v\n", p, err)
				return nil
			}
			a.sigs[fmt.Sprintf("%s v%d cksum=%d", t.Footer.Family, t.Footer.Version, t.Footer.Checksum)]++
			for _, ft := range t.Features {
				d := ft.Detail
				if ft.Name == "data-blocks" {
					d = "contiguous"
				}
				a.feats[fmt.Sprintf("%s [%s] %s", ft.Name, ft.Class, d)]++
			}
			for _, k := range []string{"rocksdb.compression", "rocksdb.filter.policy", "rocksdb.comparator", "rocksdb.format.version", "rocksdb.index.key.is.user.key", "rocksdb.index.value.is.delta.encoded", "rocksdb.num.range-deletions", "rocksdb.num.data.blocks", "rocksdb.block.based.table.index.type", "rocksdb.data.block.index.type", "rocksdb.creating.db.identity", "rocksdb.merge.operator", "rocksdb.property.collectors"} {
				if k == "rocksdb.num.data.blocks" {
					continue
				}
				if v, ok := t.Props[k]; ok {
					s := string(v)
					if n, ok := t.PropUint(k); ok && len(v) < 5 {
						s = fmt.Sprint(n)
					}
					if len(s) > 40 {
						s = s[:40] + "…"
					}
					if a.props[k] == nil {
						a.props[k] = map[string]int{}
					}
					a.props[k][s]++
				}
			}
			a.blocks += len(t.Blocks)
			a.tailBytes += t.Size - t.DataEnd
			if t.Reject() != nil {
				return nil
			}
			var n int64
			for _, h := range t.Blocks {
				rb, err := sstz3.ReadBlock(f, t.Size, h, t.Footer.Checksum, true, sstz3.Limits{})
				if err != nil {
					a.perr++
					fmt.Printf("%s: %v\n", p, err)
					return nil
				}
				a.compBlocks[rb.Comp()]++
				a.compBytes[rb.Comp()] += int64(h.Size) + 5
				if !*deep {
					continue
				}
				dec, _, err := sstz3.DecodeBlock(rb, sstz3.Limits{})
				if err == nil {
					var lay *sstz3.Layout
					lay, err = sstz3.WalkBlock(dec, sstz3.Limits{}, nil)
					if lay != nil {
						n += int64(lay.Entries)
						a.maxRestartCount = max(a.maxRestartCount, len(lay.RestartIdx))
					}
				}
				if err != nil {
					a.perr++
					fmt.Printf("%s block@%d: %v\n", p, h.Off, err)
					return nil
				}
			}
			if *deep {
				a.entries += n
				if pn, ok := t.PropUint("rocksdb.num.entries"); ok {
					a.propN += int64(pn)
					if int64(pn) != n {
						a.mismatch++
						fmt.Printf("%s: parsed %d entries, properties say %d\n", p, n, pn)
					}
				}
			}
			return nil
		})
	}
	names := []string{"snappy", "none"}
	_ = names
	var prods []string
	for p := range byProducer {
		prods = append(prods, p)
	}
	sort.Strings(prods)
	for _, p := range prods {
		a := byProducer[p]
		fmt.Printf("== %s: %d tables, %.1f MiB, %d data blocks, tail (non-data) %.2f%% of bytes, parse errors %d\n", p, a.files, float64(a.bytes)/(1<<20), a.blocks, 100*float64(a.tailBytes)/float64(a.bytes), a.perr)
		for _, m := range []map[string]int{a.sigs, a.feats} {
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				fmt.Printf("   %5d  %s\n", m[k], k)
			}
		}
		var pk []string
		for k := range a.props {
			pk = append(pk, k)
		}
		sort.Strings(pk)
		for _, k := range pk {
			fmt.Printf("   prop %s = %v\n", k, a.props[k])
		}
		for c, n := range a.compBlocks {
			fmt.Printf("   compression type %d: %d blocks, %.1f MiB (%.1f%% of data-block bytes)\n", c, n, float64(a.compBytes[c])/(1<<20), 100*float64(a.compBytes[c])/float64(a.bytes-a.tailBytes))
		}
		if *deep {
			fmt.Printf("   entries parsed %d, properties %d, mismatching tables %d, max restarts/block %d\n", a.entries, a.propN, a.mismatch, a.maxRestartCount)
		}
	}
	return nil
}
