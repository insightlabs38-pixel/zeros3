package ir

import (
	"bytes"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"zeros3-z3/deflate"
)

// AutoProfile in Config.Profile selects the predictor profile per stream.
const AutoProfile = 255

// Config selects how a parsed stream is turned into a Package.
type Config struct {
	Codec   Codec
	Policy  Policy
	Profile uint8 // predictor profile (Codec == Pred)
	// Cost, if non-nil, accumulates ideal code lengths (bits) per category for
	// range-coded codecs.
	Cost *[NumCats]float64
}

// Package is the encoded reconstruction IR of one DEFLATE stream.
type Package struct {
	Skel     *Skeleton
	Skeleton []byte   // Skel.Marshal()
	Units    [][]byte // encoded units, in stream order

	// Diagnostics (not part of the representation).
	Tokens, Matches, Literals int
	BlockBytes                [][]byte // encoded block table entries (coupling studies)
	UnitBlocks                [][2]int // first and last block index whose tokens a unit holds
	UnitMaxDist               []int    // largest match distance used by each unit
	UnitPlainStart            []int
	Pred                      PredStats
}

// TotalUnitBytes sums the encoded units.
func (p *Package) TotalUnitBytes() int {
	n := 0
	for _, u := range p.Units {
		n += len(u)
	}
	return n
}

type crcCounter struct {
	h hash.Hash32
	n int
}

func (c *crcCounter) Write(b []byte) (int, error) { c.n += len(b); return c.h.Write(b) }

// tokenStarts returns the plaintext offset of the first token of each global
// token index in idxs (ascending), plus its block index.
func tokenStarts(s *deflate.Stream, f *flat, idxs []int) (pos, block []int) {
	pos, block = make([]int, len(idxs)), make([]int, len(idxs))
	k := 0
	g := 0 // global token index
	for bi := range s.Blocks {
		b := &s.Blocks[bi]
		p := b.PlainOff
		for _, t := range b.Tokens {
			for k < len(idxs) && idxs[k] == g {
				pos[k], block[k] = p, bi
				k++
			}
			p += tokenPlain(t)
			g++
		}
	}
	return
}

// Encode builds the skeleton and units of a parsed stream.
func Encode(s *deflate.Stream, cfg Config) (*Package, error) {
	f := flatten(s)
	cs := cuts(s, f, cfg.Policy)
	starts := append([]int{0}, cs...)
	var posAt, blockAt []int
	if len(f.toks) > 0 {
		posAt, blockAt = tokenStarts(s, f, starts)
	}
	ends := append(append([]int(nil), cs...), len(f.toks))
	sk := &Skeleton{Codec: cfg.Codec, Profile: cfg.Profile, TailPadBits: s.TailPadBits, TailPadLen: s.TailPadLen}
	pk := &Package{Skel: sk}
	for i := range s.Blocks {
		m := blockMeta(&s.Blocks[i])
		sk.Blocks = append(sk.Blocks, m)
		pk.BlockBytes = append(pk.BlockBytes, encodeBlockMeta(nil, &m))
	}
	var pred *predictor
	if cfg.Codec == Pred {
		if cfg.Profile == AutoProfile {
			sk.Profile = ChooseProfile(s, cfg.Policy)
		}
		pred = newPredictor(s.Plain, sk.Profile)
	}
	if len(f.toks) > 0 {
		for u := range starts {
			a, b := starts[u], ends[u]
			if a >= b {
				continue
			}
			runs, plain := toRuns(f.toks[a:b])
			var data []byte
			switch cfg.Codec {
			case E1:
				data = encodeE1(runs)
			case E1Flate:
				data = encodeE1Flate(runs)
			case E2:
				data = encodeE2(runs, cfg.Cost)
			case Pred:
				var err error
				if data, err = pred.encodeUnit(runs, posAt[u], plain, cfg.Cost); err != nil {
					return nil, fmt.Errorf("ir: predictive unit %d: %w", u, err)
				}
			default:
				return nil, fmt.Errorf("ir: unknown codec %d", cfg.Codec)
			}
			md := 0
			for _, t := range f.toks[a:b] {
				if !t.IsLiteral() {
					md = max(md, t.Distance())
					pk.Matches++
				}
			}
			pk.Tokens += b - a
			sk.Units = append(sk.Units, UnitIdx{PlainLen: plain, NTokens: b - a})
			pk.Units = append(pk.Units, data)
			pk.UnitMaxDist = append(pk.UnitMaxDist, md)
			pk.UnitPlainStart = append(pk.UnitPlainStart, posAt[u])
			// last block holding a token of the unit
			last := blockAt[u]
			for last+1 < len(s.Blocks) && f.blockTok[last+1] < b {
				last++
			}
			pk.UnitBlocks = append(pk.UnitBlocks, [2]int{blockAt[u], last})
		}
	}
	pk.Literals = pk.Tokens - pk.Matches
	if pred != nil {
		pk.Pred = pred.Stats
	}
	cc := &crcCounter{h: crc32.NewIEEE()}
	if err := s.Replay(cc); err != nil {
		return nil, fmt.Errorf("ir: replay while encoding: %w", err)
	}
	sk.StreamLen, sk.CRC = cc.n, cc.h.Sum32()
	pk.Skeleton = sk.Marshal()
	return pk, nil
}

