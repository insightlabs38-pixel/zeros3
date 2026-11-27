package lzl

import (
	"encoding/binary"
	"math/bits"
)

// Parser modes.
const (
	ModeFast  = 0 // LZMA "fast mode" heuristics: one-step lazy match with distance rules
	ModePrice = 1 // one-step lazy evaluation priced with the live adaptive model
)

// Params selects the match finder effort and the parser.
type Params struct {
	Depth  int // hash-chain candidates examined per position
	Nice   int // a match this long ends the search and is taken
	Mode   int
	Window int  // maximum distance (0: the whole frame)
	AvgAll bool // price parser: value uncovered bytes at the frame's mean price (default: mean literal price)
	Model  int  // literal and position contexts, zero selects the archived model
}

type pair struct{ l, d int32 } // d is the distance (>= 1)

// Encoder reuses its tables between frames; it is not safe for concurrent use.
type Encoder struct {
	P      Params
	config coderModel
	m      model
	rc     rcEnc
	f      finder
	cur    []pair
	nxt    []pair
	state  int
	reps   [numReps]uint32 // distance - 1
	// running price estimates for the price parser (1/16 bit units)
	spent, coded       uint64
	litSpent, litCount uint64
	// optimal parser
	lp     *lenPrices
	nodes  []optNode
	joined []byte
}

func NewEncoder(p Params) *Encoder {
	if p.Depth <= 0 {
		p.Depth = 48
	}
	if p.Nice <= 0 || p.Nice > maxMatch {
		p.Nice = min(max(p.Nice, 64), maxMatch)
	}
	return &Encoder{P: p, cur: make([]pair, 0, 64), nxt: make([]pair, 0, 64)}
}

// ---- match finder: hash chains over 4-byte hashes plus a 3-byte head table ----

type finder struct {
	src          []byte
	pos          int
	hb           uint
	head4, head3 []int32
	prev         []int32
	depth, nice  int
	window       int
}

const hash3Bits = 16

func (f *finder) reset(src []byte, depth, nice, window int) {
	f.src, f.pos, f.depth, f.nice = src, 0, depth, nice
	f.window = window
	if f.window <= 0 || f.window > len(src) {
		f.window = len(src)
	}
	hb := uint(bits.Len(uint(len(src))))
	hb = min(max(hb, 12), 20)
	if f.hb != hb || f.head4 == nil {
		f.hb = hb
		f.head4 = make([]int32, 1<<hb)
	} else {
		clear(f.head4)
	}
	if f.head3 == nil {
		f.head3 = make([]int32, 1<<hash3Bits)
	} else {
		clear(f.head3)
	}
	if cap(f.prev) < len(src) {
		f.prev = make([]int32, len(src))
	}
	f.prev = f.prev[:len(src)]
}

func matchLen(s []byte, a, b, lim int) int {
	n := 0
	for n+8 <= lim {
		x := binary.LittleEndian.Uint64(s[a+n:]) ^ binary.LittleEndian.Uint64(s[b+n:])
		if x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
		n += 8
	}
	for n < lim && s[a+n] == s[b+n] {
		n++
	}
	return n
}

func (f *finder) insert(i int) (c4, c3 int) {
	x := binary.LittleEndian.Uint32(f.src[i:])
	k4 := (x * 2654435761) >> (32 - f.hb)
	k3 := ((x & 0xFFFFFF) * 506832829) >> (32 - hash3Bits)
	c4, c3 = int(f.head4[k4])-1, int(f.head3[k3])-1
	f.prev[i] = f.head4[k4]
	f.head4[k4] = int32(i + 1)
	f.head3[k3] = int32(i + 1)
	return
}

// find returns the matches at f.pos in increasing length order and advances.
func (f *finder) find(out []pair) []pair {
	i := f.pos
	f.pos++
	out = out[:0]
	avail := len(f.src) - i
	if avail < 4 {
		return out
	}
	c, c3 := f.insert(i)
	lim := min(maxMatch, avail)
	best := 2
	if c3 >= 0 && i-c3 <= f.window {
		if l := matchLen(f.src, c3, i, lim); l > best {
			best = l
			out = append(out, pair{int32(l), int32(i - c3)})
			if l >= f.nice || l == lim {
				return out
			}
		}
	}
	s := f.src
	for d := f.depth; c >= 0 && d > 0 && i-c <= f.window; d-- {
		if s[c+best] == s[i+best] {
			if l := matchLen(s, c, i, lim); l > best {
				best = l
				out = append(out, pair{int32(l), int32(i - c)})
				if l >= f.nice || l == lim {
					break
				}
			}
		}
		c = int(f.prev[c]) - 1
	}
	return out
}

