package ir

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/deflate"
)

// PolicyKind selects how the token sequence is cut into reconstruction units.
type PolicyKind uint8

const (
	// PStream: one unit for everything (control).
	PStream PolicyKind = iota
	// PBlock: each non-empty token block is a unit.
	PBlock
	// PGroup: adjacent blocks grouped until Target plaintext bytes (block boundaries only).
	PGroup
	// PContentBlocks: block-boundary cuts chosen by a hash of the plaintext
	// preceding the boundary, so unrelated edits do not renumber later groups.
	PContentBlocks
	// PTokRun: cuts at token boundaries chosen by a rolling hash of the preceding
	// plaintext (avg Target bytes), independent of DEFLATE block boundaries.
	PTokRun
)

// Policy is a unit-cut policy. Target is a plaintext byte size (power of two
// for the content-defined kinds).
type Policy struct {
	Kind   PolicyKind
	Target int
}

func (p Policy) String() string {
	switch p.Kind {
	case PStream:
		return "stream"
	case PBlock:
		return "block"
	case PGroup:
		return fmt.Sprintf("group%s", sizeName(p.Target))
	case PContentBlocks:
		return fmt.Sprintf("cblk%s", sizeName(p.Target))
	case PTokRun:
		return fmt.Sprintf("tok%s", sizeName(p.Target))
	}
	return "?"
}

func sizeName(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dM", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%dK", n>>10)
	}
	return fmt.Sprint(n)
}

var gear = func() (t [256]uint64) {
	for i := range t {
		h := sha256.Sum256([]byte(fmt.Sprintf("Z3/ir/gear/%d", i)))
		t[i] = binary.BigEndian.Uint64(h[:8])
	}
	return
}()

func pow2floor(n int) int {
	p := 1
	for p*2 <= n {
		p *= 2
	}
	return p
}

// cuts returns the sorted global token indices (exclusive of 0 and len) at
// which a new unit starts. A unit never spans a stored block (stored payload
// bytes sit in the plaintext between tokens), so those boundaries are forced.
func cuts(s *deflate.Stream, f *flat, pol Policy) []int {
	set := map[int]bool{}
	add := func(i int) {
		if i > 0 && i < len(f.toks) {
			set[i] = true
		}
	}
	for i := range s.Blocks {
		if f.stored[i] {
			add(f.blockTok[i])
		}
	}
	switch pol.Kind {
	case PStream:
	case PBlock:
		for i := range s.Blocks {
			add(f.blockTok[i])
		}
	case PGroup, PContentBlocks:
		target := max(pol.Target, 1)
		mask := uint64(pow2floor(target)) - 1
		lo, hi := target/4, target*4
		acc := 0
		for i := range s.Blocks {
			b := &s.Blocks[i]
			if f.stored[i] {
				acc = 0
				continue
			}
			if acc > 0 {
				var cut bool
				if pol.Kind == PGroup {
					cut = acc >= target
				} else {
					cut = acc >= hi || (acc >= lo && boundaryHash(s.Plain, b.PlainOff)&mask == 0)
				}
				if cut {
					add(f.blockTok[i])
					acc = 0
				}
			}
			acc += b.PlainLen
		}
	case PTokRun:
		target := max(pol.Target, 64)
		mask := uint64(pow2floor(target)) - 1
		lo, hi := target/4, target*4
		// The hash is tested at every plaintext byte; a hit inside a token
		// requests a cut at the next token boundary, so the cut density does
		// not depend on how many positions happen to be token boundaries.
		var h uint64
		size := 0
		pending := false
		for bi := range s.Blocks {
			b := &s.Blocks[bi]
			pos := b.PlainOff
			if f.stored[bi] {
				for _, c := range s.Plain[pos : pos+b.PlainLen] {
					h = h<<1 + gear[c]
				}
				size, pending = 0, false
				continue
			}
			for k, t := range b.Tokens {
				if size > 0 && (pending || size >= hi) {
					add(f.blockTok[bi] + k)
					size, pending = 0, false
				}
				n := tokenPlain(t)
				for _, c := range s.Plain[pos : pos+n] {
					h = h<<1 + gear[c]
					size++
					if size >= lo && h&mask == 0 {
						pending = true
					}
				}
				pos += n
			}
		}
	}
	out := make([]int, 0, len(set))
	for i := range set {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// boundaryHash hashes the 64 plaintext bytes preceding off.
func boundaryHash(plain []byte, off int) uint64 {
	var h uint64
	for i := max(0, off-64); i < off; i++ {
		h = h<<1 + gear[plain[i]]
	}
	return h
}
