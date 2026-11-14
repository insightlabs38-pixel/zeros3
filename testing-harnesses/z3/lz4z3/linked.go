package lz4z3

import "encoding/binary"

// Linked-block predictors: LZ4F blockLinked frames at levels < 3 call
// LZ4_compress_fast_continue once per block on one stream, so the u32 hash table is carried
// from block to block. Accel is the acceleration (1 for level 0..2, 1-level below 0).
const (
	PredLinkPrefix = 6 // blocks contiguous in memory (LZ4F_compressFrame, python lz4.frame.compress)
	PredLinkExt    = 7 // previous block seen through the saved 64 KiB dictionary (one LZ4F_compressUpdate per block)
)

// Linked is the carried compressor state of one frame.
type Linked struct {
	Ext   bool
	Accel int
	tab   [1 << hashLog]uint32
	pos   int // content bytes consumed
	prev  int // length of the previous block
}

// Next predicts the block content[l.pos:end] under LZ4F's output budget (block length - 1).
// ok is false when the compressor would have overflowed the budget (LZ4F then stores the
// block raw); the table still advances exactly as far as the real compressor got.
func (l *Linked) Next(content []byte, end int) (seqs []Seq, ok bool) {
	start := l.pos
	n := end - start
	budget := n - 1
	dictLow, idxLow := 0, 0 // lowest history position reachable by catch-up; lowest valid index
	if l.Ext {
		d := min(l.prev, 1<<16)
		dictLow = start - d
		if d < 1<<16 && d < start { // dictSmall
			idxLow = dictLow
		}
	}
	l.pos, l.prev = end, n
	src := content[:end]
	accel := max(l.Accel, 1)
	op, anchor := 0, start
	if n >= mfLimit+1 {
		mflimitPlusOne := end - mfLimit + 1
		matchlimit := end - lastLiterals
		hashAt := func(p int) uint32 { return hash5(binary.LittleEndian.Uint64(src[p:]), hashLog) }
		r32 := func(p int) uint32 { return binary.LittleEndian.Uint32(src[p:]) }
		ip := start
		l.tab[hashAt(ip)] = uint32(ip)
		ip++
		forwardH := hashAt(ip)
	outer:
		for {
			var match int
			forwardIp, step, search := ip, 1, accel<<skipTrigger
			for {
				h := forwardH
				cur := forwardIp
				mi := int(l.tab[h])
				ip = forwardIp
				forwardIp += step
				step = search >> skipTrigger
				search++
				if forwardIp > mflimitPlusOne {
					break outer
				}
				forwardH = hashAt(forwardIp)
				l.tab[h] = uint32(cur)
				if mi < idxLow || mi+maxOff < cur {
					continue
				}
				if r32(mi) == r32(ip) {
					match = mi
					break
				}
			}
			low := 0
			if l.Ext {
				low = start
				if match < start {
					low = dictLow
				}
			}
			for ip > anchor && match > low && src[ip-1] == src[match-1] {
				ip--
				match--
			}
			lit := ip - anchor
			op++
			if op+lit+8+lit/15 > budget {
				return nil, false
			}
			if lit >= 15 {
				op += (lit-15)/255 + 1
			}
			op += lit
			for {
				op += 2
				ml := count(src, ip+minMatch, match+minMatch, matchlimit)
				seqs = append(seqs, Seq{Lit: lit, Match: ml + minMatch, Off: ip - match})
				ip += ml + minMatch
				if op+6+(ml+240)/255 > budget {
					return nil, false
				}
				if ml >= 15 {
					op += (ml-15)/255 + 1
				}
				anchor = ip
				if ip >= mflimitPlusOne {
					break outer
				}
				l.tab[hashAt(ip-2)] = uint32(ip - 2)
				h := hashAt(ip)
				mi := int(l.tab[h])
				l.tab[h] = uint32(ip)
				if mi >= idxLow && mi+maxOff >= ip && r32(mi) == r32(ip) {
					op++
					match, lit = mi, 0
					continue
				}
				break
			}
			ip++
			forwardH = hashAt(ip)
		}
	}
	last := end - anchor
	if op+last+1+(last+255-15)/255 > budget {
		return nil, false
	}
	return append(seqs, Seq{Lit: last}), true
}

// linkRun replays the linked predictor lazily: blocks are fed in order up to the one asked
// for, so frames without linked recipes cost nothing. One profile per frame.
type linkRun struct {
	l    *Linked
	pred int
	ends []int // end offset of every block of the frame so far
	done int
}

