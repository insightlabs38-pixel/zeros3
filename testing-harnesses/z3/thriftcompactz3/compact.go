// Package thriftcompactz3 is the smallest strict, bounded Thrift Compact Protocol reader
// that Parquet metadata needs (FileMetaData, PageHeader and the structures nested in
// them). It is a laboratory object, not a ZeroS3 production format.
//
// It is not a Thrift framework: no RPC, no code generation, no reflection. A caller
// walks a struct with Struct, handles the field ids it understands and skips the rest;
// Skip is bounded in depth, in container size and in bytes, and never allocates from a
// declared count (a count larger than the bytes that remain is rejected before any
// element is read). Reads past the end report ErrShort, so a caller that reads a
// header from a growing window can tell "need more bytes" from "corrupt". Unknown
// fields are skipped, never re-serialised: callers keep the original byte span.
//
// The package imports only the standard library.
package thriftcompactz3

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	// ErrShort reports that the buffer ended inside a value.
	ErrShort = errors.New("thriftcompactz3: truncated")
	// ErrCorrupt reports bytes that are not a valid Compact Protocol value.
	ErrCorrupt = errors.New("thriftcompactz3: corrupt")
	// ErrLimit reports that a configured bound would be exceeded.
	ErrLimit = errors.New("thriftcompactz3: limit exceeded")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}

// Type is a Compact Protocol type id as it appears in a field or element header.
type Type uint8

// Compact type ids (the boolean pair is a field-header-only encoding).
const (
	Stop      Type = 0
	BoolTrue  Type = 1
	BoolFalse Type = 2
	Byte      Type = 3
	I16       Type = 4
	I32       Type = 5
	I64       Type = 6
	Double    Type = 7
	Binary    Type = 8
	List      Type = 9
	Set       Type = 10
	Map       Type = 11
	Struct    Type = 12
)

// Limits bound a parse. The zero value selects the defaults.
type Limits struct {
	MaxDepth     int // struct/container nesting (default 16)
	MaxContainer int // elements of one list/set/map (default 1<<22)
	MaxBinary    int // bytes of one string/binary (default 64 MiB)
	MaxFields    int // fields of one struct, unknown ones included (default 4096)
	MaxValues    int // values (fields + elements) skipped or read in one Reader (default 1<<26)
}

func (l Limits) norm() Limits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = 16
	}
	if l.MaxContainer <= 0 {
		l.MaxContainer = 1 << 22
	}
	if l.MaxBinary <= 0 {
		l.MaxBinary = 64 << 20
	}
	if l.MaxFields <= 0 {
		l.MaxFields = 4096
	}
	if l.MaxValues <= 0 {
		l.MaxValues = 1 << 26
	}
	return l
}

// Reader decodes one buffer. It is not safe for concurrent use.
type Reader struct {
	b      []byte
	pos    int
	lim    Limits
	depth  int
	values int
}

// NewReader reads b from the start.
func NewReader(b []byte, lim Limits) *Reader { return &Reader{b: b, lim: lim.norm()} }

// Pos is the number of bytes consumed so far.
func (r *Reader) Pos() int { return r.pos }

// Remaining is the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.b) - r.pos }

func (r *Reader) count() error {
	r.values++
	if r.values > r.lim.MaxValues {
		return fmt.Errorf("%w: more than %d values", ErrLimit, r.lim.MaxValues)
	}
	return nil
}

func (r *Reader) byte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, ErrShort
	}
	c := r.b[r.pos]
	r.pos++
	return c, nil
}

func (r *Reader) uvarint(maxBytes int) (uint64, error) {
	var v uint64
	for i := 0; i < maxBytes; i++ {
		c, err := r.byte()
		if err != nil {
			return 0, err
		}
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c < 0x80 {
			if i == maxBytes-1 && maxBytes == 10 && c > 1 {
				return 0, corrupt("varint overflows 64 bits")
			}
			return v, nil
		}
	}
	return 0, corrupt("varint longer than %d bytes", maxBytes)
}

