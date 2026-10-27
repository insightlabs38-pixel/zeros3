package main

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"zeros3-z3/dexz3"
	"zeros3-z3/lab"
)

func init() { cmds["dexsec"] = dexSec }

// dexsec OLD NEW CUTTER: packed bytes of the new components of NEW, by the section the component starts in.
func dexSec(args []string) error {
	cut := unitCut(4)
	enc := func(p string) (*dexz3.Rep, error) {
		f, b, err := loadDex(p)
		if err != nil {
			return nil, err
		}
		return dexz3.Encode(f, b, dexz3.Config{Cut: cut.Cut})
	}
	a, err := enc(args[0])
	if err != nil {
		return err
	}
	b, err := enc(args[1])
	if err != nil {
		return err
	}
	have := map[[32]byte]bool{}
	for _, c := range a.Comps {
		have[sha256.Sum256(c)] = true
	}
	names := map[uint16]string{0: "header", 1: "string_id", 2: "type_id", 3: "proto_id", 4: "field_id", 5: "method_id", 6: "class_def", 0x1001: "type_list", 0x1002: "anno_set_ref", 0x1003: "anno_set", 0x2000: "class_data", 0x2001: "code", 0x2002: "string_data", 0x2003: "debug_info", 0x2004: "annotation", 0x2005: "enc_array", 0x2006: "anno_dir", 0xFFFF: "gap"}
	tot := map[string]int64{}
	cnt := map[string]int{}
	all := map[string]int64{}
	start := 0
	for i, c := range b.Comps {
		end := b.Cuts[i]
		sec := "?"
		for _, s := range b.Secs {
			if start >= s.Off && start < s.End {
				sec = names[s.Type]
			}
		}
		all[sec] += int64(len(c))
		if !have[sha256.Sum256(c)] {
			tot[sec] += lab.PackedLen(c)
			cnt[sec]++
		}
		start = end
	}
	var keys []string
	for k := range all {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return tot[keys[i]] > tot[keys[j]] })
	var sum int64
	for _, k := range keys {
		sum += tot[k]
	}
	fmt.Printf("new packed total %d\n", sum)
	for _, k := range keys {
		fmt.Printf("   %-13s new packed %9d (%4.1f%%)  new comps %5d  section bytes %9d\n", k, tot[k], 100*float64(tot[k])/float64(sum), cnt[k], all[k])
	}
	return nil
}
