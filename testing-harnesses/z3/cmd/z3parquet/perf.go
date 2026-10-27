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
	"strings"
	"time"

	"zeros3-z3/parquetz3"
)

func rssKiB() (hwm, cur int64) {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		var v int64
		if strings.HasPrefix(l, "VmHWM:") {
			fmt.Sscanf(strings.TrimSpace(l[6:]), "%d", &v)
			hwm = v
		}
		if strings.HasPrefix(l, "VmRSS:") {
			fmt.Sscanf(strings.TrimSpace(l[6:]), "%d", &v)
			cur = v
		}
	}
	return
}

// diskStore keeps components as files named by digest.
type diskStore struct {
	dir  string
	seen map[parquetz3.ID]int64
	n    int
	byt  int64
}

func (d *diskStore) emit(class string, id parquetz3.ID, b []byte) error {
	if _, ok := d.seen[id]; ok {
		return nil
	}
	d.seen[id] = int64(len(b))
	d.n++
	d.byt += int64(len(b))
	return os.WriteFile(filepath.Join(d.dir, hex.EncodeToString(id[:])), b, 0o644)
}

func (d *diskStore) get(id parquetz3.ID) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.dir, hex.EncodeToString(id[:])))
}

// cmdPerf measures one phase on one (large) file in its own process, so the peak resident
// set of the phase is its own: parse, analyze (logical decode + layout/codec verification
// of every page, no components), encode (to a directory store), decode (from it), replay.
func cmdPerf(args []string) error {
	fs := flag.NewFlagSet("perf", flag.ExitOnError)
	path := fs.String("file", "", "parquet file")
	phase := fs.String("phase", "parse", "parse|analyze|encode|decode|replay")
	dir := fs.String("store", "/tmp/z3pq-store", "component directory")
	target := fs.Int("target", 8<<10, "group target")
	fs.Parse(args)
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	t0 := time.Now()
	var extra string
	switch *phase {
	case "parse":
		pf, err := parquetz3.Open(f, size, parquetz3.Limits{})
		if err != nil {
			return err
		}
		pages := 0
		for gi := range pf.RowGroups {
			for ci := range pf.RowGroups[gi].Chunks {
				pf.Pages(f, &pf.RowGroups[gi].Chunks[ci], func(*parquetz3.Page, []byte) error { pages++; return nil })
			}
		}
		extra = fmt.Sprintf("%d row groups, %d pages", len(pf.RowGroups), pages)
	case "analyze":
		pf, err := parquetz3.Open(f, size, parquetz3.Limits{})
		if err != nil {
			return err
		}
		an := parquetz3.NewAnalyzer(f, pf, parquetz3.Limits{})
		var pages, opaque int
		for gi := range pf.RowGroups {
			for ci := range pf.RowGroups[gi].Chunks {
				plan, err := an.Chunk(&pf.RowGroups[gi].Chunks[ci], nil, nil)
				if err != nil {
					return err
				}
				pages += len(plan.Pages)
				if plan.Opaque {
					opaque++
				}
			}
		}
		extra = fmt.Sprintf("%d logical pages verified, %d opaque chunks", pages, opaque)
	case "encode":
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			return err
		}
		ds := &diskStore{dir: *dir, seen: map[parquetz3.ID]int64{}}
		rec, sz, stats, err := parquetz3.Encode(f, size, parquetz3.Options{Target: *target}, ds.emit)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*dir, "recipe.bin"), rec, 0o644); err != nil {
			return err
		}
		extra = fmt.Sprintf("%d components (%s stored), recipe %d B (%d members), val groups %d, lvl groups %d, nodes %d, pages %d", ds.n, human(ds.byt), len(rec), stats.Members, stats.ValueGroups, stats.LevelGroups, stats.Nodes, stats.Pages)
		_ = sz
	case "decode":
		rec, err := os.ReadFile(filepath.Join(*dir, "recipe.bin"))
		if err != nil {
			return err
		}
		ds := &diskStore{dir: *dir}
		h := sha256.New()
		var n int64
		w := countWriter{h, &n}
		t0 = time.Now()
		if err := parquetz3.Decode(w, rec, ds.get, parquetz3.Limits{}); err != nil {
			return err
		}
		want := sha256.New()
		f.Seek(0, io.SeekStart)
		io.Copy(want, f)
		ok := bytes.Equal(h.Sum(nil), want.Sum(nil)) && n == size
		extra = fmt.Sprintf("exact=%v (%d bytes)", ok, n)
		if !ok {
			return fmt.Errorf("decode differs")
		}
	case "replay":
		pf, err := parquetz3.Open(f, size, parquetz3.Limits{})
		if err != nil {
			return err
		}
		an := parquetz3.NewAnalyzer(f, pf, parquetz3.Limits{})
		type pb struct {
			body []byte
			rec  parquetz3.CompRec
			pg   parquetz3.Page
		}
		var pbs []pb
		var total int64
		for gi := 0; gi < len(pf.RowGroups) && total < 256<<20; gi++ {
			for ci := range pf.RowGroups[gi].Chunks {
				ch := &pf.RowGroups[gi].Chunks[ci]
				pf.Pages(f, ch, func(p *parquetz3.Page, _ []byte) error {
					body, rec, err := an.PageBody(ch, p)
					if err == nil {
						pbs = append(pbs, pb{body, rec, *p})
						total += int64(len(body))
					}
					return nil
				})
			}
		}
		t0 = time.Now()
		var out int64
		for _, p := range pbs {
			b, err := an.PagePayload(&p.pg, p.body, p.rec)
			if err != nil {
				return err
			}
			out += int64(len(b))
		}
		d := time.Since(t0)
		fmt.Printf("replay: %d pages, %s of bodies -> %s payload in %.2fs = %.0f MiB/s of bodies\n", len(pbs), human(total), human(out), d.Seconds(), float64(total)/(1<<20)/d.Seconds())
		hwm, _ := rssKiB()
		fmt.Printf("peak RSS %d MiB\n", hwm>>10)
		return nil
	}
	d := time.Since(t0)
	hwm, cur := rssKiB()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("%s %s: %s in %.2fs = %.0f MiB/s; peak RSS %d MiB (now %d), heap in use %d MiB, total allocated %d MiB; %s\n", *phase, filepath.Base(*path), human(size), d.Seconds(), float64(size)/(1<<20)/d.Seconds(), hwm>>10, cur>>10, ms.HeapInuse>>20, ms.TotalAlloc>>20, extra)
	return nil
}

type countWriter struct {
	h io.Writer
	n *int64
}

func (c countWriter) Write(p []byte) (int, error) { *c.n += int64(len(p)); return c.h.Write(p) }
