package brotliz3

// Bit-stream layer of the reference encoder (brotli_bit_stream.c): meta-block headers, Huffman
// code storage, block-switch codes, context maps and the command/literal/distance symbols.

type bitWriter struct {
	buf  []byte
	acc  uint64
	nacc uint
}

func (w *bitWriter) write(n uint, v uint64) {
	for n > 32 { // callers stay below 57 bits; split to keep the accumulator in range
		w.write(32, v&0xffffffff)
		v >>= 32
		n -= 32
	}
	w.acc |= v << w.nacc
	w.nacc += n
	for w.nacc >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
}

func (w *bitWriter) align() {
	if w.nacc > 0 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc, w.nacc = 0, 0
	}
}

type bitMark struct {
	n    int
	acc  uint64
	nacc uint
}

func (w *bitWriter) mark() bitMark   { return bitMark{len(w.buf), w.acc, w.nacc} }
func (w *bitWriter) reset(m bitMark) { w.buf, w.acc, w.nacc = w.buf[:m.n], m.acc, m.nacc }

// bitsSince is the reference's storage_ix relative to the mark (carried partial byte included).
func (w *bitWriter) bitsSince(m bitMark) int { return (len(w.buf)-m.n)*8 + int(w.nacc) }

var blockLenRanges = [26][2]uint32{{1, 2}, {5, 2}, {9, 2}, {13, 2}, {17, 3}, {25, 3}, {33, 3}, {41, 3}, {49, 4}, {65, 4}, {81, 4}, {97, 4},
	{113, 5}, {145, 5}, {177, 5}, {209, 5}, {241, 6}, {305, 6}, {369, 7}, {497, 8}, {753, 9}, {1265, 10}, {2289, 11}, {4337, 12}, {8433, 13}, {16625, 24}}

func blockLenCode(l uint32) int {
	code := 0
	switch {
	case l >= 753:
		code = 20
	case l >= 177:
		code = 14
	case l >= 41:
		code = 7
	}
	for code < 25 && l >= blockLenRanges[code+1][0] {
		code++
	}
	return code
}

func (w *bitWriter) varLenUint8(n int) {
	if n == 0 {
		w.write(1, 0)
		return
	}
	nb := uint(log2Floor(uint32(n)))
	w.write(1, 1)
	w.write(3, uint64(nb))
	w.write(nb, uint64(n)-1<<nb)
}

func encodeMlen(length int) (bits uint64, nbits uint, nibbles uint64) {
	lg := uint(1)
	if length != 1 {
		lg = uint(log2Floor(uint32(length-1))) + 1
	}
	mn := uint(4)
	if lg >= 16 {
		mn = (lg + 3) / 4
	}
	return uint64(length - 1), mn * 4, uint64(mn - 4)
}

func (w *bitWriter) uncompressedMetaBlock(final bool, data []byte) {
	bits, nb, nib := encodeMlen(len(data))
	w.write(1, 0)
	w.write(2, nib)
	w.write(nb, bits)
	w.write(1, 1)
	w.align()
	w.buf = append(w.buf, data...)
	if final {
		w.write(1, 1)
		w.write(1, 1)
		w.align()
	}
}

type huffScratch struct {
	tree   [2*numCommandSymbols + 1]huffNode
	ht, hx []uint8
}