func (f *finder) skip(k int) {
	for ; k > 0; k-- {
		if len(f.src)-f.pos >= 4 {
			f.insert(f.pos)
		}
		f.pos++
	}
}

// ---- emission ----

func (z *Encoder) emitLit(src []byte, i int) {
	m, ps := &z.m, i&z.config.positionMask
	z.rc.bit(&m.isMatch[z.state][ps], 0)
	var prev byte
	if i > 0 {
		prev = src[i-1]
	}
	p := &m.lit[prev>>z.config.literalShift]
	if z.state >= 7 {
		encLitMatched(&z.rc, p, src[i], src[i-int(z.reps[0])-1])
	} else {
		encLit(&z.rc, p, src[i])
	}
	z.state = stateLit(z.state)
}

func (z *Encoder) emitMatch(i, l int, dist int) {
	m, ps := &z.m, i&z.config.positionMask
	z.rc.bit(&m.isMatch[z.state][ps], 1)
	z.rc.bit(&m.isRep[z.state], 0)
	encLen(&z.rc, &m.matchLen, uint32(l-minMatch), ps)
	dd := uint32(dist - 1)
	encDist(&z.rc, m, dd, lenState(l))
	z.reps[3], z.reps[2], z.reps[1], z.reps[0] = z.reps[2], z.reps[1], z.reps[0], dd
	z.state = stateMatch(z.state)
}

func (z *Encoder) emitRep(i, r, l int) {
	m, ps, s := &z.m, i&z.config.positionMask, z.state
	z.rc.bit(&m.isMatch[s][ps], 1)
	z.rc.bit(&m.isRep[s], 1)
	if r == 0 {
		z.rc.bit(&m.isRepG0[s], 0)
		z.rc.bit(&m.isRep0Long[s][ps], 1)
	} else {
		z.rc.bit(&m.isRepG0[s], 1)
		d := z.reps[r]
		if r == 1 {
			z.rc.bit(&m.isRepG1[s], 0)
		} else {
			z.rc.bit(&m.isRepG1[s], 1)
			z.rc.bit(&m.isRepG2[s], uint32(r-2))
			if r == 3 {
				z.reps[3] = z.reps[2]
			}
			z.reps[2] = z.reps[1]
		}
		z.reps[1] = z.reps[0]
		z.reps[0] = d
	}
	encLen(&z.rc, &m.repLen, uint32(l-minMatch), ps)
	z.state = stateRep(s)
}

func (z *Encoder) emitShortRep(i int) {
	m, ps, s := &z.m, i&z.config.positionMask, z.state
	z.rc.bit(&m.isMatch[s][ps], 1)
	z.rc.bit(&m.isRep[s], 1)
	z.rc.bit(&m.isRepG0[s], 0)
	z.rc.bit(&m.isRep0Long[s][ps], 0)
	z.state = stateShortRep(s)
}

// ---- prices ----

func (z *Encoder) priceLitAt(src []byte, i, state int, rep0 uint32) uint32 {
	m := &z.m
	var prev byte
	if i > 0 {
		prev = src[i-1]
	}
	p := &m.lit[prev>>z.config.literalShift]
	c := priceBit(m.isMatch[state][i&z.config.positionMask], 0)
	if state >= 7 {
		return c + priceLitMatched(p, src[i], src[i-int(rep0)-1])
	}
	return c + priceLit(p, src[i])
}

func (z *Encoder) priceMatch(i, state, l, dist int) uint32 {
	m, ps := &z.m, i&z.config.positionMask
	return priceBit(m.isMatch[state][ps], 1) + priceBit(m.isRep[state], 0) +
		priceLen(&m.matchLen, uint32(l-minMatch), ps) + priceDist(m, uint32(dist-1), lenState(l))
}

func (z *Encoder) priceRep(i, state, r, l int) uint32 {
	m, ps := &z.m, i&z.config.positionMask
	c := priceBit(m.isMatch[state][ps], 1) + priceBit(m.isRep[state], 1)
	if r == 0 {
		c += priceBit(m.isRepG0[state], 0) + priceBit(m.isRep0Long[state][ps], 1)
	} else {
		c += priceBit(m.isRepG0[state], 1)
		if r == 1 {
			c += priceBit(m.isRepG1[state], 0)
		} else {
			c += priceBit(m.isRepG1[state], 1) + priceBit(m.isRepG2[state], uint32(r-2))
		}
	}
	return c + priceLen(&m.repLen, uint32(l-minMatch), ps)
}

