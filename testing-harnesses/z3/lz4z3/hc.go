package lz4z3

import (
	"encoding/binary"
	"math/bits"
	"sync"
)

// LZ4HC (hash-chain, levels 3..8) reference encoder re-implementation. Level 9 enables the
// repeated-pattern shortcut of the reference; it is not modelled (see HCSeqs). Levels >= 10 use
// the optimal parser and are out of scope. Verified against producer output, never trusted.
const (
	hcHashLog    = 15
	hcStart      = 1 << 16 // the reference starts indices at 64 KiB so that 0 means "empty"
	hcOptimalML  = 18
	hcMaxAttempt = 128 // levels with more attempts enable pattern analysis (unsupported here)
)

// HCAttempts maps a compression level to the reference's search depth (levels 3..9).
func HCAttempts(level int) int {
	switch {
	case level >= 3 && level <= 9:
		return 1 << (level - 1)
	case level == 10:
		return 96
	case level == 11:
		return 512
	case level == 12:
		return 16384
	}
	return 0
}

type hcState struct {
	src      []byte
	hash     [1 << hcHashLog]uint32
	hstamp   [1 << hcHashLog]uint32 // generation of each hash slot: stale slots read as empty
	chain    [1 << 16]uint16        // always written before it is read within one run
	gen      uint32
	next     uint32
	attempts int
	pa       bool // pattern analysis (levels with more than 128 attempts)
	swap     bool // chain swap (optimal parser, levels 10..12)
}

func (h *hcState) hashAt(p int) uint32 {
	return (binary.LittleEndian.Uint32(h.src[p:]) * 2654435761) >> (32 - hcHashLog)
}

func (h *hcState) insert(ip int) {
	target := uint32(ip) + hcStart
	for idx := h.next; idx < target; idx++ {
		hh := h.hashAt(int(idx - hcStart))
		delta := idx - h.head(hh)
		if delta > maxOff {
			delta = maxOff
		}
		h.chain[uint16(idx)] = uint16(delta)
		h.hash[hh], h.hstamp[hh] = idx, h.gen
	}
	h.next = target
}

func (h *hcState) head(hh uint32) uint32 {
	if h.hstamp[hh] != h.gen {
		return 0
	}
	return h.hash[hh]
}

// wider mirrors LZ4HC_InsertAndGetWiderMatch (no dictionary, no chain swap, no pattern analysis).
func (h *hcState) wider(ip, lowLimit, highLimit, longest int) (best, matchPos, startPos int) {
	src := h.src
	ipIndex := uint32(ip) + hcStart
	lowest := uint32(hcStart)
	if hcStart+maxOff+1 <= ipIndex {
		lowest = ipIndex - maxOff
	}
	lookBack := ip - lowLimit
	attempts := h.attempts
	pattern := binary.LittleEndian.Uint32(src[ip:])
	repeat, srcPatLen := repUntested, 0
	h.insert(ip)
	mi := h.head(h.hashAt(ip))
	matchPos, startPos = -1, -1
	chainPos := uint32(0)
	for mi >= lowest && attempts > 0 {
		attempts--
		mp := int(mi - hcStart)
		ml := 0
		if binary.LittleEndian.Uint16(src[lowLimit+longest-1:]) == binary.LittleEndian.Uint16(src[mp-lookBack+longest-1:]) &&
			binary.LittleEndian.Uint32(src[mp:]) == pattern {
			back := 0
			if lookBack != 0 {
				lim := max(lowLimit-ip, 0-mp)
				for back > lim && src[ip+back-1] == src[mp+back-1] {
					back--
				}
			}
			ml = minMatch + count(src, ip+minMatch, mp+minMatch, highLimit) - back
			if ml > longest {
				longest, matchPos, startPos = ml, mp+back, ip+back
			}
		}
		if h.swap && ml == longest && mi+uint32(longest) <= ipIndex { // better match: select a better chain
			distNext := uint32(1)
			end := longest - minMatch + 1
			step, accel := 1, 1<<4
			for pos := 0; pos < end; pos += step {
				cd := uint32(h.chain[uint16(mi+uint32(pos))])
				step = accel >> 4
				accel++
				if cd > distNext {
					distNext, chainPos, accel = cd, uint32(pos), 1<<4
				}
			}
			if distNext > 1 {
				if distNext > mi {
					break
				}
				mi -= distNext
				continue
			}
		}
		// repeated-pattern shortcut of the reference
		if h.pa && chainPos == 0 && h.chain[uint16(mi)] == 1 {
			cand := mi - 1
			if repeat == repUntested {
				if pattern&0xFFFF == pattern>>16 && pattern&0xFF == pattern>>24 {
					repeat = repConfirmed
					srcPatLen = countPattern(src, ip+4, highLimit, pattern) + 4
				} else {
					repeat = repNot
				}
			}
			if repeat == repConfirmed && cand >= lowest {
				cp := int(cand - hcStart)
				if binary.LittleEndian.Uint32(src[cp:]) == pattern {
					fwd := countPattern(src, cp+4, highLimit, pattern) + 4
					// counting stops at the lowest admissible index: the reference clamps to it anyway
					back := reverseCountPattern(src, cp, int(lowest-hcStart), pattern)
					cur := back + fwd
					if cur >= srcPatLen && fwd <= srcPatLen {
						mi = cand + uint32(fwd) - uint32(srcPatLen)
					} else {
						mi = cand - uint32(back)
						if lookBack == 0 {
							maxML := min(cur, srcPatLen)
							if longest < maxML {
								if ip-int(mi-hcStart) > maxOff {
									break
								}
								longest, matchPos, startPos = maxML, int(mi-hcStart), ip
							}
							d := uint32(h.chain[uint16(mi)])
							if d > mi {
								break
							}
							mi -= d
						}
					}
					continue
				}
			}
		}
		mi -= uint32(h.chain[uint16(mi+chainPos)])
	}
	return longest, matchPos, startPos
}

