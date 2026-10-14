// Package carz3 is the Z3-14 laboratory parser for CAR (Content Addressable
// aRchive) v1 and v2 files: a bounded, stdlib-only subset sufficient to
// separate semantic content blocks from archive serialisation and to rebuild
// the archive byte for byte. It is not a multiformats, IPLD or CBOR library.
package carz3

import (
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"errors"
	"fmt"
)

// MalformedError reports bytes that are not a CAR (truncated, out of bounds,
// inconsistent). UnsupportedError reports a well-formed input outside the
// laboratory subset (non-canonical varint, unknown header shape). Both mean
// "fall back to raw CDC"; the distinction is only for reporting.
type MalformedError struct{ Msg string }

func (e *MalformedError) Error() string { return "carz3: malformed: " + e.Msg }

type UnsupportedError struct{ Msg string }

func (e *UnsupportedError) Error() string { return "carz3: unsupported: " + e.Msg }

func malformed(f string, a ...any) error   { return &MalformedError{fmt.Sprintf(f, a...)} }
func unsupported(f string, a ...any) error { return &UnsupportedError{fmt.Sprintf(f, a...)} }

// IsFallback reports whether err means the input must stay on the raw path.
func IsFallback(err error) bool {
	var m *MalformedError
	var u *UnsupportedError
	return errors.As(err, &m) || errors.As(err, &u)
}

// Multicodec / multihash codes the laboratory names.
const (
	CodecRaw    = 0x55
	CodecDagPB  = 0x70
	CodecDagCBR = 0x71

	MhIdentity = 0x00
	MhSHA2_256 = 0x12
	MhSHA2_512 = 0x13
	MhSHA3_512 = 0x14
	MhSHA3_256 = 0x16
)

// MaxDigest bounds a multihash digest length.
const MaxDigest = 64

// maxVarintLen is the multiformats unsigned-varint limit (9 bytes, 63 bits).
const maxVarintLen = 9

// Uvarint decodes a canonical (minimal) unsigned varint. n is the number of
// bytes consumed; n == 0 means b ends inside the varint.
func Uvarint(b []byte) (v uint64, n int, err error) {
	for i := 0; i < len(b); i++ {
		if i >= maxVarintLen {
			return 0, 0, malformed("varint longer than %d bytes", maxVarintLen)
		}
		c := b[i]
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c&0x80 == 0 {
			if c == 0 && i > 0 {
				return 0, 0, unsupported("non-minimal varint")
			}
			return v, i + 1, nil
		}
	}
	return 0, 0, nil
}

// AppendUvarint appends the canonical encoding of v.
func AppendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// CID is the parsed view of one exactly-encoded CID. Raw is the exact encoded
// bytes (aliasing the input unless Clone'd).
type CID struct {
	Raw      []byte
	Version  uint64 // 0 or 1
	Codec    uint64 // CIDv0 is dag-pb
	HashCode uint64
	Digest   []byte
	Prefix   int // len(Raw)-len(Digest): the bytes before the digest
}

// Clone detaches Raw and Digest from the parse buffer.
func (c CID) Clone() CID {
	raw := append([]byte(nil), c.Raw...)
	c.Raw = raw
	c.Digest = raw[c.Prefix:]
	return c
}

// ParseCID parses the CID at the start of b and returns it with its length.
// Only CIDv0 (34-byte sha2-256 multihash) and CIDv1 are understood.
func ParseCID(b []byte) (CID, int, error) {
	if len(b) >= 2 && b[0] == MhSHA2_256 && b[1] == 0x20 {
		if len(b) < 34 {
			return CID{}, 0, malformed("truncated CIDv0")
		}
		return CID{Raw: b[:34], Version: 0, Codec: CodecDagPB, HashCode: MhSHA2_256, Digest: b[2:34], Prefix: 2}, 34, nil
	}
	p := 0
	var f [4]uint64
	for k := range f {
		v, n, err := Uvarint(b[p:])
		if err != nil {
			return CID{}, 0, err
		}
		if n == 0 {
			return CID{}, 0, malformed("truncated CID")
		}
		f[k] = v
		p += n
	}
	if f[0] != 1 {
		return CID{}, 0, unsupported("CID version %d", f[0])
	}
	if f[3] > MaxDigest {
		return CID{}, 0, malformed("CID digest length %d", f[3])
	}
	if uint64(len(b)-p) < f[3] {
		return CID{}, 0, malformed("truncated CID digest")
	}
	end := p + int(f[3])
	return CID{Raw: b[:end], Version: 1, Codec: f[1], HashCode: f[2], Digest: b[p:end], Prefix: p}, end, nil
}

// Key is the native identity used to decide whether two CIDs name the same
// block: codec and multihash, version-normalised (a CIDv0 equals the CIDv1
// dag-pb of the same multihash). It is an opaque string.
func (c CID) Key() string {
	k := make([]byte, 0, 8+len(c.Digest))
	k = AppendUvarint(k, c.Codec)
	k = AppendUvarint(k, c.HashCode)
	k = AppendUvarint(k, uint64(len(c.Digest)))
	return string(append(k, c.Digest...))
}

// Verification says whether a CID's multihash was checked against its block.
type Verification int

const (
	NotVerified Verification = iota // hash function not supported by the lab
	Verified
	Mismatch
)

func (v Verification) String() string { return [...]string{"not-verified", "verified", "mismatch"}[v] }

// Verify checks block against the CID's multihash for the algorithms the
// standard library provides (identity, sha2-256/512, sha3-256/512).
func (c CID) Verify(block []byte) Verification {
	var d []byte
	switch c.HashCode {
	case MhIdentity:
		d = block
	case MhSHA2_256:
		s := sha256.Sum256(block)
		d = s[:]
	case MhSHA2_512:
		s := sha512.Sum512(block)
		d = s[:]
	case MhSHA3_256:
		s := sha3.Sum256(block)
		d = s[:]
	case MhSHA3_512:
		s := sha3.Sum512(block)
		d = s[:]
	default:
		return NotVerified
	}
	if string(d) == string(c.Digest) {
		return Verified
	}
	return Mismatch
}

// DerivableFrom256 reports whether the CID is exactly Prefix || sha256(block),
// i.e. the whole digest is the ZeroS3 content digest of the block, so a recipe
// need only keep the prefix. This is an encoding shortcut, never an assumption:
// callers must check it per block.
func (c CID) DerivableFrom256(sum [32]byte) bool {
	return c.HashCode == MhSHA2_256 && len(c.Digest) == 32 && string(c.Digest) == string(sum[:])
}
