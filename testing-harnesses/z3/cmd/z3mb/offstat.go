package main

import (
	"fmt"
	"sort"

	"zeros3-z3/dexz3"
)

func init() { cmds["offstat"] = offStat }

// offStat: per offset kind, how often does the target item follow the previous target in item order?
func offStat(args []string) error {
	for _, p := range args {
		f, b, err := loadDex(p)
		if err != nil {
			return err
		}
		w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			return err
		}
		// item ordinal by (type, offset)
		type k struct {
			t   uint16
			off int64
		}
		ord := map[k]int{}
		cnt := map[uint16]int{}
		start := map[int64]uint16{}
		for _, it := range w.Items {
			ord[k{it.Type, it.Off}] = cnt[it.Type]
			cnt[it.Type]++
			start[it.Off] = it.Type
		}
		for i := range w.StrOff {
			ord[k{dexz3.TypeStringData, int64(w.StrOff[i])}] = i
			start[int64(w.StrOff[i])] = dexz3.TypeStringData
		}
		type kind struct {
			cls dexz3.Class
			at  int64 // field position class (for classdef/idtab) else 0
		}
		prev := map[string]int{}
		stat := map[string]map[int]int{}
		bad := map[string]int{}
		for _, s := range w.Slots {
			if s.Space != dexz3.SpOffset {
				continue
			}
			t, ok := start[int64(s.Val)]
			name := fmt.Sprintf("%s", s.Cls)
			// distinguish the field inside records
			switch s.Cls {
			case dexz3.ClIDTab:
				if t == dexz3.TypeStringData {
					name += "/string"
				} else {
					name += "/proto-params"
				}
			case dexz3.ClClassDef:
				name += fmt.Sprintf("/%#x", t)
			case dexz3.ClAnno:
				name += fmt.Sprintf("/%#x", t)
			}
			if !ok {
				bad[name]++
				continue
			}
			o := ord[k{t, int64(s.Val)}]
			if _, seen := prev[name]; !seen {
				prev[name] = -1
			}
			d := o - prev[name]
			prev[name] = o
			if stat[name] == nil {
				stat[name] = map[int]int{}
			}
			if d > 4 || d < -4 {
				d = 99
			}
			stat[name][d]++
		}
		fmt.Println(p)
		var names []string
		for n := range stat {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			tot := 0
			for _, c := range stat[n] {
				tot += c
			}
			fmt.Printf("   %-22s n=%7d  +1: %5.1f%%  0: %5.1f%%  other: %5.1f%%  bad %d\n", n, tot, 100*float64(stat[n][1])/float64(tot), 100*float64(stat[n][0])/float64(tot), 100*float64(tot-stat[n][1]-stat[n][0])/float64(tot), bad[n])
		}
		for n, c := range bad {
			if stat[n] == nil {
				fmt.Printf("   %-22s bad %d\n", n, c)
			}
		}
	}
	return nil
}