// HCSeqs returns the sequences of the hash-chain encoder at the given level (3..9).
func HCSeqs(src []byte, level int) ([]Seq, bool) {
	s, _, ok := hcSeqs(src, level, nil)
	return s, ok
}

// hcSeqs optionally stops at the first sequence that disagrees with target.
func hcSeqs(src []byte, level int, target []Seq) (seqs []Seq, aborted, ok bool) {
	att := HCAttempts(level)
	if att == 0 {
		return nil, false, false
	}
	n := len(src)
	anchor := 0
	if n >= mfLimit+1 {
		h := hcPool.Get().(*hcState)
		defer hcPool.Put(h)
		h.gen++
		if h.gen == 0 {
			*h = hcState{gen: 1}
		}
		if level >= 10 {
			return hcOptSeqs(h, src, level, att, target)
		}
		h.src, h.next, h.attempts, h.pa, h.swap = src, hcStart, att, att > hcMaxAttempt, false
		mflimit := n - mfLimit
		matchlimit := n - lastLiterals
		ip := 0
		var ml, ml0, ml2, ml3, start0, ref0, ref, start2, ref2, start3, ref3 int
		emit := func(mlen, r int) bool {
			seqs = append(seqs, Seq{Lit: ip - anchor, Match: mlen, Off: ip - r})
			ip += mlen
			anchor = ip
			return target != nil && (len(seqs) > len(target) || seqs[len(seqs)-1] != target[len(seqs)-1])
		}
		for ip <= mflimit {
			ml, ref, _ = h.wider(ip, ip, matchlimit, minMatch-1)
			if ml < minMatch {
				ip++
				continue
			}
			start0, ref0, ml0 = ip, ref, ml
		search2:
			if ip+ml <= mflimit {
				ml2, ref2, start2 = h.wider(ip+ml-2, ip, matchlimit, ml)
			} else {
				ml2 = ml
			}
			if ml2 == ml { // no better match: encode ML1
				if emit(ml, ref) {
					return seqs[:len(seqs)-1], true, true
				}
				continue
			}
			if start0 < ip && start2 < ip+ml0 { // ML1 was skipped and ML2 squeezes it: restore
				ip, ref, ml = start0, ref0, ml0
			}
			if start2-ip < 3 { // first match too small: removed
				ml, ip, ref = ml2, start2, ref2
				goto search2
			}
		search3:
			if start2-ip < hcOptimalML {
				newML := ml
				if newML > hcOptimalML {
					newML = hcOptimalML
				}
				if ip+newML > start2+ml2-minMatch {
					newML = start2 - ip + ml2 - minMatch
				}
				if corr := newML - (start2 - ip); corr > 0 {
					start2 += corr
					ref2 += corr
					ml2 -= corr
				}
			}
			if start2+ml2 <= mflimit {
				ml3, ref3, start3 = h.wider(start2+ml2-3, start2, matchlimit, ml2)
			} else {
				ml3 = ml2
			}
			if ml3 == ml2 { // encode ML1 and ML2
				if start2 < ip+ml {
					ml = start2 - ip
				}
				if emit(ml, ref) {
					return seqs[:len(seqs)-1], true, true
				}
				ip = start2
				if emit(ml2, ref2) {
					return seqs[:len(seqs)-1], true, true
				}
				continue
			}
			if start3 < ip+ml+3 { // not enough space for match 2: remove it
				if start3 >= ip+ml { // seq1 can be written now; seq3 becomes seq1
					if start2 < ip+ml {
						corr := ip + ml - start2
						start2 += corr
						ref2 += corr
						ml2 -= corr
						if ml2 < minMatch {
							start2, ref2, ml2 = start3, ref3, ml3
						}
					}
					if emit(ml, ref) {
						return seqs[:len(seqs)-1], true, true
					}
					ip, ref, ml = start3, ref3, ml3
					start0, ref0, ml0 = start2, ref2, ml2
					goto search2
				}
				start2, ref2, ml2 = start3, ref3, ml3
				goto search3
			}
			// three ascending matches: write the first one
			if start2 < ip+ml {
				if start2-ip < hcOptimalML {
					if ml > hcOptimalML {
						ml = hcOptimalML
					}
					if ip+ml > start2+ml2-minMatch {
						ml = start2 - ip + ml2 - minMatch
					}
					if corr := ml - (start2 - ip); corr > 0 {
						start2 += corr
						ref2 += corr
						ml2 -= corr
					}
				} else {
					ml = start2 - ip
				}
			}
			if emit(ml, ref) {
				return seqs[:len(seqs)-1], true, true
			}
			ip, ref, ml = start2, ref2, ml2
			start2, ref2, ml2 = start3, ref3, ml3
			goto search3
		}
	}
	return append(seqs, Seq{Lit: n - anchor}), false, true
}

