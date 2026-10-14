package ir

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"zeros3-z3/deflate"
)

// Predictive reconstruction (Z3-07). The plaintext of the unit and the 32 KiB
// before it are known to the decoder, so for every token position it can list
// the plausible LZ77 candidates itself. A unit then stores only *corrections*:
//
//	isMatch   was a match taken here?          (context: best candidate length,
//	                                            lazy-lookahead hint, literal run)
//	idx       which candidate? (unary; idx==n is an escape)
//	len       full length of that candidate? else a delta
//	escape    explicit E2 coding of an unexplained match
//
// Prediction is never trusted: the decoded token stream replays through the
// exact DEFLATE writer and is checked against the recorded CRC, and the
// explicit (E2) form remains the universal fallback. Candidate search is a
// 32 KiB hash chain rebuilt from the plaintext at every unit start, so the
// predictor state is cheap to initialise at any unit boundary.

// Profile parameterises candidate generation; it is not a separate engine.
type Profile struct {
	ID       uint8
	Name     string
	HashLen  int  // minimum candidate length (bytes hashed)
	MaxChain int  // chain entries examined per position
	Lazy     bool // compute the lookahead hint for the next position
	MaxCand  int  // candidates kept
	Recency  bool // candidates nearest-first; otherwise longest-first
	// Frontier keeps only candidates that are nearer or longer than everything
	// nearer (chain matchers); table-based matchers (Go levels 1-6) pick
	// dominated candidates too, so their profile keeps every candidate.
	Frontier bool
	// MaxInsert > 0: matches longer than this do not insert their interior
	// positions into the chain (zlib deflate_fast's max_insert_length).
	MaxInsert int
}

// Profiles is the small fixed profile set (index = ID).
var Profiles = []Profile{
	{0, "generic", 3, 128, true, 32, true, true, 0},
	{1, "zlib-like", 3, 256, true, 32, false, true, 0},
	{2, "go-like", 4, 32, true, 32, true, false, 0},
	{3, "zlib-fast-like", 3, 8, false, 32, true, true, 4},
}

// Ablate switches predictor features off for the Z3-07A ablation study. It is
// experiment-only global state (never set by library code) and must stay zero
// in normal operation: units encoded with a feature off are not decodable
// with it on.
var Ablate struct {
	NoLazyHint bool // drop the next-position lookahead context
	NoRunCtx   bool // drop the literal-run-length context of the match flag
	NoRepCtx   bool // drop the repeat-distance context of the candidate choice
	NoLenCtx   bool // drop candidate length contexts of the candidate choice
	NoIdxCtx   bool // one context for every candidate decision
}

// PredStats are diagnostics gathered while encoding with the predictor.
type PredStats struct {
	Tokens, Matches int
	Escapes         int
	IdxHist         [8]int
	LenExact        int
	Work            int64
}

type window struct {
	plain     []byte
	base, end int // window covers [base, end); candidates are searched at positions in [start, end)
	prof      *Profile
	head      [1 << 16]int32
	prev      [32768]int32
	inserted  int // next position to insert
	work      int64
	budget    int64
	ent       [2]centry // two-entry candidate cache (pos and its lookahead)
	nextEvict int
}

type centry struct {
	pos   int
	valid bool
	n     int
	lmax  int
	buf   [64]cand
}

type cand struct{ dist, length int }

func hashAt(b []byte, q, n int) uint32 {
	v := uint32(b[q]) | uint32(b[q+1])<<8 | uint32(b[q+2])<<16
	if n >= 4 {
		v |= uint32(b[q+3]) << 24
	}
	return v * 0x9E3779B1 >> 16
}

func newWindow(plain []byte, start, plainLen int, prof *Profile) *window {
	w := &window{plain: plain, prof: prof, end: start + plainLen}
	w.base = max(0, start-32768)
	w.inserted = w.base
	w.budget = int64(plainLen)*512 + 1<<20
	if prof.MaxInsert > 0 && start > w.base {
		w.simulateWindow(start)
	}
	w.insertTo(start)
	return w
}

