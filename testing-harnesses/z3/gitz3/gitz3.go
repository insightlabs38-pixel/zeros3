// Package gitz3 is the Z3-16 bounded Git substrate: loose objects, pack files
// (full objects, OFS_DELTA, REF_DELTA), the Git delta format and pack-index v2,
// plus the exact representations the laboratory compares. It is not a Git
// client: no refs, worktrees, config, protocol or bitmap/MIDX/commit-graph.
//
// Everything here is research code. Every parser is bounded (Limits), returns
// an error rather than panicking on hostile input, and treats "cannot parse /
// cannot verify" as a reason to fall back to ordinary bytes, never as a reason
// to reject an object.
package gitz3

import (
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
)

var (
	ErrCorrupt     = errors.New("gitz3: corrupt")
	ErrLimit       = errors.New("gitz3: limit exceeded")
	ErrChecksum    = errors.New("gitz3: checksum mismatch")
	ErrThin        = errors.New("gitz3: unresolved delta base (thin pack or cycle)")
	ErrUnsupported = errors.New("gitz3: unsupported")
	ErrMismatch    = errors.New("gitz3: reconstruction mismatch")
)

func corrupt(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(f, a...)) }
func limit(f string, a ...any) error   { return fmt.Errorf("%w: %s", ErrLimit, fmt.Sprintf(f, a...)) }

// Algo is the repository object format.
type Algo uint8

const (
	SHA1   Algo = 1
	SHA256 Algo = 2
)

func (a Algo) Size() int {
	if a == SHA256 {
		return 32
	}
	return 20
}

func (a Algo) String() string {
	if a == SHA256 {
		return "sha256"
	}
	return "sha1"
}

func (a Algo) New() hash.Hash {
	if a == SHA256 {
		return sha256.New()
	}
	return sha1.New()
}

func (a Algo) Sum(b []byte) []byte {
	h := a.New()
	h.Write(b)
	return h.Sum(nil)
}

// Limits bound every parse. Zero fields take the defaults.
type Limits struct {
	MaxObjects    int   // entries in a pack / index
	MaxObjectSize int64 // inflated size of one entry / resolved object
	MaxInflated   int64 // total inflated bytes over a pack parse or resolve
	MaxDeltaDepth int   // delta chain depth
	MaxDeltaOut   int64 // total bytes produced by delta application
	MaxPack       int64 // input pack bytes
}

func DefaultLimits() Limits {
	return Limits{MaxObjects: 1 << 22, MaxObjectSize: 256 << 20, MaxInflated: 8 << 30, MaxDeltaDepth: 4096, MaxDeltaOut: 8 << 30, MaxPack: 8 << 30}
}

func (l Limits) norm() Limits {
	d := DefaultLimits()
	if l.MaxObjects <= 0 {
		l.MaxObjects = d.MaxObjects
	}
	if l.MaxObjectSize <= 0 {
		l.MaxObjectSize = d.MaxObjectSize
	}
	if l.MaxInflated <= 0 {
		l.MaxInflated = d.MaxInflated
	}
	if l.MaxDeltaDepth <= 0 {
		l.MaxDeltaDepth = d.MaxDeltaDepth
	}
	if l.MaxDeltaOut <= 0 {
		l.MaxDeltaOut = d.MaxDeltaOut
	}
	if l.MaxPack <= 0 {
		l.MaxPack = d.MaxPack
	}
	return l
}

// ObjType is a Git object type (1..4 as in pack entries).
type ObjType uint8

const (
	Commit ObjType = 1
	Tree   ObjType = 2
	Blob   ObjType = 3
	Tag    ObjType = 4
	// pack entry kinds that are not object types
	ofsDelta = 6
	refDelta = 7
)

func (t ObjType) String() string {
	switch t {
	case Commit:
		return "commit"
	case Tree:
		return "tree"
	case Blob:
		return "blob"
	case Tag:
		return "tag"
	}
	return fmt.Sprintf("type%d", uint8(t))
}
