package ir

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/deflate"
)

// Range materialization (Z3-08G). A future range GET on a transformed object
// needs bytes [a, b) of the original compressed stream without rebuilding the
// prefix. DEFLATE has no random access, but this representation does: every
// unit is a self-contained token run, so the only extra information needed is
// *where in the compressed output each unit starts*. RangeIndex records that.
// The materializer here is a laboratory proof, not a production reader.

// RangeIndex holds the compressed bit offset at which each unit's first token
// is written (after any block header that precedes it).
type RangeIndex struct {
	UnitBits []int64
}

// Marshal encodes the index as delta varints.
func (ix *RangeIndex) Marshal() []byte {
	var b []byte
	prev := int64(0)
	for _, v := range ix.UnitBits {
		b = binary.AppendUvarint(b, uint64(v-prev))
		prev = v
	}
	return b
}

// ParseRangeIndex decodes n offsets.
func ParseRangeIndex(b []byte, n int) (*RangeIndex, error) {
	ix := &RangeIndex{UnitBits: make([]int64, 0, n)}
	prev := int64(0)
	for i := 0; i < n; i++ {
		v, k := binary.Uvarint(b)
		if k <= 0 {
			return nil, corrupt("range index truncated")
		}
		b = b[k:]
		prev += int64(v)
		ix.UnitBits = append(ix.UnitBits, prev)
	}
	if len(b) != 0 {
		return nil, corrupt("range index has trailing bytes")
	}
	return ix, nil
}

// Checkpoints keeps every step-th offset only (a sparser index; the
// materializer then decodes more units before the range).
func (ix *RangeIndex) Checkpoints(step int) *RangeIndex {
	out := &RangeIndex{}
	for i := 0; i < len(ix.UnitBits); i += step {
		out.UnitBits = append(out.UnitBits, ix.UnitBits[i])
	}
	return out
}

// RangeNeeds reports what one range materialization had to touch.
type RangeNeeds struct {
	FirstUnit     int   // unit decoding started at
	UnitsDecoded  int   // units decoded (including look-ahead for predicted headers)
	UnitBytes     int   // encoded bytes of those units
	PlainBytes    int64 // distinct semantic plaintext bytes fetched: warm-up windows, units and stored payloads
	SkeletonBytes int   // the whole skeleton is read here (splittable by block range, see the report)
	RangeUnits    int   // units whose tokens are written into the range
	HeaderUnits   int   // units decoded only to predict a dynamic header (UnitsDecoded - RangeUnits)
	OutBytes      int   // compressed bytes produced, including the discarded lead-in
}

