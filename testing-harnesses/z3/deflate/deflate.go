// Package deflate is an exact RFC1951 parser and replayer for the Z3 research
// lab. Parse turns a raw DEFLATE stream into an explicit in-memory
// representation (block structure, dynamic-header encoding choices, token
// stream with the raw code/extra-bit values, padding bits) plus the decoded
// plaintext. Replay regenerates the original bytes bit for bit:
//
//	replay(parse(x)) == x
//
// This is a correctness foundation, not a compact serialization: the in-memory
// form is for inspection and for deriving statistics. Everything is stdlib only.
//
// Acceptance follows Go's compress/flate, the independent differential oracle:
// Huffman codes must be complete or the degenerate single length-1 code, a
// dynamic block must give the end-of-block symbol a code, HLIT <= 286 symbols
// and HDIST <= 30 symbols are enforced, length symbol 284 with extra value 31
// (length 258) is accepted, and streams start with an empty window (no preset
// dictionary). A stored block must have NLEN == ^LEN.
package deflate

import (
	"errors"
	"fmt"
)

// Errors returned by Parse (wrapped with a bit offset where known).
var (
	ErrTruncated   = errors.New("deflate: truncated stream")
	ErrCorrupt     = errors.New("deflate: corrupt stream")
	ErrTrailing    = errors.New("deflate: trailing data after final block")
	ErrLimit       = errors.New("deflate: size limit exceeded")
	ErrInvalidIR   = errors.New("deflate: invalid representation")
	ErrChecksum    = errors.New("deflate: checksum mismatch")
	ErrUnsupported = errors.New("deflate: unsupported")
)

func corrupt(bit int64, why string) error {
	return fmt.Errorf("%w: %s at bit %d", ErrCorrupt, why, bit)
}

// Options bound a parse. Zero fields take the defaults below.
type Options struct {
	MaxOutput     int  // decoded plaintext bytes (default 64 MiB)
	MaxTokens     int  // literal/match tokens (default 32 Mi, 128 MiB of tokens)
	MaxBlocks     int  // blocks (default 256 Ki)
	AllowTrailing bool // permit input bytes after the final block
}

const (
	DefaultMaxOutput = 64 << 20
	DefaultMaxTokens = 32 << 20
	DefaultMaxBlocks = 256 << 10
)

func (o Options) norm() Options {
	if o.MaxOutput <= 0 {
		o.MaxOutput = DefaultMaxOutput
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	if o.MaxBlocks <= 0 {
		o.MaxBlocks = DefaultMaxBlocks
	}
	return o
}

// BlockKind is the BTYPE field.
type BlockKind uint8

const (
	Stored  BlockKind = 0
	Fixed   BlockKind = 1
	Dynamic BlockKind = 2
)

func (k BlockKind) String() string {
	switch k {
	case Stored:
		return "stored"
	case Fixed:
		return "fixed"
	case Dynamic:
		return "dynamic"
	}
	return "invalid"
}

// Token is one literal or length/distance pair, holding the raw symbol and
// extra-bit values so that non-canonical choices (for example length 258 as
// symbol 284 + extra 31 instead of symbol 285) survive replay.
//
//	bits  0..8   literal/length symbol (0..255 literal, 257..285 length)
//	bits  9..13  length extra-bit value
//	bits 14..18  distance code
//	bits 19..31  distance extra-bit value
type Token uint32

// Literal returns the token for byte b.
func Literal(b byte) Token { return Token(b) }

// Match returns a length/distance token from raw symbol and extra values.
func Match(lenSym int, lenExtra uint32, distCode int, distExtra uint32) Token {
	return Token(uint32(lenSym) | lenExtra<<9 | uint32(distCode)<<14 | distExtra<<19)
}

func (t Token) Sym() int          { return int(t & 0x1ff) }
func (t Token) IsLiteral() bool   { return t&0x1ff < 256 }
func (t Token) LenExtra() uint32  { return uint32(t>>9) & 31 }
func (t Token) DistCode() int     { return int(t>>14) & 31 }
func (t Token) DistExtra() uint32 { return uint32(t >> 19) }

// Length is the match length (valid for match tokens).
func (t Token) Length() int { return lenBase[t.Sym()-257] + int(t.LenExtra()) }

// Distance is the match distance (valid for match tokens).
func (t Token) Distance() int { return distBase[t.DistCode()] + int(t.DistExtra()) }

var (
	lenBase  = [29]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	lenExtra = [29]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	distBase = [30]int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	distExtr = [30]uint8{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
	// clOrder is the order in which code-length code lengths are transmitted.
	clOrder = [19]uint8{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}
)

// CLSym is one symbol of the encoded dynamic-header code-length sequence:
// 0..15 a literal length, 16 repeat previous (2 extra bits, 3..6), 17 repeat
// zero (3 extra bits, 3..10), 18 repeat zero (7 extra bits, 11..138).
type CLSym struct {
	Sym   uint8
	Extra uint8
}

// Block is one DEFLATE block with everything needed to reproduce its bits.
type Block struct {
	Final bool
	Kind  BlockKind

	// Plaintext span produced by this block.
	PlainOff, PlainLen int
	// Bit span in the compressed stream, header through end-of-block.
	StartBit, EndBit int64

	// Stored: the padding bits between the 3-bit header and the byte boundary.
	PadBits uint8
	PadLen  uint8
	// AutoPad: zero padding whose length Replay derives from the bit position
	// (PadBits/PadLen are ignored). Set only by reconstruction layers.
	AutoPad bool

	// Dynamic header, as encoded.
	HLIT, HDIST, HCLEN uint8     // raw field values
	CLLens             [19]uint8 // code-length code lengths indexed by symbol; first HCLEN+4 of clOrder are transmitted
	CLSyms             []CLSym   // the exact code-length symbol sequence
	LitLens, DistLens  []uint8   // resulting lengths (HLIT+257, HDIST+1 entries)
	HeaderBits         int       // bits from after BTYPE to the first token

	// Fixed and dynamic: tokens in order; the end-of-block symbol is implicit.
	Tokens []Token
}

// Stream is a parsed raw DEFLATE stream.
type Stream struct {
	Blocks []Block
	Plain  []byte
	// Consumed is the number of input bytes covered by the stream (through the
	// byte holding the final bit).
	Consumed int
	// TailPadBits/TailPadLen are the padding bits after the final block.
	TailPadBits, TailPadLen uint8
}

func (b *Block) String() string {
	return fmt.Sprintf("%s final=%v plain=%d tokens=%d bits=%d", b.Kind, b.Final, b.PlainLen, len(b.Tokens), b.EndBit-b.StartBit)
}
