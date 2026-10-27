package main

import (
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/modelz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// runShards charges a sharded checkpoint as a set of Safetensors objects (every shard, the index JSON, the
// base dependencies): base = SmolLM2-135M base, target = SmolLM2-135M Instruct (a real revision), both
// re-sharded from the real files (REAL_REEXPORT: same tensor bytes, shard assignment is synthetic).
func runShards(s *store, work string) {
	type set struct {
		name   string
		files  []*file
		index  []byte
		layout []*modelz3.Layout
	}
	build := func(tag, src string, n int) *set {
		f := s.get(src)
		files, idx, err := modelz3.RewriteST(f.f, f.Size, modelz3.STEdit{Shards: n})
		check(err)
		out := &set{name: fmt.Sprintf("%s/%d shards", tag, n), index: idx}
		for i, b := range files {
			p := filepath.Join(work, fmt.Sprintf("%s.%d.%d.safetensors", tag, n, i))
			check(os.WriteFile(p, b, 0o644))
			fh, err := os.Open(p)
			check(err)
			l, _, err := modelz3.STLayout(fh, int64(len(b)))
			check(err)
			out.files = append(out.files, &file{Name: p, Path: p, Size: int64(len(b)), f: fh, src: &modelz3.Src{R: fh, Size: int64(len(b)), Name: p}, lay: l, cuts: map[string]modelz3.CompList{}})
			out.layout = append(out.layout, l)
		}
		return out
	}
	pk := modelz3.NewPacker()
	fmt.Printf("%-52s | %9s %9s | %s\n", "sharded revision pair (cost % of target set bytes, pack rule)", "cdc", "kind-delta", "notes")
	for _, c := range []struct{ an, bn int }{{3, 3}, {3, 5}} {
		base := build("base", "st-smollm2-star/base", c.an)
		tgt := build("inst", "st-smollm2-star/instruct", c.bn)
		var total int64
		for _, f := range tgt.files {
			total += f.Size
		}
		total += int64(len(tgt.index))
		// CDC: shards + index as ordinary objects
		cseen := modelz3.Seen{}
		for _, f := range base.files {
			cseen.Add(f.cut(modelz3.CutCDC))
		}
		var ccost int64
		for _, f := range tgt.files {
			cc, err := modelz3.CostOf(f.cut(modelz3.CutCDC), cseen, pk)
			check(err)
			cseen.Add(f.cut(modelz3.CutCDC))
			ccost += cc.Total(true)
		}
		// kind-delta: base shards as stored components (planes allowed), every target shard anchored on the union of base tensors
		store := repz3.NewMemStore()
		anchor := map[string]*modelz3.BaseTensor{}
		for _, f := range base.files {
			b, bts, _, err := modelz3.EncodeModel(f.src, f.lay, nil, store.Has, modelz3.Policy{})
			check(err)
			_, _, err = modelz3.Commit(store, b)
			check(err)
			for k, v := range bts {
				anchor[k] = v
			}
		}
		var kcost int64
		ok := true
		for _, f := range tgt.files {
			b, _, _, err := modelz3.EncodeModel(f.src, f.lay, []map[string]*modelz3.BaseTensor{anchor}, store.Has, modelz3.Policy{Delta: true, Planes: true, MaxHops: 2})
			check(err)
			id, cost, err := modelz3.Commit(store, b)
			check(err)
			kcost += cost.Total(true)
			ex, _ := reconstructOK(store, id, f)
			ok = ok && ex
		}
		// the index JSON is one more object (CDC-cut)
		ib, err := kinds.CDC(tgt.index)
		check(err)
		_, ic, err := modelz3.Commit(store, ib)
		check(err)
		kcost += ic.Total(true)
		fmt.Printf("%-52s | %8.1f%% %8.1f%% | %d target shards + index %d B, every shard exact %v\n", fmt.Sprintf("base %d shards > instruct %d shards", c.an, c.bn), pct(ccost+int64(len(tgt.index)), total), pct(kcost, total), len(tgt.files), len(tgt.index), ok)
	}
}
