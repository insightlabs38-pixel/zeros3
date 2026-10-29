// z4managed measures one fresh-process managed encode or independent proof.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
	"syscall"
	"time"
	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	if len(os.Args) != 4 {
		panic("usage: z4managed stream|legacy|proof input outdir")
	}
	mode, path, dir := os.Args[1], os.Args[2], os.Args[3]
	must(os.MkdirAll(dir, 0700))
	start := time.Now()
	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(time.Millisecond * 2)
		defer t.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
				if peak.CompareAndSwap(old, m.HeapAlloc) {
					break
				}
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	file, e := os.Open(path)
	must(e)
	defer file.Close()
	info, e := file.Stat()
	must(e)
	var count int
	var meta []byte
	h := sha256.New()
	if mode == "proof" {
		meta, e = os.ReadFile(filepath.Join(dir, "meta"))
		must(e)
		f, e := os.Open(filepath.Join(dir, "components"))
		must(e)
		defer f.Close()
		r := bufio.NewReader(f)
		var comps [][]byte
		for {
			var n [4]byte
			_, e := io.ReadFull(r, n[:])
			if e == io.EOF {
				break
			}
			must(e)
			l := binary.LittleEndian.Uint32(n[:])
			if l > 16<<20 {
				panic("component limit")
			}
			b := make([]byte, l)
			_, e = io.ReadFull(r, b)
			must(e)
			comps = append(comps, b)
		}
		var out []byte
		if filepath.Ext(path) == ".dll" {
			out, e = clrz3.Decode(meta, comps)
		} else {
			out, e = dexz3.Decode(meta, comps)
		}
		must(e)
		expected := sha256.New()
		_, e = io.Copy(expected, file)
		must(e)
		actual := sha256.Sum256(out)
		if int64(len(out)) != info.Size() || !bytes.Equal(actual[:], expected.Sum(nil)) {
			panic("proof mismatch")
		}
		count = len(comps)
		h.Write(out)
	} else {
		out, e := os.Create(filepath.Join(dir, "components"))
		must(e)
		writer := bufio.NewWriter(out)
		emit := func(b []byte) error {
			var n [4]byte
			binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
			for _, p := range [][]byte{n[:], b} {
				if _, e := writer.Write(p); e != nil {
					return e
				}
				h.Write(p)
			}
			count++
			return nil
		}
		if filepath.Ext(path) == ".dll" {
			var data []byte
			if mode != "stream-at" {
				data, e = os.ReadFile(path)
				must(e)
			}
			f, e := clrz3.Parse(file, info.Size(), clrz3.Limits{})
			must(e)
			if mode == "stream" || mode == "stream-at" {
				encode := func(f *clrz3.File, data []byte, cfg clrz3.StreamConfig) (*clrz3.Rep, error) {
					if mode == "stream-at" {
						return clrz3.EncodeAt(f, file, cfg)
					}
					return clrz3.EncodeTo(f, data, cfg)
				}
				rep, e := encode(f, data, clrz3.StreamConfig{Auto: 12, TempDir: dir, Observe: func(phase string) {
					if os.Getenv("Z4_PROFILE") == "1" {
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						fmt.Fprintf(os.Stderr, "%s heap=%d total=%d\n", phase, m.HeapAlloc, m.TotalAlloc)
						if phase == "references" {
							f, e := os.Create(filepath.Join(dir, "refs-allocs.pprof"))
							must(e)
							must(pprof.Lookup("allocs").WriteTo(f, 0))
							must(f.Close())
						}
					}
				}, Cut: func(r io.ReaderAt, s []idxref.Sec) ([]int, error) { return econx.UnitCutAt(r, s, 4096) }, Emit: emit})
				must(e)
				meta = rep.Meta
				runtime.KeepAlive(rep)
			} else if mode == "legacy" {
				rep, e := clrz3.Encode(f, data, clrz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
				must(e)
				meta = rep.Meta
				for _, c := range rep.Comps {
					must(emit(c))
				}
				runtime.KeepAlive(rep)
			} else {
				panic("unknown mode")
			}
			runtime.KeepAlive(data)
		} else {
			f, e := dexz3.Parse(file, info.Size(), dexz3.Limits{})
			must(e)
			if mode == "stream" {
				observe := func(phase string) {
					if os.Getenv("Z4_PROFILE") == "1" {
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						fmt.Fprintf(os.Stderr, "%s heap=%d total=%d\n", phase, m.HeapAlloc, m.TotalAlloc)
						if phase == "offsets" {
							f, e := os.Create(filepath.Join(dir, "offset-allocs.pprof"))
							must(e)
							must(pprof.Lookup("allocs").WriteTo(f, 0))
							must(f.Close())
						}
					}
				}
				rep, e := dexz3.EncodeAt(f, file, dexz3.StreamConfig{Auto: 12, TempDir: dir, Observe: observe, Cut: func(r io.ReaderAt, s []dexz3.Sec) ([]int, error) { return econx.UnitCutAt(r, s, 4096) }, Emit: emit})
				must(e)
				meta = rep.Meta
				runtime.KeepAlive(rep)
			} else if mode == "legacy" {
				data, e := os.ReadFile(path)
				must(e)
				rep, e := dexz3.Encode(f, data, dexz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
				must(e)
				meta = rep.Meta
				for _, c := range rep.Comps {
					must(emit(c))
				}
				runtime.KeepAlive(rep)
				runtime.KeepAlive(data)
			} else {
				panic("unknown mode")
			}

		}
		must(writer.Flush())
		must(out.Close())
		must(os.WriteFile(filepath.Join(dir, "meta"), meta, 0600))
	}
	close(done)
	<-stopped
	var usage syscall.Rusage
	must(syscall.Getrusage(syscall.RUSAGE_SELF, &usage))
	ms := sha256.Sum256(meta)
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": mode, "input": path, "bytes": info.Size(), "seconds": time.Since(start).Seconds(), "components": count, "meta_sha256": hex.EncodeToString(ms[:]), "stream_sha256": hex.EncodeToString(h.Sum(nil)), "heap_peak": peak.Load(), "max_rss_bytes": usage.Maxrss * 1024}))
}
