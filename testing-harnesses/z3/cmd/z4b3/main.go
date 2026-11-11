// Command z4b3 is the Z4-B3 cross-format semantic CAS laboratory driver.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"time"
	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
	"zeros3-z3/simdelta"

	"zeros3-z3/semcas"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4b3:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z4b3 ingest|replay|... ")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "ingest":
		cmdIngest(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	case "optimize":
		cmdOptimize(os.Args[2:])
	case "stats":
		cmdStats(os.Args[2:])
	case "pq-oracle":
		cmdPQOracle(os.Args[2:])
	case "pq-values":
		cmdPQValues(os.Args[2:])
	case "ranges":
		cmdRanges(os.Args[2:])
	case "b1-sample":
		cmdB1Sample(os.Args[2:])
	case "files":
		cmdFiles(os.Args[2:])
	case "cdc":
		cmdCDC(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func cmdIngest(args []string) {
	fl := flag.NewFlagSet("ingest", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	mode := fl.String("mode", "full", "identity|semantic|full")
	prof := fl.String("cpuprofile", "", "write a CPU profile (P6 measurement)")
	fl.Parse(args)
	if *prof != "" {
		pf, err := os.Create(*prof)
		must(err)
		must(pprof.StartCPUProfile(pf))
		defer pprof.StopCPUProfile()
	}
	s, err := semcas.Open(*dir)
	must(err)
	opt := semcas.DefaultOptions()
	switch *mode {
	case "identity":
		opt.Semantic, opt.Convert = false, false
	case "semantic":
		opt.Convert = false
	}
	p := semcas.NewPlanner(s, opt)
	enc := json.NewEncoder(os.Stdout)
	for _, f := range fl.Args() {
		data, err := os.ReadFile(f)
		must(err)
		doc, err := semcas.Detect(filepath.Base(f), data)
		must(err)
		rec, fs, err := p.Ingest(doc)
		must(err)
		must(s.PutRecipe(rec))
		c, err := semcas.CostOf(s, *dir)
		must(err)
		enc.Encode(struct {
			*semcas.FileStats
			Cum   semcas.Cost
			Total int64
		}{fs, c, c.Total()})
	}
}

func cmdReplay(args []string) {
	fl := flag.NewFlagSet("replay", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	sha := fl.String("sha", "", "original file sha256 (hex); empty = all")
	out := fl.String("out", "", "output file (single) or directory (all)")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	ents, err := os.ReadDir(filepath.Join(*dir, "recipes"))
	must(err)
	var names []string
	for _, e := range ents {
		if *sha == "" || e.Name() == *sha {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(*dir, "recipes", n))
		must(err)
		r, err := semcas.ParseRecipe(b)
		must(err)
		path := os.DevNull
		if *out != "" {
			path = *out
			if *sha == "" {
				path = filepath.Join(*out, n)
			}
		}
		f, err := os.Create(path)
		must(err)
		w := bufio.NewWriterSize(f, 1<<20)
		t := time.Now()
		err = s.Replay(r, w, 256<<20)
		must(err)
		must(w.Flush())
		f.Close()
		fmt.Printf("{\"sha\":\"%s\",\"bytes\":%d,\"ms\":%d,\"ok\":true}\n", n, r.Size, time.Since(t).Milliseconds())
	}
}

func cmdCDC(args []string) {
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	enc := json.NewEncoder(os.Stdout)
	for _, f := range args {
		data, err := os.ReadFile(f)
		must(err)
		a, b := prod.Add(data), fine.Add(data)
		enc.Encode(map[string]any{"file": filepath.Base(f), "size": len(data), "prod_inc": a, "prod_cum": prod.Total, "fine_inc": b, "fine_cum": fine.Total})
	}
}

func cmdOptimize(args []string) {
	fl := flag.NewFlagSet("optimize", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	before, err := semcas.CostOf(s, *dir)
	must(err)
	p := semcas.NewPlanner(s, semcas.DefaultOptions())
	t := time.Now()
	st, err := p.Optimize(3)
	must(err)
	after, err := semcas.CostOf(s, *dir)
	must(err)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"optimize": st, "before": before.Total(), "after": after.Total(), "ms": time.Since(t).Milliseconds(), "derived_after": after.DerivedComps})
}

// cmdFiles ingests container files given as kind:path (tar, pack, loose, raw).
func cmdFiles(args []string) {
	fl := flag.NewFlagSet("files", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	fp := semcas.NewFilePlanner(s)
	enc := json.NewEncoder(os.Stdout)
	for _, a := range fl.Args() {
		kind, path, ok := strings.Cut(a, ":")
		if !ok {
			must(fmt.Errorf("want kind:path, got %q", a))
		}
		data, err := os.ReadFile(path)
		must(err)
		t := time.Now()
		w0 := s.Written
		var rec *semcas.Recipe
		switch kind {
		case "tar":
			rec, err = fp.IngestTar(path, data)
		case "pack":
			rec, err = fp.IngestGitPack(path, data)
		case "loose":
			rec, err = fp.IngestGitLoose(path, data)
		case "arrow", "parquet", "pqx":
			var cs *semcas.ColumnStats
			switch kind {
			case "arrow":
				rec, cs, err = fp.IngestArrow(path, data)
			case "pqx":
				rec, cs, err = fp.IngestParquetExact(path, data)
			default:
				rec, cs, err = fp.IngestParquet(path, data)
			}
			if err == nil {
				enc.Encode(map[string]any{"columns": cs, "file": filepath.Base(path)})
			}
		default:
			rec, err = fp.IngestRaw(path, data)
		}
		must(err)
		must(s.PutRecipe(rec))
		c, err := semcas.CostOf(s, *dir)
		must(err)
		enc.Encode(map[string]any{"file": filepath.Base(path), "kind": kind, "size": len(data), "new_blob_bytes": s.Written - w0,
			"recipe_bytes": len(rec.Marshal()), "cum_total": c.Total(), "comps": c.Comps, "derived": c.DerivedComps, "ms": time.Since(t).Milliseconds(), "exact": fp.Exact})
	}
}

func cmdStats(args []string) {
	fl := flag.NewFlagSet("stats", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	type agg struct {
		N               int
		Bytes, RecBytes int64
	}
	raw, der := map[string]*agg{}, map[string]*agg{}
	for _, r := range s.All() {
		k := fmt.Sprintf("k%d/%s", r.Desc.Kind, semcas.DTypeName(int(r.Desc.DType)))
		m := raw
		if r.Derived() {
			m = der
		}
		if m[k] == nil {
			m[k] = &agg{}
		}
		m[k].N++
		m[k].Bytes += r.PayloadLen
		m[k].RecBytes += int64(len(r.Marshal()))
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"raw": raw, "derived": der})
}

// cmdPQOracle runs the existing exact Parquet representation (kind 10 research codec) and
// reports how much of each file is logical value stream versus framing/levels/recipe.
func cmdPQOracle(args []string) {
	enc := json.NewEncoder(os.Stdout)
	for _, f := range args {
		data, err := os.ReadFile(f)
		must(err)
		cls := map[string]int64{}
		seen := map[[32]byte]bool{}
		recipe, sizes, st, err := parquetz3.Encode(bytes.NewReader(data), int64(len(data)), parquetz3.Options{}, func(class string, id parquetz3.ID, b []byte) error {
			if !seen[id] {
				seen[id] = true
				cls[class] += int64(len(b))
			}
			return nil
		})
		out := map[string]any{"file": filepath.Base(f), "size": len(data)}
		if err != nil {
			out["error"] = err.Error()
			enc.Encode(out)
			continue
		}
		out["recipe"] = len(recipe)
		out["recipe_total"] = sizes.Total()
		out["unique_component_bytes_by_class"] = cls
		out["value_stream_bytes"] = st.ValueBytes
		out["level_bytes"] = st.LevelBytes
		out["opaque_bytes"] = st.OpaqueBytes
		out["logical_chunks"] = st.LogicalChunks
		out["opaque_chunks"] = st.OpaqueChunks
		out["dict_predicted"] = st.DictPredicted
		out["dict_explicit"] = st.DictExplicit
		out["comp_pages"] = st.CompPages
		enc.Encode(out)
	}
}

// cmdPQValues proves that the logical value streams of the existing exact Parquet codec are
// byte-identical to the canonical column components: one whole-column group per column is
// emitted and its SHA-256 is looked up among the store's column payload digests.
func cmdPQValues(args []string) {
	fl := flag.NewFlagSet("pq-values", flag.ExitOnError)
	dir := fl.String("store", "", "store holding canonical columns")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	have := map[[32]byte]string{}
	for _, r := range s.All() {
		if r.Desc.Kind == semcas.KindColumn {
			have[r.Payload] = semcas.DTypeName(int(r.Desc.DType))
		}
	}
	enc := json.NewEncoder(os.Stdout)
	for _, f := range fl.Args() {
		data, err := os.ReadFile(f)
		must(err)
		var rows []map[string]any
		_, _, _, err = parquetz3.Encode(bytes.NewReader(data), int64(len(data)), parquetz3.Options{Target: 1 << 30, Cut: parquetz3.CutFixed, FixedCount: 1 << 30, ColumnClasses: true},
			func(class string, id parquetz3.ID, b []byte) error {
				if len(class) > 4 && class[:4] == "val:" {
					k, ok := have[id]
					rows = append(rows, map[string]any{"class": class, "bytes": len(b), "canonical_match": ok, "dtype": k})
				}
				return nil
			})
		out := map[string]any{"file": filepath.Base(f), "values": rows}
		if err != nil {
			out["error"] = err.Error()
		}
		enc.Encode(out)
	}
}

// cmdRanges measures random range reads (4 KiB, 64 KiB, 1 MiB) through the recipe and the
// bytes/pages a production-CDC store would read for the same ranges (compressed chunks).
func cmdRanges(args []string) {
	fl := flag.NewFlagSet("ranges", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	orig := fl.String("orig", "", "original file")
	count := fl.Int("n", 200, "ranges per size")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	data, err := os.ReadFile(*orig)
	must(err)
	rc, err := s.GetRecipe(semcas.PayloadSHA(data))
	must(err)
	type chunk struct{ off, end, packed int64 }
	var chunks []chunk
	pos := int64(0)
	for _, c := range lab.ChunkScaled(data, 16<<10, 64<<10, 256<<10) {
		chunks = append(chunks, chunk{pos, pos + int64(len(c)), lab.PackedLen(c)})
		pos += int64(len(c))
	}
	rng := rand.New(rand.NewSource(1))
	out := []map[string]any{}
	for _, size := range []int64{4096, 65536, 1 << 20} {
		var sBytes, sMat, sPages, sReads, cdcBytes int64
		var dur time.Duration
		for i := 0; i < *count; i++ {
			off := rng.Int63n(int64(len(data)) - size)
			var io semcas.IO
			t := time.Now()
			got, err := s.ReadRange(rc, off, size, &io)
			dur += time.Since(t)
			must(err)
			if !bytes.Equal(got, data[off:off+size]) {
				must(fmt.Errorf("range %d+%d differs", off, size))
			}
			sBytes += io.Bytes
			sMat += io.Materialized
			sPages += int64(io.Pages())
			sReads += int64(io.Reads)
			for _, c := range chunks {
				if c.end > off && c.off < off+size {
					cdcBytes += c.packed
				}
			}
		}
		n := int64(*count)
		out = append(out, map[string]any{"range": size, "semcas_bytes_read": sBytes / n, "semcas_materialized": sMat / n, "semcas_pages4k": sPages / n,
			"semcas_reads": sReads / n, "semcas_us": dur.Microseconds() / n, "cdc_chunk_bytes": cdcBytes / n})
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"file": filepath.Base(*orig), "size": len(data), "ranges": out})
}

// cmdB1Sample prices the B1 generic similarity delta (bottom16 + local features, adaptive
// COPY/ADD against up to four ranked candidates, direct fallback) on tensor-window units
// of later files, using units of all earlier files as bases. Targets are sampled: every
// stride-th tensor of each later file, windows at most 8 MiB (B1 codec inputs are <=16 MiB).
func cmdB1Sample(args []string) {
	fl := flag.NewFlagSet("b1-sample", flag.ExitOnError)
	stride := fl.Int("stride", 8, "sample every Nth tensor of later files")
	fl.Parse(args)
	const win = 8 << 20
	type unit struct {
		data []byte
		file int
	}
	var units []unit
	ia, ib := simdelta.NewIndex(), simdelta.NewIndex()
	enc := json.NewEncoder(os.Stdout)
	for fi, f := range fl.Args() {
		data, err := os.ReadFile(f)
		must(err)
		doc, err := semcas.Detect(filepath.Base(f), data)
		must(err)
		var sampled, ratioNum, direct int64
		nT := 0
		var added []unit
		for _, sg := range doc.Segs {
			if sg.T == nil {
				continue
			}
			nT++
			raw := data[sg.Off : sg.Off+sg.Len]
			for o := 0; o < len(raw); o += win {
				w := raw[o:min(o+win, len(raw))]
				if fi > 0 && nT%*stride == 0 && len(w) >= 4096 {
					bd := map[int]int{}
					for _, q := range ia.Query(simdelta.Features(w, 16), 4) {
						bd[int(q.ID)] += q.Hits
					}
					for _, q := range ib.Query(simdelta.BlockFeatures(w), 4) {
						bd[int(q.ID)] += q.Hits
					}
					type kv struct{ id, h int }
					var ranked []kv
					for id, h := range bd {
						ranked = append(ranked, kv{id, h})
					}
					sort.Slice(ranked, func(i, j int) bool {
						if ranked[i].h != ranked[j].h {
							return ranked[i].h > ranked[j].h
						}
						return ranked[i].id < ranked[j].id
					})
					d, e := simdelta.Encode("direct", nil, w)
					must(e)
					best := len(d)
					for i := 0; i < len(ranked) && i < 4; i++ {
						if len(units[ranked[i].id].data) > 16<<20 {
							continue
						}
						x, e := simdelta.Encode("adaptive", [][]byte{units[ranked[i].id].data}, w)
						must(e)
						best = min(best, len(x))
					}
					sampled += int64(len(w))
					ratioNum += int64(best) + 96
					direct += int64(len(d)) + 96
				}
				added = append(added, unit{w, fi})
			}
		}
		for _, u := range added {
			id := uint32(len(units))
			units = append(units, u)
			ia.Add(id, simdelta.Features(u.data, 16))
			ib.Add(id, simdelta.BlockFeatures(u.data))
		}
		enc.Encode(map[string]any{"file": filepath.Base(f), "sampled_target_bytes": sampled, "b1_bytes": ratioNum, "direct_bytes": direct})
	}
}
