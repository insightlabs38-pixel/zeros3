package main

import (
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/diskimg"
)

func TestGuestFloorNegativeControlAndEdits(t *testing.T) {
	a := &diskimg.Disk{Size: 64 * MiB, Writes: []diskimg.Extent{{Off: 0, Len: 2 * MiB, Kind: diskimg.KRandom, Seed: 1}, {Off: 8 * MiB, Len: 256 * KiB, Kind: diskimg.KMeta, Seed: 2}}}
	g := NewGuestFloor()
	c0, s0, err := g.Add(a, nil)
	if err != nil || c0.NewRaw != 2*MiB+256*KiB {
		t.Fatalf("first sight %+v %v", c0, err)
	}
	// tiny edit: one sector
	b := a.Clone()
	b.Writes = append(b.Writes, diskimg.Extent{Off: MiB, Len: 4096, Kind: diskimg.KRandom, Seed: 9})
	c1, s1, _ := g.Add(b, s0)
	if c1.Changed != 1 || c1.NewRaw != 4096 || c1.Runs != 1 {
		t.Fatalf("edit %+v", c1)
	}
	// zeroing: no new bytes, a mapping entry
	z := b.Clone()
	z.Writes = append(z.Writes, diskimg.Extent{Off: 0, Len: 64 * KiB, Kind: diskimg.KZero})
	c2, s2, _ := g.Add(z, s1)
	if c2.NewRaw != 0 || c2.Runs != 1 || c2.ToZero != 16 {
		t.Fatalf("zero %+v", c2)
	}
	// rewrite-equivalent state: free
	c3, _, _ := g.Add(z.Clone(), s2)
	if c3.Total != 0 || c3.Changed != 0 {
		t.Fatalf("equivalent state charged %+v", c3)
	}
	// negative control: unrelated content must be fully charged (no false reuse)
	u := &diskimg.Disk{Size: 64 * MiB, Writes: []diskimg.Extent{{Off: 0, Len: 2 * MiB, Kind: diskimg.KRandom, Seed: 77}}}
	c4, _, _ := g.Add(u, s2)
	if c4.NewRaw != 2*MiB {
		t.Fatalf("unrelated disk reused content: %+v", c4)
	}
}

func TestPieceStoreReuseAndRelocation(t *testing.T) {
	dir := t.TempDir()
	blk := func(seed uint64) []byte {
		b := make([]byte, 64*KiB)
		diskimg.FillExtent(b, diskimg.Extent{Off: 0, Len: 64 * KiB, Kind: diskimg.KRandom, Seed: seed}, 0)
		return b
	}
	write := func(name string, blocks ...[]byte) string {
		p := filepath.Join(dir, name)
		var all []byte
		for _, b := range blocks {
			all = append(all, b...)
		}
		os.WriteFile(p, all, 0o644)
		return p
	}
	regs := func(n int) []diskimg.Region {
		return []diskimg.Region{{Off: 0, Len: int64(n) * 64 * KiB, Class: diskimg.ClassData}}
	}
	s := NewPieceStore(func(string) int64 { return 64 * KiB }, false)
	c0, err := s.Add(write("a", blk(1), blk(2), blk(3)), regs(3))
	if err != nil || c0.NewPieces != 3 {
		t.Fatalf("%+v %v", c0, err)
	}
	// swap blocks 2 and 3 and add a new block: two relocated pieces, one new, zero new bytes for moved content
	c1, _ := s.Add(write("b", blk(1), blk(3), blk(2), blk(9)), regs(4))
	if c1.Relocated != 2 || c1.NewPieces != 1 || c1.SamePos != 1 || c1.NewRaw != 64*KiB {
		t.Fatalf("%+v", c1)
	}
	if c1.CopyRuns != 3 || c1.Refs != 1 {
		t.Fatalf("delta recipe: %+v", c1)
	}
	// a pure shift of the whole content by one block is a single copy run
	c2, _ := s.Add(write("c", blk(7), blk(1), blk(3), blk(2), blk(9)), regs(5))
	if c2.CopyRuns != 1 || c2.Refs != 1 || c2.NewPieces != 1 {
		t.Fatalf("shift: %+v", c2)
	}
}