func (lr *linkRun) seqs(content []byte, k int, rc Recipe) ([]Seq, error) {
	if rc.Accel < 1 || rc.Accel > 65537 {
		return nil, recipeErr("linked acceleration %d", rc.Accel)
	}
	if lr.l == nil {
		lr.l, lr.pred = &Linked{Ext: rc.Pred == PredLinkExt, Accel: rc.Accel}, rc.Pred
	} else if lr.pred != rc.Pred || lr.l.Accel != rc.Accel {
		return nil, recipeErr("linked profile changes inside a frame")
	}
	if k < lr.done {
		return nil, recipeErr("linked block order")
	}
	var s []Seq
	ok := false
	for ; lr.done <= k; lr.done++ {
		s, ok = lr.l.Next(content, lr.ends[lr.done])
	}
	if !ok {
		return nil, recipeErr("linked predictor overflows the block budget")
	}
	if rc.Kind == KindHybrid {
		if rc.Prefix > len(s) {
			return nil, recipeErr("prefix %d beyond %d predicted sequences", rc.Prefix, len(s))
		}
		s = append(append([]Seq(nil), s[:rc.Prefix]...), rc.Seqs...)
	}
	return s, nil
}

func isLinkPred(rc Recipe) bool {
	return rc.Kind != KindExplicit && (rc.Pred == PredLinkPrefix || rc.Pred == PredLinkExt)
}

// linkAnalysis runs both linked profiles over a dependent frame and emits the block recipes
// of the profile that survives.
type linkAnalysis struct {
	recs  []linkRec
	l     [2]*Linked
	miss  [2]int
	died  [2]int // block index at which the profile was dropped
	score [2]int
	accel int
}

type linkRec struct {
	tag   byte
	n     int
	ext   []byte
	seqs  []Seq // nil for stored blocks and for blocks every live profile reproduces
	k     [2]int
	exact [2]bool
}

const linkGiveUp = 8 // consecutive unpredicted blocks before a profile is dropped

// block records one block; content ends with its n payload bytes, seqs is nil for stored blocks.
func (la *linkAnalysis) block(content []byte, n int, seqs []Seq, tag byte, ext []byte) {
	rec := linkRec{tag: tag, n: n, ext: ext}
	if la.accel == 0 { // the first block is an ordinary fresh-stream block: its acceleration is visible
		la.accel = 1
		for _, ac := range accelSweep {
			if seqs == nil {
				break
			}
			if k, _ := predictPrefix(PredContinue, content, ac, seqs); k == len(seqs) {
				la.accel = ac
				break
			}
		}
		la.l = [2]*Linked{{Accel: la.accel}, {Ext: true, Accel: la.accel}}
	}
	keep := la.l[0] == nil && la.l[1] == nil
	for m, l := range la.l {
		if l == nil {
			continue
		}
		s, ok := l.Next(content, len(content))
		if seqs == nil {
			continue
		}
		if ok {
			rec.k[m] = commonPrefix(s, seqs)
			rec.exact[m] = rec.k[m] == len(seqs) && len(s) == len(seqs)
		}
		if rec.exact[m] {
			la.miss[m] = 0
			la.score[m]++
			continue
		}
		keep = true
		if la.miss[m]++; la.miss[m] >= linkGiveUp {
			la.l[m], la.died[m] = nil, len(la.recs)+1
		}
	}
	if keep {
		rec.seqs = seqs
	}
	la.recs = append(la.recs, rec)
}

func (la *linkAnalysis) emit(st *FrameStats) []byte {
	// the profile that lived longest has every sequence list it needs; ties go to the score
	m := 0
	d0, d1 := la.died[0], la.died[1]
	if d0 == 0 {
		d0 = len(la.recs) + 2
	}
	if d1 == 0 {
		d1 = len(la.recs) + 2
	}
	if d1 > d0 || (d1 == d0 && la.score[1] > la.score[0]) {
		m = 1
	}
	var out []byte
	for _, r := range la.recs {
		out = append(out, r.tag)
		out = binary.AppendUvarint(out, uint64(r.n))
		if r.tag&1 == 0 {
			rc := Recipe{Kind: KindExplicit, Seqs: r.seqs}
			if r.exact[m] {
				rc = Recipe{Kind: KindPredicted, Pred: PredLinkPrefix + m, Accel: la.accel}
			} else if r.k[m] > 0 {
				h := Recipe{Kind: KindHybrid, Pred: PredLinkPrefix + m, Accel: la.accel, Prefix: r.k[m], Seqs: r.seqs[r.k[m]:]}
				if len(h.Marshal()) < len(rc.Marshal()) {
					rc = h
				}
			}
			st.count(rc, len(r.seqs))
			out = append(out, rc.Marshal()...)
		}
		out = append(out, r.ext...)
	}
	return out
}
