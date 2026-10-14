// Package ir is the Z3-06/07 research layer on top of the native exact DEFLATE
// parser: a compact explicit reconstruction IR split into
//
//	skeleton  per-stream block table (BFINAL/BTYPE, exact dynamic headers,
//	          stored padding, tail padding), a unit index and a stream CRC32
//	units     content-addressable runs of the literal/match token sequence,
//	          without literal byte values (those live in the semantic plaintext)
//
// Units never cut the DEFLATE bitstream; they cut the *metadata*. Because the
// bits of a block are a function of its header and tokens, a revision's
// bitstream is rebuilt exactly from skeleton + units + plaintext, and every
// unit can be stored, deduplicated and fetched independently. None of this is
// a persistent format; it is laboratory code.
package ir

import (
	"errors"
	"fmt"
	"hash/crc32"

	"zeros3-z3/deflate"
)

var (
	ErrCorrupt = errors.New("ir: corrupt representation")
	ErrLimit   = errors.New("ir: limit exceeded")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(f, a...))
}

// Run is a (possibly empty) literal run followed by an optional match.
// M == 0 means no match follows (only allowed at the end of a unit).
type Run struct {
	Lits uint32
	M    deflate.Token
}

func (r Run) tokens() int {
	if r.M != 0 {
		return int(r.Lits) + 1
	}
	return int(r.Lits)
}

func (r Run) plainLen() int {
	if r.M != 0 {
		return int(r.Lits) + r.M.Length()
	}
	return int(r.Lits)
}

// BlockMeta is the exact per-block reconstruction metadata (no tokens).
type BlockMeta struct {
	Final bool
	Kind  deflate.BlockKind

	PadBits, PadLen uint8 // stored: padding after the 3-bit header
	StoredLen       int   // stored: payload bytes

	HLIT, HDIST, HCLEN uint8
	CLLens             [19]uint8 // by symbol
	CLSyms             []deflate.CLSym

	NTokens int // fixed/dynamic

	// Pred (dynamic): kind and header are not stored, they are recomputed from
	// the block's tokens by zlibBlock. AutoPad (stored): zero padding bits, so
	// PadLen is implied by the bit position.
	Pred, AutoPad bool
}

// UnitIdx is the skeleton's index entry for one unit.
type UnitIdx struct {
	PlainLen int // bytes of plaintext the unit's tokens produce
	NTokens  int
}

// Skeleton is everything except the unit contents.
type Skeleton struct {
	Codec   Codec
	Profile uint8
	Blocks  []BlockMeta
	// Tail padding bits after the final block.
	TailPadBits, TailPadLen uint8
	Units                   []UnitIdx
	StreamLen               int    // bytes of the replayed DEFLATE stream
	CRC                     uint32 // CRC-32 (IEEE) of the replayed stream
}

// Codec identifies a unit representation.
type Codec uint8

const (
	E1      Codec = 1 // compact structural, byte oriented
	E1Flate Codec = 2 // E1 bytes through compress/flate
	E2      Codec = 3 // range-coded structural
	Pred    Codec = 4 // predictor + corrections (predict.go)
)

func (c Codec) String() string {
	switch c {
	case E1:
		return "E1"
	case E1Flate:
		return "E1+flate"
	case E2:
		return "E2"
	case Pred:
		return "P"
	}
	return fmt.Sprintf("codec%d", uint8(c))
}

// blockMeta extracts the metadata of one parsed block.
func blockMeta(b *deflate.Block) BlockMeta {
	m := BlockMeta{Final: b.Final, Kind: b.Kind}
	switch b.Kind {
	case deflate.Stored:
		m.PadBits, m.PadLen, m.StoredLen = b.PadBits, b.PadLen, b.PlainLen
	case deflate.Dynamic:
		m.HLIT, m.HDIST, m.HCLEN, m.CLLens = b.HLIT, b.HDIST, b.HCLEN, b.CLLens
		m.CLSyms = append([]deflate.CLSym(nil), b.CLSyms...)
		m.NTokens = len(b.Tokens)
	default:
		m.NTokens = len(b.Tokens)
	}
	return m
}

// flat is the token sequence of a parsed stream with block bookkeeping.
type flat struct {
	toks     []deflate.Token
	blockTok []int // global token index at which each block's tokens start
	stored   []bool
	plainAt  []int // plaintext offset at which each block starts
}

func flatten(s *deflate.Stream) *flat {
	f := &flat{}
	n := 0
	for i := range s.Blocks {
		n += len(s.Blocks[i].Tokens)
	}
	f.toks = make([]deflate.Token, 0, n)
	for i := range s.Blocks {
		b := &s.Blocks[i]
		f.blockTok = append(f.blockTok, len(f.toks))
		f.stored = append(f.stored, b.Kind == deflate.Stored)
		f.plainAt = append(f.plainAt, b.PlainOff)
		f.toks = append(f.toks, b.Tokens...)
	}
	return f
}

func tokenPlain(t deflate.Token) int {
	if t.IsLiteral() {
		return 1
	}
	return t.Length()
}

// toRuns folds tokens into maximal runs.
func toRuns(toks []deflate.Token) (runs []Run, plain int) {
	var lits uint32
	for _, t := range toks {
		if t.IsLiteral() {
			lits++
			plain++
			continue
		}
		runs = append(runs, Run{Lits: lits, M: t})
		plain += t.Length()
		lits = 0
	}
	if lits > 0 {
		runs = append(runs, Run{Lits: lits})
	}
	return
}

func streamCRC(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
