package brotliz3

import (
	"encoding/binary"
	"math/bits"
)

// Backward-reference search of the reference encoder for qualities 5..9 with lgwin >= 17:
// hashers H5 (4-byte keys) and H6 (5-byte keys, inputs of 1 MiB and more), the lazy matcher of
// backward_references_inc.h and command construction. Every data-dependent decision of the C code
// is kept, including its tie-breaking, the 16-bit bucket counters and the static-dictionary probe.

const (
	hashMul32    = 0x1E35A7BD
	hashMul64    = 0x1FE35A7BD3579BD3
	scoreBase    = 30 * 8 * 8
	windowGap    = 16
	numDistShort = 16
)

var h6Mul = func() uint64 { m := uint64(hashMul64); return m << 24 }() // kHashMul64 << (64 - 5*8): keys use the low five bytes

type command struct {
	insertLen  uint32
	copyLen    uint32 // low 25 bits length, high 7 bits length-code delta
	distExtra  uint32
	cmdPrefix  uint16
	distPrefix uint16 // low 10 bits code, high 6 bits extra-bit count
}

func (c *command) copyLength() uint32 { return c.copyLen & 0x1FFFFFF }

func (c *command) copyLenCode() uint32 {
	mod := c.copyLen >> 25
	delta := int32(int8(uint8(mod | (mod&0x40)<<1)))
	return uint32(int32(c.copyLen&0x1FFFFFF) + delta)
}

func log2Floor(n uint32) uint32 { return uint32(bits.Len32(n)) - 1 }

func insertLenCode(n uint32) uint16 {
	switch {
	case n < 6:
		return uint16(n)
	case n < 130:
		nb := log2Floor(n-2) - 1
		return uint16((nb << 1) + ((n - 2) >> nb) + 2)
	case n < 2114:
		return uint16(log2Floor(n-66) + 10)
	case n < 6210:
		return 21
	case n < 22594:
		return 22
	}
	return 23
}

func copyLenCodeOf(n uint32) uint16 {
	switch {
	case n < 10:
		return uint16(n - 2)
	case n < 134:
		nb := log2Floor(n-6) - 1
		return uint16((nb << 1) + ((n - 6) >> nb) + 4)
	case n < 2118:
		return uint16(log2Floor(n-70) + 12)
	}
	return 23
}

func combineLenCodes(ins, cp uint16, useLast bool) uint16 {
	bits64 := cp&7 | (ins&7)<<3
	if useLast && ins < 8 && cp < 16 {
		if cp < 8 {
			return bits64
		}
		return bits64 | 64
	}
	off := uint32(2 * ((cp >> 3) + 3*(ins>>3)))
	off = (off << 5) + 0x40 + ((0x520D40 >> off) & 0xC0)
	return uint16(off) | bits64
}

func lengthCode(ins, cp uint32, useLast bool) uint16 {
	return combineLenCodes(insertLenCode(ins), copyLenCodeOf(cp), useLast)
}

// prefixEncodeCopyDistance with npostfix = ndirect = 0 (the only parameters of this port).
func prefixEncodeDistance(code uint32) (uint16, uint32) {
	if code < numDistShort {
		return uint16(code), 0
	}
	dist := uint32(4) + (code - numDistShort)
	bucket := log2Floor(dist) - 1
	prefix := (dist >> bucket) & 1
	offset := (2 + prefix) << bucket
	nbits := bucket
	return uint16(nbits<<10 | (numDistShort + 2*(nbits-1) + prefix)), dist - offset
}

func (c *command) init(insertLen, copyLen uint32, lenCodeDelta int32, distCode uint32) {
	c.insertLen = insertLen
	c.copyLen = copyLen | uint32(uint8(int8(lenCodeDelta)))<<25
	c.distPrefix, c.distExtra = prefixEncodeDistance(distCode)
	c.cmdPrefix = lengthCode(insertLen, uint32(int32(copyLen)+lenCodeDelta), c.distPrefix&0x3FF == 0)
}

