package main

import (
	"fmt"
	"os"
	"sort"

	"zeros3-z3/diskimg"
	"zeros3-z3/lab"
)

type chunk struct{ off, n int64 }

// Layer is one image of a chain with its scanner view and production-CDC chunk boundaries.
type Layer struct {
	Name   string
	Path   string
	Q      *diskimg.Qcow2
	V      *diskimg.Vhdx
	Regs   []diskimg.Region
	Chunks []chunk
}

func loadLayer(name, path string) (*Layer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	l := &Layer{Name: name, Path: path}
	if q, err := diskimg.ScanQcow2(f, st.Size()); err == nil {
		l.Q, l.Regs = q, q.Regions
	} else if v, err := diskimg.ScanVhdx(f, st.Size()); err == nil {
		l.V, l.Regs = v, v.Regions
	} else {
		return nil, err
	}
	f.Seek(0, 0)
	err = lab.ChunkCDC(f, func(off int64, c []byte) error { l.Chunks = append(l.Chunks, chunk{off, int64(len(c))}); return nil })
	return l, err
}

type rng struct{ off, n int64 }

type touch struct {
	meta, pay []rng // file byte ranges touched in this layer
}

type res struct {
	hit  int // layer index that supplied the unit, -1 = zeros
	zero bool
}

// lookup resolves one 4 KiB guest unit in one layer: supplied (true) or deferred to the parent / zero.
func (l *Layer) lookup(u int64, t *touch) (supplied bool, zero bool) {
	off := u * 4096
	if l.Q != nil {
		q := l.Q
		cs := q.ClusterSize
		g := off / cs
		es := int64(8)
		if q.ExtendedL2 {
			es = 16
		}
		l2n := cs / es
		l1i := g / l2n
		if l1i >= int64(len(q.L2Off)) {
			return false, false
		}
		t.meta = append(t.meta, rng{q.L1Off + 8*l1i, 8})
		if q.L2Off[l1i] == 0 {
			return false, false
		}
		t.meta = append(t.meta, rng{q.L2Off[l1i] + (g%l2n)*es, es})
		e := q.Map[g]
		switch e.State {
		case diskimg.QData:
			if q.ExtendedL2 {
				j := uint((off % cs) / (cs / 32))
				switch {
				case e.Sub>>j&1 == 1:
				case e.Sub>>(32+j)&1 == 1:
					return true, true
				default:
					return false, false
				}
			}
			t.pay = append(t.pay, rng{e.Host + off%cs, 4096})
			return true, false
		case diskimg.QCompressed:
			t.pay = append(t.pay, rng{e.Host, e.CSize})
			return true, false
		case diskimg.QZero:
			return true, true
		}
		if q.ExtendedL2 {
			j := uint((off % cs) / (cs / 32))
			if e.Sub>>(32+j)&1 == 1 {
				return true, true
			}
		}
		return false, false
	}
	v := l.V
	b := off / v.BlockSize
	if b >= int64(len(v.Blocks)) {
		return false, false
	}
	t.meta = append(t.meta, rng{v.BATOff + 8*(b+b/v.ChunkRatio), 8})
	blk := v.Blocks[b]
	switch blk.State {
	case diskimg.VFull:
		t.pay = append(t.pay, rng{blk.Off + off%v.BlockSize, 4096})
		return true, false
	case diskimg.VPartial:
		c := b / v.ChunkRatio
		sec := (off % v.BlockSize) / v.LSS
		bit := (b%v.ChunkRatio)*(v.BlockSize/v.LSS) + sec
		t.meta = append(t.meta, rng{v.BitmapOff[c] + bit/8, 1})
		for _, r := range blk.Present {
			if int64(r.First) <= sec && sec < int64(r.First+r.Count) {
				t.pay = append(t.pay, rng{blk.Off + off%v.BlockSize, 4096})
				return true, false
			}
		}
		return false, false
	case diskimg.VNotPresent:
		return false, false
	}
	return true, true // zero / unmapped / undefined
}

