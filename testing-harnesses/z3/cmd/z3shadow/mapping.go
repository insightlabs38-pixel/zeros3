package main

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"zeros3-z3/prodshadow"
)

// mapping compares the two component mappings on exact size classes: one direct
// production chunk per component (Model A, legitimate only up to the 256 KiB record
// limit) against a descriptor over production-CDC chunks (Model B).
func (c config) mapping() {
	fmt.Println("== component mapping (random, incompressible components; physical via production stats)")
	fmt.Printf("%-8s %5s %-8s | %6s %6s | %8s %8s %8s | %7s %7s | %9s %9s\n",
		"size", "n", "mapping", "chunks", "descB", "loose", "alloc", "packed", "ptChnk", "ptRead", "wholeRd", "recs")
	classes := []struct {
		size int
		n    int
	}{{1 << 10, 2048}, {4 << 10, 2048}, {64 << 10, 128}, {256 << 10, 32}, {(256 << 10) + 1, 32}, {1 << 20, 8}, {8 << 20, 2}}
	for _, cl := range classes {
		for _, m := range []string{"direct", "virtual"} {
			if m == "direct" && cl.size > prodshadow.MaxChunk {
				continue // would exceed the production record limit: shown by the oversize test, not stored
			}
			parts := make([][]byte, cl.n)
			rng := rand.New(rand.NewSource(int64(cl.size)))
			for i := range parts {
				parts[i] = make([]byte, cl.size)
				rng.Read(parts[i])
			}
			dir := filepath.Join(c.work, fmt.Sprintf("map-%d-%s", cl.size, m))
			os.RemoveAll(dir)
			l, err := prodshadow.NewLab(c.b, dir, prodshadow.Options{AllVirtual: m == "virtual"})
			must(err)
			must(l.Start())
			b, err := prodshadow.ConcatBundle(parts...)
			must(err)
			st, err := l.Sh.Stage(b, 0)
			must(err)
			_, err = l.Sh.Publish("k", sha256.Sum256(nil), 0, b.ID, nil)
			must(err)
			loose, _ := l.Store.Stats()
			_, alloc := prodshadow.DirBytes(filepath.Join(l.Store.Dir, "chunks"))
			live := filepath.Join(dir, "live")
			_, err = l.LiveFile(live)
			must(err)
			must(l.Stop())
			cp := &prodshadow.Store{B: c.b, Dir: dir + "/packed"}
			must(prodshadow.CopyDir(l.Store.Dir, cp.Dir))
			_, err = cp.Compact(c.packMiB, true, live)
			must(err)
			ps, _ := cp.Stats()
			// point read: 4 KiB in the middle of the middle component; whole read: that component
			pl := &prodshadow.Lab{B: c.b, Base: dir, Store: cp, Meta: l.Meta, Opt: l.Opt, Cache: 16}
			must(pl.Start())
			id := sha256.Sum256(parts[cl.n/2])
			pc, err := pl.ShadowCost(func() error {
				p := make([]byte, min(4096, cl.size))
				_, err := pl.Sh.Src.ReadAt(id, p, int64(cl.size-len(p))/2)
				return err
			})
			must(err)
			wc, err := pl.ShadowCost(func() error { return pl.Sh.Src.VerifyComponent(id) })
			must(err)
			must(pl.Stop())
			fmt.Printf("%-8s %5d %-8s | %6d %6d | %8s %8s %8s | %7d %7d | %9d %9d\n", sz(cl.size), cl.n, m,
				st.Stats.ChunksNew, st.Stats.DescBytes, mib(loose.ChunkStoreFileBytes), mib(alloc), mib(ps.ChunkStoreFileBytes),
				pc.Chunks, pc.StoredBytes, wc.StoredBytes, ps.PackedChunkCount)
			os.RemoveAll(dir)
		}
	}
}

func sz(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMiB", float64(n)/(1<<20))
	case n > 256<<10 && n < 257<<10:
		return "256K+1"
	}
	return fmt.Sprintf("%dKiB", n>>10)
}