func (c *command) initInsert(insertLen uint32) {
	c.insertLen = insertLen
	c.copyLen = 4 << 25
	c.distExtra = 0
	c.distPrefix = numDistShort
	c.cmdPrefix = lengthCode(insertLen, 4, false)
}

// restoreDistanceCode inverts prefixEncodeDistance.
func (c *command) restoreDistanceCode() uint32 {
	d := uint32(c.distPrefix & 0x3FF)
	if d < numDistShort {
		return d
	}
	nbits := uint32(c.distPrefix >> 10)
	h := d - numDistShort
	offset := ((2 + (h & 1)) << nbits) - 4
	return offset + c.distExtra + numDistShort
}

func (c *command) distanceContext() uint32 {
	r, k := uint32(c.cmdPrefix>>6), uint32(c.cmdPrefix&7)
	if (r == 0 || r == 2 || r == 4 || r == 7) && k <= 2 {
		return k
	}
	return 3
}

type searchResult struct {
	len, distance uint64
	score         uint64
	lenCodeDelta  int32
}

type hasher struct {
	h6          bool
	shift       uint
	blockBits   uint
	blockSize   uint32
	blockMask   uint32
	nLast       int
	num         []uint16
	buckets     []uint32
	dictLookups uint64
	dictMatches uint64
	dict        *encDict
}

func newHasher(quality, lgwin, sizeHint int, dict *encDict) *hasher {
	h := &hasher{dict: dict, blockBits: uint(quality - 1)}
	bucketBits := uint(15)
	if sizeHint >= 1<<20 && lgwin >= 19 {
		h.h6 = true
	} else if quality < 7 {
		bucketBits = 14
	}
	h.shift = 32 - bucketBits
	h.blockSize = 1 << h.blockBits
	h.blockMask = h.blockSize - 1
	switch {
	case quality < 7:
		h.nLast = 4
	case quality < 9:
		h.nLast = 10
	default:
		h.nLast = 16
	}
	h.num = make([]uint16, 1<<bucketBits)
	h.buckets = make([]uint32, (1<<bucketBits)<<h.blockBits)
	return h
}

func (h *hasher) typeLen() int {
	if h.h6 {
		return 8
	}
	return 4
}

func (h *hasher) key(data []byte, p int) uint32 {
	if h.h6 {
		return uint32((binary.LittleEndian.Uint64(data[p:]) * h6Mul) >> (64 - 15))
	}
	return (binary.LittleEndian.Uint32(data[p:]) * hashMul32) >> h.shift
}

func (h *hasher) store(data []byte, ix int) {
	key := h.key(data, ix)
	off := uint32(h.num[key])&h.blockMask + key<<h.blockBits
	h.num[key]++
	h.buckets[off] = uint32(ix)
}

func matchLen(data []byte, a, b int, limit int) int {
	n := 0
	for n+8 <= limit {
		x := binary.LittleEndian.Uint64(data[a+n:]) ^ binary.LittleEndian.Uint64(data[b+n:])
		if x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
		n += 8
	}
	for n < limit && data[a+n] == data[b+n] {
		n++
	}
	return n
}

func prepareDistanceCache(dc *[16]int32, n int) {
	if n > 4 {
		l := dc[0]
		dc[4], dc[5], dc[6], dc[7], dc[8], dc[9] = l-1, l+1, l-2, l+2, l-3, l+3
		if n > 10 {
			m := dc[1]
			dc[10], dc[11], dc[12], dc[13], dc[14], dc[15] = m-1, m+1, m-2, m+2, m-3, m+3
		}
	}
}

func backwardScore(length, backward uint64) uint64 {
	return scoreBase + 135*length - 30*uint64(log2Floor(uint32(backward)))
}

