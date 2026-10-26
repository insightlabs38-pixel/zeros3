package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"zeros3-z3/prodshadow"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func heapPeakKiB() int64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.Sys) >> 10
}

// restart: for the final store of each chain, loose and packed (compressed): stop the
// store, start it again (locator reload, nothing in memory but the persisted lab
// roots), reconstruct the shadow and GET the authoritative object; then time
// whole reconstruction, random point reads and 8 concurrent readers.
func (c config) restart() {
	chs, err := c.workloads()
	must(err)
	fmt.Println("== restart / locator rebuild / throughput (final store of each chain)")
	fmt.Printf("%-24s %-7s | open ms | roots | GET | shadow whole MiB/s | 1 reader reads/s | 8 readers reads/s | bridge VmHWM MiB | catalog rebuild\n", "chain", "state")
	for _, ch := range chs {
		for _, state := range []string{"loose", "packed"} {
			base := filepath.Join(c.work, "chain-"+filepath.Base(ch.Name), "Q")
			dst := filepath.Join(c.work, "restart-"+filepath.Base(ch.Name)+"-"+state)
			os.RemoveAll(dst)
			l, err := prodshadow.NewLab(c.b, dst, prodshadow.Options{})
			must(err)
			must(prodshadow.CopyDir(filepath.Join(base, "store"), l.Store.Dir))
			must(prodshadow.CopyDir(filepath.Join(base, "shadow"), l.Meta.Dir))
			if state == "packed" {
				must(l.Start())
				live := filepath.Join(dst, "live")
				_, err = l.LiveFile(live)
				must(err)
				must(l.Stop())
				_, err = l.Store.Compact(c.packMiB, true, live)
				must(err)
			}
			// restart twice: the second start proves nothing hidden survived the first
			must(l.Start())
			must(l.Stop())
			must(l.Start())
			data := ch.Stages[len(ch.Stages)-1].Data
			t0 := time.Now()
			roots, bad := l.Sh.Adopt()
			adopt := time.Since(t0)
			var rep repz3.SHA
			for _, r := range roots {
				if r.ObjSHA == sha256.Sum256(data) {
					rep = r.Rep
				}
			}
			getOK := l.CheckGET("obj", data) == nil
			var out bytes.Buffer
			t0 = time.Now()
			_, sum, err := l.Sh.Env().Reconstruct(rep, &out)
			el := time.Since(t0)
			exact := err == nil && sum == sha256.Sum256(data) && bytes.Equal(out.Bytes(), data)
			// point reads over all stored components
			reach, err := l.Sh.Env().Reachable(rep)
			must(err)
			var comps []repz3.SHA
			for id := range reach.Role {
				comps = append(comps, id)
			}
			rate := func(workers int) float64 {
				var wg sync.WaitGroup
				var mu sync.Mutex
				total := 0
				t0 := time.Now()
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						rng := rand.New(rand.NewSource(int64(w)))
						n := 0
						for time.Since(t0) < 1500*time.Millisecond {
							id := comps[rng.Intn(len(comps))]
							sz, _ := l.Sh.Src.Size(id)
							if sz == 0 {
								continue
							}
							p := make([]byte, min(sz, 4096))
							l.Sh.Src.ReadAt(id, p, rng.Int63n(sz-int64(len(p))+1))
							n++
						}
						mu.Lock()
						total += n
						mu.Unlock()
					}(w)
				}
				wg.Wait()
				return float64(total) / time.Since(t0).Seconds()
			}
			l.Sh.Src.Reset()
			l.Sh.Src.AddDescriptors(l.Sh.Src.AllDescs(roots))
			r1 := rate(1)
			r8 := rate(8)
			var reps []repz3.SHA
			for _, r := range roots {
				reps = append(reps, r.Rep)
			}
			t0 = time.Now()
			cat, cerr := repz3.Rebuild(l.Sh.Src, kinds.Domain, reps)
			catT := time.Since(t0)
			catNote := "n/a"
			if cerr == nil && cat.Entries() > 0 {
				catNote = fmt.Sprintf("%v/%d keys", catT.Round(time.Millisecond), cat.Entries())
			}
			rss := l.Br.PeakRSSKiB()
			open := l.Br.OpenMS
			must(l.Stop())
			fmt.Printf("%-24s %-7s | %7.1f | %d/%d bad | %v | %7.1f exact=%v (adopt %v) | %8.0f | %8.0f | %d | %s\n", ch.Name, state, open, len(roots), len(bad), getOK,
				float64(len(data))/(1<<20)/el.Seconds(), exact, adopt.Round(time.Microsecond), r1, r8, rss>>10, catNote)
		}
	}
	_ = kinds.Prove
	fmt.Printf("z3shadow process peak sys memory: %d MiB\n", heapPeakKiB()>>10)
}
