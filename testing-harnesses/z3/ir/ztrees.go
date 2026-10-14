package ir

import (
	"reflect"

	"zeros3-z3/deflate"
)

// Prediction of a block's kind and dynamic header from its tokens (Z3-08D).
//
// zlib's trees.c (and GNU gzip's, its ancestor) derive BTYPE, the Huffman code
// lengths and the code-length-code run-length sequence deterministically from
// the block's symbol frequencies. This is a port of exactly those decisions
// (build_tree/gen_bitlen with the depth tie-break, scan_tree/send_tree run
// rules, _tr_flush_block's stored/fixed/dynamic cost test) - not of any
// compressor. A block whose real header equals the prediction is stored in the
// skeleton as one flag; any other producer simply fails the equality test at
// encode time and keeps its exact header.

const (
	zLCodes    = 286
	zDCodes    = 30
	zBLCodes   = 19
	zHeapSize  = 2*zLCodes + 1
	zMaxBits   = 15
	zMaxBLBits = 7
)

var zBLOrder = [19]int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}

type zdesc struct {
	slen      []uint8 // static code lengths (nil for the bit-length tree)
	extra     []uint8
	extraBase int
	elems     int
	maxLength int
}

var zLDesc, zDDesc, zBLDesc zdesc

func init() {
	sl := make([]uint8, zLCodes+2)
	for i := range sl {
		switch {
		case i < 144:
			sl[i] = 8
		case i < 256:
			sl[i] = 9
		case i < 280:
			sl[i] = 7
		default:
			sl[i] = 8
		}
	}
	le := make([]uint8, 29)
	for i := range le {
		le[i] = uint8(deflate.LengthExtraBits(257 + i))
	}
	sd := make([]uint8, zDCodes)
	de := make([]uint8, zDCodes)
	for i := range sd {
		sd[i] = 5
		de[i] = uint8(deflate.DistExtraBits(i))
	}
	bl := make([]uint8, zBLCodes)
	bl[16], bl[17], bl[18] = 2, 3, 7
	zLDesc = zdesc{sl, le, 257, zLCodes, zMaxBits}
	zDDesc = zdesc{sd, de, 0, zDCodes, zMaxBits}
	zBLDesc = zdesc{nil, bl, 0, zBLCodes, zMaxBLBits}
}

type ztree struct {
	freq, ln, dad []int
	maxCode       int
}

func newZTree(n int) *ztree {
	return &ztree{freq: make([]int, 2*n+2), ln: make([]int, 2*n+2), dad: make([]int, 2*n+2)}
}

type zbuilder struct {
	heap             [zHeapSize + 1]int
	heapLen, heapMax int
	depth            [zHeapSize + 1]uint8
	blCount          [zMaxBits + 1]int
	optLen, stLen    int
}

func (b *zbuilder) smaller(t *ztree, n, m int) bool {
	return t.freq[n] < t.freq[m] || t.freq[n] == t.freq[m] && b.depth[n] <= b.depth[m]
}

func (b *zbuilder) down(t *ztree, k int) {
	v := b.heap[k]
	j := k << 1
	for j <= b.heapLen {
		if j < b.heapLen && b.smaller(t, b.heap[j+1], b.heap[j]) {
			j++
		}
		if b.smaller(t, v, b.heap[j]) {
			break
		}
		b.heap[k] = b.heap[j]
		k = j
		j <<= 1
	}
	b.heap[k] = v
}

