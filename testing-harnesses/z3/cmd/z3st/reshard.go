package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/st"
)

// shardSet re-packs the tensors of c into shards of about target bytes, in the
// given tensor order. Tensors stay byte-identical; only their container moves.
func shardSet(c *ckpt, target int64, order []string) []*ckpt {
	var out []*ckpt
	var cur []st.BuildTensor
	var size int64
	flush := func() {
		if len(cur) == 0 {
			return
		}
		b := st.Build([]st.KV{{K: "format", V: "pt"}}, cur)
		f, err := st.ParseBytes(b, st.Options{})
		if err != nil {
			fatal(err)
		}
		out = append(out, &ckpt{label: fmt.Sprintf("shard%d", len(out)+1), data: b, f: f})
		cur, size = nil, 0
	}
	for _, n := range order {
		t, _ := c.f.Lookup(n)
		cur = append(cur, st.BuildTensor{Name: n, DType: t.DType, Shape: t.Shape, Data: c.payload(t)})
		size += t.Size()
		if size >= target {
			flush()
		}
	}
	flush()
	return out
}

// reshardCmd (Z3-09L): tensor identity across re-sharding. Real tensors of one
// checkpoint are repacked into different shard sets (constructed, not
// downloaded); a format-aware store sees identical tensors, raw CDC does not
// when the tensors move relative to shard boundaries.
func reshardCmd(args []string) {
	fs := flag.NewFlagSet("reshard", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "rw-pythia14m-train", "family globs")
	which := fs.String("label", "", "checkpoint label to reshard (default: the last)")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| source | shard sets (A -> B) | tensors | raw CDC: new bytes % of B | CDC of B given A, one tensor changed % | tensor identity: new tensor bytes % | tensor identity, one tensor changed % |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		c := cks[fam]
		src := c[len(c)-1]
		for _, x := range c {
			if x.label == *which {
				src = x
			}
		}
		var names []string
		for i := range src.f.Tensors {
			names = append(names, src.f.Tensors[i].Name)
		}
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		reversed := append([]string(nil), names...)
		for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
			reversed[i], reversed[j] = reversed[j], reversed[i]
		}
		total := int64(len(src.data))
		cases := []struct {
			name       string
			aT, bT     int64
			aOrd, bOrd []string
		}{
			{"2 shards (file order) -> 3 shards (file order)", total / 2, total / 3, names, names},
			{"4 shards (file order) -> 4 shards (name order)", total / 4, total / 4, names, sorted},
			{"3 shards (file order) -> 5 shards (reverse order)", total / 3, total / 5, names, reversed},
		}
		for _, cs := range cases {
			A := shardSet(src, cs.aT, cs.aOrd)
			B := shardSet(src, cs.bT, cs.bOrd)
			// variant: one tensor of B changed (a single flipped byte in the middle of the largest)
			Bm := shardSet(src, cs.bT, cs.bOrd)
			mod := 0
			for k, s := range Bm {
				for i := range s.f.Tensors {
					if s.f.Tensors[i].Size() > Bm[mod].f.Tensors[0].Size() {
						mod = k
					}
				}
			}
			big, bigS := -1, int64(0)
			for i := range Bm[mod].f.Tensors {
				if Bm[mod].f.Tensors[i].Size() > bigS {
					big, bigS = i, Bm[mod].f.Tensors[i].Size()
				}
			}
			t := &Bm[mod].f.Tensors[big]
			Bm[mod].data[Bm[mod].f.DataStart+t.Begin+t.Size()/2] ^= 0x10

			cdcNew := func(B []*ckpt) int64 {
				have := map[[32]byte]bool{}
				for _, a := range A {
					forChunks(a, func(h [32]byte, _ int) { have[h] = true })
				}
				var n int64
				for _, b := range B {
					forChunks(b, func(h [32]byte, sz int) {
						if !have[h] {
							n += int64(sz)
						}
					})
				}
				return n
			}
			tensNew := func(B []*ckpt) int64 {
				have := map[[32]byte]bool{}
				for _, a := range A {
					for i := range a.f.Tensors {
						have[sha256.Sum256(a.payload(&a.f.Tensors[i]))] = true
					}
				}
				var n int64
				for _, b := range B {
					for i := range b.f.Tensors {
						if !have[sha256.Sum256(b.payload(&b.f.Tensors[i]))] {
							n += b.f.Tensors[i].Size()
						}
					}
				}
				return n
			}
			var bBytes int64
			for _, b := range B {
				bBytes += int64(len(b.data))
			}
			fmt.Printf("| %s %s | %s | %d | %s | %s | %s | %s |\n", strings.TrimPrefix(fam, "rw-"), src.label, cs.name, len(names),
				pct(cdcNew(B), bBytes), pct(cdcNew(Bm), bBytes), pct(tensNew(B), bBytes), pct(tensNew(Bm), bBytes))
		}
	}
}
