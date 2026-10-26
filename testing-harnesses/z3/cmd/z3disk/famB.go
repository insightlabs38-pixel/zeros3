package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"zeros3-z3/diskimg"
)

// runFamilyB builds B0, children C1..C6 (parent B0) and grandchild C7 (parent C6) in every lane. Images are kept.
func runFamilyB(e *Env, lanes []Lane) error {
	revs := familyB()
	acct := map[string]*LaneAcct{}
	for _, l := range lanes {
		acct[l.Name] = NewLaneAcct(l)
	}
	gf := NewGuestFloor()
	states := map[string]State{}
	imgs := map[string]map[string]string{} // lane -> rev -> path
	raws := map[string]string{}
	for _, l := range lanes {
		imgs[l.Name] = map[string]string{}
	}
	var rows []Row
	var mu sync.Mutex
	disks := map[string]*diskimg.Disk{}
	for _, rv := range revs {
		disks[rv.Name] = rv.Disk
		raw := filepath.Join(e.dir("raw"), "B-"+rv.Name+".raw")
		if err := rv.Disk.WriteRaw(raw); err != nil {
			return err
		}
		raws[rv.Name] = raw
		gc, st, err := gf.Add(rv.Disk, states[rv.Parent])
		if err != nil {
			return err
		}
		states[rv.Name] = st
		extra := ""
		if rv.Name == "C7" {
			own, inh := 0, 0
			for s, id := range st {
				if pid, ok := states["C6"][s]; ok && pid == id {
					continue
				}
				// changed vs C6: "inherited" if C6 still carries the base's content there, else the sector was already child-owned
				b0, inB0 := states["B0"][s]
				c6, inC6 := states["C6"][s]
				if inB0 == inC6 && b0 == c6 {
					inh++
				} else {
					own++
				}
			}
			extra = fmt.Sprintf(" [changed non-zero sectors: child-owned-before=%d inherited-from-base=%d]", own, inh)
		}
		fmt.Printf("== %s (parent %s; %s): parent-aware floor changed=%d runs=%d new=%dKiB total=%dKiB%s\n", rv.Name, rv.Parent, rv.Note, gc.Changed, gc.Runs, gc.NewRaw>>10, gc.Total>>10, extra)
		var wg sync.WaitGroup
		errs := make(chan error, len(lanes))
		for _, l := range lanes {
			wg.Add(1)
			go func(l Lane) {
				defer wg.Done()
				dir := e.dir("B", l.Name)
				img := filepath.Join(dir, rv.Name+"."+l.Fmt)
				var par *Parent
				var chain []string
				if rv.Parent != "" {
					pd := disks[rv.Parent]
					par = &Parent{Img: imgs[l.Name][rv.Parent], Raw: raws[rv.Parent], Delta: rv.Disk.Writes[len(pd.Writes):], Size: rv.Disk.Size}
					for p := rv.Parent; p != ""; p = parentOf(revs, p) {
						chain = append(chain, imgs[l.Name][p])
					}
				}
				if err := build(l, raw, img, "B/"+l.Name+"/"+rv.Name, par, ""); err != nil {
					errs <- err
					return
				}
				if err := verify(l, img, raw, rv.Disk, chain); err != nil {
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
				imgs[l.Name][rv.Name] = img
				rows = append(rows, row)
				mu.Unlock()
				fmt.Printf("  %-11s size=%6dKiB alloc=%6dKiB cdc=%8dKiB native(flat/delta)=%7d/%7dKiB mid=%7d fine=%7d states=%v\n", l.Name, row.Size>>10, row.Alloc>>10,
					row.CDC.Incr>>10, row.Native.Flat>>10, row.Native.Delta>>10, row.Mid.Delta>>10, row.Fine.Delta>>10, row.Comps)
			}(l)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			return err
		}
		if rv.Name != "B0" && rv.Name != "C6" {
			os.Remove(raw)
			delete(raws, rv.Name)
		}
		dump(filepath.Join(e.dir("res"), "famB.json"), rows)
	}
	for _, r := range raws {
		os.Remove(r)
	}
	return nil
}

func parentOf(revs []Rev, name string) string {
	for _, r := range revs {
		if r.Name == name {
			return r.Parent
		}
	}
	return ""
}
