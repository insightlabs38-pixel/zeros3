// Package zstdz3 is the Z3-25 exact Zstandard (RFC 8878) parse/replay substrate. It is a
// laboratory object, not a ZeroS3 production format: the serialized reconstruction
// description is experimental and carries only a local version byte.
//
// Parse decodes dictionary-free Zstandard frames with a stdlib-only decoder and, while
// doing so, records the compression decisions the entropy coders made:
//
//	semantic plaintext (written progressively to a Writer, never stored in the description)
//	+ skeleton   frame headers, block types, literal modes, verbatim Huffman and FSE table
//	             descriptions, residual final-state ranks (small, canonical, sequential)
//	+ units      one per block that has sequences: the (literal length, match length,
//	             offset value) decisions as varints; one per fallback block: its original bytes
//
// Replay rebuilds the original bytes from the skeleton, the units and a sequential
// plaintext Reader. The Huffman literal streams and the FSE sequence bitstream of a
// transformed block are re-encoded from the plaintext and the decisions; they are never
// retained. A block whose replay does not equal the original (or that cannot be parsed
// for replay) is a FALLBACK block: its original compressed bytes become a unit and are
// charged as such. Stats reports transformed versus fallback bytes so nothing hides.
//
// The package imports only the standard library and no laboratory package.
package zstdz3

import (
	"errors"
	"fmt"
)

var (
	// ErrCorrupt reports a stream that violates the format.
	ErrCorrupt = errors.New("zstdz3: corrupt stream")
	// ErrUnsupported reports a valid-looking construct outside the implemented subset
	// (dictionary dependency, legacy frames); never decoded on a best-effort basis.
	ErrUnsupported = errors.New("zstdz3: unsupported feature")
	// ErrLimit reports that a configured bound would be exceeded.
	ErrLimit = errors.New("zstdz3: limit exceeded")
	// ErrIR reports a malformed serialized reconstruction description.
	ErrIR = errors.New("zstdz3: malformed reconstruction description")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}

func unsupported(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrUnsupported}, a...)...)
}

func limit(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrLimit}, a...)...)
}

func badIR(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrIR}, a...)...)
}

// Limits bound every resource a hostile stream could consume. Zero fields take the
// DefaultLimits value. Memory is the window (history) plus one block plus the
// description being built; it never follows a declared Frame_Content_Size.
type Limits struct {
	MaxFrames       int   // frames (standard and skippable)
	MaxBlocks       int   // blocks in the stream
	MaxWindow       int64 // Window_Size a frame may declare
	MaxFrameDecoded int64 // decoded bytes of one frame
	MaxTotalDecoded int64 // decoded bytes of the stream
	MaxLiterals     int64 // regenerated literal bytes in the stream
	MaxSequences    int64 // sequences in the stream
	MaxSkippable    int64 // bytes of one skippable frame payload (kept verbatim)
	MaxIRBytes      int64 // skeleton + unit bytes
	MaxRatio        int64 // decoded bytes per compressed byte (plus a fixed allowance)
}

// DefaultLimits are generous enough for multi-gigabyte container layers and small
// enough that a hostile header cannot force a large allocation.
func DefaultLimits() Limits {
	return Limits{
		MaxFrames: 1 << 20, MaxBlocks: 1 << 24, MaxWindow: 1 << 27,
		MaxFrameDecoded: 1 << 40, MaxTotalDecoded: 1 << 40,
		MaxLiterals: 1 << 40, MaxSequences: 1 << 34, MaxSkippable: 1 << 28,
		MaxIRBytes: 1 << 34, MaxRatio: 1 << 16,
	}
}

func (l Limits) fill() Limits {
	d := DefaultLimits()
	if l.MaxFrames <= 0 {
		l.MaxFrames = d.MaxFrames
	}
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = d.MaxBlocks
	}
	if l.MaxWindow <= 0 {
		l.MaxWindow = d.MaxWindow
	}
	if l.MaxFrameDecoded <= 0 {
		l.MaxFrameDecoded = d.MaxFrameDecoded
	}
	if l.MaxTotalDecoded <= 0 {
		l.MaxTotalDecoded = d.MaxTotalDecoded
	}
	if l.MaxLiterals <= 0 {
		l.MaxLiterals = d.MaxLiterals
	}
	if l.MaxSequences <= 0 {
		l.MaxSequences = d.MaxSequences
	}
	if l.MaxSkippable <= 0 {
		l.MaxSkippable = d.MaxSkippable
	}
	if l.MaxIRBytes <= 0 {
		l.MaxIRBytes = d.MaxIRBytes
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = d.MaxRatio
	}
	return l
}

const (
	magicStd   = 0xFD2FB528
	magicSkip  = 0x184D2A50
	magicSkipM = 0xFFFFFFF0
	maxBlock   = 128 << 10 // Block_Maximum_Size
	// ratioSlack lets tiny streams (a few header bytes) decode to a block or two.
	ratioSlack = 1 << 20
)
