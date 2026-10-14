package tdelta

import (
	"fmt"
	"math/bits"

	"zeros3-z3/ir"
)

// PXR is the Z3-10A native byte-plane XOR codec: the XOR of target and base is
// coded element by element with the Z3 adaptive range coder. Per element one
// "changed" flag (context: the last three flags), then, only for changed
// elements, the w XOR bytes most-significant first. Each byte is a zero flag and
// an 8-bit tree over (value-1). The contexts are small:
//
//	plane index (position in the element, MSB first),
//	state of the byte above in this element (zero / magnitude bucket),
//	magnitude bucket of the same plane in the previous element,
//	for the byte models also the base element's exponent-ish bits
//	(top byte below the sign, 32 levels): weights of small magnitude move by
//	more ULPs than large ones.
//
// Exact by construction: only XOR of bit patterns, no float arithmetic. A nil
// base codes the target against zeros (the standalone, no-base candidate).
// Decoding needs n and w; the stream carries nothing else (no header).

const (
	maxPlanes = 8
	nBuckets  = 4
	nMag      = 32
)

type pxrModel struct {
	flag [8]ir.BitModel
	zero [maxPlanes][nBuckets][nBuckets][nMag]ir.BitModel
	tree [maxPlanes][nBuckets][nBuckets * nMag]*[256]ir.BitModel // allocated on first use
}

func newPXRModel() *pxrModel {
	m := new(pxrModel)
	init := ir.NewBitModels(1)[0]
	for i := range m.flag {
		m.flag[i] = init
	}
	for p := range m.zero {
		for h := range m.zero[p] {
			for b := range m.zero[p][h] {
				for g := range m.zero[p][h][b] {
					m.zero[p][h][b][g] = init
				}
			}
		}
	}
	return m
}

func (m *pxrModel) treeFor(k, hb, ci int) *[256]ir.BitModel {
	tr := m.tree[k][hb][ci]
	if tr == nil {
		tr = new([256]ir.BitModel)
		init := ir.NewBitModels(1)[0]
		for q := range tr {
			tr[q] = init
		}
		m.tree[k][hb][ci] = tr
	}
	return tr
}

func bucket(b byte) int {
	switch l := bits.Len8(b); {
	case l == 0:
		return 0
	case l <= 2:
		return 1
	case l <= 5:
		return 2
	}
	return 3
}

// bitIO is one direction of the coder.
type bitIO struct {
	e   *ir.RCEncoder
	d   *ir.RCDecoder
	bad bool // decoder saw a symbol the encoder never emits
}

func (c *bitIO) bit(m *ir.BitModel, b int) int {
	if c.d != nil {
		return c.d.Bit(m)
	}
	c.e.Bit(m, b)
	return b
}

// pxr runs the coder over n bytes. Encoding reads target; decoding writes out.
func pxr(c *bitIO, base, target, out []byte, n, w int) {
	m := newPXRModel()
	var prev [maxPlanes]byte
	var x [maxPlanes]byte
	hist := 0
	enc := c.d == nil
	for o := 0; o < n; o += w {
		changed := 0
		if enc {
			for j := 0; j < w; j++ {
				x[j] = target[o+j]
				if base != nil {
					x[j] ^= base[o+j]
				}
				if x[j] != 0 {
					changed = 1
				}
			}
		}
		mg := 0
		if base != nil {
			mg = int(base[o+w-1]&0x7f) >> 2
		}
		changed = c.bit(&m.flag[hist&7], changed)
		hist = hist<<1 | changed
		if changed == 0 {
			for j := 0; j < w; j++ {
				x[j] = 0
				prev[j] = 0
			}
		} else {
			hb := 0 // bucket of the byte above in this element
			any := false
			for k := 0; k < w; k++ { // k-th coded byte is the byte at offset w-1-k
				j := w - 1 - k
				bk := bucket(prev[k])
				nz := 1
				if k < w-1 || any { // else: all higher bytes zero, so this one must be nonzero
					nz = c.bit(&m.zero[k][hb][bk][mg], b2i(enc && x[j] != 0))
				}
				v := 0
				if nz != 0 {
					tr := m.treeFor(k, hb, bk*nMag+mg)
					node := 1
					want := int(x[j]) - 1
					for i := 7; i >= 0; i-- {
						bit := c.bit(&tr[node], (want>>i)&1)
						node = node<<1 | bit
					}
					v = node - 256 + 1
					if v > 255 {
						c.bad = true
						v = 0
					}
					any = true
				}
				x[j] = byte(v)
				prev[k] = byte(v)
				hb = bucket(byte(v))
			}
		}
		if !enc {
			for j := 0; j < w; j++ {
				b := x[j]
				if base != nil {
					b ^= base[o+j]
				}
				out[o+j] = b
			}
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pxrWidth(n, w int) int {
	if w < 1 || w > maxPlanes || n%w != 0 {
		return 1
	}
	return w
}

// EncodePXR codes target against base (nil = zeros). len(base) must equal len(target) when base is set.
func EncodePXR(base, target []byte, w int) ([]byte, error) {
	if base != nil && len(base) != len(target) {
		return nil, fmt.Errorf("tdelta: base %d bytes, target %d", len(base), len(target))
	}
	w = pxrWidth(len(target), w)
	c := &bitIO{e: ir.NewRCEncoder()}
	pxr(c, base, target, nil, len(target), w)
	return c.e.Finish(), nil
}

// DecodePXR rebuilds n bytes. It always returns exactly n bytes or ErrCorrupt
// (the stream must be fully consumed: trailing junk is rejected).
func DecodePXR(base, payload []byte, n, w int) ([]byte, error) {
	if base != nil && len(base) != n {
		return nil, fmt.Errorf("tdelta: base %d bytes, want %d", len(base), n)
	}
	w = pxrWidth(n, w)
	out := make([]byte, n)
	c := &bitIO{d: ir.NewRCDecoder(payload)}
	pxr(c, base, nil, out, n, w)
	if c.bad || !c.d.Exhausted() {
		return nil, ErrCorrupt
	}
	return out, nil
}
