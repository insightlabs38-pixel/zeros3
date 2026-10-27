package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

// cmdIsolation shows which column components a change touches: the new packed bytes by
// component class (per column) when a variant is added to a store holding r0.
func cmdIsolation(args []string) error {
	sts, _, err := loadFamily("pyarrow-variants")
	if err != nil {
		return err
	}
	by := map[string]state{}
	for _, s := range sts {
		by[s.Rev] = s
	}
	opt := parquetz3.Options{ColumnClasses: true}
	have := map[parquetz3.ID]bool{}
	enc := func(s state, fn func(class string, id parquetz3.ID, b []byte)) error {
		data, err := readFile(s.Path)
		if err != nil {
			return err
		}
		_, _, _, err = parquetz3.Encode(bytes.NewReader(data), int64(len(data)), opt, func(class string, id parquetz3.ID, b []byte) error { fn(class, id, b); return nil })
		return err
	}
	if err := enc(by["r0"], func(_ string, id parquetz3.ID, _ []byte) { have[id] = true }); err != nil {
		return err
	}
	fmt.Println("| variant | new components by class (packed bytes; node = stream index nodes, frame/crec/dict = per-file metadata) |")
	fmt.Println("|---|---|")
	for _, rev := range []string{"m01-update-one-value", "m02-update-100-sparse", "m09-one-column-recomputed", "m17-nulls-moved", "m19-wholesale-payload", "m18-dictionary-order", "m06-row-group-100k"} {
		seen := map[parquetz3.ID]bool{}
		by2 := map[string]int64{}
		if err := enc(by[rev], func(class string, id parquetz3.ID, b []byte) {
			if have[id] || seen[id] {
				return
			}
			seen[id] = true
			by2[class] += lab.PackedSize(b)
		}); err != nil {
			return err
		}
		var parts []string
		var ks []string
		for k := range by2 {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("%s %s", k, human(by2[k])))
		}
		fmt.Printf("| %s | %s |\n", rev, strings.Join(parts, "; "))
	}
	return nil
}