// storeHuffmanTree is BrotliStoreHuffmanTree (the complex code-length form).
func (w *bitWriter) storeHuffmanTree(depths []uint8, sc *huffScratch) {
	sc.ht, sc.hx = writeHuffmanTree(depths, sc.ht[:0], sc.hx[:0])
	var histo [18]uint32
	for _, s := range sc.ht {
		histo[s]++
	}
	numCodes, code := 0, 0
	for i := 0; i < 18; i++ {
		if histo[i] != 0 {
			if numCodes == 0 {
				code, numCodes = i, 1
			} else if numCodes == 1 {
				numCodes = 2
				break
			}
		}
	}
	var clDepth [18]uint8
	var clBits [18]uint16
	createHuffmanTree(histo[:], 5, sc.tree[:], clDepth[:])
	depthsToSymbols(clDepth[:], clBits[:])
	// BrotliStoreHuffmanTreeOfHuffmanTreeToBitMask
	order := [18]uint8{1, 2, 3, 4, 0, 5, 17, 6, 16, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	sym := [6]uint64{0, 7, 3, 2, 1, 15}
	ln := [6]uint{2, 4, 3, 2, 2, 4}
	skip, store := 0, 18
	if numCodes > 1 {
		for ; store > 0; store-- {
			if clDepth[order[store-1]] != 0 {
				break
			}
		}
	}
	if clDepth[order[0]] == 0 && clDepth[order[1]] == 0 {
		skip = 2
		if clDepth[order[2]] == 0 {
			skip = 3
		}
	}
	w.write(2, uint64(skip))
	for i := skip; i < store; i++ {
		l := clDepth[order[i]]
		w.write(ln[l], sym[l])
	}
	if numCodes == 1 {
		clDepth[code] = 0
	}
	for i, ix := range sc.ht {
		w.write(uint(clDepth[ix]), uint64(clBits[ix]))
		switch ix {
		case 16:
			w.write(2, uint64(sc.hx[i]))
		case 17:
			w.write(3, uint64(sc.hx[i]))
		}
	}
}

// buildAndStoreHuffmanTree is BuildAndStoreHuffmanTree.
func (w *bitWriter) buildAndStoreHuffmanTree(histo []uint32, alphabet int, depth []uint8, bits []uint16, sc *huffScratch) {
	count := 0
	var s4 [4]int
	for i, h := range histo {
		if h != 0 {
			if count < 4 {
				s4[count] = i
			} else if count > 4 {
				break
			}
			count++
		}
	}
	maxBits := uint(0)
	for c := alphabet - 1; c != 0; c >>= 1 {
		maxBits++
	}
	if count <= 1 {
		w.write(4, 1)
		w.write(maxBits, uint64(s4[0]))
		depth[s4[0]], bits[s4[0]] = 0, 0
		return
	}
	for i := range depth[:len(histo)] {
		depth[i] = 0
	}
	createHuffmanTree(histo, 15, sc.tree[:], depth)
	depthsToSymbols(depth[:len(histo)], bits)
	if count > 4 {
		w.storeHuffmanTree(depth[:len(histo)], sc)
		return
	}
	// StoreSimpleHuffmanTree
	w.write(2, 1)
	w.write(2, uint64(count-1))
	for i := 0; i < count; i++ {
		for j := i + 1; j < count; j++ {
			if depth[s4[j]] < depth[s4[i]] {
				s4[j], s4[i] = s4[i], s4[j]
			}
		}
	}
	for i := 0; i < count; i++ {
		w.write(maxBits, uint64(s4[i]))
	}
	if count == 4 {
		v := uint64(0)
		if depth[s4[0]] == 1 {
			v = 1
		}
		w.write(1, v)
	}
}

type typeCodeCalc struct{ last, secondLast int }

func (c *typeCodeCalc) next(t uint8) int {
	code := int(t) + 2
	if int(t) == c.last+1 {
		code = 1
	} else if int(t) == c.secondLast {
		code = 0
	}
	c.secondLast, c.last = c.last, int(t)
	return code
}

// blockEncoder is BlockEncoder: entropy codes per histogram plus the block-switch code.
type blockEncoder struct {
	alpha      int
	split      *blockSplit
	calc       typeCodeCalc
	typeDepths [maxBlockTypes + 2]uint8
	typeBits   [maxBlockTypes + 2]uint16
	lenDepths  [26]uint8
	lenBits    [26]uint16
	blockIx    int
	blockLen   uint32
	entropyIx  int
	depths     []uint8
	bits       []uint16
}

func newBlockEncoder(alpha int, split *blockSplit) *blockEncoder {
	b := &blockEncoder{alpha: alpha, split: split, calc: typeCodeCalc{last: 1}}
	if len(split.lengths) > 0 {
		b.blockLen = split.lengths[0]
	}
	return b
}

func (b *blockEncoder) storeSwitch(w *bitWriter, blockLen uint32, blockType uint8, first bool) {
	tc := b.calc.next(blockType)
	if !first {
		w.write(uint(b.typeDepths[tc]), uint64(b.typeBits[tc]))
	}
	lc := blockLenCode(blockLen)
	w.write(uint(b.lenDepths[lc]), uint64(b.lenBits[lc]))
	w.write(uint(blockLenRanges[lc][1]), uint64(blockLen-blockLenRanges[lc][0]))
}

// storeSplitCode is BuildAndStoreBlockSplitCode.
func (b *blockEncoder) storeSplitCode(w *bitWriter, sc *huffScratch) {
	sp := b.split
	var typeHisto [maxBlockTypes + 2]uint32
	var lenHisto [26]uint32
	calc := typeCodeCalc{last: 1}
	for i, t := range sp.types {
		tc := calc.next(t)
		if i != 0 {
			typeHisto[tc]++
		}
		lenHisto[blockLenCode(sp.lengths[i])]++
	}
	w.varLenUint8(sp.numTypes - 1)
	if sp.numTypes > 1 {
		w.buildAndStoreHuffmanTree(typeHisto[:sp.numTypes+2], sp.numTypes+2, b.typeDepths[:], b.typeBits[:], sc)
		w.buildAndStoreHuffmanTree(lenHisto[:], 26, b.lenDepths[:], b.lenBits[:], sc)
		b.storeSwitch(w, sp.lengths[0], sp.types[0], true)
	}
}

func (b *blockEncoder) storeCodes(w *bitWriter, histo []uint32, n, alphabet int, sc *huffScratch) {
	b.depths = make([]uint8, n*b.alpha)
	b.bits = make([]uint16, n*b.alpha)
	for i := 0; i < n; i++ {
		w.buildAndStoreHuffmanTree(histo[i*b.alpha:(i+1)*b.alpha], alphabet, b.depths[i*b.alpha:(i+1)*b.alpha], b.bits[i*b.alpha:(i+1)*b.alpha], sc)
	}
}

func (b *blockEncoder) symbol(w *bitWriter, sym int) {
	if b.blockLen == 0 {
		b.blockIx++
		b.blockLen = b.split.lengths[b.blockIx]
		t := b.split.types[b.blockIx]
		b.entropyIx = int(t) * b.alpha
		b.storeSwitch(w, b.blockLen, t, false)
	}
	b.blockLen--
	ix := b.entropyIx + sym
	w.write(uint(b.depths[ix]), uint64(b.bits[ix]))
}

func (b *blockEncoder) symbolCtx(w *bitWriter, sym, ctx int, ctxMap []uint32, ctxBits uint) {
	if b.blockLen == 0 {
		b.blockIx++
		b.blockLen = b.split.lengths[b.blockIx]
		t := b.split.types[b.blockIx]
		b.entropyIx = int(t) << ctxBits
		b.storeSwitch(w, b.blockLen, t, false)
	}
	b.blockLen--
	ix := int(ctxMap[b.entropyIx+ctx])*b.alpha + sym
	w.write(uint(b.depths[ix]), uint64(b.bits[ix]))
}

// trivialContextMap is StoreTrivialContextMap.
func (w *bitWriter) trivialContextMap(numTypes int, ctxBits int, sc *huffScratch) {
	w.varLenUint8(numTypes - 1)
	if numTypes <= 1 {
		return
	}
	repeatCode := ctxBits - 1
	repeatBits := uint64(1)<<uint(repeatCode) - 1
	alpha := numTypes + repeatCode
	histo := make([]uint32, alpha)
	depths := make([]uint8, alpha)
	bits := make([]uint16, alpha)
	w.write(1, 1)
	w.write(4, uint64(repeatCode-1))
	histo[repeatCode] = uint32(numTypes)
	histo[0] = 1
	for i := ctxBits; i < alpha; i++ {
		histo[i] = 1
	}
	w.buildAndStoreHuffmanTree(histo, alpha, depths, bits, sc)
	for i := 0; i < numTypes; i++ {
		code := 0
		if i != 0 {
			code = i + ctxBits - 1
		}
		w.write(uint(depths[code]), uint64(bits[code]))
		w.write(uint(depths[repeatCode]), uint64(bits[repeatCode]))
		w.write(uint(repeatCode), repeatBits)
	}
	w.write(1, 1)
}

// encodeContextMap is EncodeContextMap (move-to-front, zero-run coding, Huffman).
func (w *bitWriter) encodeContextMap(cm []uint32, numClusters int, sc *huffScratch) {
	w.varLenUint8(numClusters - 1)
	if numClusters == 1 {
		return
	}
	// MoveToFrontTransform
	rle := make([]uint32, len(cm))
	maxV := uint32(0)
	for _, v := range cm {
		if v > maxV {
			maxV = v
		}
	}
	var mtf [256]uint8
	for i := 0; i <= int(maxV); i++ {
		mtf[i] = uint8(i)
	}
	for i, v := range cm {
		idx := 0
		for idx <= int(maxV) && mtf[idx] != uint8(v) {
			idx++
		}
		rle[i] = uint32(idx)
		val := mtf[idx]
		copy(mtf[1:idx+1], mtf[:idx])
		mtf[0] = val
	}
	// RunLengthCodeZeros
	maxPrefix := uint32(6)
	maxReps := uint32(0)
	for i := 0; i < len(rle); {
		reps := uint32(0)
		for ; i < len(rle) && rle[i] != 0; i++ {
		}
		for ; i < len(rle) && rle[i] == 0; i++ {
			reps++
		}
		if reps > maxReps {
			maxReps = reps
		}
	}
	mp := uint32(0)
	if maxReps > 0 {
		mp = log2Floor(maxReps)
	}
	if mp < maxPrefix {
		maxPrefix = mp
	}
	out := 0
	for i := 0; i < len(rle); {
		if rle[i] != 0 {
			rle[out] = rle[i] + maxPrefix
			i++
			out++
			continue
		}
		reps := uint32(1)
		for k := i + 1; k < len(rle) && rle[k] == 0; k++ {
			reps++
		}
		i += int(reps)
		for reps != 0 {
			if reps < 2<<maxPrefix {
				p := log2Floor(reps)
				rle[out] = p + (reps-1<<p)<<9
				out++
				break
			}
			rle[out] = maxPrefix + (1<<maxPrefix-1)<<9
			reps -= 2<<maxPrefix - 1
			out++
		}
	}
	rle = rle[:out]
	alpha := numClusters + int(maxPrefix)
	histo := make([]uint32, alpha)
	depths := make([]uint8, alpha)
	bits := make([]uint16, alpha)
	for _, s := range rle {
		histo[s&511]++
	}
	if maxPrefix > 0 {
		w.write(1, 1)
		w.write(4, uint64(maxPrefix-1))
	} else {
		w.write(1, 0)
	}
	w.buildAndStoreHuffmanTree(histo, alpha, depths, bits, sc)
	for _, s := range rle {
		sym, extra := s&511, s>>9
		w.write(uint(depths[sym]), uint64(bits[sym]))
		if sym > 0 && sym <= maxPrefix {
			w.write(uint(sym), uint64(extra))
		}
	}
	w.write(1, 1)
}

var (
	insBase   = [24]uint32{0, 1, 2, 3, 4, 5, 6, 8, 10, 14, 18, 26, 34, 50, 66, 98, 130, 194, 322, 578, 1090, 2114, 6210, 22594}
	insExtra  = [24]uint{0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 12, 14, 24}
	copyBase  = [24]uint32{2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 14, 18, 22, 30, 38, 54, 70, 102, 134, 198, 326, 582, 1094, 2118}
	copyExtra = [24]uint{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 24}
)

// storeMetaBlock is BrotliStoreMetaBlock.
func (w *bitWriter) storeMetaBlock(data []byte, start, length int, prev1, prev2 byte, final bool, cmds []command, mb *metaBlock) {
	pos := start
	sc := &huffScratch{}
	// StoreCompressedMetaBlockHeader
	if final {
		w.write(1, 1)
		w.write(1, 0)
	} else {
		w.write(1, 0)
	}
	bits, nb, nib := encodeMlen(length)
	w.write(2, nib)
	w.write(nb, bits)
	if !final {
		w.write(1, 0)
	}
	lit := newBlockEncoder(256, &mb.lit)
	cmd := newBlockEncoder(numCommandSymbols, &mb.cmd)
	dist := newBlockEncoder(numDistSymbols, &mb.dist)
	lit.storeSplitCode(w, sc)
	cmd.storeSplitCode(w, sc)
	dist.storeSplitCode(w, sc)
	w.write(2, 0) // NPOSTFIX
	w.write(4, 0) // NDIRECT >> NPOSTFIX
	for i := 0; i < mb.lit.numTypes; i++ {
		w.write(2, 2) // CONTEXT_UTF8
	}
	if mb.litCtxMap == nil {
		w.trivialContextMap(mb.nLitH, 6, sc)
	} else {
		w.encodeContextMap(mb.litCtxMap, mb.nLitH, sc)
	}
	w.trivialContextMap(mb.nDistH, 2, sc)
	lit.storeCodes(w, mb.litH, mb.nLitH, 256, sc)
	cmd.storeCodes(w, mb.cmdH, mb.nCmdH, numCommandSymbols, sc)
	dist.storeCodes(w, mb.distH, mb.nDistH, numDistSymbols, sc)
	for i := range cmds {
		c := &cmds[i]
		cmd.symbol(w, int(c.cmdPrefix))
		// StoreCommandExtra
		clc := c.copyLenCode()
		ic, cc := insertLenCode(c.insertLen), copyLenCodeOf(clc)
		ie := insExtra[ic]
		w.write(ie+copyExtra[cc], uint64(clc-copyBase[cc])<<ie|uint64(c.insertLen-insBase[ic]))
		if mb.litCtxMap == nil {
			for j := c.insertLen; j != 0; j-- {
				lit.symbol(w, int(data[pos]))
				pos++
			}
		} else {
			for j := c.insertLen; j != 0; j-- {
				l := data[pos]
				lit.symbolCtx(w, int(l), int(utf8Context(prev1, prev2)), mb.litCtxMap, 6)
				prev2, prev1 = prev1, l
				pos++
			}
		}
		pos += int(c.copyLength())
		if c.copyLength() != 0 {
			prev2, prev1 = data[pos-2], data[pos-1]
			if c.cmdPrefix >= 128 {
				dist.symbol(w, int(c.distPrefix&0x3FF))
				w.write(uint(c.distPrefix>>10), uint64(c.distExtra))
			}
		}
	}
	if final {
		w.align()
	}
}