// DecodeOptions control decoding.
type DecodeOptions struct {
	// VerifyMatches additionally checks that every match copies equal bytes in
	// the supplied plaintext (needs up to 32 KiB of plaintext before each unit).
	VerifyMatches bool
}

// Decode rebuilds the in-memory Stream from the skeleton, the units and the
// semantic plaintext. unit(i) returns the i-th unit's bytes.
func Decode(skel []byte, unit func(i int) ([]byte, error), plain []byte, opt DecodeOptions) (*deflate.Stream, *Skeleton, error) {
	sk, err := ParseSkeleton(skel, len(plain))
	if err != nil {
		return nil, nil, err
	}
	// plaintext accounting: units + stored blocks must tile the plaintext
	storedTotal, tok := 0, 0
	type stored struct{ atTok, n int }
	var sts []stored
	for i := range sk.Blocks {
		if sk.Blocks[i].Kind == deflate.Stored {
			storedTotal += sk.Blocks[i].StoredLen
			sts = append(sts, stored{tok, sk.Blocks[i].StoredLen})
		}
		tok += sk.Blocks[i].NTokens
	}
	unitPlain := 0
	for _, u := range sk.Units {
		unitPlain += u.PlainLen
	}
	if unitPlain+storedTotal != len(plain) {
		return nil, nil, corrupt("units+stored cover %d plaintext bytes, have %d", unitPlain+storedTotal, len(plain))
	}
	// unit start offsets and stored-block placement
	startPos := make([]int, len(sk.Units))
	{
		acc, si, ti := 0, 0, 0
		for k, u := range sk.Units {
			for si < len(sts) && sts[si].atTok <= ti {
				acc += sts[si].n
				si++
			}
			if si < len(sts) && sts[si].atTok < ti+u.NTokens {
				return nil, nil, corrupt("unit %d spans a stored block", k)
			}
			startPos[k] = acc
			acc += u.PlainLen
			ti += u.NTokens
		}
	}
	pr := newPredictor(plain, 0)
	units := make([][]Run, len(sk.Units))
	for k, u := range sk.Units {
		data, err := unit(k)
		if err != nil {
			return nil, nil, err
		}
		var runs []Run
		switch sk.Codec {
		case E1:
			runs, err = decodeE1(data, u.PlainLen)
		case E1Flate:
			runs, err = decodeE1Flate(data, u.PlainLen)
		case E2:
			runs, err = decodeE2(data, u.PlainLen)
		case Pred:
			runs, err = pr.decodeUnit(data, startPos[k], u.PlainLen)
		default:
			err = corrupt("unknown codec %d", sk.Codec)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("unit %d: %w", k, err)
		}
		n, p := 0, 0
		for _, r := range runs {
			n += r.tokens()
			p += r.plainLen()
		}
		if n != u.NTokens || p != u.PlainLen {
			return nil, nil, corrupt("unit %d decodes to %d tokens / %d bytes, index says %d / %d", k, n, p, u.NTokens, u.PlainLen)
		}
		units[k] = runs
	}
	// assemble blocks
	st := &deflate.Stream{Plain: plain, TailPadBits: sk.TailPadBits, TailPadLen: sk.TailPadLen}
	it := runIter{units: units, plain: plain}
	pos := 0
	for bi := range sk.Blocks {
		m := &sk.Blocks[bi]
		b := deflate.Block{Final: m.Final, Kind: m.Kind, PlainOff: pos}
		switch m.Kind {
		case deflate.Stored:
			b.PadBits, b.PadLen, b.PlainLen = m.PadBits, m.PadLen, m.StoredLen
			pos += m.StoredLen
		default:
			if m.Kind == deflate.Dynamic {
				b.HLIT, b.HDIST, b.HCLEN, b.CLLens = m.HLIT, m.HDIST, m.HCLEN, m.CLLens
				b.CLSyms = m.CLSyms
				b.LitLens, b.DistLens = ExpandLengths(m)
				if b.LitLens == nil {
					return nil, nil, corrupt("block %d code lengths", bi)
				}
			}
			b.Tokens = make([]deflate.Token, 0, m.NTokens)
			for j := 0; j < m.NTokens; j++ {
				t, n, err := it.next(pos, opt.VerifyMatches)
				if err != nil {
					return nil, nil, fmt.Errorf("block %d token %d: %w", bi, j, err)
				}
				b.Tokens = append(b.Tokens, t)
				pos += n
			}
			b.PlainLen = pos - b.PlainOff
		}
		st.Blocks = append(st.Blocks, b)
	}
	if pos != len(plain) {
		return nil, nil, corrupt("blocks cover %d plaintext bytes, have %d", pos, len(plain))
	}
	return st, sk, nil
}