// MaterializeRange rebuilds bytes [a, b) of the compressed stream. plainAt
// returns the semantic plaintext [from, to) (a ring or a store read); unit(i)
// returns unit i. ix lists the offsets of every step-th unit (step 1 = all).
func MaterializeRange(skel []byte, ix *RangeIndex, step int, unit func(i int) ([]byte, error),
	plainAt func(from, to int64) ([]byte, error), a, b int64) ([]byte, RangeNeeds, error) {
	var need RangeNeeds
	need.SkeletonBytes = len(skel)
	sk, err := ParseSkeleton(skel, 1<<40)
	if err != nil {
		return nil, need, err
	}
	if sk.Codec != Pred {
		return nil, need, corrupt("range materialization supports the predictive codec only")
	}
	if a < 0 || b < a || b > int64(sk.StreamLen) {
		return nil, need, fmt.Errorf("range [%d,%d) outside the %d byte stream", a, b, sk.StreamLen)
	}
	nu := len(sk.Units)
	startPos := make([]int64, nu) // plaintext offset of each unit
	tokStart := make([]int, nu)   // global token index of each unit's first token
	storedPos := map[int]int64{}  // plaintext offset of each stored block
	blockTok := make([]int, len(sk.Blocks)+1)
	{
		for i := range sk.Blocks {
			blockTok[i+1] = blockTok[i] + sk.Blocks[i].NTokens
		}
		var acc int64
		next := 0 // next block to examine for stored placement
		ti := 0
		place := func(limit int) {
			for next < len(sk.Blocks) && blockTok[next] <= limit {
				if sk.Blocks[next].Kind == deflate.Stored {
					storedPos[next] = acc
					acc += int64(sk.Blocks[next].StoredLen)
				}
				next++
			}
		}
		for k, u := range sk.Units {
			place(ti)
			startPos[k], tokStart[k] = acc, ti
			acc += int64(u.PlainLen)
			ti += u.NTokens
		}
		place(1 << 60)
	}
	// starting unit: the last indexed unit whose first byte lies before a
	u0 := -1
	if a > 0 {
		if j := sort.Search(len(ix.UnitBits), func(i int) bool { return ix.UnitBits[i]/8 >= a }) - 1; j >= 0 {
			u0 = j * step
		}
	}
	var startBit int64
	g := 0 // global token index decoding starts at
	if u0 >= 0 {
		startBit, g = ix.UnitBits[u0/step], tokStart[u0]
		need.FirstUnit = u0
	}
	// block containing token g
	bi := 0
	for u0 >= 0 && bi < len(sk.Blocks) && (sk.Blocks[bi].Kind == deflate.Stored || blockTok[bi+1] <= g) {
		bi++
	}
	if u0 >= 0 && bi == len(sk.Blocks) {
		return nil, need, corrupt("unit %d starts after the last block", u0)
	}
	bi0 := bi

	// unit token cache (small LRU)
	type cached struct {
		k    int
		toks []deflate.Token
	}
	var cache []cached
	var fetched [][2]int64
	tokensOf := func(k int) ([]deflate.Token, error) {
		for _, c := range cache {
			if c.k == k {
				return c.toks, nil
			}
		}
		u := sk.Units[k]
		wbase := max(0, startPos[k]-warmMax)
		pl, err := plainAt(wbase, startPos[k]+int64(u.PlainLen))
		if err != nil {
			return nil, err
		}
		if int64(len(pl)) != startPos[k]+int64(u.PlainLen)-wbase {
			return nil, corrupt("plaintext fetch returned %d bytes", len(pl))
		}
		fetched = append(fetched, [2]int64{wbase, wbase + int64(len(pl))})
		data, err := unit(k)
		if err != nil {
			return nil, err
		}
		need.UnitBytes += len(data)
		need.UnitsDecoded++
		runs, err := newPredictor(pl, 0).decodeUnit(data, int(startPos[k]-wbase), u.PlainLen)
		if err != nil {
			return nil, fmt.Errorf("unit %d: %w", k, err)
		}
		toks := make([]deflate.Token, 0, u.NTokens)
		pos := int(startPos[k] - wbase)
		for _, r := range runs {
			for j := uint32(0); j < r.Lits; j++ {
				toks = append(toks, deflate.Literal(pl[pos]))
				pos++
			}
			if r.M != 0 {
				toks = append(toks, r.M)
				pos += r.M.Length()
			}
		}
		if len(toks) != u.NTokens {
			return nil, corrupt("unit %d decodes to %d tokens, index says %d", k, len(toks), u.NTokens)
		}
		cache = append(cache, cached{k, toks})
		if len(cache) > 128 {
			cache = cache[1:]
		}
		return toks, nil
	}
	unitOf := func(tok int) int { return sort.Search(nu, func(i int) bool { return tokStart[i] > tok }) - 1 }
	tokenAt := func(tok int) (deflate.Token, error) {
		k := unitOf(tok)
		if k < 0 || k >= nu {
			return 0, corrupt("token %d has no unit", tok)
		}
		ts, err := tokensOf(k)
		if err != nil {
			return 0, err
		}
		return ts[tok-tokStart[k]], nil
	}

	emitted := map[int]bool{}
	var out bytes.Buffer
	w := deflate.NewStreamWriterAt(&out, startBit)
	done := func() bool { return w.BitPos() >= 8*b }
	for ; bi < len(sk.Blocks) && !done(); bi++ {
		m := &sk.Blocks[bi]
		blk := deflate.Block{Final: m.Final, Kind: m.Kind}
		if m.Kind == deflate.Stored {
			blk.PadBits, blk.PadLen, blk.PlainLen, blk.AutoPad = m.PadBits, m.PadLen, m.StoredLen, m.AutoPad
			from := storedPos[bi]
			data, err := plainAt(from, from+int64(m.StoredLen))
			if err != nil {
				return nil, need, err
			}
			fetched = append(fetched, [2]int64{from, from + int64(len(data))})
			if err := w.Begin(&blk); err != nil {
				return nil, need, err
			}
			if err := w.Stored(&blk, data); err != nil {
				return nil, need, err
			}
			continue
		}
		if m.Kind == deflate.Dynamic && !m.Pred {
			blk.HLIT, blk.HDIST, blk.HCLEN, blk.CLLens, blk.CLSyms = m.HLIT, m.HDIST, m.HCLEN, m.CLLens, m.CLSyms
			if blk.LitLens, blk.DistLens = ExpandLengths(m); blk.LitLens == nil {
				return nil, need, corrupt("block %d code lengths", bi)
			}
		}
		if m.Pred { // every token of the block is needed before its header is known
			toks := make([]deflate.Token, 0, m.NTokens)
			for t := blockTok[bi]; t < blockTok[bi+1]; t++ {
				tk, err := tokenAt(t)
				if err != nil {
					return nil, need, err
				}
				toks = append(toks, tk)
			}
			plainLen := 0
			for _, tk := range toks {
				plainLen += tokenPlain(tk)
			}
			kind, pm, ok := zlibBlock(toks, plainLen)
			if !ok || kind != deflate.Dynamic {
				return nil, need, corrupt("block %d: predicted header is not dynamic", bi)
			}
			blk.HLIT, blk.HDIST, blk.HCLEN, blk.CLLens, blk.CLSyms = pm.HLIT, pm.HDIST, pm.HCLEN, pm.CLLens, pm.CLSyms
			if blk.LitLens, blk.DistLens = ExpandLengths(&pm); blk.LitLens == nil {
				return nil, need, corrupt("block %d: predicted code lengths", bi)
			}
		}
		first := blockTok[bi]
		if u0 >= 0 && bi == bi0 {
			// the range starts inside this block: its header lies before startBit
			first = g
			if err := w.Resume(&blk); err != nil {
				return nil, need, err
			}
		} else if err := w.Begin(&blk); err != nil {
			return nil, need, err
		}
		for t := first; t < blockTok[bi+1]; t++ {
			emitted[unitOf(t)] = true
			tk, err := tokenAt(t)
			if err != nil {
				return nil, need, err
			}
			if err := w.Token(tk); err != nil {
				return nil, need, err
			}
			if done() {
				break
			}
		}
		if done() {
			break
		}
		if err := w.End(); err != nil {
			return nil, need, err
		}
	}
	if bi >= len(sk.Blocks) && !done() {
		if err := w.Finish(sk.TailPadBits, sk.TailPadLen); err != nil {
			return nil, need, err
		}
	}
	if err := w.Drain(); err != nil {
		return nil, need, err
	}
	need.OutBytes = out.Len()
	need.RangeUnits = len(emitted)
	need.HeaderUnits = need.UnitsDecoded - need.RangeUnits
	sort.Slice(fetched, func(i, j int) bool { return fetched[i][0] < fetched[j][0] })
	hi := int64(-1)
	for _, iv := range fetched { // union of the fetched intervals
		if iv[1] <= hi {
			continue
		}
		need.PlainBytes += iv[1] - max(iv[0], hi)
		hi = iv[1]
	}
	base := startBit / 8
	if b > base+int64(out.Len()) {
		return nil, need, corrupt("range ends at %d but only %d bytes were produced from %d", b, base+int64(out.Len()), base)
	}
	return append([]byte(nil), out.Bytes()[a-base:b-base]...), need, nil
}
