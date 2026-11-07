package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"

	"zeros3-z3/mdelta2"
	"zeros3-z3/modelz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// model8Chain runs the lab Model kind 8 (Z3-29 modelz3, the mechanical source of production kind 8)
// with its strongest bounded policy (per revision best of CDC, structural planes, anchor, previous,
// hybrid; hops <= 3) over ordered files. Returns per-state incremental charged bytes (pack rule +
// recipe) and exactness.
func model8Chain(paths []string, fixed bool) (inc []int64, total int64, exact bool, err error) {
	type f struct {
		src *modelz3.Src
		lay *modelz3.Layout
		fh  *os.File
		sz  int64
	}
	var files []f
	for _, p := range paths {
		fh, e := os.Open(p)
		if e != nil {
			return nil, 0, false, e
		}
		fi, _ := fh.Stat()
		m, e := mdelta2.Open(p)
		if e != nil {
			return nil, 0, false, e
		}
		isGGUF := m.Format == "gguf"
		m.Close()
		var lay *modelz3.Layout
		if isGGUF {
			lay, e = modelz3.GGUFLayout(fh, fi.Size())
		} else {
			lay, _, e = modelz3.STLayout(fh, fi.Size())
		}
		if e != nil {
			return nil, 0, false, e
		}
		files = append(files, f{&modelz3.Src{R: fh, Size: fi.Size(), Name: p}, lay, fh, fi.Size()})
	}
	rawBases := func(x f) map[string]*modelz3.BaseTensor {
		m := map[string]*modelz3.BaseTensor{}
		for _, t := range x.lay.Tensors() {
			b := make([]byte, t.Size())
			if _, e := x.fh.ReadAt(b, t.Begin); e != nil {
				return nil
			}
			m[t.Name] = &modelz3.BaseTensor{Name: t.Name, Type: t.Type, Shape: t.Shape, SHA: sha256.Sum256(b), Size: t.Size(), Src: x.src, Off: t.Begin, Width: t.Width, Raw: true}
		}
		return m
	}
	store := repz3.NewMemStore()
	pk := modelz3.NewPacker()
	var anchor, prev map[string]*modelz3.BaseTensor
	exact = true
	full := modelz3.Policy{Delta: true, Planes: true}
	hops := func(h int) modelz3.Policy { p := full; p.MaxHops = h; return p }
	for i, x := range files {
		enc := func(pol modelz3.Policy, bases []map[string]*modelz3.BaseTensor) (*kinds.Bundle, map[string]*modelz3.BaseTensor) {
			b, bts, _, e := modelz3.EncodeModel(x.src, x.lay, bases, store.Has, pol)
			if e != nil {
				err = e
			}
			return b, bts
		}
		type cand struct {
			b   *kinds.Bundle
			bts map[string]*modelz3.BaseTensor
			c   modelz3.Cost
		}
		var cs []cand
		add := func(b *kinds.Bundle, bts map[string]*modelz3.BaseTensor) {
			if b != nil {
				cs = append(cs, cand{b, bts, modelz3.DryCost(store, b, pk)})
			}
		}
		all := make([]byte, x.sz)
		x.fh.ReadAt(all, 0)
		if !fixed { // the greedy policy may pick CDC at first sight, which locks later deltas out (Z3-28)
			cb, e := kinds.CDC(all)
			if e != nil {
				return nil, 0, false, e
			}
			add(cb, rawBases(x))
		}
		big := x.sz > m8BigBytes() // the lab encoder holds whole tensors: limit candidates on large files
		if !big || fixed {
			b1, t1 := enc(modelz3.Policy{Planes: true}, nil)
			add(b1, t1)
		}
		if i > 0 {
			if !big && !fixed {
				b2, t2 := enc(hops(2), []map[string]*modelz3.BaseTensor{anchor})
				add(b2, t2)
				b3, t3 := enc(hops(3), []map[string]*modelz3.BaseTensor{prev})
				add(b3, t3)
			}
			b4, t4 := enc(hops(3), []map[string]*modelz3.BaseTensor{anchor, prev})
			add(b4, t4)
		}
		if err != nil {
			return nil, 0, false, err
		}
		best := 0
		for k := range cs {
			if cs[k].c.Total(true) < cs[best].c.Total(true) {
				best = k
			}
		}
		id, c, e := modelz3.Commit(store, cs[best].b)
		if e != nil {
			return nil, 0, false, e
		}
		inc = append(inc, c.Total(true))
		total += c.Total(true)
		env := kinds.NewEnvLimits(store, repz3.Limits{MaxDepth: repz3.HardMaxDepth, MaxWhole: 1 << 30})
		h := sha256.New()
		if _, _, e := env.Reconstruct(id, h); e != nil {
			exact = false
		} else {
			// compare with the original digest
			if fmt.Sprintf("%x", h.Sum(nil)) != fmt.Sprintf("%x", sha256.Sum256(all)) {
				exact = false
			}
		}
		if i == 0 {
			anchor = cs[best].bts
			if anchor == nil {
				anchor = rawBases(x)
			}
		}
		prev = cs[best].bts
		if prev == nil {
			prev = rawBases(x)
		}
	}
	return inc, total, exact, nil
}

// m8BigBytes: files above this size get only the CDC and anchor|previous candidates (memory).
// Z4B5_M8BIG_MB overrides the 600 MB default used by the Z3-29 study.
func m8BigBytes() int64 {
	if v, err := strconv.Atoi(os.Getenv("Z4B5_M8BIG_MB")); err == nil && v > 0 {
		return int64(v) << 20
	}
	return 600 << 20
}
