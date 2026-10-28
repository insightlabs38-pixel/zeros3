package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"zeros3-z3/sstz3"
)

func vmHWM() int64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			f := strings.Fields(l)
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n << 10
		}
	}
	return 0
}

// cmdPerf measures the reusable analyzer and decoder alone: tables are read through
// os.File (ReaderAt), components go to a directory store, nothing is retained. Run it in a
// fresh process: the reported peak is the process high-water mark.
func cmdPerf(args []string) error {
	fs := flag.NewFlagSet("perf", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	prods := fs.String("prod", "leveldb,rocksdb,pebble", "producers")
	store := fs.String("store", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "perfstore"), "component directory")
	target := fs.Int("target", 8<<10, "group target bytes")
	form := fs.Int("form", int(sstz3.FormUKV), "form")
	decode := fs.Bool("decode", true, "also decode every table from the directory store and compare")
	fs.Parse(args)
	os.RemoveAll(*store)
	if err := os.MkdirAll(*store, 0o755); err != nil {
		return err
	}
	var files []string
	for _, p := range strings.Split(*prods, ",") {
		for _, f := range tableFiles([]string{filepath.Join(*root, p)}) {
			if !strings.Contains(f, "/codecs/") { // the codec controls hold tables the representation rejects
				files = append(files, f)
			}
		}
	}
	var in, comps, compBytes int64
	var tot sstz3.Stats
	var sz sstz3.Sizes
	type rec struct {
		path   string
		recipe []byte
	}
	var recs []rec
	var enc time.Duration
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		st, _ := f.Stat()
		t0 := time.Now()
		recipe, zs, es, err := sstz3.Encode(f, st.Size(), sstz3.Options{Form: sstz3.Form(*form), Cutter: &sstz3.HashCutter{Target: *target, Max: 4 * *target}}, func(_ string, id sstz3.ID, b []byte) error {
			comps++
			compBytes += int64(len(b))
			path := filepath.Join(*store, hex.EncodeToString(id[:]))
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			return os.WriteFile(path, b, 0o644)
		})
		enc += time.Since(t0)
		f.Close()
		tot.Blocks += es.Blocks
		tot.ExplicitBlocks += es.ExplicitBlocks
		tot.Records += es.Records
		tot.KeyBytes += es.KeyBytes
		tot.ValueBytes += es.ValueBytes
		tot.TrailerBytes += es.TrailerBytes
		tot.DataBytes += es.DataBytes
		tot.TailBytes += es.TailBytes
		for i := range tot.PredBlocks {
			tot.PredBlocks[i] += es.PredBlocks[i]
		}
		sz.Head += zs.Head
		sz.Framing += zs.Framing
		sz.Snappy += zs.Snappy
		sz.Zstd += zs.Zstd
		sz.Explicit += zs.Explicit
		sz.Groups += zs.Groups
		sz.Trailers += zs.Trailers
		sz.Digests += zs.Digests
		if err != nil {
			return err
		}
		in += st.Size()
		recs = append(recs, rec{p, recipe})
	}
	runtime.ReadMemStats(&m1)
	fmt.Printf("encode: %d tables, %.0f MiB in %.1fs = %.0f MiB/s; components %d (%.0f MiB logical, written once per digest); process peak RSS so far %.0f MiB; Go heap in use after %.0f MiB, total allocated %.1f GiB\n",
		len(files), float64(in)/(1<<20), enc.Seconds(), float64(in)/(1<<20)/enc.Seconds(), comps, float64(compBytes)/(1<<20), float64(vmHWM())/(1<<20), float64(m1.HeapInuse)/(1<<20), float64(m1.TotalAlloc-m0.TotalAlloc)/(1<<30))
	fmt.Printf("structure: %d blocks (explicit-layout blocks %d), Snappy blocks explicit/go/cpp14/cpp15 %v, %d records; keys %.1f MiB, values %.1f MiB, trailers %.1f MiB (%.1f%% of logical bytes are keys); data blocks %.0f MiB, tail %.1f MiB (%.2f%%)\n", tot.Blocks, tot.ExplicitBlocks, tot.PredBlocks, tot.Records, float64(tot.KeyBytes)/(1<<20), float64(tot.ValueBytes)/(1<<20), float64(tot.TrailerBytes)/(1<<20), 100*float64(tot.KeyBytes)/float64(tot.KeyBytes+tot.ValueBytes), float64(tot.DataBytes)/(1<<20), float64(tot.TailBytes)/(1<<20), 100*float64(tot.TailBytes)/float64(tot.DataBytes+tot.TailBytes))
	fmt.Printf("recipes: head %d, block framing %d, Snappy %d, Zstd %d, explicit layouts %d, group counts %d, sequence/type words %d, digests %d (bytes, all tables)\n", sz.Head, sz.Framing, sz.Snappy, sz.Zstd, sz.Explicit, sz.Groups, sz.Trailers, sz.Digests)
	if !*decode {
		return nil
	}
	var dec time.Duration
	var out int64
	for _, r := range recs {
		orig, err := os.ReadFile(r.path)
		if err != nil {
			return err
		}
		h := sha256.New()
		cw := &countW{}
		t0 := time.Now()
		err = sstz3.Decode(io.MultiWriter(cw, h), r.recipe, func(id sstz3.ID) ([]byte, error) {
			return os.ReadFile(filepath.Join(*store, hex.EncodeToString(id[:])))
		}, sstz3.Limits{})
		dec += time.Since(t0)
		if err != nil || cw.n != int64(len(orig)) || !bytes.Equal(h.Sum(nil), func() []byte { s := sha256.Sum256(orig); return s[:] }()) {
			return fmt.Errorf("decode %s: %v (%d/%d bytes)", r.path, err, cw.n, len(orig))
		}
		out += cw.n
	}
	fmt.Printf("decode: %.0f MiB in %.1fs = %.0f MiB/s (components read from disk files, SHA-256 of every output compared); process peak RSS %.0f MiB\n", float64(out)/(1<<20), dec.Seconds(), float64(out)/(1<<20)/dec.Seconds(), float64(vmHWM())/(1<<20))
	return nil
}

type countW struct{ n int64 }

func (c *countW) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