// simulateWindow rebuilds the chain over [base, start) the way the profile's
// compressor would have built it: interior positions of long matches are not
// inserted, and the decoder does not know the earlier token boundaries, so the
// predictor walks the window greedily with its own top candidate. The walk
// resynchronises with the real boundaries within a few tokens.
func (w *window) simulateWindow(start int) {
	n := w.prof.HashLen
	pos := w.base
	for pos < start {
		cands, lmax, ok := w.search(pos, min(258, w.end-pos))
		if !ok || len(cands) == 0 || lmax < n {
			pos++
			continue
		}
		l := min(cands[0].length, start-pos)
		w.afterMatch(pos, l)
		pos += l
	}
	for i := range w.ent { // the cache must not outlive the simulated walk
		w.ent[i].valid = false
	}
}

func (w *window) insertTo(limit int) {
	n := w.prof.HashLen
	for q := w.inserted; q < limit; q++ {
		if q+n > w.end { // positions near the unit end cannot start a candidate
			continue
		}
		h := hashAt(w.plain, q, n)
		w.prev[q&32767] = w.head[h]
		w.head[h] = int32(q - w.base + 1)
	}
	if limit > w.inserted {
		w.inserted = limit
	}
}

// afterMatch applies the profile's insertion policy for a match at pos.
func (w *window) afterMatch(pos, length int) {
	if mi := w.prof.MaxInsert; mi > 0 && length > mi {
		w.insertTo(pos + 1)
		if pos+length > w.inserted {
			w.inserted = pos + length
		}
	}
}

