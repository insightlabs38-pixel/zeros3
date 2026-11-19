package brotliz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
)

// Recipe is everything a streaming producer's output depends on besides the decoded bytes: the
// encoder parameters, how the size hint came out, the library generation, where the producer
// flushed, and whether it finished with an empty call after a full block. Replay re-runs the
// encoder from the decoded bytes; the caller compares the result with the stored stream.
type Recipe struct {
	Quality, LgWin, LgBlock int // LgBlock 0 = the encoder's default
	BigHint                 bool
	Legacy                  bool
	EmptyLast               bool
	Flushes                 []uint64 // ascending offsets in the decoded bytes (0 and the length are allowed)
}

var ErrRecipe = errors.New("brotliz3: malformed recipe")

// MaxFlushes bounds the flush list a recipe may carry.
const MaxFlushes = 1 << 16

func (r Recipe) params() Params {
	h := uint32(1)
	if r.BigHint {
		h = 1 << 20
	}
	return Params{Quality: r.Quality, LgWin: r.LgWin, LgBlock: r.LgBlock, SizeHint: h, Legacy: r.Legacy}
}

// Marshal is byte0 = quality-5 | (lgwin-17)<<3, byte1 = flags | (lgblock-15)<<4 (lgblock 0 -> 0), then the
// flush count and delta-coded offsets as uvarints.
func (r Recipe) Marshal() []byte {
	b := []byte{byte(r.Quality-5) | byte(r.LgWin-17)<<3, 0}
	if r.BigHint {
		b[1] |= 1
	}
	if r.Legacy {
		b[1] |= 2
	}
	if r.EmptyLast {
		b[1] |= 4
	}
	if r.LgBlock != 0 {
		b[1] |= byte(r.LgBlock-15) << 4
	}
	b = binary.AppendUvarint(b, uint64(len(r.Flushes)))
	prev := uint64(0)
	for _, f := range r.Flushes {
		b = binary.AppendUvarint(b, f-prev)
		prev = f
	}
	return b
}

// UnmarshalRecipe parses a recipe from the front of b and returns the bytes consumed. Offsets are
// checked against the decoded length n.
func UnmarshalRecipe(b []byte, n int64) (Recipe, int, error) {
	if len(b) < 3 {
		return Recipe{}, 0, ErrRecipe
	}
	r := Recipe{Quality: int(b[0]&7) + 5, LgWin: int(b[0]>>3) + 17, BigHint: b[1]&1 != 0, Legacy: b[1]&2 != 0, EmptyLast: b[1]&4 != 0}
	if b[1]&8 != 0 || b[0]>>6 != 0 {
		return Recipe{}, 0, ErrRecipe
	}
	if lb := int(b[1] >> 4); lb != 0 {
		r.LgBlock = lb + 15
	}
	if !(Params{Quality: r.Quality, LgWin: r.LgWin, LgBlock: r.LgBlock}).valid() {
		return Recipe{}, 0, ErrRecipe
	}
	pos := 2
	cnt, k := binary.Uvarint(b[pos:])
	if k <= 0 || cnt > MaxFlushes || cnt > uint64(len(b)) {
		return Recipe{}, 0, ErrRecipe
	}
	pos += k
	var cur uint64
	for i := uint64(0); i < cnt; i++ {
		d, k := binary.Uvarint(b[pos:])
		if k <= 0 || d > uint64(n) || cur+d > uint64(n) {
			return Recipe{}, 0, ErrRecipe
		}
		pos += k
		cur += d
		r.Flushes = append(r.Flushes, cur)
	}
	return r, pos, nil
}

// cmdProbe compares the encoder's commands with the ones decoded from the producer's stream, block by block, so a
// wrong parameter guess is rejected after one input block instead of after the whole stream.
type cmdProbe struct {
	exp     []TraceCmd
	stored  map[int]bool // start offsets of the stream's uncompressed meta-blocks (they carry no commands)
	base    int          // commands of earlier meta-blocks
	checked int          // commands of this meta-block already compared
}

func newCmdProbe(tr *Trace) *cmdProbe {
	p := &cmdProbe{exp: tr.Cmds, stored: map[int]bool{}}
	for _, b := range tr.Blocks {
		if b.Uncompressed {
			p.stored[b.Start] = true
		}
	}
	return p
}

// check compares the commands of the meta-block starting at start (decoded offset) that are final.
func (p *cmdProbe) check(cmds []command, start int) bool {
	if p.stored[start] {
		return true
	}
	for ; p.checked < len(cmds)-1; p.checked++ { // the last command may still be extended by the next block
		i := p.base + p.checked
		if i >= len(p.exp) {
			return true // beyond the recorded prefix
		}
		c := &cmds[p.checked]
		if c.insertLen != uint32(p.exp[i].Ins) || c.copyLenCode() != uint32(p.exp[i].Copy) {
			return false
		}
	}
	return true
}

func (p *cmdProbe) advance(n int) { p.base += n; p.checked = 0 }

// ReplayTo regenerates the producer's stream from body into w, holding at most the encoder window.
func (r Recipe) ReplayTo(w io.Writer, body []byte) error { return r.replay(w, body, nil) }

