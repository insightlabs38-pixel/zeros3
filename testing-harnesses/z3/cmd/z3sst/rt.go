package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zeros3-z3/sstz3"
)

func tableFiles(roots []string) []string {
	var files []string
	for _, a := range roots {
		filepath.Walk(a, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && (strings.HasSuffix(p, ".ldb") || strings.HasSuffix(p, ".sst")) && !strings.Contains(p, "_other") {
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// cmdRT round-trips real tables through every representation form: encode, discard the
// analysis state, decode from the serialized recipe and the stored components only, and
// compare length, SHA-256 and bytes with the original.
func cmdRT(args []string) error {
	fs := flag.NewFlagSet("rt", flag.ExitOnError)
	target := fs.Int("target", 8<<10, "group target bytes")
	fs.Parse(args)
	forms := []sstz3.Form{sstz3.FormBlocks, sstz3.FormUKV, sstz3.FormFull, sstz3.FormKV, sstz3.FormKTV, sstz3.FormKVT}
	type agg struct {
		files                      int
		bytes, rec, comps, payload int64
		enc, dec                   time.Duration
		bad                        int
		sz                         sstz3.Sizes
		cmds, blocks               int64
		pred                       [4]int
	}
	res := map[string]*agg{}
	for _, p := range tableFiles(fs.Args()) {
		data, _ := os.ReadFile(p)
		prod := "?"
		for _, k := range []string{"leveldb", "rocksdb", "pebble"} {
			if strings.Contains(p, "/"+k+"/") {
				prod = k
			}
		}
		for _, form := range forms {
			store := map[sstz3.ID][]byte{}
			t0 := time.Now()
			var cut sstz3.Cutter
			if form != sstz3.FormBlocks {
				cut = &sstz3.HashCutter{Target: *target, Max: 4 * *target}
			}
			recipe, sz, es, err := sstz3.Encode(bytes.NewReader(data), int64(len(data)), sstz3.Options{Form: form, Cutter: cut}, func(_ string, id sstz3.ID, b []byte) error {
				if _, ok := store[id]; !ok {
					store[id] = append([]byte(nil), b...)
				}
				return nil
			})
			a := res[prod+"/"+form.String()]
			if a == nil {
				a = &agg{}
				res[prod+"/"+form.String()] = a
			}
			a.files++
			a.bytes += int64(len(data))
			if err != nil {
				a.bad++
				fmt.Printf("%s %s: encode: %v\n", p, form, err)
				continue
			}
			a.enc += time.Since(t0)
			a.rec += int64(len(recipe))
			a.sz.Head += sz.Head
			a.sz.Framing += sz.Framing
			a.sz.Snappy += sz.Snappy
			a.sz.Explicit += sz.Explicit
			a.sz.Groups += sz.Groups
			a.sz.Trailers += sz.Trailers
			a.sz.Digests += sz.Digests
			a.cmds += es.SnappyCmds
			a.pred[0] += es.PredBlocks[0]
			a.pred[1] += es.PredBlocks[1]
			a.pred[2] += es.PredBlocks[2]
			a.pred[3] += es.PredBlocks[3]
			a.blocks += int64(es.Blocks)
			for _, b := range store {
				a.payload += int64(len(b))
			}
			a.comps += int64(len(store))
			t1 := time.Now()
			var out bytes.Buffer
			err = sstz3.Decode(&out, recipe, func(id sstz3.ID) ([]byte, error) {
				b, ok := store[id]
				if !ok {
					return nil, fmt.Errorf("missing %x", id[:6])
				}
				return b, nil
			}, sstz3.Limits{})
			a.dec += time.Since(t1)
			if err != nil || out.Len() != len(data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(data) || !bytes.Equal(out.Bytes(), data) {
				a.bad++
				fmt.Printf("%s %s: DECODE MISMATCH err=%v len=%d/%d\n", p, form, err, out.Len(), len(data))
			}
		}
	}
	var ks []string
	failed := 0
	for k := range res {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		a := res[k]
		failed += a.bad
		fmt.Printf("%-16s files=%d bad=%d in=%.0f MiB comps=%d payload=%.1f MiB recipe=%.2f MiB enc=%.0f MiB/s dec=%.0f MiB/s\n", k, a.files, a.bad, float64(a.bytes)/(1<<20), a.comps,
			float64(a.payload)/(1<<20), float64(a.rec)/(1<<20), float64(a.bytes)/(1<<20)/a.enc.Seconds(), float64(a.bytes)/(1<<20)/a.dec.Seconds())
		fmt.Printf("    snappy blocks explicit/go/cpp14/cpp15: %v\n", a.pred)
		fmt.Printf("    recipe: framing %d snappy %d explicit %d groups %d trailers %d digests %d; %d blocks, %.1f snappy cmds/block\n", a.sz.Framing, a.sz.Snappy, a.sz.Explicit, a.sz.Groups, a.sz.Trailers, a.sz.Digests, a.blocks, float64(a.cmds)/float64(a.blocks))
	}
	if failed > 0 {
		return fmt.Errorf("%d inexact round trips", failed)
	}
	return nil
}