func (b *zbuilder) build(t *ztree, d *zdesc) {
	maxCode := -1
	b.heapLen, b.heapMax = 0, zHeapSize
	for n := 0; n < d.elems; n++ {
		if t.freq[n] != 0 {
			b.heapLen++
			b.heap[b.heapLen] = n
			maxCode = n
			b.depth[n] = 0
		} else {
			t.ln[n] = 0
		}
	}
	for b.heapLen < 2 {
		node := 0
		if maxCode < 2 {
			maxCode++
			node = maxCode
		}
		b.heapLen++
		b.heap[b.heapLen] = node
		t.freq[node] = 1
		b.depth[node] = 0
		b.optLen--
		if d.slen != nil {
			b.stLen -= int(d.slen[node])
		}
	}
	t.maxCode = maxCode
	for n := b.heapLen / 2; n >= 1; n-- {
		b.down(t, n)
	}
	node := d.elems
	for {
		n := b.heap[1]
		b.heap[1] = b.heap[b.heapLen]
		b.heapLen--
		b.down(t, 1)
		m := b.heap[1]
		b.heapMax--
		b.heap[b.heapMax] = n
		b.heapMax--
		b.heap[b.heapMax] = m
		t.freq[node] = t.freq[n] + t.freq[m]
		b.depth[node] = max(b.depth[n], b.depth[m]) + 1
		t.dad[n], t.dad[m] = node, node
		b.heap[1] = node
		node++
		b.down(t, 1)
		if b.heapLen < 2 {
			break
		}
	}
	b.heapMax--
	b.heap[b.heapMax] = b.heap[1]
	b.genBitlen(t, d)
}

func (b *zbuilder) genBitlen(t *ztree, d *zdesc) {
	maxCode := t.maxCode
	overflow := 0
	for i := range b.blCount {
		b.blCount[i] = 0
	}
	t.ln[b.heap[b.heapMax]] = 0
	h := b.heapMax + 1
	for ; h < zHeapSize; h++ {
		n := b.heap[h]
		bits := t.ln[t.dad[n]] + 1
		if bits > d.maxLength {
			bits = d.maxLength
			overflow++
		}
		t.ln[n] = bits
		if n > maxCode {
			continue
		}
		b.blCount[bits]++
		xbits := 0
		if n >= d.extraBase {
			xbits = int(d.extra[n-d.extraBase])
		}
		f := t.freq[n]
		b.optLen += f * (bits + xbits)
		if d.slen != nil {
			b.stLen += f * (int(d.slen[n]) + xbits)
		}
	}
	if overflow == 0 {
		return
	}
	for {
		bits := d.maxLength - 1
		for b.blCount[bits] == 0 {
			bits--
		}
		b.blCount[bits]--
		b.blCount[bits+1] += 2
		b.blCount[d.maxLength]--
		overflow -= 2
		if overflow <= 0 {
			break
		}
	}
	for bits := d.maxLength; bits != 0; bits-- {
		n := b.blCount[bits]
		for n != 0 {
			h--
			m := b.heap[h]
			if m > maxCode {
				continue
			}
			if t.ln[m] != bits {
				b.optLen += (bits - t.ln[m]) * t.freq[m]
				t.ln[m] = bits
			}
			n--
		}
	}
}

// scan counts the code-length symbols zlib's scan_tree would emit.
func zScan(t *ztree, maxCode int, bl *ztree) {
	prevlen, nextlen, count, maxCount, minCount := -1, t.ln[0], 0, 7, 4
	if nextlen == 0 {
		maxCount, minCount = 138, 3
	}
	t.ln[maxCode+1] = 0xffff
	for n := 0; n <= maxCode; n++ {
		curlen := nextlen
		nextlen = t.ln[n+1]
		count++
		if count < maxCount && curlen == nextlen {
			continue
		} else if count < minCount {
			bl.freq[curlen] += count
		} else if curlen != 0 {
			if curlen != prevlen {
				bl.freq[curlen]++
			}
			bl.freq[16]++
		} else if count <= 10 {
			bl.freq[17]++
		} else {
			bl.freq[18]++
		}
		count, prevlen = 0, curlen
		if nextlen == 0 {
			maxCount, minCount = 138, 3
		} else if curlen == nextlen {
			maxCount, minCount = 6, 3
		} else {
			maxCount, minCount = 7, 4
		}
	}
}

