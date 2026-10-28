// z3breadth proves every file or its explicit CDC fallback and prices complete
// candidate closures. It holds one input, never an entire history's plaintext.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"zeros3-z3/admission"
	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
)

type row struct {
	Path, SHA, Family, Evidence, Fallback string
	Bytes                                 int
	Objects                               int
	CDC, Representation, Additive         int64
	Proof                                 bool
}
type state struct {
	cdc, rep, all map[int]bool
	cost          [3]int64
}

func main() {
	flag.Parse()
	var paths []string
	for _, root := range flag.Args() {
		if e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if !i.IsDir() && (strings.HasSuffix(p, ".sst") || strings.HasSuffix(p, ".parquet")) {
				paths = append(paths, p)
			}
			return nil
		}); e != nil {
			panic(e)
		}
	}
	sort.Strings(paths)
	c := admission.NewCapture()
	states := map[string]*state{}
	enc := json.NewEncoder(os.Stdout)
	for _, p := range paths {
		info, e := os.Stat(p)
		if e != nil || info.Size() > 128<<20 {
			panic(fmt.Sprint("input bound: ", e))
		}
		data, e := os.ReadFile(p)
		if e != nil {
			panic(e)
		}
		family := filepath.Dir(filepath.Dir(p))
		evidence := "PRODUCER_WORKLOAD"
		if strings.Contains(p, "/ref/") {
			family = filepath.Dir(p)
			evidence = "REFERENCE_FIXTURE"
		}
		if strings.Contains(p, "/duckdb-") {
			evidence = "REAL_REEXPORT"
		}
		st := states[family]
		if st == nil {
			st = &state{cdc: map[int]bool{}, rep: map[int]bool{}, all: map[int]bool{}}
			states[family] = st
		}
		c.ResetStore()
		r := row{Path: p, SHA: fmt.Sprintf("%x", sha256.Sum256(data)), Family: family, Evidence: evidence, Bytes: len(data)}
		start := time.Now()
		base, e := admission.CDC(data)
		if e != nil {
			panic(e)
		}
		bc, e := c.Variant("cdc", data, base, start)
		if e != nil {
			panic(e)
		}
		start = time.Now()
		var b *kinds.Bundle
		if strings.HasSuffix(p, ".sst") {
			b, _, _, e = kinds.EncodeSST(bytes.NewReader(data), int64(len(data)), sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 8192, Max: 32768}})
		} else {
			b, _, _, e = kinds.EncodeParquet(bytes.NewReader(data), int64(len(data)), parquetz3.Options{Target: 8192})
		}
		rc := bc
		if e != nil {
			if strings.HasSuffix(p, ".sst") {
				if !errors.Is(e, sstz3.ErrUnsupported) && !errors.Is(e, sstz3.ErrCorrupt) && !errors.Is(e, sstz3.ErrLimit) {
					panic(e)
				}
			} else if parquetz3.Supported(e) {
				panic(e)
			}
			r.Fallback = e.Error()
		} else {
			rc, e = c.Variant("representation", data, b, start)
			if e != nil {
				panic(fmt.Sprintf("%s proof: %v", p, e))
			}
		}
		for i, ids := range [][]int{bc.Objects, rc.Objects, append(append([]int(nil), bc.Objects...), rc.Objects...)} {
			seen := []map[int]bool{st.cdc, st.rep, st.all}[i]
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					o := c.Corpus.Objects[id]
					st.cost[i] += o.Packed + 64
				}
			}
		}
		r.CDC, r.Representation, r.Additive = st.cost[0], st.cost[1], st.cost[2]
		r.Objects = len(rc.Objects)
		r.Proof = true
		if e := enc.Encode(r); e != nil {
			panic(e)
		}
	}
}
