package ir

// Exact emulation of zlib's deflate_fast (levels 1-3) as a predictor profile
// (Z3-08C). The generic predictor rebuilds hash chains from the plaintext
// alone; deflate_fast differs in exactly the ways a plaintext-only rebuild
// cannot see: the 15-bit hash buckets mix colliding strings into the chain
// budget, interior positions of matches longer than max_insert_length are never
// inserted, and the walk stops at nice_length. The emulator reproduces those
// decisions. It is not a compressor: it only proposes the one match zlib would
// have chosen; the unit still codes the real token against it, so a wrong
// guess (other zlib variant, flush points) costs bits, never correctness.
//
// State at a unit start is rebuilt by free-running the emulator over a warm-up
// window before the unit (the decoder knows that plaintext); once the emulator
// is synchronised its decisions equal the compressor's. Inside the unit the
// emulator follows the real tokens.

// zlevel is the deflate_fast row of zlib's configuration_table.
type zlevel struct{ chain, nice, maxInsert int }

var zfastLevels = [...]zlevel{{}, {4, 8, 4}, {8, 16, 5}, {32, 32, 6}}

const (
	zMaxDist = 32768 - (258 + 3 + 1) // MAX_DIST = w_size - MIN_LOOKAHEAD
	zHashLen = 1 << 15
)

// EmuWarm overrides the warm-up length of emulating profiles (experiments only;
// zero means the profile's own value). It must be identical when decoding.
var EmuWarm int

// zhash is zlib's 3-byte hash for memLevel 8 (hash_bits 15, shift 5).
func zhash(b []byte, q int) uint32 {
	return (uint32(b[q])<<10 ^ uint32(b[q+1])<<5 ^ uint32(b[q+2])) & (zHashLen - 1)
}

func (w *window) emuInsert(q int) (prevHead int) {
	if q+3 > w.end {
		return 0
	}
	h := zhash(w.plain, q)
	prevHead = int(w.headGet(h))
	w.prev[q&32767] = int32(prevHead)
	w.headSet(h, int32(q+1))
	return prevHead
}

// emuDecide inserts pos and returns the match deflate_fast would take there
// (length 0: literal). capLen bounds the match length (remaining input).
func (w *window) emuDecide(pos, capLen int) (dist, length int) {
	head := w.emuInsert(pos)
	if head == 0 || capLen < 3 {
		return 0, 0
	}
	lv := zfastLevels[w.prof.Emu]
	cur := head - 1
	if pos-cur > zMaxDist {
		return 0, 0
	}
	nice := min(lv.nice, capLen)
	best, bestAt := 2, -1
	chain := lv.chain
	limit := pos - zMaxDist // candidates must be > limit
	for {
		// zlib's quick reject: bytes at best_len, best_len-1 and the first two
		if w.plain[cur+best] == w.plain[pos+best] && w.plain[cur+best-1] == w.plain[pos+best-1] &&
			w.plain[cur] == w.plain[pos] && w.plain[cur+1] == w.plain[pos+1] {
			l := matchLen(w.plain, cur, pos, capLen)
			w.work += int64(l) + 1
			if l > best {
				best, bestAt = l, cur
				if l >= nice {
					break
				}
			}
		} else {
			w.work++
		}
		nx := int(w.prev[cur&32767]) - 1
		chain--
		if nx <= limit || nx < 0 || nx >= cur || chain == 0 {
			break
		}
		cur = nx
	}
	if bestAt < 0 || best < 3 {
		return 0, 0
	}
	return pos - bestAt, min(best, capLen)
}

// emuWarm free-runs the emulator over [from, start). Tokens never cross start
// (the real ones cannot, a unit starts on a token boundary).
func (w *window) emuWarm(from, start int) {
	lv := zfastLevels[w.prof.Emu]
	pos := from
	for pos < start {
		_, l := w.emuDecide(pos, min(258, start-pos))
		if l < 3 {
			pos++
			continue
		}
		if l <= lv.maxInsert {
			for q := pos + 1; q < pos+l; q++ {
				w.emuInsert(q)
			}
		}
		pos += l
	}
	w.inserted = start
}

// emuSearch is search() for emulating profiles: at most one candidate.
func (w *window) emuSearch(pos, capLen int) ([]cand, int, bool) {
	for i := range w.ent {
		if e := &w.ent[i]; e.valid && e.pos == pos {
			return e.buf[:e.n], e.lmax, true
		}
	}
	e := &w.ent[w.nextEvict]
	w.nextEvict ^= 1
	e.valid = false
	out := e.buf[:0]
	lmax := 0
	if pos >= w.inserted { // positions are visited once, in order
		d, l := w.emuDecide(pos, capLen)
		w.inserted = pos + 1
		if l >= 3 {
			out = append(out, cand{d, l})
			lmax = l
		}
	}
	if w.work > w.budget {
		return nil, 0, false
	}
	e.pos, e.valid, e.n, e.lmax = pos, true, len(out), lmax
	return out, lmax, true
}

// emuAfter applies deflate_fast's insertion rule for a token of length l at pos.
func (w *window) emuAfter(pos, l int) {
	if l <= zfastLevels[w.prof.Emu].maxInsert {
		for q := pos + 1; q < pos+l; q++ {
			w.emuInsert(q)
		}
	}
	w.inserted = pos + l
}
