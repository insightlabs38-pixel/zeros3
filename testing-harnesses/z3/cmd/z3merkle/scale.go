package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"zeros3-z3/merklez3"
)

type peak struct {
	max  atomic.Uint64
	done chan struct{}
	fin  chan struct{}
}

func startPeak() *peak {
	p := &peak{done: make(chan struct{}), fin: make(chan struct{})}
	go func() {
		defer close(p.fin)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > p.max.Load() {
				p.max.Store(m.HeapAlloc)
			}
			select {
			case <-p.done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return p
}

func (p *peak) stop() uint64 { close(p.done); <-p.fin; return p.max.Load() }

func buildDisk(dir string, s *merklez3.State, cfg merklez3.Config) (*merklez3.DiskStore, merklez3.ID, merklez3.BuildStats, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", merklez3.BuildStats{}, err
	}
	db, err := merklez3.NewDiskBuilder(dir, len(cfg.Hash.Sum(nil)))
	if err != nil {
		return nil, "", merklez3.BuildStats{}, err
	}
	root, st, err := s.Build(cfg, db.Put)
	if err != nil {
		return nil, "", st, err
	}
	ds, err := db.Finish(merklez3.DecodeKids)
	return ds, root, st, err
}

func cmdScale(args []string) error {
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	keys := fs.String("keys", "25000,100000,400000,1600000,3200000", "comma-separated key counts")
	dir := fs.String("dir", os.TempDir(), "scratch directory")
	frac := fs.Float64("frac", 0.0001, "fraction of keys changed (scattered)")
	fs.Parse(args)
	cfg := merklez3.DefaultConfig()
	fmt.Printf("disk-backed stores (node log + sorted index, ReadAt); %.4g%% of keys changed (scattered)\n\n| keys | nodes | snapshot MiB | new nodes | guided: nodes read | hash compares | guided time | full-scan time (nodes read) | traversal peak heap MiB | max stack |\n|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n", *frac*100)
	for _, f := range strings.Split(*keys, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return err
		}
		root := fmt.Sprintf("%s/z3merkle-scale-%d", *dir, n)
		func() {
			defer os.RemoveAll(root)
			base := merklez3.NewState(1, n)
			tgt := base.Update(base.PickScattered(31, max(1, int(float64(n)**frac))))
			bs, broot, bst, err := buildDisk(root+"/base", base, cfg)
			if err != nil {
				fatal(err)
			}
			ts, troot, _, err := buildDisk(root+"/target", tgt, cfg)
			if err != nil {
				fatal(err)
			}
			base, tgt = nil, nil
			defer bs.Close()
			defer ts.Close()
			runtime.GC()
			var m0 runtime.MemStats
			runtime.ReadMemStats(&m0)
			pk := startPeak()
			t0 := time.Now()
			st, err := merklez3.Discover(ts, troot, bs.Has, nil, nil)
			dt := time.Since(t0)
			hp := pk.stop()
			if err != nil {
				fatal(err)
			}
			_ = broot
			// full scan of the target (component-by-component enumeration)
			runtime.GC()
			t0 = time.Now()
			ws, err := merklez3.Walk(ts, troot, nil)
			wt := time.Since(t0)
			if err != nil {
				fatal(err)
			}
			over := int64(hp) - int64(m0.HeapAlloc)
			if over < 0 {
				over = 0
			}
			fmt.Printf("| %d | %d | %.0f | %d | %d | %d | %v | %v (%d) | %.2f | %d |\n", n, bst.Nodes, float64(bst.Bytes)/(1<<20), st.NewNodes, st.NodesRead, st.HashCompares, dt.Round(time.Microsecond), wt.Round(time.Millisecond), ws.NodesRead, float64(over)/(1<<20), st.MaxFrontier)
		}()
	}
	return nil
}
