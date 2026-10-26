package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Env struct {
	Work string
	Keep map[string]bool // "<lane>/<rev>" images kept for the read study
}

func (e *Env) dir(parts ...string) string {
	p := filepath.Join(append([]string{e.Work}, parts...)...)
	os.MkdirAll(p, 0o755)
	return p
}

func dump(path string, v any) {
	b, _ := json.MarshalIndent(v, "", " ")
	os.WriteFile(path, b, 0o644)
}

// runFamilyA rebuilds each logical state independently in every lane.
func runFamilyA(e *Env, lanes []Lane, limit int) error {
	revs := familyA()
	if limit > 0 && limit < len(revs) {
		revs = revs[:limit]
	}
	acct := map[string]*LaneAcct{}
	for _, l := range lanes {
		acct[l.Name] = NewLaneAcct(l)
	}
	gf := NewGuestFloor()
	var prev State
	var rows []Row
	var mu sync.Mutex
	for _, rv := range revs {
		raw := filepath.Join(e.dir("raw"), rv.Name+".raw")
		if err := rv.Disk.WriteRaw(raw); err != nil {
			return err
		}
		gc, st, err := gf.Add(rv.Disk, prev)
		if err != nil {
			return err
		}
		prev = st
		fmt.Printf("== %s (%s): guest floor changed=%d runs=%d new=%dKiB total=%dKiB\n", rv.Name, rv.Note, gc.Changed, gc.Runs, gc.NewRaw>>10, gc.Total>>10)
		var wg sync.WaitGroup
		errs := make(chan error, len(lanes))
		for _, l := range lanes {
			wg.Add(1)
			go func(l Lane) {
				defer wg.Done()
				dir := e.dir("A", l.Name)
				img := filepath.Join(dir, rv.Name+"."+l.Fmt)
				seed := "A/" + l.Name + "/" + rv.Name
				var err error
				if rv.Name == "R8" || rv.Name == "R8b" {
					if l.Compress {
						return // qemu-img cannot write compressed clusters out of order into an existing image
					}
					mode := map[string]string{"R8": "desc", "R8b": "shuf"}[rv.Name]
					err = buildHistory(l, rv.Disk, e.dir("hist", l.Name), img, seed, mode)
				} else {
					err = build(l, raw, img, seed, nil, "")
				}
				if err != nil {
					errs <- err
					return
				}
				if err := verify(l, img, raw, rv.Disk, nil); err != nil {
					errs <- fmt.Errorf("%s %s: %w", l.Name, rv.Name, err)
					return
				}
				row, err := acct[l.Name].Analyse(rv.Name, img)
				if err != nil {
					errs <- err
					return
				}
				row.Verified, row.Guest = true, gc
				mu.Lock()
				rows = append(rows, row)
				mu.Unlock()
				fmt.Printf("  %-11s size=%6dKiB alloc=%6dKiB cdc=%8dKiB native(flat/delta)=%7d/%7dKiB mid=%7d fine=%7d reloc=%d new=%d\n", l.Name, row.Size>>10, row.Alloc>>10,
					row.CDC.Incr>>10, row.Native.Flat>>10, row.Native.Delta>>10, row.Mid.Delta>>10, row.Fine.Delta>>10, row.Native.Relocated, row.Native.NewPieces)
				if !e.Keep[l.Name+"/"+rv.Name] {
					os.Remove(img)
				}
			}(l)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			return err
		}
		os.Remove(raw)
		dump(filepath.Join(e.dir("res"), "famA.json"), rows)
	}
	return nil
}