// search lists the candidates at pos ranked by (length desc, distance asc).
// ok=false means the work budget was exhausted.
func (w *window) search(pos, capLen int) ([]cand, int, bool) {
	for i := range w.ent {
		if e := &w.ent[i]; e.valid && e.pos == pos {
			return e.buf[:e.n], e.lmax, true
		}
	}
	n := w.prof.HashLen
	w.insertTo(pos)
	e := &w.ent[w.nextEvict]
	w.nextEvict ^= 1
	e.valid = false
	out := e.buf[:0]
	lmax := 0
	if capLen >= n && pos+n <= w.end {
		h := hashAt(w.plain, pos, n)
		q := int(w.head[h])
		maxc := w.prof.MaxCand
		for steps := 0; q != 0 && steps < w.prof.MaxChain; steps++ {
			qa := w.base + q - 1
			d := pos - qa
			if d <= 0 || d > 32768 {
				break
			}
			var l int
			if w.prof.Frontier && lmax >= n && lmax < capLen && w.plain[qa+lmax] != w.plain[pos+lmax] {
				w.work++ // cannot beat the best so far (zlib's quick reject)
				goto next
			}
			l = matchLen(w.plain, qa, pos, capLen)
			w.work += int64(l) + 1
			if l >= n {
				if l > lmax {
					lmax = l
				}
				if !w.prof.Frontier || l == lmax && (len(out) == 0 || out[len(out)-1].length < l) {
					if len(out) < maxc {
						out = append(out, cand{d, l})
					}
				}
			}
		next:
			nq := int(w.prev[qa&32767])
			if nq >= q { // stale link
				break
			}
			q = nq
		}
	}
	if w.work > w.budget {
		return nil, 0, false
	}
	if !w.prof.Recency { // longest first
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	e.pos, e.valid, e.n, e.lmax = pos, true, len(out), lmax
	return out, lmax, true
}

// matchLen returns how many bytes of p[q:] and p[pos:] agree, at most max
// (q < pos, pos+max <= len(p)).
func matchLen(p []byte, q, pos, max int) int {
	l := 0
	for l+8 <= max {
		x := binary.LittleEndian.Uint64(p[q+l:]) ^ binary.LittleEndian.Uint64(p[pos+l:])
		if x != 0 {
			return l + bits.TrailingZeros64(x)>>3
		}
		l += 8
	}
	for l < max && p[q+l] == p[pos+l] {
		l++
	}
	return l
}

func lengthCode(l int) (sym int, extra uint32) {
	lo, hi := 0, 28
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if deflate.LengthBase(257+mid) <= l {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if l == 258 {
		return 285, 0
	}
	return 257 + lo, uint32(l - deflate.LengthBase(257+lo))
}

// canonical reports whether t is the canonical encoding of its length/distance.
func canonical(t deflate.Token) bool {
	s, e := lengthCode(t.Length())
	return s == t.Sym() && e == t.LenExtra()
}

func lmaxBucket(l int) int {
	switch {
	case l == 0:
		return 0
	case l <= 3:
		return 1
	case l == 4:
		return 2
	case l <= 6:
		return 3
	case l <= 9:
		return 4
	case l <= 17:
		return 5
	case l <= 33:
		return 6
	}
	return 7
}

func runBucket(n int) int {
	switch {
	case n == 0:
		return 0
	case n <= 2:
		return 1
	case n <= 8:
		return 2
	case n <= 63:
		return 3
	case n <= 127:
		return 4
	}
	return 5
}

type predModel struct {
	kind    [8][3][6]bitModel
	idx     [2][3][4][4]bitModel // [isRepeatDistance][length class][candidate length bucket][min(i,3)]
	lenSame [3][3]bitModel
	lenUn   [20]bitModel
	lenMant [20][4]bitModel
	esc     *e2model
}

func newPredModel(p *Profile) *predModel {
	m := &predModel{esc: newE2()}
	for a := range m.kind {
		for b := range m.kind[a] {
			for c := range m.kind[a][b] {
				mm := &m.kind[a][b][c]
				mm.init()
				// prior P(isMatch==0)
				switch {
				case a == 0: // no candidate: almost surely a literal
					mm.setP(4060)
				case a == 1:
					mm.setP(2600)
				default:
					mm.setP(500)
				}
			}
		}
	}
	for r := range m.idx {
		for lc := range m.idx[r] {
			for lb := range m.idx[r][lc] {
				for i := range m.idx[r][lc][lb] {
					mm := &m.idx[r][lc][lb][i]
					mm.init()
					// bit 1 = "this candidate"; prior P(bit==0)
					p0 := 3000
					if i == 0 {
						p0 = 900
					} else if i == 1 {
						p0 = 2200
					}
					if lc > 0 {
						p0 += 600
					}
					if r == 1 {
						p0 -= 700
					}
					mm.setP(min(max(p0, 200), 3900))
				}
			}
		}
	}
	for i := range m.lenSame {
		for j := range m.lenSame[i] {
			m.lenSame[i][j].init()
			m.lenSame[i][j].setP(500) // bit 1 = the candidate's full length
		}
	}
	for i := range m.lenUn {
		m.lenUn[i].init()
		for j := range m.lenMant[i] {
			m.lenMant[i][j].init()
		}
	}
	return m
}

// predictor encodes/decodes units against a fixed plaintext.
type predictor struct {
	plain    []byte
	prof     *Profile
	Stats    PredStats
	lastIdx  int
	lastDist int // distance of the previous match in this unit (repeat-offset context)
}

func newPredictor(plain []byte, profile uint8) *predictor {
	p := &predictor{plain: plain}
	if int(profile) < len(Profiles) {
		p.prof = &Profiles[profile]
	} else {
		p.prof = &Profiles[0]
	}
	return p
}

// token codes one token at pos (encoding when t is meaningful, decoding when
// io.dec != nil). lits is the number of literals since the last match.
func (p *predictor) token(io *rcio, m *predModel, w *window, pos, lits int, t deflate.Token) (deflate.Token, int, error) {
	enc := io.dec == nil
	capLen := min(258, w.end-pos)
	cands, lmax, ok := w.search(pos, capLen)
	if !ok {
		return 0, 0, fmt.Errorf("%w: predictor work budget", ErrLimit)
	}
	lazy := 0
	if p.prof.Lazy && !Ablate.NoLazyHint && lmax > 0 && pos+1 < w.end {
		_, l2, ok := w.search(pos+1, min(258, w.end-pos-1))
		if !ok {
			return 0, 0, fmt.Errorf("%w: predictor work budget", ErrLimit)
		}
		lazy = 1
		if l2 > lmax {
			lazy = 2
		}
	}
	lb := lmaxBucket(lmax)
	rb := runBucket(lits)
	if Ablate.NoRunCtx {
		rb = 0
	}
	km := &m.kind[lb][lazy][rb]
	isMatch := 0
	if enc && !t.IsLiteral() {
		isMatch = 1
	}
	io.cat = CatPredHit
	if io.bit(km, isMatch) == 0 {
		return deflate.Literal(0), 1, nil
	}
	// match: find the candidate
	n := len(cands)
	idx := n
	var length int
	if enc {
		length = t.Length()
		if canonical(t) {
			d := t.Distance()
			for i, c := range cands {
				if c.dist == d && c.length >= length {
					idx = i
					break
				}
			}
		}
	}
	io.cat = CatPredDist
	got := n
	for i := 0; i < n; i++ {
		want := 0
		if enc && idx == i {
			want = 1
		}
		rep, lc := 0, 0
		if cands[i].dist == p.lastDist {
			rep = 1
		}
		if cands[i].length < lmax {
			lc = 1
			if cands[i].length+3 < lmax {
				lc = 2
			}
		}
		lb := 0
		switch l := cands[i].length; {
		case l >= 32:
			lb = 3
		case l >= 16:
			lb = 2
		case l >= 8:
			lb = 1
		}
		if Ablate.NoRepCtx {
			rep = 0
		}
		if Ablate.NoLenCtx {
			lc, lb = 0, 0
		}
		ii := min(i, 3)
		if Ablate.NoIdxCtx {
			rep, lc, lb, ii = 0, 0, 0, 0
		}
		if io.bit(&m.idx[rep][lc][lb][ii], want) == 1 {
			got = i
			break
		}
	}
	p.lastIdx = got
	if got < n {
		c := cands[got]
		io.cat = CatPredLen
		same := 0
		if enc && length == c.length {
			same = 1
		}
		ctx := &m.lenSame[min(got, 2)][lazy]
		if io.bit(ctx, same) == 1 {
			length = c.length
		} else {
			delta := 0
			if enc {
				delta = c.length - length - 1
			}
			delta = io.gamma(m.lenUn[:], m.lenMant[:], delta)
			length = c.length - delta - 1
			if length < p.prof.HashLen || length > c.length {
				return 0, 0, corrupt("predicted length %d", length)
			}
		}
		sym, extra := lengthCode(length)
		dc, de := distCodeOf(c.dist)
		p.lastDist = c.dist
		w.afterMatch(pos, length)
		return deflate.Match(sym, extra, dc, de), length, nil
	}
	// escape: explicit coding
	if io.dec == nil {
		p.Stats.Escapes++
	}
	io.cat = CatPredEscape
	io.force = CatPredEscape + 1
	tok, ok2 := m.esc.match(io, lits, t)
	io.force = 0
	if !ok2 {
		return 0, 0, corrupt("escape match")
	}
	p.lastDist = tok.Distance()
	w.afterMatch(pos, tok.Length())
	return tok, tok.Length(), nil
}

func (p *predictor) newState(start, plainLen int) (*window, *predModel) {
	p.lastDist = 0
	return newWindow(p.plain, start, plainLen, p.prof), newPredModel(p.prof)
}

// encodeUnit encodes one unit whose first token starts at plaintext offset start.
func (p *predictor) encodeUnit(runs []Run, start, plainLen int, cost *[NumCats]float64) ([]byte, error) {
	return p.encode(runs, start, plainLen, cost)
}

func (p *predictor) encode(runs []Run, start, plainLen int, cost *[NumCats]float64) ([]byte, error) {
	w, m := p.newState(start, plainLen)
	e := newEncoder()
	io := &rcio{enc: e, cost: cost}
	pos, lits := start, 0
	for _, r := range runs {
		for i := uint32(0); i < r.Lits; i++ {
			if _, n, err := p.token(io, m, w, pos, lits, deflate.Literal(0)); err != nil {
				return nil, err
			} else {
				pos += n
			}
			lits++
			p.Stats.Tokens++
		}
		if r.M != 0 {
			tok, n, err := p.token(io, m, w, pos, lits, r.M)
			if err == nil {
				p.Stats.IdxHist[min(p.lastIdx, 7)]++
			}
			if err != nil {
				return nil, err
			}
			if tok != r.M {
				return nil, fmt.Errorf("predictor changed a token (%#x -> %#x)", uint32(r.M), uint32(tok))
			}
			pos += n
			lits = 0
			p.Stats.Tokens++
			p.Stats.Matches++
		}
	}
	p.Stats.Work += w.work
	return append([]byte{byte(Pred) | p.prof.ID<<4}, e.finish()...), nil
}

func (p *predictor) decodeUnit(data []byte, start, plainLen int) ([]Run, error) {
	if len(data) < 1 || Codec(data[0]&15) != Pred {
		return nil, corrupt("predictive unit header")
	}
	if int(data[0]>>4) >= len(Profiles) {
		return nil, corrupt("unknown predictor profile %d", data[0]>>4)
	}
	p.prof = &Profiles[data[0]>>4] // units carry their profile
	if start < 0 || start+plainLen > len(p.plain) {
		return nil, corrupt("unit outside plaintext")
	}
	w, m := p.newState(start, plainLen)
	io := &rcio{dec: newDecoder(data[1:])}
	var runs []Run
	pos, lits := start, 0
	end := start + plainLen
	for pos < end {
		tok, n, err := p.token(io, m, w, pos, lits, 0)
		if err != nil {
			return nil, err
		}
		if pos+n > end {
			return nil, corrupt("predicted token overruns its unit")
		}
		pos += n
		if tok.IsLiteral() {
			lits++
			continue
		}
		runs = append(runs, Run{Lits: uint32(lits), M: tok})
		lits = 0
	}
	if lits > 0 {
		runs = append(runs, Run{Lits: uint32(lits)})
	}
	return runs, nil
}

// ChooseProfile picks the profile with the smallest encoding of a sample of
// units (at most 8 spread across the stream, at most 64 KiB of plaintext each).
// It is deterministic: ties go to the lower profile id.
func ChooseProfile(s *deflate.Stream, pol Policy) uint8 {
	f := flatten(s)
	cs := cuts(s, f, pol)
	starts := append([]int{0}, cs...)
	ends := append(append([]int(nil), cs...), len(f.toks))
	if len(f.toks) == 0 {
		return 0
	}
	posAt, _ := tokenStarts(s, f, starts)
	step := max(1, len(starts)/8)
	best, bestSize := uint8(0), int(^uint(0)>>1)
	for _, prof := range Profiles {
		total := 0
		for u := 0; u < len(starts); u += step {
			a, b := starts[u], ends[u]
			if a >= b {
				continue
			}
			// cap very large units (stream/block policies) to a prefix of tokens
			if b-a > 40000 {
				b = a + 40000
			}
			runs, plain := toRuns(f.toks[a:b])
			pr := newPredictor(s.Plain, prof.ID)
			data, err := pr.encode(runs, posAt[u], plain, nil)
			if err != nil {
				total += plain
				continue
			}
			total += len(data)
		}
		if total < bestSize {
			best, bestSize = prof.ID, total
		}
	}
	return best
}
