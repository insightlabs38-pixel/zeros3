package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/sqlitez3"
)

// sqlitezRep plans a file with the exact sqlitez3 page tree. The plan's only
// recipe is the serialised sqlitez3 recipe (charged as plan meta bytes, byte for
// byte); every component is reachable through it, so no piece is listed. The
// rebuild closure sees nothing but those recipe bytes and the stored components.
// Anything that is not a SQLite database is production CDC.
func sqlitezPlan(f fstate.File, fan int) (*lab.Plan, error) {
	comps := map[lab.ID]*lab.Comp{}
	var order []*lab.Comp
	rec, _, err := sqlitez3.Encode(bytes.NewReader(f.Data), int64(len(f.Data)), sqlitez3.Options{Fanout: fan}, func(id sqlitez3.ID, b []byte) error {
		if _, ok := comps[id]; ok {
			return nil
		}
		cp := append([]byte(nil), b...)
		cls := "page"
		if len(b) != 4096 && len(b) != 4092 { // lab corpora use 4 KiB pages; anything else is a node
			cls = "node"
		}
		c := &lab.Comp{ID: id, Size: int64(len(cp)), Class: cls, Data: func() ([]byte, error) { return cp, nil }}
		comps[id] = c
		order = append(order, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	p := &lab.Plan{MetaBytes: int64(len(rec))}
	for _, c := range order {
		p.Pieces = append(p.Pieces, lab.Piece{Comp: c, Via: true})
	}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		return sqlitez3.Decode(w, rec, func(id sqlitez3.ID) ([]byte, error) { return get(lab.ID(id)) }, sqlitez3.Limits{})
	}
	return p, nil
}

func sqlitezRep(name string, fan int) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		p, err := sqlitezPlan(f, fan)
		if err != nil {
			c.Counts["fallback_files"]++
			return fstate.FallbackCDC(f)
		}
		return p, nil
	}}
}

func promotedSQLiteReps() []fstate.RepDef {
	reps := laneReps("sqlite") // cdc, fixed-4k, fixed-64k, pages, pages-norm
	var out []fstate.RepDef
	for _, r := range reps {
		if r.Name == "fixed-64k" || r.Name == "pages-norm" {
			continue
		}
		out = append(out, r)
	}
	return append(out, sqlitezRep("tree-16", 16), sqlitezRep("tree-64", 64), sqlitezRep("tree-256", 256))
}

type sqlRow struct {
	Variant string
	Logical int64
	Changed float64 // fraction of pages whose bytes differ from the base
	Cost    map[string]int64
	New     map[string]int64
	Exact   map[string]bool
}

func cmdSQLite(args []string) error {
	fs := flag.NewFlagSet("sqlite", flag.ExitOnError)
	jsonOut := fs.String("json", "", "write results as JSON")
	readsOnly := fs.Bool("reads-only", false, "only the read/throughput/memory measurements")
	fs.Parse(args)
	if *readsOnly {
		st, err := loadState(filepath.Join(cacheRoot(), "sqlite", "kv24"), "r0", "sqlite")
		if err != nil {
			return err
		}
		rd, err := sqliteReads(st.Files[0].Data)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(rd, "", " ")
		fmt.Println(string(b))
		return nil
	}
	reps := promotedSQLiteReps()
	r := &fstate.Runner{RefBytes: 96, Pack: true}
	out := map[string]any{}

	// 1. independent variants of r0
	dir := filepath.Join(cacheRoot(), "sqlite", "kv24")
	meta, err := loadMeta(dir)
	if err != nil {
		return err
	}
	base, err := loadState(dir, "r0", "sqlite")
	if err != nil {
		return err
	}
	var rows []sqlRow
	hdr := func() {
		fmt.Printf("%-30s %8s %7s", "variant", "logical", "pages%")
		for _, rp := range reps {
			fmt.Printf(" %10s", rp.Name)
		}
		fmt.Println()
	}
	hdr()
	for _, m := range meta.Revisions {
		st := base
		if m.Rev != "r0" {
			if st, err = loadState(dir, m.Rev, "sqlite"); err != nil {
				return err
			}
		}
		var res []fstate.Row
		if m.Rev == "r0" {
			res = r.RunStates([]*fstate.State{st}, reps)
		} else {
			res = r.RunStates([]*fstate.State{base, st}, reps)
		}
		row := sqlRow{Variant: m.Rev, Logical: m.LogicalBytes, Cost: map[string]int64{}, New: map[string]int64{}, Exact: map[string]bool{}}
		if m.Rev != "r0" {
			row.Changed = changedPages(base.Files[0].Data, st.Files[0].Data)
		}
		for _, x := range res {
			if x.State != st.Label {
				continue
			}
			row.Cost[x.Rep], row.New[x.Rep], row.Exact[x.Rep] = x.CostPacked, x.NewBytes, x.Exact && x.Err == ""
			if x.Err != "" {
				fmt.Fprintln(os.Stderr, "ERR", m.Rev, x.Rep, x.Err)
			}
		}
		rows = append(rows, row)
		fmt.Printf("%-30.30s %8s %6.1f%%", row.Variant, kib(row.Logical), row.Changed*100)
		for _, rp := range reps {
			fmt.Printf(" %10s", kib(row.Cost[rp.Name]))
		}
		fmt.Println()
	}
	out["variants"] = rows

	// 2. a 10-revision application history (cumulative store)
	hdir := filepath.Join(cacheRoot(), "sqlite", "hist10")
	hm, err := loadMeta(hdir)
	if err != nil {
		return err
	}
	var states []*fstate.State
	for _, m := range hm.Revisions {
		st, err := loadState(hdir, m.Rev, "sqlite")
		if err != nil {
			return err
		}
		states = append(states, st)
	}
	hres := r.RunStates(states, reps)
	fmt.Printf("\nhistory chain (packed cost per revision, cumulative at right)\n%-6s", "rev")
	for _, rp := range reps {
		fmt.Printf(" %10s", rp.Name)
	}
	fmt.Println()
	cum := map[string]int64{}
	hist := []map[string]int64{}
	for _, st := range states {
		line := map[string]int64{}
		fmt.Printf("%-6s", st.Label)
		for _, rp := range reps {
			for _, x := range hres {
				if x.State == st.Label && x.Rep == rp.Name {
					line[rp.Name] = x.CostPacked
					cum[rp.Name] += x.CostPacked
					if !x.Exact || x.Err != "" {
						fmt.Printf("!")
					}
				}
			}
			fmt.Printf(" %10s", kib(line[rp.Name]))
		}
		fmt.Println()
		hist = append(hist, line)
	}
	fmt.Printf("%-6s", "total")
	for _, rp := range reps {
		fmt.Printf(" %10s", kib(cum[rp.Name]))
	}
	fmt.Println()
	out["history"], out["history_total"] = hist, cum

	// 3. reads, throughput and memory on r0
	rd, err := sqliteReads(base.Files[0].Data)
	if err != nil {
		return err
	}
	out["reads"] = rd
	b, _ := json.MarshalIndent(rd, "", " ")
	fmt.Printf("\nreads/perf (r0 %d bytes)\n%s\n", len(base.Files[0].Data), b)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(out, "", " ")
		return os.WriteFile(*jsonOut, b, 0o644)
	}
	return nil
}

