// Package eraez3 is the Z3-15C laboratory parser/representation for EraE
// (Ethereum execution-layer history) archives, an e2store container. It parses
// the container with bounds, recognises the grouped component layout, and
// builds an exact representation in which framed Snappy entries are stored as
// plaintext streams (when the reference encoder reproduces them byte for byte)
// and the component index is derived from the entry positions.
package eraez3

import (
	"encoding/binary"
	"fmt"
	"io"
)

// e2store entry types used by EraE / Era1.
const (
	TypeVersion        = 0x3265
	TypeBlockIndex1    = 0x3266 // Era1
	TypeComponentIndex = 0x3267 // EraE
	TypeHeader         = 0x0003
	TypeBody           = 0x0004
	TypeReceipts       = 0x0005 // Era1 compressed receipts
	TypeTD             = 0x0006
	TypeAccumulator    = 0x0007
	TypeSlimReceipts   = 0x000a
	TypeProof          = 0x000b
)

// Entry is one e2store entry: an 8-byte header (type u16, length u32, reserved
// u16, little endian) and Len payload bytes starting at Off+8.
type Entry struct {
	Type uint16
	Off  int64
	Len  uint32
}

func (e Entry) End() int64 { return e.Off + 8 + int64(e.Len) }

type Limits struct {
	MaxEntries int
	MaxEntry   int64
	MaxTotal   int64
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: 1 << 22, MaxEntry: 256 << 20, MaxTotal: 1 << 36}
}

// MalformedError / UnsupportedError mirror carz3: both mean "keep the raw path".
type MalformedError struct{ Msg string }

func (e *MalformedError) Error() string { return "eraez3: malformed: " + e.Msg }

type UnsupportedError struct{ Msg string }

func (e *UnsupportedError) Error() string { return "eraez3: unsupported: " + e.Msg }

func malformed(f string, a ...any) error   { return &MalformedError{fmt.Sprintf(f, a...)} }
func unsupported(f string, a ...any) error { return &UnsupportedError{fmt.Sprintf(f, a...)} }

// IsFallback reports whether err is a classified parse/representation refusal.
func IsFallback(err error) bool {
	switch err.(type) {
	case *MalformedError, *UnsupportedError:
		return true
	}
	return false
}

// Parse reads the e2store entry headers of src; the entries must tile the file
// exactly and carry a zero reserved field.
func Parse(src io.ReaderAt, size int64, lim Limits) ([]Entry, error) {
	if size > lim.MaxTotal {
		return nil, malformed("file of %d bytes", size)
	}
	var ents []Entry
	var h [8]byte
	for pos := int64(0); pos < size; {
		if size-pos < 8 {
			return nil, malformed("truncated entry header at %d", pos)
		}
		if _, err := src.ReadAt(h[:], pos); err != nil {
			return nil, malformed("read at %d: %v", pos, err)
		}
		e := Entry{Type: binary.LittleEndian.Uint16(h[0:]), Off: pos, Len: binary.LittleEndian.Uint32(h[2:])}
		if binary.LittleEndian.Uint16(h[6:]) != 0 {
			return nil, unsupported("non-zero reserved field at %d", pos)
		}
		if int64(e.Len) > lim.MaxEntry || e.End() > size {
			return nil, malformed("entry at %d claims %d bytes", pos, e.Len)
		}
		if len(ents) >= lim.MaxEntries {
			return nil, malformed("more than %d entries", lim.MaxEntries)
		}
		ents = append(ents, e)
		pos = e.End()
	}
	if len(ents) == 0 || ents[0].Type != TypeVersion || ents[0].Len != 0 {
		return nil, unsupported("not an e2store archive starting with a Version entry")
	}
	return ents, nil
}

func appendEntry(b []byte, typ uint16, payload []byte) []byte {
	b = binary.LittleEndian.AppendUint16(b, typ)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
	b = append(b, 0, 0)
	return append(b, payload...)
}

// rlpItemLen returns the length of the RLP item at the start of b.
func rlpItemLen(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	c := b[0]
	var hdr, n int
	switch {
	case c < 0x80:
		return 1, true
	case c <= 0xb7:
		hdr, n = 1, int(c-0x80)
	case c <= 0xbf:
		ll := int(c - 0xb7)
		if len(b) < 1+ll || ll > 4 {
			return 0, false
		}
		for i := 0; i < ll; i++ {
			n = n<<8 | int(b[1+i])
		}
		hdr = 1 + ll
	case c <= 0xf7:
		hdr, n = 1, int(c-0xc0)
	default:
		ll := int(c - 0xf7)
		if len(b) < 1+ll || ll > 4 {
			return 0, false
		}
		for i := 0; i < ll; i++ {
			n = n<<8 | int(b[1+i])
		}
		hdr = 1 + ll
	}
	if n < 0 || hdr+n > len(b) || hdr+n < 0 {
		return 0, false
	}
	return hdr + n, true
}