func (r Recipe) replay(w io.Writer, body []byte, probe *cmdProbe) error {
	st, err := NewStream(r.params(), w)
	if err != nil {
		return err
	}
	st.e.probe = probe
	defer st.Close()
	pos := uint64(0)
	for _, f := range r.Flushes {
		if f < pos || f > uint64(len(body)) {
			return ErrRecipe
		}
		if f > pos {
			if err := st.Process(body[pos:f]); err != nil {
				return err
			}
		}
		if err := st.Flush(); err != nil {
			return err
		}
		pos = f
	}
	rest := body[pos:]
	if r.EmptyLast {
		if err := st.Process(rest); err != nil {
			return err
		}
		rest = nil
	}
	return st.Write(OpFinish, rest)
}

// Replay returns the regenerated stream.
func (r Recipe) Replay(body []byte) ([]byte, error) {
	var w sliceWriter
	if err := r.ReplayTo(&w, body); err != nil {
		return nil, err
	}
	return w.b, nil
}

// errMismatch ends a replay at the first byte that differs from the stored stream.
var errMismatch = errors.New("brotliz3: replay differs")

// matchWriter compares everything written with the stored stream and fails at the first difference.
type matchWriter struct{ want []byte }

func (m *matchWriter) Write(p []byte) (int, error) {
	if len(p) > len(m.want) || !bytes.Equal(p, m.want[:len(p)]) {
		return 0, errMismatch
	}
	m.want = m.want[len(p):]
	return len(p), nil
}

// Matches reports whether the recipe reproduces payload from body (early exit on the first difference).
func (r Recipe) Matches(payload, body []byte) bool { return r.matches(payload, body, nil) }

func (r Recipe) matches(payload, body []byte, probe *cmdProbe) bool {
	m := &matchWriter{want: payload}
	return r.replay(m, body, probe) == nil && len(m.want) == 0
}

// Derive searches the recipes a streaming producer can leave behind for one that reproduces payload from its
// decoding body (tr is DecodeTrace's structure). Qualities are tried from the caller's guess first. Every
// candidate is checked by an early-exit replay, so a wrong guess costs one block of encoding.
func Derive(payload, body []byte, tr *Trace, firstQuality int) (Recipe, bool) {
	if tr == nil || tr.LgWin < 17 || tr.LgWin > 24 || len(body) >= maxStream {
		return Recipe{}, false
	}
	order := []int{8, 9, 5, 6, 7}
	if firstQuality >= 5 && firstQuality <= 9 {
		order = slices.DeleteFunc(order, func(q int) bool { return q == firstQuality })
		order = append([]int{firstQuality}, order...)
	}
	bigFirst := len(body) >= 1<<20
	for _, fl := range flushCandidates(tr, body) {
		// stage 1: the encoder's default block size; stage 2: an explicit one. Legacy ring handling only
		// matters once the data reaches the end of the ring, an empty finish only after a full block.
		for stage := 0; stage < 2; stage++ {
			for _, q := range order {
				for _, big := range []bool{bigFirst, !bigFirst} {
					for _, legacy := range []bool{false, true} {
						for _, lb := range lgBlockCandidates(q, tr.LgWin, stage) {
							ring := 1 << uint(1+max(tr.LgWin, Params{Quality: q, LgWin: tr.LgWin, LgBlock: lb}.lgBlock()))
							if legacy && len(body) <= ring-1<<uint(Params{Quality: q, LgWin: tr.LgWin, LgBlock: lb}.lgBlock()) {
								continue
							}
							for _, empty := range []bool{false, true} {
								r := Recipe{Quality: q, LgWin: tr.LgWin, LgBlock: lb, BigHint: big, Legacy: legacy, EmptyLast: empty, Flushes: fl}
								if empty && !emptyPossible(r, len(body)) {
									continue
								}
								if r.matches(payload, body, newCmdProbe(tr)) {
									return r, true
								}
							}
						}
					}
				}
			}
		}
	}
	return Recipe{}, false
}

func lgBlockCandidates(q, lgwin, stage int) []int {
	if stage == 0 {
		return []int{0}
	}
	auto := Params{Quality: q, LgWin: lgwin}.lgBlock()
	var out []int
	for lb := 16; lb <= 24; lb++ {
		if lb != auto {
			out = append(out, lb)
		}
	}
	return out
}

// emptyPossible: a finish with no new bytes ends a stream only after a full, already encoded block.
func emptyPossible(r Recipe, n int) bool {
	last := uint64(0)
	if len(r.Flushes) > 0 {
		last = r.Flushes[len(r.Flushes)-1]
	}
	block := uint64(1) << uint(Params{Quality: r.Quality, LgWin: r.LgWin, LgBlock: r.LgBlock}.lgBlock())
	return uint64(n) > last && (uint64(n)-last)%block == 0
}

// flushCandidates lists the flush sets worth trying: the padding markers plus the meta-block ends no
// natural block boundary explains; then the same with every remaining boundary forced.
func flushCandidates(tr *Trace, body []byte) [][]uint64 {
	seen := map[int]bool{}
	var base []uint64
	for _, m := range tr.Markers {
		if !seen[m] {
			seen[m] = true
			base = append(base, uint64(m))
		}
	}
	var natural []uint64
	for _, b := range tr.Blocks {
		if b.Last || seen[b.End] || b.End >= len(body) {
			continue
		}
		if b.End%65536 != 0 { // not a multiple of the smallest input block: only a flush ends a meta-block here
			seen[b.End] = true
			base = append(base, uint64(b.End))
		} else {
			natural = append(natural, uint64(b.End))
		}
	}
	slices.Sort(base)
	sets := [][]uint64{base}
	if len(natural) > 0 && len(natural) <= 64 {
		all := append(slices.Clone(base), natural...)
		slices.Sort(all)
		sets = append(sets, all)
	}
	return sets
}
