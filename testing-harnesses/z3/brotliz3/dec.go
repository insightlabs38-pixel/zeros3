package brotliz3

// Bounded RFC 7932 decoder. It exists for the analysis direction (compressed page -> body) and
// for self-checks; replay only needs the encoder. Every length is validated against the caller's
// limit before memory is committed, and any malformed stream returns ErrCorrupt.

type bitReader struct {
	b    []byte
	pos  int
	acc  uint64
	nacc uint
	bad  bool
}

func (r *bitReader) fill() {
	for r.nacc <= 56 && r.pos < len(r.b) {
		r.acc |= uint64(r.b[r.pos]) << r.nacc
		r.pos++
		r.nacc += 8
	}
}

func (r *bitReader) bits(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if r.nacc < n {
		r.fill()
		if r.nacc < n {
			r.bad = true
			return 0
		}
	}
	v := uint32(r.acc & (1<<n - 1))
	r.acc >>= n
	r.nacc -= n
	return v
}

// peek returns up to n bits without consuming (missing bits read as zero).
func (r *bitReader) peek(n uint) uint32 {
	if r.nacc < n {
		r.fill()
	}
	return uint32(r.acc & (1<<n - 1))
}

func (r *bitReader) drop(n uint) {
	if r.nacc < n {
		r.bad = true
		return
	}
	r.acc >>= n
	r.nacc -= n
}

// alignByte discards the padding bits, which must be zero.
func (r *bitReader) alignByte() {
	if k := r.nacc & 7; k != 0 && r.bits(k) != 0 {
		r.bad = true
	}
}

// huff is one prefix code: an 8-bit root table plus canonical decoding for longer codes.
type huff struct {
	root   [256]uint16 // len<<10 | symbol; 0 = longer than 8 bits
	single int         // >= 0: zero-length code for this symbol
	count  [16]uint16
	sorted []uint16
}

func revBits(v uint32, n uint) uint32 {
	var r uint32
	for i := uint(0); i < n; i++ {
		r = r<<1 | v&1
		v >>= 1
	}
	return r
}

// build makes the decoder for code lengths (0 = unused). Returns false for an invalid code.
func (h *huff) build(lens []uint8) bool {
	h.single = -1
	h.count = [16]uint16{}
	nz, last := 0, 0
	for s, l := range lens {
		if l != 0 {
			h.count[l]++
			nz++
			last = s
		}
	}
	if nz == 0 {
		return false
	}
	if nz == 1 {
		h.single = last
		return true
	}
	space := 0
	for l := 1; l <= 15; l++ {
		space += int(h.count[l]) << uint(15-l)
	}
	if space != 1<<15 {
		return false
	}
	var offs [17]uint16
	for l := 1; l <= 15; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	h.sorted = make([]uint16, nz)
	o := offs
	for s, l := range lens {
		if l != 0 {
			h.sorted[o[l]] = uint16(s)
			o[l]++
		}
	}
	h.root = [256]uint16{}
	code := uint32(0)
	idx := 0
	for l := uint(1); l <= 8; l++ {
		for k := 0; k < int(h.count[l]); k++ {
			sym := h.sorted[idx]
			idx++
			r := revBits(code, l)
			for x := r; x < 256; x += 1 << l {
				h.root[x] = uint16(l)<<10 | sym
			}
			code++
		}
		code <<= 1
	}
	return true
}

func (h *huff) decode(r *bitReader) int {
	if h.single >= 0 {
		return h.single
	}
	if e := h.root[r.peek(8)]; e != 0 {
		r.drop(uint(e >> 10))
		return int(e & 1023)
	}
	// canonical walk for codes longer than 8 bits
	code, first, idx := 0, 0, 0
	for l := 1; l <= 15; l++ {
		code |= int(r.bits(1))
		c := int(h.count[l])
		if code-first < c {
			return int(h.sorted[idx+code-first])
		}
		idx += c
		first = (first + c) << 1
		code <<= 1
	}
	r.bad = true
	return 0
}

func alphabetBits(n int) uint {
	b := uint(0)
	for n--; n > 0; n >>= 1 {
		b++
	}
	return b
}