func (l *Layer) pieceOf(off int64) (int64, int64) {
	for _, r := range l.Regs { // regions are few for the classes we touch; linear is fine for a study
		if off >= r.Off && off < r.Off+r.Len {
			g := int64(64 * KiB)
			if l.V != nil {
				switch r.Class {
				case diskimg.ClassPayload:
					g = l.V.BlockSize
				case diskimg.ClassBitmap:
					g = MiB
				}
			}
			base := r.Off + (off-r.Off)/g*g
			return base, min(g, r.Off+r.Len-base)
		}
	}
	return off, 4096
}

func union(rs []rng) []rng {
	sort.Slice(rs, func(i, j int) bool { return rs[i].off < rs[j].off })
	var out []rng
	for _, r := range rs {
		if n := len(out); n > 0 && r.off <= out[n-1].off+out[n-1].n {
			out[n-1].n = max(out[n-1].n, r.off+r.n-out[n-1].off)
		} else {
			out = append(out, r)
		}
	}
	return out
}

// ReadCost is the cost of one guest read.
type ReadCost struct {
	Meta, Payload int64
	Pieces        int
	PieceBytes    int64
	Hops          int
	CDCChunks     int
	CDCBytes      int64
	Supplier      string
}

func (l *Layer) cdcCover(rs []rng) (int, int64) {
	seen := map[int]bool{}
	var bytes int64
	for _, r := range rs {
		i := sort.Search(len(l.Chunks), func(i int) bool { return l.Chunks[i].off+l.Chunks[i].n > r.off })
		for ; i < len(l.Chunks) && l.Chunks[i].off < r.off+r.n; i++ {
			if !seen[i] {
				seen[i] = true
				bytes += l.Chunks[i].n
			}
		}
	}
	return len(seen), bytes
}

// guestRead costs reading [off,off+n) through chain (top first).
func guestRead(chain []*Layer, off, n int64) ReadCost {
	touches := make([]touch, len(chain))
	supplier := map[int]bool{}
	hops := 0
	for u := off / 4096; u < (off+n+4095)/4096; u++ {
		for i, l := range chain {
			ok, _ := l.lookup(u, &touches[i])
			if i > hops {
				hops = i
			}
			if ok {
				supplier[i] = true
				break
			}
		}
	}
	var c ReadCost
	c.Hops = hops
	for i, l := range chain {
		m, p := union(touches[i].meta), union(touches[i].pay)
		for _, r := range m {
			c.Meta += r.n
		}
		for _, r := range p {
			c.Payload += r.n
		}
		all := append(append([]rng{}, m...), p...)
		pcs := map[int64]int64{}
		for _, r := range all {
			for o := r.off; o < r.off+r.n; {
				b, ln := l.pieceOf(o)
				pcs[b] = ln
				o = b + ln
			}
		}
		for _, ln := range pcs {
			c.Pieces++
			c.PieceBytes += ln
		}
		k, b := l.cdcCover(all)
		c.CDCChunks += k
		c.CDCBytes += b
	}
	for i := range chain {
		if supplier[i] {
			c.Supplier += chain[i].Name + " "
		}
	}
	return c
}

// artifactRead costs reading [off,off+n) of the exact image file.
func artifactRead(l *Layer, off, n int64) (pieces int, pieceBytes int64, hybrid int64, k int, kb int64) {
	pcs := map[int64]int64{}
	for o := off; o < off+n; {
		b, ln := l.pieceOf(o)
		pcs[b] = ln
		o = b + ln
	}
	f, _ := os.Open(l.Path)
	defer f.Close()
	for b, ln := range pcs {
		pieces++
		pieceBytes += ln
		if ln <= 256*KiB {
			hybrid += ln
			continue
		}
		// >256 KiB: bounded descriptor over the piece's own CDC chunks; fetch only the overlapping chunks
		buf := make([]byte, ln)
		f.ReadAt(buf, b)
		lo, hi := max(off, b)-b, min(off+n, b+ln)-b
		lab.ChunkCDC(bytesReader(buf), func(co int64, c []byte) error {
			if co < hi && co+int64(len(c)) > lo {
				hybrid += int64(len(c))
			}
			return nil
		})
	}
	k, kb = l.cdcCover([]rng{{off, n}})
	return
}

