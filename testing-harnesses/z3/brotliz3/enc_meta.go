package brotliz3

// Greedy meta-block construction of the reference encoder (qualities below 10): literal context
// decisions from sampled statistics and the three greedy block splitters.

const (
	numCommandSymbols = 704
	numDistSymbols    = 64
	maxBlockTypes     = 256
	maxStaticContexts = 13
)

type blockSplit struct {
	numTypes int
	types    []uint8
	lengths  []uint32
}

type metaBlock struct {
	lit, cmd, dist       blockSplit
	litCtxMap            []uint32 // nil: one histogram per literal block type
	litH, cmdH, distH    []uint32 // flat histograms
	nLitH, nCmdH, nDistH int
}

var (
	ctxMapContinuation = [64]uint32{1, 1, 2, 2}
	ctxMapSimpleUTF8   = [64]uint32{0, 0, 1, 1}
	ctxMapComplexUTF8  = [64]uint32{
		11, 11, 12, 12, 0, 0, 0, 0, 1, 1, 9, 9, 2, 2, 2, 2,
		1, 1, 1, 1, 8, 3, 3, 3, 1, 1, 1, 1, 2, 2, 2, 2,
		8, 4, 4, 4, 8, 7, 4, 4, 8, 0, 0, 0, 3, 3, 3, 3,
		5, 5, 10, 5, 5, 5, 10, 5, 6, 6, 6, 6, 6, 6, 6, 6,
	}
)

// utf8Context is BROTLI_CONTEXT(p1, p2, CONTEXT_UTF8).
func utf8Context(p1, p2 byte) byte { return contextLUT[1024+int(p1)] | contextLUT[1024+256+int(p2)] }

// decideLiteralContexts is DecideOverLiteralContextModeling.
func decideLiteralContexts(data []byte, start, length, quality, sizeHint int) (int, *[64]uint32) {
	if length < 64 {
		return 1, nil
	}
	end := start + length
	if sizeHint >= 1<<20 { // ShouldUseComplexStaticContextMap
		var combined [32]uint32
		var ctx [32 * maxStaticContexts]uint32
		total := uint32(0)
		for p := start; p+64 <= end; p += 4096 {
			prev2, prev1 := data[p], data[p+1]
			for pos := p + 2; pos < p+64; pos++ {
				lit := data[pos]
				c := ctxMapComplexUTF8[utf8Context(prev1, prev2)]
				total++
				combined[lit>>3]++
				ctx[c<<5+uint32(lit>>3)]++
				prev2, prev1 = prev1, lit
			}
		}
		e1 := estimateEntropy(combined[:])
		e2 := 0.0
		for i := 0; i < maxStaticContexts; i++ {
			e2 += estimateEntropy(ctx[i<<5 : (i+1)<<5])
		}
		e0 := 1.0 / float64(total)
		e1, e2 = float64(e1*e0), float64(e2*e0)
		if !(e2 > 3.0 || e1-e2 < 0.2) {
			return maxStaticContexts, &ctxMapComplexUTF8
		}
	}
	var bigram [9]uint32
	lut := [4]int{0, 0, 1, 2}
	for p := start; p+64 <= end; p += 4096 {
		prev := lut[data[p]>>6] * 3
		for pos := p + 1; pos < p+64; pos++ {
			lit := data[pos]
			bigram[prev+lut[lit>>6]]++
			prev = lut[lit>>6] * 3
		}
	}
	// ChooseContextMap
	var mono [3]uint32
	var two [6]uint32
	for i := 0; i < 9; i++ {
		mono[i%3] += bigram[i]
		two[i%6] += bigram[i]
	}
	var ent [4]float64
	ent[1] = estimateEntropy(mono[:])
	ent[2] = estimateEntropy(two[:3]) + estimateEntropy(two[3:])
	for i := 0; i < 3; i++ {
		ent[3] += estimateEntropy(bigram[3*i : 3*i+3])
	}
	total := mono[0] + mono[1] + mono[2]
	ent[0] = 1.0 / float64(total)
	ent[1], ent[2], ent[3] = float64(ent[1]*ent[0]), float64(ent[2]*ent[0]), float64(ent[3]*ent[0])
	if quality < 7 {
		ent[3] = float64(ent[1] * 10)
	}
	switch {
	case ent[1]-ent[2] < 0.2 && ent[1]-ent[3] < 0.2:
		return 1, nil
	case ent[2]-ent[3] < 0.02:
		return 2, &ctxMapSimpleUTF8
	}
	return 3, &ctxMapContinuation
}

