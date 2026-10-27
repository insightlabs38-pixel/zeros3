package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"runtime"
	"sort"
	"time"

	"zeros3-z3/modelz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// runReads measures what the KindModel delta representation fetches for a whole GET, one tensor and
// a 4 KiB range, split into residual, base and representation bytes. Revision 0 is the stored base
// (tensors as components), revision 1 the anchored delta.
func runReads(s *store) {
	type cs struct{ name, format, a, b string }
	cases := []cs{
		{"Qwen2.5-0.5B BF16 Instruct vs base", "st", "st-qwen05/base", "st-qwen05/instruct"},
		{"SmolLM2-135M BF16 sft-only vs instruct (sibling)", "st", "st-smollm2-star/instruct", "st-smollm2-star/sft-only"},
		{"SmolLM2-135M BF16 shakespeare vs base", "st", "st-smollm2-star/base", "st-smollm2-star/shakespeare"},
		{"SmolLM2-135M Q4_K_M GGUF instruct vs base", "gguf", "gguf-smollm2/qf-base-Q4_K_M", "gguf-smollm2/qf-inst-Q4_K_M"},
		{"Qwen3-0.6B Q4_K_M GGUF post vs base", "gguf", "gguf-qwen3/mr-base-Q4_K_M", "gguf-qwen3/mr-post-Q4_K_M"},
	}
	fmt.Printf("%-50s %-22s %10s | %10s %10s %8s | %6s %8s\n", "case", "read", "requested", "residual", "base", "rep+hdr", "ampl", "ms")
	for _, c := range cases {
		a, b := s.get(c.a), s.get(c.b)
		layoutOf(a, c.format)
		layoutOf(b, c.format)
		store := repz3.NewMemStore()
		b0, bts0, _, err := modelz3.EncodeModel(a.src, a.lay, nil, store.Has, modelz3.Policy{})
		check(err)
		_, _, err = modelz3.Commit(store, b0)
		check(err)
		te := time.Now()
		b1, _, st1, err := modelz3.EncodeModel(b.src, b.lay, []map[string]*modelz3.BaseTensor{bts0}, store.Has, modelz3.Policy{Delta: true, Planes: true, MaxHops: 2})
		check(err)
		encDur := time.Since(te)
		id, cost, err := modelz3.Commit(store, b1)
		check(err)
		reps := map[repz3.SHA]bool{b1.ID: true}
		for _, k := range b1.Children {
			reps[k.ID] = true
		}
		env := kinds.NewEnvLimits(store, repz3.Limits{MaxDepth: repz3.HardMaxDepth, MaxWhole: 1 << 30})
		meas := func(label string, requested int64, f func() error) {
			store.ResetReads()
			t0 := time.Now()
			check(f())
			el := time.Since(t0)
			_, by := store.Touched()
			var res, base, rep int64
			for sid, n := range by {
				switch {
				case reps[sid]:
					rep += n
				case st1.ResIDs[sid]:
					res += n
				default:
					base += n
				}
			}
			fmt.Printf("%-50s %-22s %10d | %10d %10d %8d | %6.2f %8.1f\n", c.name, label, requested, res, base, rep, float64(res+base+rep)/float64(requested), float64(el.Microseconds())/1000)
		}
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		meas("whole GET", b.Size, func() error { _, _, err := env.Reconstruct(id, io.Discard); return err })
		runtime.ReadMemStats(&m1)
		fmt.Printf("%-50s decoder allocation during the whole GET: %.3f x output (TotalAlloc), live heap change %+d KiB\n", c.name, float64(m1.TotalAlloc-m0.TotalAlloc)/float64(b.Size), (int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/1024)
		ts := append([]*modelz3.Seg(nil), b.lay.Tensors()...)
		sort.Slice(ts, func(i, j int) bool { return ts[i].Size() < ts[j].Size() })
		for _, pick := range []struct {
			n string
			t *modelz3.Seg
		}{{"largest tensor", ts[len(ts)-1]}, {"median tensor", ts[len(ts)/2]}} {
			t := pick.t
			meas(pick.n, t.Size(), func() error {
				cl, err := env.Open(id)
				if err != nil {
					return err
				}
				got, err := kinds.ReadModelRange(cl, t.Begin, t.Size())
				if err == nil {
					want := make([]byte, t.Size())
					check(readAtFull(b, want, t.Begin))
					if sha256.Sum256(got) != sha256.Sum256(want) {
						return fmt.Errorf("tensor read differs")
					}
				}
				return err
			})
		}
		m := ts[len(ts)/2]
		meas("4 KiB in median tensor", 4096, func() error {
			cl, err := env.Open(id)
			if err != nil {
				return err
			}
			_, err = kinds.ReadModelRange(cl, m.Begin+m.Size()/2, 4096)
			return err
		})
		// the same whole GET and a 4 KiB read through a store that holds objects packed (DEFLATE under the pack rule)
		ps := modelz3.NewPackedSource(store)
		penv := kinds.NewEnvLimits(ps, repz3.Limits{MaxDepth: repz3.HardMaxDepth, MaxWhole: 1 << 30})
		t0 := time.Now()
		_, sum, err := penv.Reconstruct(id, io.Discard)
		check(err)
		el := time.Since(t0)
		want := sha256.New()
		_, err = io.Copy(want, io.NewSectionReader(b.f, 0, b.Size))
		check(err)
		fmt.Printf("%-50s %-22s %10d | packed store: %.0f MiB/s end to end (inflated %d B), exact %v\n", c.name, "whole GET (packed)", b.Size, float64(b.Size)/1048576/el.Seconds(), ps.Inflate, string(sum[:]) == string(want.Sum(nil)))
		ps.Inflate = 0
		t0 = time.Now()
		cl, err := penv.Open(id)
		check(err)
		_, err = kinds.ReadModelRange(cl, m.Begin+m.Size()/2, 4096)
		check(err)
		fmt.Printf("%-50s %-22s %10d | packed store: %.2f ms, inflated %d B of residual+base records\n", c.name, "4 KiB (packed)", 4096, float64(time.Since(t0).Microseconds())/1000, ps.Inflate)
		var resObjs [][]byte
		for rid := range st1.ResIDs {
			resObjs = append(resObjs, store.Bytes(rid))
			if len(resObjs) == 64 {
				break
			}
		}
		fmt.Printf("%-50s residual packed %.1f%% of file (%d components), store-side inflate %.0f MiB/s, analysis encode %.0f MiB/s (4 cores, 3 DEFLATE passes per tensor)\n", c.name, pct(cost.Packed+cost.Recipe, b.Size), len(st1.ResIDs), inflateMiBs(resObjs), float64(b.Size)/1048576/encDur.Seconds())
	}
	printHWM()
}
