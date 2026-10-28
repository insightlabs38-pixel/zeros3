package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
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

// reads FILE : bytes fetched (packed) to serve the whole assembly, one method body and a small
// range inside it under production CDC and under the indexed-reference representation.
func readsCmd(args []string) error {
	d, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
	if err != nil {
		return err
	}
	cfg := clrz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut}
	rep, err := clrz3.Encode(f, d, cfg)
	if err != nil {
		return err
	}
	bodies := clrz3.FindBodies(f, d, false)
	// a body with several tokens near the middle, and the largest body near it for the range
	mid := bodies[len(bodies)/2]
	for i := len(bodies) / 2; i < len(bodies); i++ {
		if len(bodies[i].Body.Toks) >= 4 && !bodies[i].Opaque {
			mid = bodies[i]
			break
		}
	}
	best := mid
	for _, b := range bodies[max(len(bodies)/2-100, 0):min(len(bodies)/2+100, len(bodies))] {
		if !b.Opaque && b.Body.End-b.Body.Off > best.Body.End-best.Body.Off {
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
		a, z := f.PayloadOffset(off), f.PayloadOffset(off+n-1)+1
		need := map[int]bool{}
		for i, c := range comps {
			if c.start < z && a < c.end {
				need[i] = true
			}
		}
		if ctx {
			// resolving references needs the identity of every candidate: all tables and heaps
			for _, s := range rep.Secs {
				if s.Type >= 0x100 && s.Type != 0x204 {
					for i, c := range comps {
						if c.start < s.End && s.Off < c.end {
							need[i] = true
						}
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
	var ctxBytes int64
	_, nctx := repCost(0, 1, true)
	ctxBytes, _ = repCost(0, 1, true)
	fmt.Printf("%s: %d B, %d bodies; %d CDC chunks, %d rep components (identity context = %d components, %s packed)\n", args[0], len(d), len(bodies), len(chunks), len(comps), nctx, econx.Kib(ctxBytes))
	fmt.Printf("%-34s %22s %34s %34s\n", "request", "CDC (fetch, chunks)", "rep: data comps only", "rep: + identity context")
	b := mid.Body
	rows := []struct {
		name   string
		off, n int64
	}{{"whole assembly", 0, int64(len(d))}, {fmt.Sprintf("one method body (%d B)", b.End-b.Off), b.Off, b.End - b.Off}, {"128 B inside the largest nearby", best.Body.Off + (best.Body.End-best.Body.Off)/2 - 64, 128}}
	for _, r := range rows {
		cf, ck := cdcCost(r.off, r.n)
		d1, n1 := repCost(r.off, r.n, false)
		d2, n2 := repCost(r.off, r.n, true)
		fmt.Printf("%-34s %14s (%3d) %20s (%4d) %20s (%4d)   amp %.0fx / %.0fx\n", r.name, econx.Kib(cf), ck, econx.Kib(d1), n1, econx.Kib(d2), n2, float64(cf)/float64(max(r.n, 1)), float64(d2)/float64(max(r.n, 1)))
	}
	return nil
}

// perf FILE... : parse / slot walk / encode / decode timings and peak RSS of this process.
func perfCmd(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		t0 := time.Now()
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			return err
		}
		tParse := time.Since(t0)
		t1 := time.Now()
		hv, _ := clrz3.NewHeapView(f, d)
		marks := clrz3.DeriveMarks(f, d, hv)
		bodies := clrz3.FindBodies(f, d, true)
		slots := append(append(clrz3.TableSlots(f, d), clrz3.BlobSlots(f, d, hv.Blobs, marks)...), clrz3.ILSlots(d, bodies)...)
		tWalk := time.Since(t1)
		t2 := time.Now()
		rep, err := clrz3.Encode(f, d, clrz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			return err
		}
		tEnc := time.Since(t2) // includes the verifying decode
		t3 := time.Now()
		if _, err := clrz3.Decode(rep.Meta, rep.Comps); err != nil {
			return err
		}
		tDec := time.Since(t3)
		mb := float64(len(d)) / (1 << 20)
		fmt.Printf("%s: %.1f MiB, %d slots | parse %v, slot walk %v (%.0f MiB/s), encode+verify %v | decode %v (%.1f MiB/s) | peak RSS %s\n", p, mb, len(slots), tParse, tWalk, mb/tWalk.Seconds(), tEnc, tDec, mb/tDec.Seconds(), vmhwm())
	}
	return nil
}
