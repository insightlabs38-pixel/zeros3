package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"zeros3-z3/simdelta"
)

// cmdB1: b1 [-chunk 65536] [-algs list] [-json f] STATE...   Position-aligned chunk deltas against the
// previous state (an optimistic base-selection oracle for B1) plus no-base predictor (PXR) and
// chunk dedup. Cumulative charged bytes per algorithm family: pack rule + 96 B/object.
func cmdB1(args []string) {
	fl := flag.NewFlagSet("b1", flag.ExitOnError)
	chunk := fl.Int("chunk", 65536, "chunk size")
	algs := fl.String("algs", "xor,plane,adaptive,pxr", "delta algorithms vs aligned previous chunk")
	jsonOut := fl.String("json", "", "output")
	fl.Parse(args)
	al := strings.Split(*algs, ",")
	type acc struct {
		Generic, PXRControl, Direct int64
		Chunks, Dedup               int
	}
	var per []acc
	seen := map[[32]byte]bool{}
	var prev []byte
	var cum acc
	for si, path := range fl.Args() {
		cur, err := os.ReadFile(path)
		must(err)
		n := (len(cur) + *chunk - 1) / *chunk
		type res struct {
			gen, pxr, dir int64
			dup           bool
		}
		out := make([]res, n)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for c := 0; c < n; c++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(c int) {
				defer wg.Done()
				defer func() { <-sem }()
				lo, hi := c**chunk, (c+1)**chunk
				if hi > len(cur) {
					hi = len(cur)
				}
				t := cur[lo:hi]
				id := sha256.Sum256(t)
				var r res
				if seen[id] { // read-only during the parallel phase
					r.dup = true
					out[c] = r
					return
				}
				d := int64(simdelta.Physical(t))
				r.dir = d
				r.gen, r.pxr = d, d
				if wire, err := simdelta.Encode("predict", nil, t); err == nil {
					if v := int64(simdelta.Physical(wire)); v < r.pxr {
						r.pxr = v
					}
				}
				if si > 0 && lo < len(prev) {
					bh := hi
					if bh > len(prev) {
						bh = len(prev)
					}
					base := prev[lo:bh]
					if len(base) == len(t) {
						for _, a := range al {
							wire, err := simdelta.Encode(a, [][]byte{base}, t)
							if err != nil {
								continue
							}
							v := int64(simdelta.Physical(wire)) + 36
							if a == "pxr" {
								if v < r.pxr {
									r.pxr = v
								}
							} else if v < r.gen {
								r.gen = v
							}
						}
					}
				}
				out[c] = r
			}(c)
		}
		wg.Wait()
		var a acc
		for c := 0; c < n; c++ {
			lo, hi := c**chunk, (c+1)**chunk
			if hi > len(cur) {
				hi = len(cur)
			}
			id := sha256.Sum256(cur[lo:hi])
			if out[c].dup || seen[id] {
				a.Dedup++
				a.Generic += 36
				a.PXRControl += 36
				a.Direct += 36
			} else {
				a.Generic += out[c].gen
				a.PXRControl += out[c].pxr
				a.Direct += out[c].dir
			}
			seen[id] = true
			a.Chunks++
		}
		per = append(per, a)
		cum.Generic += a.Generic
		cum.PXRControl += a.PXRControl
		cum.Direct += a.Direct
		fmt.Printf("{\"state\":%d,\"generic\":%d,\"pxr_control\":%d,\"direct_dedup\":%d,\"dedup\":%d}\n", si, a.Generic, a.PXRControl, a.Direct, a.Dedup)
		prev = cur
		_ = bytes.MinRead
	}
	fmt.Printf("TOTAL generic=%d pxr_control=%d direct_dedup=%d\n", cum.Generic, cum.PXRControl, cum.Direct)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"chunk": *chunk, "algs": al, "per_state": per, "total": cum}, "", " ")
		os.WriteFile(*jsonOut, b, 0o644)
	}
}
