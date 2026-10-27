package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"zeros3-z3/jvmz3"
)

func init() { cmds["jvmstat"] = jvmStat }

// jarClasses returns the inflated class members of a jar (analysis only).
func jarClasses(path string) (names []string, data [][]byte, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		names = append(names, f.Name)
		data = append(data, b)
	}
	return
}

func jvmStat(args []string) error {
	for _, p := range args {
		names, data, err := jarClasses(p)
		if err != nil {
			return err
		}
		var tot, fails int
		cls := map[jvmz3.Class]int{}
		clsB := map[jvmz3.Class]int{}
		attrs := map[string]*jvmz3.AttrStat{}
		var slots, methods int
		var code int64
		errs := map[string]int{}
		for i, b := range data {
			c, _, err := jvmz3.Parse(bytes.NewReader(b), int64(len(b)), jvmz3.Limits{})
			if err != nil {
				fails++
				e := err.Error()
				if len(e) > 60 {
					e = e[:60]
				}
				errs[e]++
				_ = names[i]
				continue
			}
			tot += len(b)
			slots += len(c.Slots)
			methods += c.Methods
			code += c.Code
			for _, s := range c.Slots {
				cls[s.Cls]++
				clsB[s.Cls] += int(s.Len)
			}
			for n, a := range c.Attrs {
				if attrs[n] == nil {
					attrs[n] = &jvmz3.AttrStat{Name: n, Deep: a.Deep}
				}
				attrs[n].Count += a.Count
				attrs[n].Bytes += a.Bytes
			}
		}
		fmt.Printf("%s: %d classes, %d ok bytes, %d failed, slots %d methods %d code %d\n", p, len(data), tot, fails, slots, methods, code)
		for e, n := range errs {
			fmt.Printf("   fail %q x%d\n", e, n)
		}
		for c := jvmz3.Class(0); c < 10; c++ {
			fmt.Printf("   %-9s slots %8d bytes %8d (%.1f%%)\n", c, cls[c], clsB[c], 100*float64(clsB[c])/float64(tot))
		}
		var ns []string
		for n := range attrs {
			ns = append(ns, n)
		}
		sort.Slice(ns, func(i, j int) bool { return attrs[ns[i]].Bytes > attrs[ns[j]].Bytes })
		for _, n := range ns {
			a := attrs[n]
			fmt.Printf("   attr %-36s n=%7d bytes %9d (%.1f%%) deep=%v\n", n, a.Count, a.Bytes, 100*float64(a.Bytes)/float64(tot), a.Deep)
		}
	}
	return nil
}
