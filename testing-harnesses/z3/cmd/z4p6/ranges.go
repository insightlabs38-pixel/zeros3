package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zeros3-z3/lab"
	"zeros3-z3/semcas"
)

// cmdRanges measures random 4 KiB / 64 KiB / 1 MiB ranges of one original file, by the kind
// of component that serves the start of the range (raw, convert, layout/perm, gather, ...),
// with verified blocks of a chosen size (0 = the unverified B3 reader). Steady-state numbers
// exclude the one-time first-touch verification, which is reported separately.
func cmdRanges(args []string) {
	fl := flag.NewFlagSet("ranges", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	orig := fl.String("orig", "", "original file")
	count := fl.Int("n", 300, "ranges per size")
	block := fl.Int("block", 64<<10, "verified block size (0 = unverified legacy reader)")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	s.BlockSize = *block
	data, err := os.ReadFile(*orig)
	must(err)
	rc, err := s.GetRecipe(semcas.PayloadSHA(data))
	must(err)
	// step boundaries and component kinds
	type seg struct {
		lo, hi int64
		kind   string
	}
	var segs []seg
	pos := int64(0)
	for _, st := range rc.Steps {
		var ln int64
		kind := "literal"
		switch st.Kind {
		case semcas.StepLit:
			ln = int64(len(st.Lit))
		default:
			rec := s.Rec(st.CID)
			if st.Kind == semcas.StepRef {
				ln = rec.PayloadLen
			} else {
				ln = int64(st.Len)
			}
			kind = classify(rec)
		}
		segs = append(segs, seg{pos, pos + ln, kind})
		pos += ln
	}
	kindOf := func(off int64) string {
		i := sort.Search(len(segs), func(i int) bool { return segs[i].hi > off })
		if i < len(segs) {
			return segs[i].kind
		}
		return "literal"
	}
	type chunk struct{ off, end, packed int64 }
	var chunks []chunk
	cp := int64(0)
	for _, c := range lab.ChunkScaled(data, 16<<10, 64<<10, 256<<10) {
		chunks = append(chunks, chunk{cp, cp + int64(len(c)), lab.PackedLen(c)})
		cp += int64(len(c))
	}
	rng := rand.New(rand.NewSource(1))
	// warm every component's verified table first so steady-state excludes first touch
	var firstTouch int64
	warm := func() {
		for _, sg := range segs {
			if sg.kind == "literal" {
				continue
			}
			var io semcas.IO
			if _, err := s.ReadRange(rc, sg.lo, 1, &io); err != nil {
				must(err)
			}
			firstTouch += io.FirstTouchBytes
		}
	}
	if *block > 0 {
		warm()
	}
	var res []map[string]any
	for _, size := range []int64{4096, 65536, 1 << 20} {
		type agg struct {
			n                  int
			bytes, mat, blocks int64
			us                 int64
			cdc                int64
		}
		per := map[string]*agg{}
		for i := 0; i < *count; i++ {
			off := rng.Int63n(int64(len(data)) - size)
			k := kindOf(off)
			var io semcas.IO
			t := time.Now()
			got, err := s.ReadRange(rc, off, size, &io)
			d := time.Since(t)
			must(err)
			if !bytes.Equal(got, data[off:off+size]) {
				must(fmt.Errorf("range %d+%d differs", off, size))
			}
			a := per[k]
			if a == nil {
				a = &agg{}
				per[k] = a
			}
			a.n++
			a.bytes += io.Bytes
			a.mat += io.Materialized
			a.blocks += int64(io.BlocksRead)
			a.us += d.Microseconds()
			for _, c := range chunks {
				if c.end > off && c.off < off+size {
					a.cdc += c.packed
				}
			}
		}
		var keys []string
		for k := range per {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a := per[k]
			n := int64(a.n)
			res = append(res, map[string]any{"range": size, "kind": k, "requests": a.n, "bytes_read_avg": a.bytes / n,
				"materialized_avg": a.mat / n, "blocks_avg": a.blocks / n, "amplification": float64(a.bytes) / float64(n) / float64(size),
				"us_avg": a.us / n, "cdc_chunk_bytes_avg": a.cdc / n})
		}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"file": filepath.Base(*orig), "size": len(data), "block": *block, "first_touch_bytes_total": firstTouch, "results": res})
}

func classify(r *semcas.Record) string {
	if !r.Derived() {
		return "raw"
	}
	var ks []string
	for i := range r.Ops {
		switch r.Ops[i].Kind {
		case semcas.OpPerm:
			ks = append(ks, "perm")
		case semcas.OpGather:
			ks = append(ks, "gather")
		case semcas.OpConvert, semcas.OpQ8_0, semcas.OpQ4_0:
			ks = append(ks, "convert")
		default:
			ks = append(ks, fmt.Sprintf("op%d", r.Ops[i].Kind))
		}
	}
	return strings.Join(uniqStr(ks), "+")
}

func uniqStr(a []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
