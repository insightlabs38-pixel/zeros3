// Package lz4z3 is the Z4-B6 exact LZ4 substrate (laboratory; stdlib only).
//
// An LZ4 block is a bijection of its sequence list {literal run, match length, offset}
// plus the literal bytes: token nibbles and 255-extension bytes have exactly one spelling.
// The original compressed bytes therefore follow from the decoded payload and either
//
//	(a) explicit sequences (Seq), or
//	(b) a deterministic reference encoder id + acceleration (Predict), verified against
//	    the stored block at analysis time, or
//	(c) a predictor plus a sparse sequence delta (hybrid; EncodeRecipe picks the cheapest).
//
// The frame layer (frame.go) keeps header, block-size words and checksums exact around the
// per-block recipes. No lz4 library or producer is used by Build, Replay or ReplayFrame.
package lz4z3

import (
	"errors"
	"fmt"
)

var (
	// ErrCorrupt reports bytes that violate the LZ4 block or frame grammar.
	ErrCorrupt = errors.New("lz4z3: corrupt input")
	// ErrLimit reports that a configured bound would be exceeded.
	ErrLimit = errors.New("lz4z3: limit exceeded")
	// ErrRecipe reports a malformed or inconsistent recipe.
	ErrRecipe = errors.New("lz4z3: bad recipe")
	// ErrUnsupported reports a well-formed feature this substrate does not express.
	ErrUnsupported = errors.New("lz4z3: unsupported")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}
func recipeErr(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrRecipe}, a...)...)
}

// Limits bound every parse and replay. The zero value selects the defaults.
type Limits struct {
	MaxDecoded int // per block / frame content (default 64 MiB)
	MaxSeqs    int // per block (default MaxDecoded/4+1)
	MaxBlocks  int // per frame (default 1<<20)
}

func (l Limits) norm() Limits {
	if l.MaxDecoded <= 0 {
		l.MaxDecoded = 64 << 20
	}
	if l.MaxSeqs <= 0 {
		l.MaxSeqs = l.MaxDecoded/4 + 1
	}
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = 1 << 20
	}
	return l
}