// Reconstruct decodes and replays, then checks the stream length and CRC-32
// recorded in the skeleton before writing a single byte to w.
func Reconstruct(w io.Writer, skel []byte, unit func(i int) ([]byte, error), plain []byte, opt DecodeOptions) error {
	st, sk, err := Decode(skel, unit, plain, opt)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := st.Replay(&buf); err != nil {
		return err
	}
	if buf.Len() != sk.StreamLen || streamCRC(buf.Bytes()) != sk.CRC {
		return corrupt("replayed stream is %d bytes crc %08x, skeleton says %d crc %08x", buf.Len(), streamCRC(buf.Bytes()), sk.StreamLen, sk.CRC)
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// runIter expands unit runs into tokens, producing literal values from the plaintext.
type runIter struct {
	units [][]Run
	u, r  int
	lits  uint32 // literals already consumed in the current run
	plain []byte
}

// next returns the next token located at plaintext offset pos and its plaintext length.
func (it *runIter) next(pos int, verify bool) (deflate.Token, int, error) {
	for it.u < len(it.units) && it.r >= len(it.units[it.u]) {
		it.u, it.r, it.lits = it.u+1, 0, 0
	}
	if it.u >= len(it.units) {
		return 0, 0, corrupt("token stream exhausted")
	}
	run := it.units[it.u][it.r]
	if it.lits < run.Lits {
		if pos >= len(it.plain) {
			return 0, 0, corrupt("literal beyond plaintext")
		}
		it.lits++
		return deflate.Literal(it.plain[pos]), 1, nil
	}
	if run.M == 0 {
		it.r, it.lits = it.r+1, 0
		return it.next(pos, verify)
	}
	t := run.M
	it.r, it.lits = it.r+1, 0
	d, n := t.Distance(), t.Length()
	if d > pos || pos+n > len(it.plain) {
		return 0, 0, corrupt("match distance %d length %d at %d", d, n, pos)
	}
	if verify {
		for i := 0; i < n; i++ {
			if it.plain[pos-d+i] != it.plain[pos+i] {
				return 0, 0, corrupt("match at %d does not copy plaintext", pos)
			}
		}
	}
	return t, n, nil
}
