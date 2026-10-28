package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/wasmz3"
)

func init() {
	cmds["reads"] = readsCmd
	cmds["perf"] = perfCmd
}

func vmhwm() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "?"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "?"
}

func loadModule(p string) (*wasmz3.Module, []byte, error) {
	d, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
	return m, d, err
}

// reads FILE : bytes fetched (packed) to serve the whole module, one function body and a range
// inside it, under production CDC and under the indexed-reference representation.
func readsCmd(args []string) error {
	m, d, err := loadModule(args[0])
	if err != nil {
		return err
	}
	rep, err := wasmz3.Encode(m, d, wasmz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
	if err != nil {
		return err
	}
	mid := m.Bodies[len(m.Bodies)/2]
	best := mid
	for _, b := range m.Bodies[max(len(m.Bodies)/2-50, 0):min(len(m.Bodies)/2+50, len(m.Bodies))] {
		if b.End-b.Start > best.End-best.Start {
			best = b
		}
	}
	type span struct{ off, n, packed int64 }
	var chunks []span
	lab.ChunkCDC(bytes.NewReader(d), func(off int64, c []byte) error {
		chunks = append(chunks, span{off, int64(len(c)), lab.PackedLen(c)})
		return nil
	})
	cdcCost := func(off, n int64) (int64, int) {
		var tot int64
		k := 0
		for _, c := range chunks {
			if c.off < off+n && off < c.off+c.n {
				tot += c.packed
				k++
			}
		}
		return tot, k
	}
	type comp struct {
		start, end int
		packed     int64
	}
	var comps []comp
	st := 0
	for i, c := range rep.Comps {
		comps = append(comps, comp{st, rep.Cuts[i], lab.PackedLen(c)})
		st = rep.Cuts[i]
	}
	repCost := func(off, n int64, ctx bool) (int64, int) {
		a, z := int(off)-wasmz3.HeaderSize, int(off+n-1)-wasmz3.HeaderSize+1
		need := map[int]bool{}
		for i, c := range comps {
			if c.start < z && a < c.end {
				need[i] = true
			}
		}
		if ctx {
			// naming a callee needs the identity of every defined function: all bodies, plus the
			// type, import, global and data sections the other identities hash
			for _, s := range m.Dir.Secs {
				if s.ID == wasmz3.SecCustom {
					continue
				}
				for i, c := range comps {
					if c.start < int(s.End())-wasmz3.HeaderSize && int(s.Off)-wasmz3.HeaderSize < c.end {
						need[i] = true
					}
				}
			}
		}
		var tot int64
		for i := range need {
			tot += comps[i].packed
		}
		return tot, len(need)
	}
	fmt.Printf("%s: %d B, %d functions; %d CDC chunks, %d rep components\n", args[0], len(d), len(m.Bodies), len(chunks), len(comps))
	fmt.Printf("%-34s %22s %34s %34s\n", "request", "CDC (fetch, chunks)", "rep: data comps only", "rep: + identity context")
	rows := []struct {
		name   string
		off, n int64
	}{{"whole module", 0, int64(len(d))}, {fmt.Sprintf("one function (%d B)", mid.End-mid.SizeOff), mid.SizeOff, mid.End - mid.SizeOff}, {"128 B inside the largest nearby", best.Start + (best.End-best.Start)/2 - 64, 128}}
	for _, r := range rows {
		cf, ck := cdcCost(r.off, r.n)
		d1, n1 := repCost(r.off, r.n, false)
		d2, n2 := repCost(r.off, r.n, true)
		fmt.Printf("%-34s %14s (%3d) %20s (%4d) %20s (%4d)   amp %.0fx / %.0fx\n", r.name, econx.Kib(cf), ck, econx.Kib(d1), n1, econx.Kib(d2), n2, float64(cf)/float64(max(r.n, 1)), float64(d2)/float64(max(r.n, 1)))
	}
	return nil
}

// perf FILE... : parse / analyse / encode / decode timings and peak RSS of this process.
func perfCmd(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		t0 := time.Now()
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		if err != nil {
			return err
		}
		tParse := time.Since(t0)
		t1 := time.Now()
		m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
		if err != nil {
			return err
		}
		tWalk := time.Since(t1)
		t2 := time.Now()
		rep, err := wasmz3.Encode(m, d, wasmz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			return err
		}
		tEnc := time.Since(t2)
		t3 := time.Now()
		if _, err := wasmz3.Decode(rep.Meta, rep.Comps); err != nil {
			return err
		}
		tDec := time.Since(t3)
		mb := float64(len(d)) / (1 << 20)
		fmt.Printf("%s: %.1f MiB, %d slots | parse %v, walk %v (%.0f MiB/s), encode+verify %v | decode %v (%.1f MiB/s) | peak RSS %s\n", p, mb, len(m.Slots), tParse, tWalk, mb/tWalk.Seconds(), tEnc, tDec, mb/tDec.Seconds(), vmhwm())
	}
	return nil
}