const (
	repUntested = iota
	repNot
	repConfirmed
)

// countPattern mirrors LZ4HC_countPattern: how many bytes from p repeat the 4-byte pattern.
func countPattern(src []byte, p, limit int, pattern uint32) int {
	start := p
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], pattern)
	p64 := uint64(pattern) | uint64(pattern)<<32
	for p+8 <= limit { // the pattern phase is preserved by 8-byte steps
		if x := binary.LittleEndian.Uint64(src[p:]) ^ p64; x != 0 {
			return p - start + bits.TrailingZeros64(x)>>3
		}
		p += 8
	}
	for i := (p - start) & 3; p < limit; i++ {
		if src[p] != pb[i&3] {
			break
		}
		p++
	}
	return p - start
}

// reverseCountPattern mirrors LZ4HC_reverseCountPattern: bytes before p that continue the pattern backwards.
func reverseCountPattern(src []byte, p, lowest int, pattern uint32) int {
	start := p
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], pattern)
	for p-4 >= lowest && binary.LittleEndian.Uint32(src[p-4:]) == pattern {
		p -= 4
	}
	for p > lowest && src[p-1] == pb[(p-1-start)&3+0] {
		p--
	}
	return start - p
}

var hcPool = sync.Pool{New: func() any { return new(hcState) }}