// findLongestMatch is FN(FindLongestMatch) of H5/H6 on a non-wrapping buffer.
func (h *hasher) findLongestMatch(data []byte, dc *[16]int32, cur int, maxLength, maxBackward, dictDistance, maxDistance uint64, out *searchResult) {
	minScore, bestScore, bestLen := out.score, out.score, out.len
	key := h.key(data, cur)
	bucket := h.buckets[key<<h.blockBits : (key+1)<<h.blockBits]
	out.len, out.lenCodeDelta = 0, 0
	for i := 0; i < h.nLast; i++ {
		backward := uint64(int64(dc[i])) // size_t conversion of an int
		prev := uint64(cur) - backward
		if prev >= uint64(cur) {
			continue
		}
		if backward > maxBackward {
			continue
		}
		p := int(prev)
		if data[cur+int(bestLen)] != data[p+int(bestLen)] {
			continue
		}
		l := uint64(matchLen(data, p, cur, int(maxLength)))
		if l >= 3 || (l == 2 && i < 2) {
			score := 135*l + scoreBase + 15
			if bestScore < score {
				if i != 0 {
					score -= 39 + uint64((0x1CA10>>(uint(i)&0xE))&0xE)
				}
				if bestScore < score {
					bestScore, bestLen = score, l
					out.len, out.distance, out.score = l, backward, score
				}
			}
		}
	}
	if bestLen < 3 {
		bestLen = 3
	}
	nk := uint32(h.num[key])
	down := uint32(0)
	if nk > h.blockSize {
		down = nk - h.blockSize
	}
	first4 := binary.LittleEndian.Uint32(data[cur:])
	for i := nk; i > down; {
		i--
		prev := bucket[i&h.blockMask]
		backward := uint64(cur) - uint64(prev)
		if backward > maxBackward {
			break
		}
		p := int(prev)
		bl := int(bestLen)
		if binary.LittleEndian.Uint32(data[cur+bl-3:]) != binary.LittleEndian.Uint32(data[p+bl-3:]) {
			continue
		}
		var l uint64
		if h.h6 {
			if binary.LittleEndian.Uint32(data[p:]) != first4 {
				continue
			}
			l = uint64(matchLen(data, p+4, cur+4, int(maxLength)-4)) + 4
		} else {
			if l = uint64(matchLen(data, p, cur, int(maxLength))); l < 4 {
				continue
			}
		}
		if score := backwardScore(l, backward); bestScore < score {
			bestScore, bestLen = score, l
			out.len, out.distance, out.score = l, backward, score
		}
	}
	bucket[uint32(h.num[key])&h.blockMask] = uint32(cur)
	h.num[key]++
	if minScore == out.score {
		h.searchStaticDictionary(data, cur, maxLength, dictDistance, maxDistance, out)
	}
}

// searchStaticDictionary is SearchInStaticDictionary (deep probe: two buckets).
func (h *hasher) searchStaticDictionary(data []byte, cur int, maxLength, maxBackward, maxDistance uint64, out *searchResult) {
	if h.dictMatches < h.dictLookups>>7 {
		return
	}
	d := h.dict
	key := ((binary.LittleEndian.Uint32(data[cur:]) * hashMul32) >> 18) << 1
	for i := 0; i < 2; i, key = i+1, key+1 {
		h.dictLookups++
		l := uint64(d.hashLengths[key])
		if l == 0 || l > maxLength {
			continue
		}
		wordIdx := uint64(d.hashWords[key])
		off := int(d.offsets[l]) + int(l)*int(wordIdx)
		ml := uint64(0)
		for ml < l && data[cur+int(ml)] == d.data[off+int(ml)] {
			ml++
		}
		if ml+10 <= l || ml == 0 {
			continue
		}
		cut := l - ml
		tid := cut<<2 + (0x071B520ADA2D3200>>(cut*6))&0x3F
		backward := maxBackward + 1 + wordIdx + tid<<d.sizeBits[l]
		if backward > maxDistance {
			continue
		}
		score := backwardScore(ml, backward)
		if score < out.score {
			continue
		}
		out.len, out.lenCodeDelta, out.distance, out.score = ml, int32(l)-int32(ml), backward, score
		h.dictMatches++
	}
}