func changedPages(a, b []byte) float64 {
	n := min(len(a), len(b)) / 4096
	if n == 0 {
		return 0
	}
	seen := map[[32]byte]bool{}
	for i := 0; i < len(a)/4096; i++ {
		seen[sha256.Sum256(a[i*4096:(i+1)*4096])] = true
	}
	ch := 0
	for i := 0; i < len(b)/4096; i++ {
		if !seen[sha256.Sum256(b[i*4096:(i+1)*4096])] {
			ch++
		}
	}
	return float64(ch) / float64(len(b)/4096)
}

// peakHeapDelta runs fn and returns the largest HeapInuse growth over the heap
// before it (sampled every 2 ms; includes garbage not yet collected).
func peakHeapDelta(fn func()) float64 {
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return float64(int64(peak.Load())-int64(m0.HeapInuse)) / (1 << 20)
}

func sqliteReads(db []byte) (map[string]any, error) {
	res := map[string]any{}
	st := map[sqlitez3.ID][]byte{}
	t0 := time.Now()
	rec, es, err := sqlitez3.Encode(bytes.NewReader(db), int64(len(db)), sqlitez3.Options{}, func(id sqlitez3.ID, b []byte) error {
		if _, ok := st[id]; !ok {
			st[id] = append([]byte(nil), b...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	enc := time.Since(t0)
	var stored int64
	for _, b := range st {
		stored += int64(len(b))
	}
	t0 = time.Now()
	var touched int
	var tbytes int64
	get := func(id sqlitez3.ID) ([]byte, error) { b := st[id]; touched++; tbytes += int64(len(b)); return b, nil }
	h := sha256.New()
	if err := sqlitez3.Decode(h, rec, get, sqlitez3.Limits{}); err != nil {
		return nil, err
	}
	dec := time.Since(t0)
	if !bytes.Equal(h.Sum(nil), func() []byte { s := sha256.Sum256(db); return s[:] }()) {
		return nil, fmt.Errorf("decode digest mismatch")
	}
	res["recipe_bytes"], res["pages"], res["nodes"], res["split_pages"] = len(rec), es.Pages, es.Nodes, es.Split
	res["stored_component_bytes"], res["stored_components"] = stored, len(st)
	res["whole_components_touched"], res["whole_bytes_touched"] = touched, tbytes
	res["encode_MBps"] = float64(len(db)) / enc.Seconds() / 1e6
	res["decode_MBps"] = float64(len(db)) / dec.Seconds() / 1e6
	res["store_MiB"] = float64(stored) / (1 << 20)
	// library working set: encode into a discarding sink, decode into a discarding writer from the store
	res["encode_peak_heap_MiB"] = peakHeapDelta(func() {
		sqlitez3.Encode(bytes.NewReader(db), int64(len(db)), sqlitez3.Options{}, func(sqlitez3.ID, []byte) error { return nil })
	})
	res["decode_peak_heap_MiB"] = peakHeapDelta(func() {
		sqlitez3.Decode(io.Discard, rec, func(id sqlitez3.ID) ([]byte, error) { return st[id], nil }, sqlitez3.Limits{})
	})
	// one page read: recipe resident, so one node + one page component
	touched, tbytes = 0, 0
	pg, err := sqlitez3.ReadPage(rec, 2000, get, sqlitez3.Limits{})
	if err != nil || !bytes.Equal(pg, db[2000*4096:2001*4096]) {
		return nil, fmt.Errorf("page read mismatch: %v", err)
	}
	res["page_read_components"], res["page_read_bytes"] = touched, tbytes
	// the same page under production CDC: the chunk that covers it
	sizes, _ := lab.CDCSizes(bytes.NewReader(db))
	var pos int64
	for _, z := range sizes {
		if pos <= 2000*4096 && 2000*4096 < pos+int64(z) {
			res["cdc_page_read_bytes"], res["cdc_chunks_total"] = z, len(sizes)
		}
		pos += int64(z)
	}
	return res, nil
}