// hcOptSeqs is LZ4HC_compress_optimal (levels 10..12 of lz4 1.9.x): price-based parsing over a
// 4096-position window with the wider-match search in pattern-analysis + chain-swap mode.
func hcOptSeqs(h *hcState, src []byte, level, attempts int, target []Seq) (seqs []Seq, aborted, ok bool) {
	const optNum, trailing = 1 << 12, 3
	type optT struct{ price, off, mlen, litlen int }
	sufficient := map[int]int{10: 64, 11: 128, 12: optNum - 1}[level]
	full := level == 12
	h.gen++
	if h.gen == 0 {
		*h = hcState{gen: 1}
	}
	h.src, h.next, h.attempts, h.pa, h.swap = src, hcStart, attempts, true, true
	n := len(src)
	mflimit, matchlimit := n-mfLimit, n-lastLiterals
	litPrice := func(l int) int {
		if l >= 15 {
			return l + 1 + (l-15)/255
		}
		return l
	}
	seqPrice := func(l, m int) int {
		p := 3 + litPrice(l)
		if m >= 19 {
			p += 1 + (m-19)/255
		}
		return p
	}
	find := func(ip, minLen int) (length, off int) {
		ml, mp, _ := h.wider(ip, ip, matchlimit, minLen)
		if ml <= minLen {
			return 0, 0
		}
		return ml, ip - mp
	}
	var opt [optNum + trailing]optT
	ip, anchor := 0, 0
	emit := func(ml, off int) bool {
		seqs = append(seqs, Seq{Lit: ip - anchor, Match: ml, Off: off})
		ip += ml
		anchor = ip
		return target != nil && (len(seqs) > len(target) || seqs[len(seqs)-1] != target[len(seqs)-1])
	}
	for ip <= mflimit {
		llen := ip - anchor
		fl, foff := find(ip, minMatch-1)
		if fl == 0 {
			ip++
			continue
		}
		if fl > sufficient {
			if emit(fl, foff) {
				return seqs[:len(seqs)-1], true, true
			}
			continue
		}
		for r := 0; r < minMatch; r++ {
			opt[r] = optT{price: litPrice(llen + r), mlen: 1, litlen: llen + r}
		}
		for m := minMatch; m <= fl; m++ {
			opt[m] = optT{price: seqPrice(llen, m), off: foff, mlen: m, litlen: llen}
		}
		last := fl
		for a := 1; a <= trailing; a++ {
			opt[last+a] = optT{price: opt[last].price + litPrice(a), mlen: 1, litlen: a}
		}
		var bestM, bestOff, cur int
		direct := false
		for cur = 1; cur < last; cur++ {
			cp := ip + cur
			if cp > mflimit {
				break
			}
			if full {
				if opt[cur+1].price <= opt[cur].price && opt[cur+minMatch].price < opt[cur].price+3 {
					continue
				}
			} else if opt[cur+1].price <= opt[cur].price {
				continue
			}
			var nl, noff int
			if full {
				nl, noff = find(cp, minMatch-1)
			} else {
				nl, noff = find(cp, last-cur)
			}
			if nl == 0 {
				continue
			}
			if nl > sufficient || nl+cur >= optNum {
				bestM, bestOff, last, direct = nl, noff, cur+1, true
				break
			}
			base := opt[cur].litlen
			for l := 1; l < minMatch; l++ {
				price := opt[cur].price - litPrice(base) + litPrice(base+l)
				if pos := cur + l; price < opt[pos].price {
					opt[pos] = optT{price: price, mlen: 1, litlen: base + l}
				}
			}
			for ml := minMatch; ml <= nl; ml++ {
				pos := cur + ml
				var price, ll int
				if opt[cur].mlen == 1 {
					ll = opt[cur].litlen
					if cur > ll {
						price = opt[cur-ll].price
					}
					price += seqPrice(ll, ml)
				} else {
					price = opt[cur].price + seqPrice(0, ml)
				}
				if pos > last+trailing || price <= opt[pos].price {
					if ml == nl && last < pos {
						last = pos
					}
					opt[pos] = optT{price: price, off: noff, mlen: ml, litlen: ll}
				}
			}
			for a := 1; a <= trailing; a++ {
				opt[last+a] = optT{price: opt[last].price + litPrice(a), mlen: 1, litlen: a}
			}
		}
		if !direct {
			bestM, bestOff = opt[last].mlen, opt[last].off
			cur = last - bestM
		}
		// reverse traversal: record the chosen path forwards
		cand, selM, selOff := cur, bestM, bestOff
		for {
			nm, no := opt[cand].mlen, opt[cand].off
			opt[cand].mlen, opt[cand].off = selM, selOff
			selM, selOff = nm, no
			if nm > cand {
				break
			}
			cand -= nm
		}
		for r := 0; r < last; {
			ml, off := opt[r].mlen, opt[r].off
			if ml == 1 {
				ip++
				r++
				continue
			}
			r += ml
			if emit(ml, off) {
				return seqs[:len(seqs)-1], true, true
			}
		}
	}
	return append(seqs, Seq{Lit: n - anchor}), false, true
}
