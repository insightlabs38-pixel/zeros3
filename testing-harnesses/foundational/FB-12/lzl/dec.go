package lzl

import "errors"

var ErrCorrupt = errors.New("lzl: corrupt frame")

// Decoder reuses its model between frames; it is not safe for concurrent use.
type Decoder struct {
	m  model
	rc rcDec
}

// Decode decodes one independent frame of n bytes into dst (reused when large enough).
func (z *Decoder) Decode(dst, src []byte, n int) ([]byte, error) {
	if n < 0 || n > MaxRecordBytes || len(src) > MaxRecordBytes+16 {
		return nil, ErrCorrupt
	}
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	out := dst[:n]
	m := &z.m
	m.reset()
	d := &z.rc
	d.reset(src)
	defer func() { d.in = nil }()
	var reps [numReps]uint32
	state := 0
	i := 0
	for i < n {
		ps := i & (posStates - 1)
		if d.bit(&m.isMatch[state][ps]) == 0 {
			var prev byte
			if i > 0 {
				prev = out[i-1]
			}
			p := &m.lit[litCtx(prev)]
			if state >= 7 {
				if uint64(reps[0]) >= uint64(i) {
					return nil, ErrCorrupt
				}
				out[i] = decLitMatched(d, p, out[i-int(reps[0])-1])
			} else {
				out[i] = decLit(d, p)
			}
			state = stateLit(state)
			i++
			continue
		}
		var l int
		if d.bit(&m.isRep[state]) == 0 {
			l = int(decLen(d, &m.matchLen, ps)) + minMatch
			dd := decDist(d, m, lenState(l))
			reps[3], reps[2], reps[1], reps[0] = reps[2], reps[1], reps[0], dd
			state = stateMatch(state)
		} else {
			if d.bit(&m.isRepG0[state]) == 0 {
				if d.bit(&m.isRep0Long[state][ps]) == 0 {
					if uint64(reps[0]) >= uint64(i) {
						return nil, ErrCorrupt
					}
					out[i] = out[i-int(reps[0])-1]
					state = stateShortRep(state)
					i++
					continue
				}
			} else {
				var r uint32
				if d.bit(&m.isRepG1[state]) == 0 {
					r = reps[1]
					reps[1] = reps[0]
				} else {
					if d.bit(&m.isRepG2[state]) == 0 {
						r = reps[2]
					} else {
						r = reps[3]
						reps[3] = reps[2]
					}
					reps[2] = reps[1]
					reps[1] = reps[0]
				}
				reps[0] = r
			}
			l = int(decLen(d, &m.repLen, ps)) + minMatch
			state = stateRep(state)
		}
		if uint64(reps[0]) >= uint64(i) || l > n-i {
			return nil, ErrCorrupt
		}
		dist := int(reps[0]) + 1
		for k := range l { // overlapping copies are legal
			out[i+k] = out[i+k-dist]
		}
		i += l
	}
	// Bytes past the end read as zeros (the encoder strips trailing zeros), so
	// overrun is not an error by itself; the record digest decides integrity.
	return out, nil
}