// splitter is the greedy block splitter for one category with nctx histograms per block type
// (BlockSplitter for nctx == 1, ContextBlockSplitter otherwise).
type splitter struct {
	alpha, nctx  int
	maxTypes     int
	minBlock     int
	thr          float64
	numBlocks    int
	split        *blockSplit
	hist         []uint32 // (types+1) * nctx * alpha
	target, size int
	cur          int // current histogram index (in histograms)
	last         [2]int
	lastEntropy  [2 * maxStaticContexts]float64
	mergeLast    int
	combined     []uint32
	nHist        int
}

func newSplitter(alpha, nctx, minBlock int, thr float64, numSymbols int, split *blockSplit) *splitter {
	maxBlocks := numSymbols/minBlock + 1
	s := &splitter{alpha: alpha, nctx: nctx, minBlock: minBlock, thr: thr, split: split, target: minBlock}
	s.maxTypes = maxBlockTypes / nctx
	if nctx == 1 {
		s.maxTypes = maxBlockTypes
	}
	types := min(maxBlocks, s.maxTypes+1)
	s.nHist = types * nctx
	s.hist = make([]uint32, (s.nHist+nctx)*alpha)
	s.combined = make([]uint32, 2*nctx*alpha)
	split.types = make([]uint8, 0, maxBlocks)
	split.lengths = make([]uint32, 0, maxBlocks)
	return s
}

func (s *splitter) h(i int) []uint32 { return s.hist[i*s.alpha : (i+1)*s.alpha] }

func (s *splitter) clear(i, n int) {
	if i < s.nHist {
		c := s.hist[i*s.alpha : (i+n)*s.alpha]
		for k := range c {
			c[k] = 0
		}
	}
}

func (s *splitter) add(symbol, ctx int) {
	s.hist[(s.cur+ctx)*s.alpha+symbol]++
	s.size++
	if s.size == s.target {
		s.finish(false)
	}
}

func (s *splitter) finish(final bool) {
	sp, n := s.split, s.nctx
	if s.size < s.minBlock {
		s.size = s.minBlock
	}
	if s.numBlocks == 0 {
		sp.lengths = append(sp.lengths, uint32(s.size))
		sp.types = append(sp.types, 0)
		for i := 0; i < n; i++ {
			s.lastEntropy[i] = bitsEntropy(s.h(i))
			s.lastEntropy[n+i] = s.lastEntropy[i]
		}
		s.numBlocks++
		sp.numTypes++
		s.cur += n
		s.clear(s.cur, n)
		s.size = 0
	} else if s.size > 0 {
		var entropy [maxStaticContexts]float64
		var combinedEntropy [2 * maxStaticContexts]float64
		var diff [2]float64
		for i := 0; i < n; i++ {
			curH := s.h(s.cur + i)
			entropy[i] = bitsEntropy(curH)
			for j := 0; j < 2; j++ {
				jx := j*n + i
				c := s.combined[jx*s.alpha : (jx+1)*s.alpha]
				lastH := s.h(s.last[j] + i)
				for k := range c {
					c[k] = curH[k] + lastH[k]
				}
				combinedEntropy[jx] = bitsEntropy(c)
				diff[j] += combinedEntropy[jx] - entropy[i] - s.lastEntropy[jx]
			}
		}
		switch {
		case sp.numTypes < s.maxTypes && diff[0] > s.thr && diff[1] > s.thr:
			sp.lengths = append(sp.lengths, uint32(s.size))
			sp.types = append(sp.types, uint8(sp.numTypes))
			s.last[1] = s.last[0]
			s.last[0] = int(uint8(sp.numTypes)) * n
			if n > 1 {
				s.last[0] = sp.numTypes * n
			}
			for i := 0; i < n; i++ {
				s.lastEntropy[n+i] = s.lastEntropy[i]
				s.lastEntropy[i] = entropy[i]
			}
			s.numBlocks++
			sp.numTypes++
			s.cur += n
			s.clear(s.cur, n)
			s.size = 0
			s.mergeLast = 0
			s.target = s.minBlock
		case diff[1] < diff[0]-20.0:
			sp.lengths = append(sp.lengths, uint32(s.size))
			sp.types = append(sp.types, sp.types[s.numBlocks-2])
			s.last[0], s.last[1] = s.last[1], s.last[0]
			for i := 0; i < n; i++ {
				copy(s.h(s.last[0]+i), s.combined[(n+i)*s.alpha:(n+i+1)*s.alpha])
				s.lastEntropy[n+i] = s.lastEntropy[i]
				s.lastEntropy[i] = combinedEntropy[n+i]
			}
			s.numBlocks++
			s.size = 0
			s.clearAlways(s.cur, n)
			s.mergeLast = 0
			s.target = s.minBlock
		default:
			sp.lengths[s.numBlocks-1] += uint32(s.size)
			for i := 0; i < n; i++ {
				copy(s.h(s.last[0]+i), s.combined[i*s.alpha:(i+1)*s.alpha])
				s.lastEntropy[i] = combinedEntropy[i]
				if sp.numTypes == 1 {
					s.lastEntropy[n+i] = s.lastEntropy[i]
				}
			}
			s.size = 0
			s.clearAlways(s.cur, n)
			if s.mergeLast++; s.mergeLast > 1 {
				s.target += s.minBlock
			}
		}
	}
	if final {
		s.nHist = sp.numTypes * n
	}
}

