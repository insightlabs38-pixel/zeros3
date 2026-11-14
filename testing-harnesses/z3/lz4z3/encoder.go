package lz4z3

import (
	"encoding/binary"
	"math/bits"
	"sync"
)

// Predictor ids: deterministic reference encoders. They are re-implementations of the public
// LZ4 fast algorithm (greedy, one-entry hash table, acceleration skip); they are verified
// against producer output block by block and never trusted.
const (
	PredNone     = 0
	PredDefault  = 1 // lz4 <= 1.9.4 fast_extState: u16 table + 4-byte hash (13 bits) below 64 KiB, u32 + 5-byte hash otherwise
	PredContinue = 2 // LZ4_compress_fast_continue on a fresh stream (RocksDB): always u32 table + 5-byte hash
	PredDefault5 = 3 // fast_extState with a 5-byte hash (12 bits) in the u16 table too (python-lz4 4.4.x bundled library)
	PredHC       = 4 // LZ4HC hash-chain encoder; Accel carries the compression level 3..9 (hc.go)
)

const (
	mfLimit      = 12
	lastLiterals = 5
	skipTrigger  = 6
	hashLog      = 12
)

// Predict runs predictor id with the given acceleration (>= 1) and returns the sequences.
func Predict(id int, plain []byte, accel int) ([]Seq, bool) {
	if accel < 1 {
		accel = 1
	}
	var u16, h4 bool
	switch id {
	case PredDefault:
		u16 = len(plain) < 65547 // LZ4_64Klimit: 64 KiB + (MFLIMIT-1)
		h4 = u16
	case PredDefault5:
		u16 = len(plain) < 65547
	case PredContinue:
	case PredHC:
		s, ok := HCSeqs(plain, accel)
		return s, ok
	case PredPierrec:
		s, _ := pierrecSeqs(plain, nil)
		return s, true
	default:
		return nil, false
	}
	s, _ := fastSeqs(plain, accel, u16, h4, nil)
	return s, true
}

// predictPrefix runs the predictor but stops at the first sequence that differs from target,
// returning the number of leading sequences that agree (len(target) with an exact run).
func predictPrefix(id int, plain []byte, accel int, target []Seq) (int, bool) {
	var u16, h4 bool
	switch id {
	case PredDefault:
		u16 = len(plain) < 65547
		h4 = u16
	case PredDefault5:
		u16 = len(plain) < 65547
	case PredContinue:
	case PredHC:
		s, aborted, ok := hcSeqs(plain, accel, target)
		if !ok {
			return 0, false
		}
		if aborted {
			return len(s), true
		}
		return commonPrefix(s, target), true
	case PredPierrec:
		s, aborted := pierrecSeqs(plain, target)
		if aborted {
			return len(s), true
		}
		return commonPrefix(s, target), true
	default:
		return 0, false
	}
	s, aborted := fastSeqs(plain, accel, u16, h4, target)
	if aborted {
		return len(s), true
	}
	return commonPrefix(s, target), true
}

// PredictBlock returns the predicted compressed block, or false for an unknown id.
func PredictBlock(id int, plain []byte, accel int) ([]byte, bool) {
	s, ok := Predict(id, plain, accel)
	if !ok {
		return nil, false
	}
	b, err := Build(plain, s)
	return b, err == nil
}

func hash5(v uint64, bits uint) uint32 { return uint32(((v << 24) * 889523592379) >> (64 - bits)) }

// fastSeqs mirrors the structure of LZ4_compress_generic for a single memory segment and a
// fresh table (noDict).
func fastSeqs(src []byte, accel int, u16, h4 bool, target []Seq) ([]Seq, bool) {
	n := len(src)
	var seqs []Seq
	anchor := 0
	// diverged reports whether the newest sequence disagrees with target (early exit for sweeps).
	diverged := func() bool {
		return target != nil && (len(seqs) > len(target) || seqs[len(seqs)-1] != target[len(seqs)-1])
	}
	if n >= mfLimit+1 {
		mflimitPlusOne := n - mfLimit + 1
		matchlimit := n - lastLiterals
		sc := fastPool.Get().(*fastScratch)
		defer fastPool.Put(sc)
		sc.gen++
		if sc.gen == 0 { // stamp wrapped: clear once
			*sc = fastScratch{gen: 1}
		}
		gen := sc.gen
		hashAt := func(p int) uint32 {
			if h4 {
				return (binary.LittleEndian.Uint32(src[p:]) * 2654435761) >> (32 - (hashLog + 1))
			}
			return hash5(binary.LittleEndian.Uint64(src[p:]), hashLog)
		}
		get := func(h uint32) int {
			if sc.stamp[h] != gen {
				return 0
			}
			if u16 {
				return int(uint16(sc.pos[h]))
			}
			return int(sc.pos[h])
		}
		put := func(h uint32, p int) {
			sc.stamp[h] = gen
			if u16 {
				sc.pos[h] = uint32(uint16(p))
			} else {
				sc.pos[h] = uint32(p)
			}
		}
		ip := 0
		put(hashAt(ip), ip)
		ip++
		forwardH := hashAt(ip)
	outer:
		for {
			var match int
			forwardIp, step, search := ip, 1, accel<<skipTrigger
			for {
				h := forwardH
				cur := forwardIp
				mi := get(h)
				ip = forwardIp
				forwardIp += step
				step = search >> skipTrigger
				search++
				if forwardIp > mflimitPlusOne {
					break outer
				}
				match = mi
				forwardH = hashAt(forwardIp)
				put(h, cur)
				if !u16 && mi+maxOff < cur {
					continue
				}
				if binary.LittleEndian.Uint32(src[match:]) == binary.LittleEndian.Uint32(src[ip:]) {
					break
				}
			}
			for ip > anchor && match > 0 && src[ip-1] == src[match-1] { // catch up
				ip--
				match--
			}
			lit := ip - anchor
			for {
				ml := count(src, ip+minMatch, match+minMatch, matchlimit)
				seqs = append(seqs, Seq{Lit: lit, Match: ml + minMatch, Off: ip - match})
				if diverged() {
					return seqs[:len(seqs)-1], true
				}
				ip += ml + minMatch
				anchor = ip
				if ip >= mflimitPlusOne {
					break outer
				}
				put(hashAt(ip-2), ip-2)
				h := hashAt(ip)
				cur := ip
				mi := get(h)
				put(h, cur)
				if (u16 || mi+maxOff >= cur) && binary.LittleEndian.Uint32(src[mi:]) == binary.LittleEndian.Uint32(src[ip:]) {
					match, lit = mi, 0
					continue
				}
				break
			}
			ip++
			forwardH = hashAt(ip)
		}
	}
	return append(seqs, Seq{Lit: n - anchor}), false
}

func count(src []byte, p, m, limit int) int {
	c := 0
	for p+c+8 <= limit { // m < p, so both loads stay inside src
		if x := binary.LittleEndian.Uint64(src[p+c:]) ^ binary.LittleEndian.Uint64(src[m+c:]); x != 0 {
			return c + bits.TrailingZeros64(x)>>3
		}
		c += 8
	}
	for p+c < limit && src[p+c] == src[m+c] {
		c++
	}
	return c
}

// fastScratch holds the predictor hash table; entries are valid only when their stamp equals the
// current generation, so a failed attempt costs no table clearing.
type fastScratch struct {
	pos   [1 << (hashLog + 1)]uint32
	stamp [1 << (hashLog + 1)]uint32
	gen   uint32
}

var fastPool = sync.Pool{New: func() any { return new(fastScratch) }}
