// Command z4b3 is the Z4-B3 cross-format semantic CAS laboratory driver.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	case "regroup":
		cmdRegroup(os.Args[2:])
	case "stats":
		cmdStats(os.Args[2:])
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
	fl.Parse(args)
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
		c, err := s.Cost()
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
	before, err := s.Cost()
	must(err)
	p := semcas.NewPlanner(s, semcas.DefaultOptions())
	t := time.Now()
	st, err := p.Optimize(3)
	must(err)
	after, err := s.Cost()
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
		default:
			rec, err = fp.IngestRaw(path, data)
		}
		must(err)
		must(s.PutRecipe(rec))
		c, err := s.Cost()
		must(err)
		enc.Encode(map[string]any{"file": filepath.Base(path), "kind": kind, "size": len(data), "new_blob_bytes": s.Written - w0,
			"recipe_bytes": len(rec.Marshal()), "cum_total": c.Total(), "comps": c.Comps, "derived": c.DerivedComps, "ms": time.Since(t).Milliseconds(), "exact": fp.Exact})
	}
}

func cmdRegroup(args []string) {
	fl := flag.NewFlagSet("regroup", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	before, err := s.Cost()
	must(err)
	p := semcas.NewPlanner(s, semcas.DefaultOptions())
	t := time.Now()
	st, err := p.Regroup()
	must(err)
	after, err := s.Cost()
	must(err)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"regroup": st, "before": before.Total(), "after": after.Total(), "ms": time.Since(t).Milliseconds(), "derived_after": after.DerivedComps})
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