func (s *splitter) clearAlways(i, n int) {
	c := s.hist[i*s.alpha : (i+n)*s.alpha]
	for k := range c {
		c[k] = 0
	}
}

// buildMetaBlockGreedy is BrotliBuildMetaBlockGreedy.
func buildMetaBlockGreedy(data []byte, pos int, prev1, prev2 byte, nctx int, ctxMap *[64]uint32, cmds []command) *metaBlock {
	mb := &metaBlock{}
	numLit := 0
	for i := range cmds {
		numLit += int(cmds[i].insertLen)
	}
	lit := newSplitter(256, nctx, 512, 400.0, numLit, &mb.lit)
	cmd := newSplitter(numCommandSymbols, 1, 1024, 500.0, len(cmds), &mb.cmd)
	dist := newSplitter(numDistSymbols, 1, 512, 100.0, len(cmds), &mb.dist)
	for i := range cmds {
		c := &cmds[i]
		cmd.add(int(c.cmdPrefix), 0)
		for j := c.insertLen; j != 0; j-- {
			l := data[pos]
			if nctx == 1 {
				lit.add(int(l), 0)
			} else {
				lit.add(int(l), int(ctxMap[utf8Context(prev1, prev2)]))
			}
			prev2, prev1 = prev1, l
			pos++
		}
		pos += int(c.copyLength())
		if c.copyLength() != 0 {
			prev2, prev1 = data[pos-2], data[pos-1]
			if c.cmdPrefix >= 128 {
				dist.add(int(c.distPrefix&0x3FF), 0)
			}
		}
	}
	lit.finish(true)
	cmd.finish(true)
	dist.finish(true)
	mb.litH, mb.nLitH = lit.hist, lit.nHist
	mb.cmdH, mb.nCmdH = cmd.hist, cmd.nHist
	mb.distH, mb.nDistH = dist.hist, dist.nHist
	if nctx > 1 {
		mb.litCtxMap = make([]uint32, mb.lit.numTypes<<6)
		for i := 0; i < mb.lit.numTypes; i++ {
			for j := 0; j < 64; j++ {
				mb.litCtxMap[i<<6+j] = uint32(i*nctx) + ctxMap[j]
			}
		}
	}
	// BrotliOptimizeHistograms
	var good [numCommandSymbols]uint8
	for i := 0; i < mb.nLitH; i++ {
		optimizeHuffmanCountsForRle(mb.litH[i*256:(i+1)*256], good[:])
	}
	for i := 0; i < mb.nCmdH; i++ {
		optimizeHuffmanCountsForRle(mb.cmdH[i*numCommandSymbols:(i+1)*numCommandSymbols], good[:])
	}
	for i := 0; i < mb.nDistH; i++ {
		optimizeHuffmanCountsForRle(mb.distH[i*numDistSymbols:(i+1)*numDistSymbols], good[:])
	}
	return mb
}
