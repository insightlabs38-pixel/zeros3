// Package dcore is the reusable, standard-library-only core of the Z4-P5
// similarity-delta work: a content-sampled COPY/ADD matcher, a cost
// estimator and candidate selector, a bounded-memory windowed delta codec,
// a compact similarity-posting index and an exact group storage accounting
// model with deterministic planners.
//
// Portability contract: no dependency outside the standard library, no
// filesystem store, no laboratory accounting. Inputs are byte slices,
// io.ReaderAt, io.Writer or caller callbacks. Serialized layouts live in
// separate serializer files so a production adapter can keep its own wire
// format around the matcher. All anchors are filters only: every emitted COPY
// is verified byte-for-byte against the source before it is emitted.
package dcore

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// ErrTooLarge reports an input whose size cannot be represented by the
// 40-bit virtual offsets of an Index. It is returned, never truncated.
var ErrTooLarge = errors.New("dcore: input exceeds addressable range")

// ErrParams reports unusable matcher parameters.
var ErrParams = errors.New("dcore: invalid parameters")

const (
	posBits  = 40
	posMask  = 1<<posBits - 1
	maxTotal = int64(1)<<posBits - 2
	maxBases = 1 << 16
	hashP    = 0x9e3779b97f4a7c15
	sampleK  = 0xd6e8feb86659fd93
)

// Op is one COPY (Base >= 0, source range Off..Off+N of base Base) or ADD
// (Base < 0, Lit holds the N literal bytes). TPos is the target offset.
// The field layout deliberately mirrors the production envelope's xdOp.
type Op struct {
	TPos, N int64
	Base    int
	Off     int64
	Lit     []byte
}

// Params tunes the matcher. The zero value is not valid; use DefaultParams.
type Params struct {
	Window     int // bytes hashed per anchor (4..64)
	MinCopy    int // shortest verified COPY that is emitted (>= Window is not required)
	MaxAnchors int // upper bound on retained base anchors (memory bound)
	Slots      int // positions retained per anchor hash (repetition bound)
	MaxProbe   int // open-addressing probe bound (adversarial collision bound)
	// MinSampleBits floors the content-sampling rate: anchors are kept with
	// probability 2^-MinSampleBits even for small bases (0 = index every position).
	MinSampleBits int
}

// DefaultParams matches the production envelope's window and minimum COPY
// and raises the anchor budget (the old index kept 2^18 anchors).
func DefaultParams() Params {
	return Params{Window: 16, MinCopy: 12, MaxAnchors: 1 << 20, Slots: 4, MaxProbe: 16}
}

func (p Params) valid() bool {
	return p.Window >= 4 && p.Window <= 64 && p.MinCopy >= 4 && p.MaxAnchors >= 16 && p.Slots >= 1 && p.Slots <= 16 && p.MaxProbe >= 1 && p.MaxProbe <= 64 && p.MinSampleBits >= 0 && p.MinSampleBits <= 16
}

// Index is an immutable anchor table over one or more base regions. It may be
// shared by concurrent Match calls and reused across targets.
type Index struct {
	p      Params
	bases  [][]byte
	starts []int64 // virtual start of each base
	total  int64
	nbits  uint // sampling: an anchor is kept when the top nbits of h*sampleK are zero
	pow    uint64
	table  []uint64 // tag<<posBits | (virtual offset + 1); 0 = empty
	tmask  uint64
	tshift uint
	// Stats from the build (not used for correctness).
	Anchors, Dropped int
}

// layout assigns virtual starts to base sizes with checked arithmetic.
func layout(sizes []int64) (starts []int64, total int64, err error) {
	starts = make([]int64, len(sizes))
	for i, n := range sizes {
		if n < 0 || n > maxTotal-total {
			return nil, 0, ErrTooLarge
		}
		starts[i] = total
		total += n
	}
	return starts, total, nil
}

// BuildIndex indexes the bases. Offsets are checked: an input set larger than
// the 40-bit virtual address space returns ErrTooLarge.
func BuildIndex(bases [][]byte, p Params) (*Index, error) {
	if !p.valid() || len(bases) > maxBases {
		return nil, ErrParams
	}
	sizes := make([]int64, len(bases))
	for i, b := range bases {
		sizes[i] = int64(len(b))
	}
	starts, total, err := layout(sizes)
	if err != nil {
		return nil, err
	}
	x := &Index{p: p, bases: bases, starts: starts, total: total}
	for x.nbits = uint(p.MinSampleBits); x.total>>x.nbits > int64(p.MaxAnchors); x.nbits++ {
	}
	pow := uint64(1)
	for i := 0; i < p.Window-1; i++ {
		pow *= hashP
	}
	x.pow = pow
	est := int(x.total>>x.nbits) + len(bases) + 64
	tbits := uint(4)
	for 1<<tbits < 2*est {
		tbits++
	}
	x.table = make([]uint64, 1<<tbits)
	x.tmask = 1<<tbits - 1
	x.tshift = 64 - tbits
	for bi, b := range bases {
		x.indexBase(bi, b)
	}
	return x, nil
}

func (x *Index) sampled(h uint64) bool {
	return x.nbits == 0 || (h*sampleK)>>(64-x.nbits) == 0
}

func mix64(v uint64) uint64 {
	v ^= v >> 30
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 27
	v *= 0x94d049bb133111eb
	return v ^ (v >> 31)
}

func (x *Index) hash0(b []byte) uint64 {
	var h uint64
	for _, v := range b[:x.p.Window] {
		h = h*hashP + uint64(v) + 1
	}
	return h
}