func computeDistanceCode(distance, maxDistance uint64, dc *[16]int32) uint32 {
	if distance <= maxDistance {
		d3 := distance + 3
		o0, o1 := d3-uint64(int64(dc[0])), d3-uint64(int64(dc[1]))
		switch {
		case distance == uint64(int64(dc[0])):
			return 0
		case distance == uint64(int64(dc[1])):
			return 1
		case o0 < 7:
			return (0x9750468 >> (4 * o0)) & 0xF
		case o1 < 7:
			return (0xFDB1ACE >> (4 * o1)) & 0xF
		case distance == uint64(int64(dc[2])):
			return 2
		case distance == uint64(int64(dc[3])):
			return 3
		}
	}
	return uint32(distance) + numDistShort - 1
}

// createBackwardReferences is BrotliCreateBackwardReferences for one input block.
func (e *encoder) createBackwardReferences(numBytes, position int) {
	h, data, dc := e.h, e.buf, &e.distCache
	maxBackwardLimit := uint64(1)<<uint(e.lgwin) - windowGap
	insertLength := e.lastInsertLen
	posEnd := position + numBytes
	look := h.typeLen()
	storeEnd := position
	if numBytes >= look {
		storeEnd = position + numBytes - look + 1
	}
	spree := 64
	if e.quality >= 9 {
		spree = 512
	}
	applyRandom := position + spree
	const minScore = scoreBase + 100
	const maxDist = 0x3FFFFFC
	prepareDistanceCache(dc, h.nLast)
	for position+look < posEnd {
		maxLength := uint64(posEnd - position)
		maxDistance := min(uint64(position), maxBackwardLimit)
		sr := searchResult{score: minScore}
		h.findLongestMatch(data, dc, position, maxLength, maxDistance, maxDistance, maxDist, &sr)
		if sr.score > minScore {
			delayed := 0
			maxLength--
			for ; ; maxLength-- {
				sr2 := searchResult{score: minScore}
				md := min(uint64(position+1), maxBackwardLimit)
				h.findLongestMatch(data, dc, position+1, maxLength, md, md, maxDist, &sr2)
				if sr2.score >= sr.score+175 {
					position++
					insertLength++
					sr = sr2
					if delayed++; delayed < 4 && position+look < posEnd {
						continue
					}
				}
				break
			}
			applyRandom = position + 2*int(sr.len) + spree
			dictStart := min(uint64(position), maxBackwardLimit)
			code := computeDistanceCode(sr.distance, dictStart, dc)
			if sr.distance <= dictStart && code > 0 {
				dc[3], dc[2], dc[1], dc[0] = dc[2], dc[1], dc[0], int32(sr.distance)
				prepareDistanceCache(dc, h.nLast)
			}
			e.cmds = append(e.cmds, command{})
			e.cmds[len(e.cmds)-1].init(uint32(insertLength), uint32(sr.len), sr.lenCodeDelta, code)
			e.numLiterals += insertLength
			insertLength = 0
			rs := position + 2
			re := min(position+int(sr.len), storeEnd)
			if sr.distance < sr.len>>2 {
				rs = min(re, max(rs, position+int(sr.len)-int(sr.distance<<2)))
			}
			for i := rs; i < re; i++ {
				h.store(data, i)
			}
			position += int(sr.len)
		} else {
			insertLength++
			position++
			if position > applyRandom {
				if position > applyRandom+4*spree {
					margin := max(look-1, 4)
					jump := min(position+16, posEnd-margin)
					for ; position < jump; position += 4 {
						h.store(data, position)
						insertLength += 4
					}
				} else {
					margin := max(look-1, 2)
					jump := min(position+8, posEnd-margin)
					for ; position < jump; position += 2 {
						h.store(data, position)
						insertLength += 2
					}
				}
			}
		}
	}
	insertLength += posEnd - position
	e.lastInsertLen = insertLength
}
