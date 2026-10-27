package modelz3

import (
	"bytes"
	"crypto/sha256"
	"sort"
	"sync"
)

// Pair is a target tensor with the base tensor of the same name, type and byte length: the only
// relation Z3-10 (and every candidate here) delta-codes against.
type Pair struct{ Base, Target *Seg }

// Match pairs tensors by name; same name with another type or size is reported as unmatched.
func Match(base, target *Layout) (pairs []Pair, unmatched []*Seg) {
	bn := map[string]*Seg{}
	for _, s := range base.Tensors() {
		bn[s.Name] = s
	}
	for _, t := range target.Tensors() {
		if b, ok := bn[t.Name]; ok && b.Type == t.Type && b.Size() == t.Size() && sameShape(b.Shape, t.Shape) {
			pairs = append(pairs, Pair{b, t})
		} else {
			unmatched = append(unmatched, t)
		}
	}
	return
}

func sameShape(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type h16 = [16]byte

func hash16(b []byte) h16 {
	s := sha256.Sum256(b)
	var h h16
	copy(h[:], s[:16])
	return h
}

// Identity is the byte-weighted exact-reuse potential of one target against one base, by
// granularity. Every number is bytes of the target's tensor payload; nothing is a representation
// cost (an index of native blocks would cost more than most of what it names).
type Identity struct {
	Tensors       int64 // target tensor bytes
	Matched       int64 // bytes of tensors with a same name/type/shape base tensor
	Whole         int64 // bytes of tensors byte-identical to their base tensor
	AlignedNative int64 // bytes in native blocks equal to the base block at the same position
	AnyNative     int64 // bytes in native blocks present anywhere in the base (any tensor, any position)
	Aligned4K     int64 // 4 KiB fixed blocks equal at the same position
	Any4K         int64
	MatchedNative int64 // native blocks of matched tensors (denominator for AlignedNative)
}

// IdentityOf measures target against base. It streams tensor by tensor.
func IdentityOf(base *Src, bl *Layout, target *Src, tl *Layout) (Identity, error) {
	var id Identity
	pairs, un := Match(bl, tl)
	// sets of every base block hash, native and 4K
	nat, k4 := map[h16]struct{}{}, map[h16]struct{}{}
	for _, s := range bl.Tensors() {
		b := make([]byte, s.Size())
		if err := readFull(base.R, b, s.Begin); err != nil {
			return id, err
		}
		w := int64(max(1, s.Width))
		for o := int64(0); o < int64(len(b)); o += w {
			nat[hash16(b[o:o+w])] = struct{}{}
		}
		for o := int64(0); o < int64(len(b)); o += 4096 {
			k4[hash16(b[o:min(int64(len(b)), o+4096)])] = struct{}{}
		}
	}
	pm := map[string]*Seg{}
	for _, p := range pairs {
		pm[p.Target.Name] = p.Base
	}
	for _, t := range tl.Tensors() {
		id.Tensors += t.Size()
		tb := make([]byte, t.Size())
		if err := readFull(target.R, tb, t.Begin); err != nil {
			return id, err
		}
		w := int64(max(1, t.Width))
		var bb []byte
		if bs, ok := pm[t.Name]; ok {
			id.Matched += t.Size()
			bb = make([]byte, bs.Size())
			if err := readFull(base.R, bb, bs.Begin); err != nil {
				return id, err
			}
			if bytes.Equal(bb, tb) {
				id.Whole += t.Size()
			}
		}
		for o := int64(0); o < int64(len(tb)); o += w {
			blk := tb[o : o+w]
			if bb != nil {
				id.MatchedNative += w
				if bytes.Equal(blk, bb[o:o+w]) {
					id.AlignedNative += w
				}
			}
			if _, ok := nat[hash16(blk)]; ok {
				id.AnyNative += w
			}
		}
		for o := int64(0); o < int64(len(tb)); o += 4096 {
			e := min(int64(len(tb)), o+4096)
			if bb != nil && bytes.Equal(bb[o:e], tb[o:e]) {
				id.Aligned4K += e - o
			}
			if _, ok := k4[hash16(tb[o:e])]; ok {
				id.Any4K += e - o
			}
		}
	}
	_ = un
	return id, nil
}

// Planes de-interleaves b (a multiple of w long) by position within each w-byte unit: all byte 0s,
// then all byte 1s, ... It is its own inverse via Unplanes.
func Planes(dst, b []byte, w int) []byte {
	if w <= 1 {
		return append(dst[:0], b...)
	}
	n := len(b) / w
	if cap(dst) < len(b) {
		dst = make([]byte, len(b))
	}
	dst = dst[:len(b)]
	for p := 0; p < w; p++ {
		col := dst[p*n : (p+1)*n]
		for i := range col {
			col[i] = b[i*w+p]
		}
	}
	return dst
}

// Unplanes inverts Planes.
func Unplanes(dst, b []byte, w int) []byte {
	if w <= 1 {
		return append(dst[:0], b...)
	}
	n := len(b) / w
	if cap(dst) < len(b) {
		dst = make([]byte, len(b))
	}
	dst = dst[:len(b)]
	for p := 0; p < w; p++ {
		col := b[p*n : (p+1)*n]
		for i := range col {
			dst[i*w+p] = col[i]
		}
	}
	return dst
}

// XOR writes a^b into dst.
func XOR(dst, a, b []byte) []byte {
	if cap(dst) < len(a) {
		dst = make([]byte, len(a))
	}
	dst = dst[:len(a)]
	for i := range a {
		dst[i] = a[i] ^ b[i]
	}
	return dst
}

// Cand names the exact per-tensor encodings compared in the aligned-delta study.
type Cand struct {
	Raw, Standalone, StandalonePlanes, XOR, XORPlanes int64
	Base                                              bool // XOR candidates were measured (a same name/type/size base tensor exists)
}

// Best is the cheapest candidate (raw is always available).
func (c Cand) Best() (int64, string) {
	best, name := c.Raw, "raw"
	cands := []struct {
		n string
		v int64
	}{{"standalone", c.Standalone}, {"standalone-planes", c.StandalonePlanes}}
	if c.Base {
		cands = append(cands, struct {
			n string
			v int64
		}{"xor", c.XOR}, struct {
			n string
			v int64
		}{"xor-planes", c.XORPlanes})
	}
	for _, x := range cands {
		if x.v < best {
			best, name = x.v, x.n
		}
	}
	return best, name
}

// DeltaRow aggregates the aligned-delta candidates of one target against one base. All sums are over
// per-tensor choices (each tensor takes its own cheapest exact candidate).
type DeltaRow struct {
	Raw, Standalone, Delta, Best int64            // tensor bytes; cost of standalone-only, delta-class (xor, xor-planes; unmatched tensors at standalone) and any candidate
	Bytes                        map[string]int64 // tensor bytes by type name
	Matched                      map[string]int64
	Cand                         map[string]Cand // summed over matched tensors, by type
	Other                        map[string]Cand // unmatched tensors (standalone options only)
	Modes                        map[string]int64
}

// semaphore bounds resident tensor bytes.
type wsem struct {
	mu   sync.Mutex
	cond *sync.Cond
	used int64
	cap  int64
}

func newWsem(cap int64) *wsem { s := &wsem{cap: cap}; s.cond = sync.NewCond(&s.mu); return s }
func (s *wsem) acquire(n int64) {
	s.mu.Lock()
	for s.used > 0 && s.used+n > s.cap {
		s.cond.Wait()
	}
	s.used += n
	s.mu.Unlock()
}
func (s *wsem) release(n int64) { s.mu.Lock(); s.used -= n; s.mu.Unlock(); s.cond.Broadcast() }

// DeltaOf computes every candidate for every tensor of target against base, with at most
// ~1.5 GiB of tensor buffers resident.
func DeltaOf(base *Src, bl *Layout, target *Src, tl *Layout) (*DeltaRow, error) {
	pairs, un := Match(bl, tl)
	d := &DeltaRow{Bytes: map[string]int64{}, Matched: map[string]int64{}, Cand: map[string]Cand{}, Other: map[string]Cand{}, Modes: map[string]int64{}}
	var mu sync.Mutex
	var firstErr error
	sem := newWsem(1536 << 20)
	var wg sync.WaitGroup
	run := func(t, b *Seg) {
		defer wg.Done()
		n := t.Size()
		need := 5 * n
		sem.acquire(need)
		defer sem.release(need)
		tb := make([]byte, n)
		var c Cand
		fail := func(err error) {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
		if err := readFull(target.R, tb, t.Begin); err != nil {
			fail(err)
			return
		}
		c.Raw = n
		c.Standalone = packLen(tb)
		// Width 0 = element width unknown (a pickle-free torch storage): probe the plausible widths
		// and keep the best, the encoder-side choice a recipe would record.
		widths := []int{t.Width}
		if t.Width == 0 {
			widths = []int{1, 2, 4}
		}
		var bb, x []byte
		if b != nil {
			bb = make([]byte, n)
			if err := readFull(base.R, bb, b.Begin); err != nil {
				fail(err)
				return
			}
			x = XOR(nil, tb, bb)
		}
		c.StandalonePlanes, c.XORPlanes = n+1, n+1
		if b != nil {
			c.XOR = packLen(x)
		}
		var pl []byte
		for _, w := range widths {
			if n%int64(w) != 0 {
				continue
			}
			pl = Planes(pl, tb, w)
			c.StandalonePlanes = min(c.StandalonePlanes, packLen(pl))
			if b != nil {
				c.XORPlanes = min(c.XORPlanes, packLen(Planes(pl, x, w)))
			}
		}
		c.Base = b != nil
		best, name := c.Best()
		sa := min(c.Raw, c.Standalone, c.StandalonePlanes)
		dl := sa
		if b != nil {
			dl = min(c.XOR, c.XORPlanes)
		}
		mu.Lock()
		d.Raw += n
		d.Standalone += sa
		d.Delta += dl
		d.Best += best
		d.Bytes[t.Type] += n
		if b != nil {
			d.Matched[t.Type] += n
			a := d.Cand[t.Type]
			a.Raw += c.Raw
			a.Standalone += c.Standalone
			a.StandalonePlanes += c.StandalonePlanes
			a.XOR += c.XOR
			a.XORPlanes += c.XORPlanes
			d.Cand[t.Type] = a
		} else {
			a := d.Other[t.Type]
			a.Raw += c.Raw
			a.Standalone += c.Standalone
			a.StandalonePlanes += c.StandalonePlanes
			d.Other[t.Type] = a
		}
		d.Modes[name] += n
		mu.Unlock()
	}
	for _, p := range pairs {
		wg.Add(1)
		go run(p.Target, p.Base)
	}
	for _, t := range un {
		wg.Add(1)
		go run(t, nil)
	}
	wg.Wait()
	return d, firstErr
}

func packLen(b []byte) int64 { return packedLen(b) }

// SortedTypes lists map keys in order.
func SortedTypes(m map[string]int64) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