func runReads(e *Env, laneNames []string) error {
	sizes := []int64{4 * KiB, 64 * KiB, MiB}
	for _, ln := range laneNames {
		dir := e.dir("B", ln)
		ext := "qcow2"
		if ln[:4] == "vhdx" {
			ext = "vhdx"
		}
		var L = map[string]*Layer{}
		for _, n := range []string{"B0", "C5", "C6", "C7"} {
			l, err := loadLayer(n, fmt.Sprintf("%s/%s.%s", dir, n, ext))
			if err != nil {
				return err
			}
			L[n] = l
		}
		fmt.Printf("== guest reads, lane %s (chunks: B0=%d C6=%d C7=%d)\n", ln, len(L["B0"].Chunks), len(L["C6"].Chunks), len(L["C7"].Chunks))
		type sc struct {
			name  string
			chain []*Layer
			off   int64
		}
		scs := []sc{
			{"child data (C6 top)", []*Layer{L["C6"], L["B0"]}, diskimg.OffRand + 45*MiB},
			{"inherited via grandchild (base data, C7>C6>B0)", []*Layer{L["C7"], L["C6"], L["B0"]}, diskimg.OffMeta + 2*MiB},
			{"child data via grandchild (C6 data)", []*Layer{L["C7"], L["C6"], L["B0"]}, diskimg.OffRand + 47*MiB},
			{"top-layer zero override (C7)", []*Layer{L["C7"], L["C6"], L["B0"]}, diskimg.OffRand + 46*MiB},
			{"never written (zeros through 3 layers)", []*Layer{L["C7"], L["C6"], L["B0"]}, diskimg.OffHole + 300*MiB},
			{"partial block: sector present in C5", []*Layer{L["C5"], L["B0"]}, diskimg.OffRand + 60*MiB + 17*4096},
			{"partial block: sector inherited from B0", []*Layer{L["C5"], L["B0"]}, diskimg.OffRand + 60*MiB + 18*4096},
		}
		for _, s := range scs {
			for _, n := range sizes {
				if n > 4096 && (s.name == "top-layer zero override (C7)") && n > 64*KiB {
					continue
				}
				c := guestRead(s.chain, s.off, n)
				fmt.Printf("  %-52s %5dKiB meta=%5dB payload=%8dB pieces=%2d (%8dB) hops=%d cdc=%2d chunks %8dB  supplied by: %s\n", s.name, n>>10, c.Meta, c.Payload, c.Pieces, c.PieceBytes, c.Hops, c.CDCChunks, c.CDCBytes, c.Supplier)
			}
		}
		fmt.Printf("== artifact range reads, lane %s (image B0, %d bytes)\n", ln, L["B0"].Chunks[len(L["B0"].Chunks)-1].off)
		lay := L["B0"]
		var offs = map[string]int64{"header": 0, "mid-file": lay.Chunks[len(lay.Chunks)/2].off, "tail": lay.Chunks[len(lay.Chunks)-1].off - MiB}
		for _, r := range lay.Regs {
			if r.Class == diskimg.ClassL2 || r.Class == diskimg.ClassBAT {
				offs["table "+r.Class] = r.Off + 8*KiB
				break
			}
		}
		var names []string
		for k := range offs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			for _, n := range sizes {
				p, pb, hy, ck, cb := artifactRead(lay, offs[k], n)
				fmt.Printf("  %-12s off=%10d %5dKiB native pieces=%2d (%8dB) hybrid=%8dB | cdc %2d chunks %8dB\n", k, offs[k], n>>10, p, pb, hy, ck, cb)
			}
		}
	}
	return nil
}

// zeroChunks reports how many production CDC chunks of an image are entirely zero (a stored-once object referenced many times).
func zeroChunks(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var n, nz int
	var bytes, zbytes int64
	err = lab.ChunkCDC(f, func(off int64, c []byte) error {
		n++
		bytes += int64(len(c))
		if allZero(c) {
			nz++
			zbytes += int64(len(c))
		}
		return nil
	})
	fmt.Printf("%s: %d chunks (%d MiB); all-zero chunks %d (%d MiB, %.1f%% of chunks, manifest %d KiB)\n", path, n, bytes>>20, nz, zbytes>>20, 100*float64(nz)/float64(n), 96*nz>>10)
	return err
}