// readHuff reads a prefix code over an alphabet of n symbols.
func readHuff(r *bitReader, n int, h *huff) bool {
	hskip := r.bits(2)
	lens := make([]uint8, n)
	if hskip == 1 { // simple code
		nsym := int(r.bits(2)) + 1
		ab := alphabetBits(n)
		var sym [4]int
		for i := 0; i < nsym; i++ {
			sym[i] = int(r.bits(ab))
			if sym[i] >= n {
				return false
			}
			for j := 0; j < i; j++ {
				if sym[j] == sym[i] {
					return false
				}
			}
		}
		switch nsym {
		case 1:
			h.single = sym[0]
			return !r.bad
		case 2:
			lens[sym[0]], lens[sym[1]] = 1, 1
		case 3:
			lens[sym[0]], lens[sym[1]], lens[sym[2]] = 1, 2, 2
		default:
			if r.bits(1) == 0 {
				lens[sym[0]], lens[sym[1]], lens[sym[2]], lens[sym[3]] = 2, 2, 2, 2
			} else {
				lens[sym[0]], lens[sym[1]], lens[sym[2]], lens[sym[3]] = 1, 2, 3, 3
			}
		}
		return !r.bad && h.build(lens)
	}
	order := [18]uint8{1, 2, 3, 4, 0, 5, 17, 6, 16, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	var cl [18]uint8
	space, num := 32, 0
	for i := int(hskip); i < 18 && space > 0; i++ {
		x := r.peek(4)
		var v uint8
		var nb uint
		switch {
		case x&3 == 0:
			v, nb = 0, 2
		case x&3 == 2:
			v, nb = 3, 2
		case x&3 == 1:
			v, nb = 4, 2
		case x&7 == 3:
			v, nb = 2, 3
		case x == 7:
			v, nb = 1, 4
		default:
			v, nb = 5, 4
		}
		r.drop(nb)
		cl[order[i]] = v
		if v != 0 {
			space -= 32 >> v
			num++
		}
	}
	if r.bad || (num != 1 && space != 0) {
		return false
	}
	var clh huff
	if !clh.build(cl[:]) {
		return false
	}
	sym, prev, repeat, repeatLen := 0, uint8(8), 0, uint8(0)
	sp := 32768
	for sym < n && sp > 0 {
		c := clh.decode(r)
		if r.bad {
			return false
		}
		if c < 16 {
			lens[sym] = uint8(c)
			sym++
			repeat = 0
			if c != 0 {
				prev = uint8(c)
				sp -= 32768 >> uint(c)
			}
			continue
		}
		extra := uint(c - 14) // 16: 2 bits, 17: 3 bits
		nl := uint8(0)
		if c == 16 {
			nl = prev
		}
		if repeatLen != nl {
			repeat, repeatLen = 0, nl
		}
		old := repeat
		if repeat > 0 {
			repeat = (repeat - 2) << extra
		}
		repeat += int(r.bits(extra)) + 3
		d := repeat - old
		if sym+d > n {
			return false
		}
		for k := 0; k < d; k++ {
			lens[sym] = repeatLen
			sym++
		}
		if repeatLen != 0 {
			sp -= d << uint(15-repeatLen)
		}
	}
	if r.bad || sp != 0 {
		return false
	}
	return h.build(lens)
}

func readVarLen8(r *bitReader) int {
	if r.bits(1) == 0 {
		return 0
	}
	n := uint(r.bits(3))
	return int(r.bits(n)) + 1<<n
}

type blockState struct {
	n         int
	typeCode  huff
	lenCode   huff
	typ, prev int
	count     int
}

func (b *blockState) init(r *bitReader) bool {
	b.n = readVarLen8(r) + 1
	b.typ, b.prev, b.count = 0, 1, 1<<28
	if b.n < 2 {
		return !r.bad
	}
	if !readHuff(r, b.n+2, &b.typeCode) || !readHuff(r, 26, &b.lenCode) {
		return false
	}
	b.count = b.readCount(r)
	return !r.bad
}

func (b *blockState) readCount(r *bitReader) int {
	c := b.lenCode.decode(r)
	return int(blockLenRanges[c][0]) + int(r.bits(uint(blockLenRanges[c][1])))
}

func (b *blockState) next(r *bitReader) {
	c := b.typeCode.decode(r)
	var t int
	switch c {
	case 0:
		t = b.prev
	case 1:
		t = b.typ + 1
		if t >= b.n {
			t -= b.n
		}
	default:
		t = c - 2
	}
	if t >= b.n {
		r.bad = true
		t = 0
	}
	b.prev, b.typ = b.typ, t
	b.count = b.readCount(r)
}

func readContextMap(r *bitReader, size, ntrees int) []uint8 {
	cm := make([]uint8, size)
	if ntrees < 2 {
		return cm
	}
	rlemax := 0
	if r.bits(1) == 1 {
		rlemax = int(r.bits(4)) + 1
	}
	var h huff
	if !readHuff(r, ntrees+rlemax, &h) {
		r.bad = true
		return cm
	}
	for i := 0; i < size && !r.bad; {
		c := h.decode(r)
		switch {
		case c == 0:
			i++
		case c <= rlemax:
			reps := 1<<uint(c) + int(r.bits(uint(c)))
			if i+reps > size {
				r.bad = true
				return cm
			}
			i += reps
		default:
			cm[i] = uint8(c - rlemax)
			i++
		}
	}
	if r.bits(1) == 1 { // inverse move-to-front
		var mtf [256]uint8
		for i := range mtf {
			mtf[i] = uint8(i)
		}
		for i, v := range cm {
			x := mtf[v]
			cm[i] = x
			copy(mtf[1:int(v)+1], mtf[:v])
			mtf[0] = x
		}
	}
	return cm
}

var (
	cellInsBase  = [11]uint8{0, 0, 0, 0, 8, 8, 0, 16, 8, 16, 16}
	cellCopyBase = [11]uint8{0, 8, 0, 8, 0, 8, 16, 0, 16, 8, 16}
)

// transformWord appends dictionary word w under transform t (RFC 7932 appendix B).
func transformWord(dst []byte, w []byte, t int) []byte {
	pre, typ, suf := transformsData[3*t], transformsData[3*t+1], transformsData[3*t+2]
	ps := func(id uint8) string {
		o := prefixSuffixMap[id]
		return prefixSuffix[int(o)+1 : int(o)+1+int(prefixSuffix[o])]
	}
	dst = append(dst, ps(pre)...)
	start := len(dst)
	switch {
	case typ >= 1 && typ <= 9: // omit last
		if n := int(typ); n < len(w) {
			w = w[:len(w)-n]
		} else {
			w = nil
		}
	case typ >= 12 && typ <= 20: // omit first
		if n := int(typ) - 11; n < len(w) {
			w = w[n:]
		} else {
			w = nil
		}
	}
	dst = append(dst, w...)
	if typ == 10 || typ == 11 {
		p := dst[start:]
		for len(p) > 0 {
			step := 1
			switch {
			case p[0] < 0xC0:
				if p[0] >= 'a' && p[0] <= 'z' {
					p[0] ^= 32
				}
			case p[0] < 0xE0:
				if len(p) > 1 {
					p[1] ^= 32
				}
				step = 2
			default:
				if len(p) > 2 {
					p[2] ^= 5
				}
				step = 3
			}
			if typ == 10 {
				break
			}
			if step > len(p) {
				step = len(p)
			}
			p = p[step:]
		}
	}
	return append(dst, ps(suf)...)
}

// Decode decompresses a complete Brotli stream producing at most maxOut bytes.
func Decode(src []byte, maxOut int) ([]byte, error) {
	d := theDict.Load()
	if d == nil {
		return nil, ErrDictionary
	}
	r := &bitReader{b: src}
	var wbits uint
	if r.bits(1) == 0 {
		wbits = 16
	} else if n := r.bits(3); n != 0 {
		wbits = 17 + uint(n)
	} else if m := r.bits(3); m == 0 {
		wbits = 17
	} else if m == 1 {
		return nil, ErrCorrupt // large-window streams are not accepted
	} else {
		wbits = 8 + uint(m)
	}
	maxDist := 1<<wbits - 16
	var out []byte
	ring := [4]int{16, 15, 11, 4}
	ri := 0 // ring[(ri-1)&3] is the last distance
	for !r.bad {
		last := r.bits(1) == 1
		if last && r.bits(1) == 1 {
			break
		}
		nib := r.bits(2)
		if nib == 3 { // metadata block
			if r.bits(1) != 0 {
				return nil, ErrCorrupt
			}
			sb := uint(r.bits(2))
			n := 0
			if sb > 0 {
				n = int(r.bits(8 * sb))
				if sb > 1 && n>>(8*(sb-1)) == 0 {
					return nil, ErrCorrupt
				}
				n++
			}
			r.alignByte()
			for ; n > 0 && !r.bad; n-- {
				r.bits(8)
			}
			if last {
				break
			}
			continue
		}
		nn := 4 + uint(nib)
		mlen := int(r.bits(4 * nn))
		if nn > 4 && mlen>>(4*(nn-1)) == 0 {
			return nil, ErrCorrupt
		}
		mlen++
		if r.bad {
			break
		}
		if mlen > maxOut-len(out) {
			return nil, ErrLimit
		}
		if !last && r.bits(1) == 1 { // uncompressed
			r.alignByte()
			for i := 0; i < mlen && !r.bad; i++ {
				out = append(out, byte(r.bits(8)))
			}
			continue
		}
		var bl, bi, bd blockState
		if !bl.init(r) || !bi.init(r) || !bd.init(r) {
			return nil, ErrCorrupt
		}
		npostfix := uint(r.bits(2))
		ndirect := int(r.bits(4)) << npostfix
		modes := make([]uint8, bl.n)
		for i := range modes {
			modes[i] = uint8(r.bits(2))
		}
		ntl := readVarLen8(r) + 1
		cml := readContextMap(r, 64*bl.n, ntl)
		ntd := readVarLen8(r) + 1
		cmd := readContextMap(r, 4*bd.n, ntd)
		if r.bad {
			return nil, ErrCorrupt
		}
		for _, v := range cml {
			if int(v) >= ntl {
				return nil, ErrCorrupt
			}
		}
		for _, v := range cmd {
			if int(v) >= ntd {
				return nil, ErrCorrupt
			}
		}
		lt := make([]huff, ntl)
		it := make([]huff, bi.n)
		dt := make([]huff, ntd)
		for i := range lt {
			if !readHuff(r, 256, &lt[i]) {
				return nil, ErrCorrupt
			}
		}
		for i := range it {
			if !readHuff(r, 704, &it[i]) {
				return nil, ErrCorrupt
			}
		}
		for i := range dt {
			if !readHuff(r, 16+ndirect+48<<npostfix, &dt[i]) {
				return nil, ErrCorrupt
			}
		}
		end := len(out) + mlen
		for len(out) < end && !r.bad {
			if bi.count == 0 {
				bi.next(r)
			}
			bi.count--
			c := it[bi.typ].decode(r)
			cell := c >> 6
			ic, cc := int(cellInsBase[cell])+(c>>3)&7, int(cellCopyBase[cell])+c&7
			ins := int(insBase[ic]) + int(r.bits(insExtra[ic]))
			cp := int(copyBase[cc]) + int(r.bits(copyExtra[cc]))
			if ins > end-len(out) {
				return nil, ErrCorrupt
			}
			for ; ins > 0 && !r.bad; ins-- {
				if bl.count == 0 {
					bl.next(r)
				}
				bl.count--
				var p1, p2 byte
				if n := len(out); n > 0 {
					p1 = out[n-1]
					if n > 1 {
						p2 = out[n-2]
					}
				}
				m := int(modes[bl.typ]) << 9
				ctx := contextLUT[m+int(p1)] | contextLUT[m+256+int(p2)]
				out = append(out, byte(lt[cml[64*bl.typ+int(ctx)]].decode(r)))
			}
			if len(out) >= end {
				break
			}
			dist := 0
			if c < 128 {
				dist = ring[(ri-1)&3]
			} else {
				if bd.count == 0 {
					bd.next(r)
				}
				bd.count--
				dctx := 3
				if cp <= 4 {
					dctx = cp - 2
				}
				s := dt[cmd[4*bd.typ+dctx]].decode(r)
				switch {
				case s < 16:
					base := ring[(ri-1)&3]
					switch {
					case s < 4:
						base = ring[(ri-1-s)&3]
					case s >= 10:
						base = ring[(ri-2)&3]
					}
					off := [16]int{0, 0, 0, 0, -1, 1, -2, 2, -3, 3, -1, 1, -2, 2, -3, 3}[s]
					dist = base + off
					if dist <= 0 {
						return nil, ErrCorrupt
					}
				case s < 16+ndirect:
					dist = s - 15
				default:
					v := s - ndirect - 16
					nb := 1 + uint(v>>(npostfix+1))
					h, l := v>>npostfix, v&(1<<npostfix-1)
					off := (2+(h&1))<<nb - 4
					dist = (off+int(r.bits(nb)))<<npostfix + l + ndirect + 1
				}
				md := min(maxDist, len(out))
				if s != 0 && dist <= md {
					ring[ri&3] = dist
					ri++
				}
			}
			md := min(maxDist, len(out))
			if dist <= md {
				if cp > end-len(out) {
					return nil, ErrCorrupt
				}
				for i := 0; i < cp; i++ {
					out = append(out, out[len(out)-dist])
				}
				continue
			}
			// static dictionary reference
			if cp < 4 || cp > 24 || d.sizeBits[cp] == 0 {
				return nil, ErrCorrupt
			}
			id := dist - md - 1
			idx, tr := id&(1<<d.sizeBits[cp]-1), id>>d.sizeBits[cp]
			if tr >= 121 {
				return nil, ErrCorrupt
			}
			off := int(d.offsets[cp]) + idx*cp
			out = transformWord(out, d.data[off:off+cp], tr)
			if len(out) > end {
				return nil, ErrCorrupt
			}
		}
		if last {
			break
		}
	}
	if r.bad {
		return nil, ErrCorrupt
	}
	return out, nil
}