func (z *Encoder) priceShortRep(i, state int) uint32 {
	m, ps := &z.m, i&z.config.positionMask
	return priceBit(m.isMatch[state][ps], 1) + priceBit(m.isRep[state], 1) +
		priceBit(m.isRepG0[state], 0) + priceBit(m.isRep0Long[state][ps], 0)
}

func changePair(small, big int) bool { return big>>7 > small }

// option is one coding choice at a position.
type option struct {
	kind  int // 0 literal, 1 match, 2 rep, 3 short rep
	l, d  int // length; distance (match) or rep index (rep)
	price uint32
}

// best scores every option at i under the live model: value = covered*avg - price.
func (z *Encoder) best(src []byte, i, state int, reps *[numReps]uint32, pairs []pair, avg int64) (option, int64) {
	n := len(src)
	lim := min(maxMatch, n-i)
	o := option{kind: 0, l: 1, price: z.priceLitAt(src, i, state, reps[0])}
	v := avg - int64(o.price)
	if i > int(reps[0]) && src[i] == src[i-int(reps[0])-1] {
		if p := z.priceShortRep(i, state); avg-int64(p) > v {
			o, v = option{kind: 3, l: 1, price: p}, avg-int64(p)
		}
	}
	if lim >= minMatch {
		for r := range numReps {
			d := int(reps[r]) + 1
			if d > i {
				continue
			}
			if l := matchLen(src, i-d, i, lim); l >= minMatch {
				p := z.priceRep(i, state, r, l)
				if vv := int64(l)*avg - int64(p); vv > v {
					o, v = option{kind: 2, l: l, d: r, price: p}, vv
				}
			}
		}
	}
	for _, pr := range pairs {
		l, d := int(pr.l), int(pr.d)
		p := z.priceMatch(i, state, l, d)
		if vv := int64(l)*avg - int64(p); vv > v {
			o, v = option{kind: 1, l: l, d: d, price: p}, vv
		}
	}
	return o, v
}

func (z *Encoder) emit(src []byte, i int, o option) {
	switch o.kind {
	case 0:
		z.emitLit(src, i)
	case 1:
		z.emitMatch(i, o.l, o.d)
	case 2:
		z.emitRep(i, o.d, o.l)
	case 3:
		z.emitShortRep(i)
	}
}

// Encode compresses one independent frame. The decoder needs len(src).
func (z *Encoder) Encode(dst, src []byte) []byte {
	return z.encodeStream(dst, src, 0)
}

func (z *Encoder) encodeStream(dst, src []byte, start int) []byte {
	z.m.reset()
	z.config = coderModels[min(max(0, z.P.Model), len(coderModels)-1)]
	z.rc.reset(dst)
	z.f.reset(src, z.P.Depth, z.P.Nice, z.P.Window)
	z.f.skip(start)
	z.state, z.reps = 0, [numReps]uint32{}
	z.spent, z.coded, z.litSpent, z.litCount = 0, 0, 0, 0
	if z.lp != nil {
		z.lp.left = 0
	}
	switch z.P.Mode {
	case ModePrice:
		z.encodePrice(src, start)
	case ModeOpt:
		z.encodeOpt(src, start)
	default:
		z.encodeFast(src, start)
	}
	return z.rc.finish()
}

func (z *Encoder) repLens(src []byte, i int) (int, int) {
	lim := min(maxMatch, len(src)-i)
	bestL, bestR := 0, 0
	if lim < minMatch {
		return 0, 0
	}
	for r := range numReps {
		d := int(z.reps[r]) + 1
		if d > i {
			continue
		}
		if l := matchLen(src, i-d, i, lim); l > bestL {
			bestL, bestR = l, r
			if l == lim {
				break
			}
		}
	}
	return bestL, bestR
}