func (x *Index) indexBase(bi int, b []byte) {
	w := x.p.Window
	if len(b) < w {
		return
	}
	h := x.hash0(b)
	vs := x.starts[bi]
	for i := 0; ; i++ {
		if x.sampled(h) {
			x.insert(h, vs+int64(i))
		}
		if i+w >= len(b) {
			return
		}
		h = (h-(uint64(b[i])+1)*x.pow)*hashP + uint64(b[i+w]) + 1
	}
}

func (x *Index) insert(h uint64, voff int64) {
	m := mix64(h)
	slot := m >> x.tshift
	tag := (m >> 8) & 0xffffff
	n := 0
	for probe := 0; probe < x.p.MaxProbe; probe++ {
		e := x.table[slot]
		if e == 0 {
			if n >= x.p.Slots {
				break
			}
			x.table[slot] = tag<<posBits | uint64(voff+1)
			x.Anchors++
			return
		}
		if e>>posBits == tag {
			n++
			if n >= x.p.Slots {
				break
			}
		}
		slot = (slot + 1) & x.tmask
	}
	x.Dropped++
}

// locate maps a virtual offset to (base, offset).
func (x *Index) locate(v int64) (int, int64) {
	bi := 0
	for lo, hi := 0, len(x.starts); lo < hi; {
		m := (lo + hi) / 2
		if x.starts[m] <= v {
			bi, lo = m, m+1
		} else {
			hi = m
		}
	}
	return bi, v - x.starts[bi]
}

// commonPrefix returns the length of the common prefix of a and b.
func commonPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i+8 <= n {
		if d := binary.LittleEndian.Uint64(a[i:]) ^ binary.LittleEndian.Uint64(b[i:]); d != 0 {
			return i + bits.TrailingZeros64(d)>>3
		}
		i += 8
	}
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffix returns the length of the common suffix of a and b.
func commonSuffix(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i+8 <= n {
		d := binary.LittleEndian.Uint64(a[len(a)-i-8:]) ^ binary.LittleEndian.Uint64(b[len(b)-i-8:])
		if d != 0 {
			return i + bits.LeadingZeros64(d)>>3
		}
		i += 8
	}
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// Match encodes t as COPY/ADD ops against the indexed bases. The result
// always reconstructs t exactly when ops are applied to the same bases.
func (x *Index) Match(t []byte) []Op {
	w := x.p.Window
	var ops []Op
	start := 0
	emitLit := func(end int) {
		if end > start {
			ops = append(ops, Op{TPos: int64(start), N: int64(end - start), Base: -1, Lit: t[start:end]})
		}
	}
	if len(t) >= w && len(x.table) > 0 {
		h := x.hash0(t)
		for i := 0; i+w <= len(t); {
			if x.sampled(h) {
				if n, back, bi, off := x.best(t, i, start, h); n >= x.p.MinCopy {
					s := i - back
					emitLit(s)
					if k := len(ops); k > 0 && ops[k-1].Base == bi && ops[k-1].Off+ops[k-1].N == off && start == s {
						ops[k-1].N += int64(n)
					} else {
						ops = append(ops, Op{TPos: int64(s), N: int64(n), Base: bi, Off: off})
					}
					i = s + n
					start = i
					if i+w <= len(t) {
						h = x.hash0(t[i:])
					}
					continue
				}
			}
			if i+w >= len(t) {
				break
			}
			h = (h-(uint64(t[i])+1)*x.pow)*hashP + uint64(t[i+w]) + 1
			i++
		}
	}
	emitLit(len(t))
	return ops
}

// best probes the anchor bucket for the window at t[i:] and returns the
// longest verified match (forward + backward, backward not crossing lo).
func (x *Index) best(t []byte, i, lo int, h uint64) (n, back, bi int, off int64) {
	m := mix64(h)
	slot := m >> x.tshift
	tag := (m >> 8) & 0xffffff
	for probe := 0; probe < x.p.MaxProbe; probe++ {
		e := x.table[slot]
		if e == 0 {
			break
		}
		if e>>posBits == tag {
			b, o := x.locate(int64(e&posMask) - 1)
			src := x.bases[b]
			f := commonPrefix(src[o:], t[i:])
			if f >= x.p.Window {
				bk := min(i-lo, int(o))
				bk = commonSuffix(src[int(o)-bk:o], t[i-bk:i])
				if f+bk > n {
					n, back, bi, off = f+bk, bk, b, o-int64(bk)
				}
			}
		}
		slot = (slot + 1) & x.tmask
	}
	return
}

// Match is the one-shot form: build an index over bases and match t.
func Match(bases [][]byte, t []byte, p Params) ([]Op, error) {
	x, err := BuildIndex(bases, p)
	if err != nil {
		return nil, err
	}
	return x.Match(t), nil
}

// Apply reconstructs the target from ops and bases. It is the reference
// interpreter used by tests and by the cost model; it validates every range.
func Apply(bases [][]byte, ops []Op, size int64) ([]byte, error) {
	out := make([]byte, 0, max(size, 0))
	for _, o := range ops {
		if o.N < 0 || int64(len(out)) != o.TPos {
			return nil, ErrParams
		}
		if o.Base < 0 {
			if int64(len(o.Lit)) != o.N {
				return nil, ErrParams
			}
			out = append(out, o.Lit...)
			continue
		}
		if o.Base >= len(bases) || o.Off < 0 || o.Off > int64(len(bases[o.Base])) || o.N > int64(len(bases[o.Base]))-o.Off {
			return nil, ErrParams
		}
		out = append(out, bases[o.Base][o.Off:o.Off+o.N]...)
	}
	if int64(len(out)) != size {
		return nil, ErrParams
	}
	return out, nil
}
