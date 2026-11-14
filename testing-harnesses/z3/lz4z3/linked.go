package lz4z3

import "encoding/binary"

// Linked-block predictors: LZ4F blockLinked frames at levels < 3 call
// LZ4_compress_fast_continue once per block on one stream, so the u32 hash table is carried
// from block to block. Accel is the acceleration (1 for level 0..2, 1-level below 0).
const (
	PredLinkPrefix = 6 // blocks contiguous in memory (LZ4F_compressFrame, python lz4.frame.compress)
	PredLinkExt    = 7 // previous block seen through the saved 64 KiB dictionary (one LZ4F_compressUpdate per block)
	PredLinkHC     = 8 // LZ4_compress_HC_continue on contiguous blocks (LZ4F levels >= 3); Accel is the level
)

// linker is the carried compressor state of one frame: Next predicts content[pos:end].
type linker interface {
	Next(content []byte, end int) ([]Seq, bool)
}

// HCLinked carries the LZ4HC hash chains across the dependent blocks of one frame. The chains
// depend only on the positions inserted, so stored (overflowed) blocks need no special case.
type HCLinked struct {
	Level int
	h     *hcState
	pos   int
}

func (l *HCLinked) Next(content []byte, end int) ([]Seq, bool) {
	cont := l.h != nil
	if !cont {
		l.h = new(hcState)
	}
	start := l.pos
	l.pos = end
	s, _, ok := hcRun(l.h, content[:end], start, l.Level, nil, cont)
	return s, ok
}

func newLinker(pred, accel int) (linker, error) {
	switch pred {
	case PredLinkPrefix, PredLinkExt:
		if accel < 1 || accel > 65537 {
			return nil, recipeErr("linked acceleration %d", accel)
		}
		return &Linked{Ext: pred == PredLinkExt, Accel: accel}, nil
	case PredLinkHC:
		if HCAttempts(accel) == 0 {
			return nil, recipeErr("linked LZ4HC level %d", accel)
		}
		return &HCLinked{Level: accel}, nil
	}
	return nil, recipeErr("linked predictor %d", pred)
}

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
	l     linker
	pred  int
	accel int
	ends  []int // end offset of every block of the frame so far
	done  int
}

func (lr *linkRun) seqs(content []byte, k int, rc Recipe) ([]Seq, error) {
	if lr.l == nil {
		l, err := newLinker(rc.Pred, rc.Accel)
		if err != nil {
			return nil, err
		}
		lr.l, lr.pred, lr.accel = l, rc.Pred, rc.Accel
	} else if lr.pred != rc.Pred || lr.accel != rc.Accel {
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
	return rc.Kind != KindExplicit && rc.Pred >= PredLinkPrefix && rc.Pred <= PredLinkHC
}

// linkAnalysis runs the candidate linked profiles over a dependent frame and emits the block
// recipes of the profile that survives. The first block is an ordinary fresh-stream block: it
// shows whether the frame is fast mode (both table-carrying profiles compete) or LZ4HC.
type linkAnalysis struct {
	recs  []linkRec
	l     [2]linker
	ids   [2]int
	acc   [2]int // acceleration (fast profiles) or level (LZ4HC) of each candidate
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
	if la.accel == 0 {
		la.accel = 1
		var hc []int
		for _, ac := range accelSweep {
			if seqs == nil {
				break
			}
			if k, _ := predictPrefix(PredContinue, content, ac, seqs); k == len(seqs) {
				la.accel = ac
				break
			}
			if ac == 1 { // not the default fast profile: LZ4HC before the rarer accelerations
				for lv := 3; lv <= 12; lv++ {
					if k, _ := predictPrefix(PredHC, content, lv, seqs); k == len(seqs) {
						hc = append(hc, lv)
					}
				}
				if hc != nil {
					break
				}
			}
		}
		if hc != nil { // neighbouring levels often agree on one block: keep the lowest and the highest
			lo, hi := hc[0], hc[len(hc)-1]
			la.l, la.ids, la.acc = [2]linker{&HCLinked{Level: lo}, &HCLinked{Level: hi}}, [2]int{PredLinkHC, PredLinkHC}, [2]int{lo, hi}
			if lo == hi {
				la.l[1], la.died[1] = nil, 1
			}
		} else {
			la.l = [2]linker{&Linked{Accel: la.accel}, &Linked{Ext: true, Accel: la.accel}}
			la.ids, la.acc = [2]int{PredLinkPrefix, PredLinkExt}, [2]int{la.accel, la.accel}
		}
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
				rc = Recipe{Kind: KindPredicted, Pred: la.ids[m], Accel: la.acc[m]}
			} else if r.k[m] > 0 {
				h := Recipe{Kind: KindHybrid, Pred: la.ids[m], Accel: la.acc[m], Prefix: r.k[m], Seqs: r.seqs[r.k[m]:]}
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