// encodeFast follows LZMA's fast-mode decision rules (GetOptimumFast).
func (z *Encoder) encodeFast(src []byte, start int) {
	n, nice := len(src), z.P.Nice
	f := &z.f
	have := false
	for i := start; i < n; {
		if !have {
			z.cur = f.find(z.cur)
		}
		have = false
		repLen, repIdx := z.repLens(src, i)
		if repLen >= nice {
			z.emitRep(i, repIdx, repLen)
			f.skip(repLen - 1)
			i += repLen
			continue
		}
		mainLen, mainDist := 0, 0
		if k := len(z.cur); k > 0 {
			mainLen, mainDist = int(z.cur[k-1].l), int(z.cur[k-1].d)
		}
		if mainLen >= nice {
			z.emitMatch(i, mainLen, mainDist)
			f.skip(mainLen - 1)
			i += mainLen
			continue
		}
		if mainLen >= 2 {
			for k := len(z.cur) - 1; k > 0 && mainLen == int(z.cur[k-1].l)+1; k-- {
				if !changePair(int(z.cur[k-1].d), mainDist) {
					break
				}
				mainLen, mainDist = int(z.cur[k-1].l), int(z.cur[k-1].d)
			}
		}
		if repLen >= 2 && (repLen+1 >= mainLen || (repLen+2 >= mainLen && mainDist >= 1<<9) || (repLen+3 >= mainLen && mainDist >= 1<<15)) {
			z.emitRep(i, repIdx, repLen)
			f.skip(repLen - 1)
			i += repLen
			continue
		}
		if mainLen < 2 || n-i <= 2 {
			z.literalOrShortRep(src, i)
			i++
			continue
		}
		z.nxt = f.find(z.nxt) // position i+1
		if k := len(z.nxt); k > 0 {
			newLen, newDist := int(z.nxt[k-1].l), int(z.nxt[k-1].d)
			if (newLen >= mainLen && newDist < mainDist) || (newLen == mainLen+1 && !changePair(mainDist, newDist)) ||
				newLen > mainLen+1 || (newLen+1 >= mainLen && mainLen >= 3 && changePair(newDist, mainDist)) {
				z.literalOrShortRep(src, i)
				z.cur, z.nxt = z.nxt, z.cur
				have = true
				i++
				continue
			}
		}
		limit := max(2, mainLen-1)
		lazy := false
		for r := range numReps {
			d := int(z.reps[r]) + 1
			if d <= i+1 && matchLen(src, i+1-d, i+1, min(maxMatch, n-i-1)) >= limit {
				lazy = true
				break
			}
		}
		if lazy {
			z.literalOrShortRep(src, i)
			z.cur, z.nxt = z.nxt, z.cur
			have = true
			i++
			continue
		}
		z.emitMatch(i, mainLen, mainDist)
		f.skip(mainLen - 2)
		i += mainLen
	}
}

func (z *Encoder) literalOrShortRep(src []byte, i int) {
	if i > int(z.reps[0]) && src[i] == src[i-int(z.reps[0])-1] &&
		z.priceShortRep(i, z.state) < z.priceLitAt(src, i, z.state, z.reps[0]) {
		z.emitShortRep(i)
		return
	}
	z.emitLit(src, i)
}

// encodePrice: at each position score every option with the live model and
// compare "take the best option here" against "literal here, best option at
// i+1", both valued as covered*avg - price, where avg is the running price of a
// literal in this frame (prior 6 bits over 256 literals): the alternative to
// covering a byte with a match is coding it as a literal.
func (z *Encoder) encodePrice(src []byte, start int) {
	n, nice := len(src), z.P.Nice
	f := &z.f
	have := false
	for i := start; i < n; {
		if !have {
			z.cur = f.find(z.cur)
		}
		have = false
		avg := int64((z.litSpent + 256*96) / (z.litCount + 256))
		if z.P.AvgAll {
			avg = int64((z.spent + 4096*64) / (z.coded + 4096))
		}
		o, v := z.best(src, i, z.state, &z.reps, z.cur, avg)
		if o.l >= nice || o.l <= 1 || n-i <= 2 {
			z.take(src, i, o)
			f.skip(o.l - 1)
			i += o.l
			continue
		}
		z.nxt = f.find(z.nxt)
		lit := option{kind: 0, l: 1, price: z.priceLitAt(src, i, z.state, z.reps[0])}
		vLit := avg - int64(lit.price)
		_, v1 := z.best(src, i+1, stateLit(z.state), &z.reps, z.nxt, avg)
		if vLit+v1 > v {
			z.take(src, i, lit)
			z.cur, z.nxt = z.nxt, z.cur
			have = true
			i++
			continue
		}
		z.take(src, i, o)
		f.skip(o.l - 2)
		i += o.l
	}
}

func (z *Encoder) take(src []byte, i int, o option) {
	z.spent += uint64(o.price)
	z.coded += uint64(o.l)
	if o.kind == 0 {
		z.litSpent += uint64(o.price)
		z.litCount++
	}
	z.emit(src, i, o)
}