// zSend returns the code-length symbol sequence zlib's send_tree would emit.
func zSend(t *ztree, maxCode int, out []deflate.CLSym) []deflate.CLSym {
	prevlen, nextlen, count, maxCount, minCount := -1, t.ln[0], 0, 7, 4
	if nextlen == 0 {
		maxCount, minCount = 138, 3
	}
	for n := 0; n <= maxCode; n++ {
		curlen := nextlen
		nextlen = t.ln[n+1]
		count++
		if count < maxCount && curlen == nextlen {
			continue
		} else if count < minCount {
			for ; count != 0; count-- {
				out = append(out, deflate.CLSym{Sym: uint8(curlen)})
			}
		} else if curlen != 0 {
			if curlen != prevlen {
				out = append(out, deflate.CLSym{Sym: uint8(curlen)})
				count--
			}
			out = append(out, deflate.CLSym{Sym: 16, Extra: uint8(count - 3)})
		} else if count <= 10 {
			out = append(out, deflate.CLSym{Sym: 17, Extra: uint8(count - 3)})
		} else {
			out = append(out, deflate.CLSym{Sym: 18, Extra: uint8(count - 11)})
		}
		count, prevlen = 0, curlen
		if nextlen == 0 {
			maxCount, minCount = 138, 3
		} else if curlen == nextlen {
			maxCount, minCount = 6, 3
		} else {
			maxCount, minCount = 7, 4
		}
	}
	return out
}

// maxPredictTokens keeps zlib's 16-bit frequency fields from overflowing.
const maxPredictTokens = 32767

// zlibBlock predicts what zlib/gzip's _tr_flush_block writes for a block of
// toks (storedLen = its plaintext bytes). ok=false when it is not predictable
// (stored, or too many tokens). For Dynamic, m carries the header fields.
func zlibBlock(toks []deflate.Token, storedLen int) (kind deflate.BlockKind, m BlockMeta, ok bool) {
	if len(toks) == 0 || len(toks) > maxPredictTokens {
		return 0, m, false
	}
	lt, dt, bt := newZTree(zLCodes), newZTree(zDCodes), newZTree(zBLCodes)
	for _, t := range toks {
		lt.freq[t.Sym()]++
		if !t.IsLiteral() {
			dt.freq[t.DistCode()]++
		}
	}
	lt.freq[256]++
	b := &zbuilder{}
	b.build(lt, &zLDesc)
	b.build(dt, &zDDesc)
	zScan(lt, lt.maxCode, bt)
	zScan(dt, dt.maxCode, bt)
	b.build(bt, &zBLDesc)
	maxBL := zBLCodes - 1
	for ; maxBL >= 3; maxBL-- {
		if bt.ln[zBLOrder[maxBL]] != 0 {
			break
		}
	}
	b.optLen += 3*(maxBL+1) + 5 + 5 + 4
	optB, stB := (b.optLen+3+7)>>3, (b.stLen+3+7)>>3
	if stB <= optB {
		optB = stB
	}
	switch {
	case storedLen+4 <= optB:
		return deflate.Stored, m, false
	case stB == optB:
		return deflate.Fixed, m, true
	}
	m.Kind = deflate.Dynamic
	m.HLIT, m.HDIST, m.HCLEN = uint8(lt.maxCode+1-257), uint8(dt.maxCode), uint8(maxBL+1-4)
	for i := 0; i < zBLCodes; i++ {
		m.CLLens[i] = uint8(bt.ln[i])
	}
	m.CLSyms = zSend(lt, lt.maxCode, nil)
	m.CLSyms = zSend(dt, dt.maxCode, m.CLSyms)
	return deflate.Dynamic, m, true
}

// blockPredicted reports whether zlibBlock reproduces the block's kind and header.
func blockPredicted(b *deflate.Block) bool {
	if b.Kind == deflate.Stored {
		return false
	}
	kind, m, ok := zlibBlock(b.Tokens, b.PlainLen)
	if !ok || kind != b.Kind {
		return false
	}
	if kind == deflate.Fixed {
		return true
	}
	return m.HLIT == b.HLIT && m.HDIST == b.HDIST && m.HCLEN == b.HCLEN && m.CLLens == b.CLLens && reflect.DeepEqual(m.CLSyms, b.CLSyms)
}