func unzig32(u uint64) int32 { return int32(uint32(u>>1) ^ -uint32(u&1)) }
func unzig64(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// I16 reads a zigzag i16 value.
func (r *Reader) I16() (int16, error) {
	u, err := r.uvarint(3)
	if err != nil {
		return 0, err
	}
	v := unzig32(u)
	if v < -1<<15 || v >= 1<<15 {
		return 0, corrupt("i16 out of range")
	}
	return int16(v), nil
}

// I32 reads a zigzag i32 value.
func (r *Reader) I32() (int32, error) {
	u, err := r.uvarint(5)
	if err != nil {
		return 0, err
	}
	if u > 1<<32-1 {
		return 0, corrupt("i32 out of range")
	}
	return unzig32(u), nil
}

// I64 reads a zigzag i64 value.
func (r *Reader) I64() (int64, error) {
	u, err := r.uvarint(10)
	if err != nil {
		return 0, err
	}
	return unzig64(u), nil
}

// Byte reads one raw byte value (Thrift i8).
func (r *Reader) Byte() (int8, error) {
	c, err := r.byte()
	return int8(c), err
}

// Binary reads a length-prefixed string/binary. The result aliases the buffer.
func (r *Reader) Binary() ([]byte, error) {
	n, err := r.uvarint(5)
	if err != nil {
		return nil, err
	}
	if n > uint64(r.lim.MaxBinary) {
		return nil, fmt.Errorf("%w: binary of %d bytes", ErrLimit, n)
	}
	if n > uint64(r.Remaining()) {
		return nil, ErrShort
	}
	b := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b, nil
}

// ListHeader reads a list or set header: the element type and count. The count is
// checked against the bytes that remain (every element takes at least one byte), so a
// hostile count never reaches an allocation.
func (r *Reader) ListHeader() (Type, int, error) {
	h, err := r.byte()
	if err != nil {
		return 0, 0, err
	}
	n := uint64(h >> 4)
	if n == 15 {
		if n, err = r.uvarint(5); err != nil {
			return 0, 0, err
		}
	}
	et := Type(h & 0x0f)
	if !validElem(et) {
		return 0, 0, corrupt("list element type %d", et)
	}
	if n > uint64(r.lim.MaxContainer) {
		return 0, 0, fmt.Errorf("%w: list of %d elements", ErrLimit, n)
	}
	if n > uint64(r.Remaining()) {
		return 0, 0, fmt.Errorf("%w: %d elements in %d bytes", ErrShort, n, r.Remaining())
	}
	return et, int(n), nil
}

func validElem(t Type) bool { return t >= BoolTrue && t <= Struct }

// Bool reads one boolean list element (a single byte; 1 is true).
func (r *Reader) Bool() (bool, error) {
	c, err := r.byte()
	return c == 1, err
}

// Struct reads one struct. fn is called for every field with its id and type and must
// consume the field's value (with the typed readers, Skip, or a nested Struct); a
// handler that consumes nothing is an error. Boolean fields carry their value in the
// type (BoolTrue/BoolFalse) and consume no bytes. The number of fields, the nesting
// depth and the total number of values are bounded.
func (r *Reader) Struct(fn func(id int16, t Type) error) error {
	if r.depth >= r.lim.MaxDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrLimit, r.lim.MaxDepth)
	}
	r.depth++
	defer func() { r.depth-- }()
	var id int16
	for n := 0; ; n++ {
		if n >= r.lim.MaxFields {
			return fmt.Errorf("%w: more than %d fields", ErrLimit, r.lim.MaxFields)
		}
		h, err := r.byte()
		if err != nil {
			return err
		}
		if h == 0 {
			return nil
		}
		t := Type(h & 0x0f)
		if d := int16(h >> 4); d != 0 {
			if int(id)+int(d) > 1<<15-1 {
				return corrupt("field id overflow")
			}
			id += d
		} else {
			v, err := r.I16()
			if err != nil {
				return err
			}
			id = v
		}
		if !validElem(t) {
			return corrupt("field %d has type %d", id, t)
		}
		if err := r.count(); err != nil {
			return err
		}
		before := r.pos
		if err := fn(id, t); err != nil {
			return err
		}
		if r.pos == before && t != BoolTrue && t != BoolFalse {
			return corrupt("field %d (type %d) was not consumed", id, t)
		}
	}
}

// Skip consumes one value of type t.
func (r *Reader) Skip(t Type) error {
	if r.depth >= r.lim.MaxDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrLimit, r.lim.MaxDepth)
	}
	if err := r.count(); err != nil {
		return err
	}
	switch t {
	case BoolTrue, BoolFalse:
		return nil
	case Byte:
		_, err := r.byte()
		return err
	case I16, I32, I64:
		_, err := r.uvarint(10)
		return err
	case Double:
		if r.Remaining() < 8 {
			return ErrShort
		}
		r.pos += 8
		return nil
	case Binary:
		_, err := r.Binary()
		return err
	case List, Set:
		et, n, err := r.ListHeader()
		if err != nil {
			return err
		}
		r.depth++
		defer func() { r.depth-- }()
		for i := 0; i < n; i++ {
			if et == BoolTrue || et == BoolFalse {
				if _, err := r.byte(); err != nil {
					return err
				}
				continue
			}
			if err := r.Skip(et); err != nil {
				return err
			}
		}
		return nil
	case Map:
		n, err := r.uvarint(5)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if n > uint64(r.lim.MaxContainer) {
			return fmt.Errorf("%w: map of %d entries", ErrLimit, n)
		}
		if 2*n > uint64(r.Remaining()) {
			return fmt.Errorf("%w: %d entries in %d bytes", ErrShort, n, r.Remaining())
		}
		h, err := r.byte()
		if err != nil {
			return err
		}
		kt, vt := Type(h>>4), Type(h&0x0f)
		if !validElem(kt) || !validElem(vt) {
			return corrupt("map types %d,%d", kt, vt)
		}
		r.depth++
		defer func() { r.depth-- }()
		for i := uint64(0); i < n; i++ {
			for _, et := range [2]Type{kt, vt} {
				if et == BoolTrue || et == BoolFalse {
					if _, err := r.byte(); err != nil {
						return err
					}
				} else if err := r.Skip(et); err != nil {
					return err
				}
			}
		}
		return nil
	case Struct:
		return r.Struct(func(_ int16, ft Type) error { return r.Skip(ft) })
	}
	return corrupt("type %d", t)
}

// AppendI64 appends a zigzag varint (the tests that build metadata use it).
func AppendI64(b []byte, v int64) []byte {
	return binary.AppendUvarint(b, uint64(v<<1)^uint64(v>>63))
}
