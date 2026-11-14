package lz4z3

import (
	"encoding/binary"
	"math/bits"
	"sync"
)

// PredPierrec models an independent, non-reference producer: the block compressor of
// github.com/pierrec/lz4/v4 (v4.1.x, internal/lz4block.CompressBlock): 6-byte hash into a
// 64 Ki-entry uint16 table, three probes per step, adaptive skipping, backward extension and
// 8-byte-granular forward extension bounded by len-14. Verified against producer output.
const PredPierrec = 5

type pierrecScratch struct {
	table [1 << 16]uint16
	inUse [1 << 11]uint32
}

var pierrecPool = sync.Pool{New: func() any { return new(pierrecScratch) }}

func pierrecHash(x uint64) uint32 { return uint32(((x << 16) * 227718039650203) >> 48) }

func (c *pierrecScratch) get(h uint32, si int) int {
	i := 0
	if c.inUse[h/32]&(1<<(h%32)) != 0 {
		i = int(c.table[h])
	}
	i += si &^ 0xffff
	if i >= si {
		i -= 1 << 16
	}
	return i
}

func (c *pierrecScratch) put(h uint32, si int) {
	c.table[h] = uint16(si)
	c.inUse[h/32] |= 1 << (h % 32)
}

// pierrecSeqs optionally stops at the first sequence that disagrees with target.
func pierrecSeqs(src []byte, target []Seq) (seqs []Seq, aborted bool) {
	c := pierrecPool.Get().(*pierrecScratch)
	defer pierrecPool.Put(c)
	c.inUse = [1 << 11]uint32{}
	const win = 1 << 16
	var si, anchor int
	sn := len(src) - 14
	for si < sn {
		match := binary.LittleEndian.Uint64(src[si:])
		h := pierrecHash(match)
		h2 := pierrecHash(match >> 8)
		ref := c.get(h, si)
		ref2 := c.get(h2, si+1)
		c.put(h, si)
		c.put(h2, si+1)
		offset := si - ref
		if offset <= 0 || offset >= win || uint32(match) != binary.LittleEndian.Uint32(src[ref:]) {
			h = pierrecHash(match >> 16)
			ref3 := c.get(h, si+2)
			si++
			offset = si - ref2
			if offset <= 0 || offset >= win || uint32(match>>8) != binary.LittleEndian.Uint32(src[ref2:]) {
				si++
				offset = si - ref3
				c.put(h, si)
				if offset <= 0 || offset >= win || uint32(match>>16) != binary.LittleEndian.Uint32(src[ref3:]) {
					si += 2 + (si-anchor)>>7
					continue
				}
			}
		}
		lLen := si - anchor
		mLen := 4
		for tOff := si - offset - 1; lLen > 0 && tOff >= 0 && src[si-1] == src[tOff]; tOff-- {
			si--
			lLen--
			mLen++
		}
		start := si
		si += mLen
		for si+8 <= sn {
			x := binary.LittleEndian.Uint64(src[si:]) ^ binary.LittleEndian.Uint64(src[si-offset:])
			if x != 0 {
				si += bits.TrailingZeros64(x) >> 3
				break
			}
			si += 8
		}
		seqs = append(seqs, Seq{Lit: lLen, Match: si - start, Off: offset})
		if target != nil && (len(seqs) > len(target) || seqs[len(seqs)-1] != target[len(seqs)-1]) {
			return seqs[:len(seqs)-1], true
		}
		anchor = si
		if si >= sn {
			break
		}
		c.put(pierrecHash(binary.LittleEndian.Uint64(src[si-2:])), si-2)
	}
	return append(seqs, Seq{Lit: len(src) - anchor}), false
}
