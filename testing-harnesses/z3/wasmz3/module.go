// Package wasmz3 is a bounded parser and indexed-reference representation for WebAssembly core
// modules (binary format 1): section directory, the module-level index spaces (type, function,
// table, memory, global, tag, element, data), a finite instruction walker and the exact-width
// LEB128 slots that name those spaces. Function-local indexes (locals, labels) are never
// references. Component-model binaries, GC types, memory64 and multi-memory immediates are
// recognized and make a module unsupported (the caller keeps CDC).
package wasmz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrFormat reports a malformed module (or a corrupt representation).
	ErrFormat = errors.New("wasmz3: malformed")
	// ErrUnsupported reports legal structure this package does not represent.
	ErrUnsupported = errors.New("wasmz3: unsupported")
	// ErrLimit reports a bound exceeded.
	ErrLimit = errors.New("wasmz3: limit exceeded")
)

func bad(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrFormat, fmt.Sprintf(f, a...)) }
func unsup(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(f, a...))
}

// Section ids.
const (
	SecCustom    = 0
	SecType      = 1
	SecImport    = 2
	SecFunction  = 3
	SecTable     = 4
	SecMemory    = 5
	SecGlobal    = 6
	SecExport    = 7
	SecStart     = 8
	SecElement   = 9
	SecCode      = 10
	SecData      = 11
	SecDataCount = 12
	SecTag       = 13
)

// order is the position of a section id in the required section order.
var order = [14]int{SecCustom: -1, SecType: 1, SecImport: 2, SecFunction: 3, SecTable: 4, SecMemory: 5, SecTag: 6, SecGlobal: 7,
	SecExport: 8, SecStart: 9, SecElement: 10, SecDataCount: 11, SecCode: 12, SecData: 13}

// Limits bounds what the parser accepts.
type Limits struct {
	MaxSections int   // default 1024
	MaxEntries  int64 // vector entries in one section (default 1<<22)
	MaxName     int   // bytes in one name (default 1<<16)
}

func (l *Limits) fill() {
	if l.MaxSections == 0 {
		l.MaxSections = 1024
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = 1 << 22
	}
	if l.MaxName == 0 {
		l.MaxName = 1 << 16
	}
}

// Section is one section of the directory.
type Section struct {
	ID      byte
	Off     int64  // offset of the id byte
	SizeW   int    // bytes of the size LEB
	Body    int64  // offset of the content
	Len     int64  // content length
	Name    string // custom section name
	NameEnd int64  // offset after the name (custom)
}

func (s Section) End() int64 { return s.Body + s.Len }

// Header is magic + version.
const HeaderSize = 8

// Dir is the section directory of a module.
type Dir struct {
	Size int64
	Secs []Section
}

// leb reads an unsigned LEB128 of at most max bytes; w is the encoded width.
func leb(b []byte, p int, max int) (v uint64, w int, ok bool) {
	var shift uint
	for i := 0; i < max; i++ {
		if p+i >= len(b) {
			return 0, 0, false
		}
		c := b[p+i]
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			if i == max-1 && max == 5 && c&0x70 != 0 { // u32 overflow bits
				return 0, 0, false
			}
			return v, i + 1, true
		}
		shift += 7
	}
	return 0, 0, false
}

// lebWidth is the encoded width of a LEB128 number (signed or unsigned) of at most max bytes.
func lebWidth(b []byte, p int, max int) (int, bool) {
	for i := 0; i < max; i++ {
		if p+i >= len(b) {
			return 0, false
		}
		if b[p+i]&0x80 == 0 {
			return i + 1, true
		}
	}
	return 0, false
}

func u32(b []byte, p int) (uint32, int, bool) {
	v, w, ok := leb(b, p, 5)
	return uint32(v), w, ok
}

// putLEB writes v as an unsigned LEB128 of exactly w bytes (padded); ok is false when v does not fit.
func putLEB(dst []byte, w int, v uint64) bool {
	if w < 1 || w > 10 || (w < 10 && v>>(7*uint(w)) != 0) {
		return false
	}
	for i := 0; i < w; i++ {
		c := byte(v & 0x7f)
		v >>= 7
		if i < w-1 {
			c |= 0x80
		}
		dst[i] = c
	}
	return true
}

// putSLEB33 writes a non-negative v as a signed LEB128 of exactly w bytes; ok is false when v
// does not fit (the sign bit of the last byte must stay clear).
func putSLEB33(dst []byte, w int, v uint64) bool {
	if w < 1 || w > 5 || v>>(7*uint(w)-1) != 0 {
		return false
	}
	return putLEB(dst, w, v)
}

func readAt(ra io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	m, err := ra.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		return nil, bad("short read at %d", off)
	}
	return nil, err
}

// Parse reads the header and the section directory. Only the section headers (and the name of
// each custom section) are read.
func Parse(ra io.ReaderAt, size int64, lim Limits) (*Dir, error) {
	lim.fill()
	if size < HeaderSize {
		return nil, bad("module of %d bytes", size)
	}
	h, err := readAt(ra, 0, HeaderSize)
	if err != nil {
		return nil, err
	}
	if string(h[:4]) != "\x00asm" {
		return nil, bad("no \\0asm magic")
	}
	switch v := binary.LittleEndian.Uint32(h[4:]); v {
	case 1:
	case 0x0001000d: // component-model layer 1
		return nil, unsup("component-model binary")
	default:
		return nil, unsup("binary version %#x", v)
	}
	d := &Dir{Size: size}
	pos := int64(HeaderSize)
	last := 0
	for pos < size {
		if len(d.Secs) >= lim.MaxSections {
			return nil, fmt.Errorf("%w: more than %d sections", ErrLimit, lim.MaxSections)
		}
		n := int(min(16, size-pos))
		hb, err := readAt(ra, pos, n)
		if err != nil {
			return nil, err
		}
		id := hb[0]
		if id > SecTag {
			return nil, bad("section id %d", id)
		}
		l, w, ok := u32(hb, 1)
		if !ok {
			return nil, bad("section size at %d", pos)
		}
		s := Section{ID: id, Off: pos, SizeW: w, Body: pos + 1 + int64(w), Len: int64(l)}
		if s.Len > size-s.Body {
			return nil, bad("section %d at %d runs past the module", id, pos)
		}
		if id != SecCustom {
			if order[id] <= last {
				return nil, bad("section %d out of order", id)
			}
			last = order[id]
		} else {
			nb, err := readAt(ra, s.Body, int(min(s.Len, int64(lim.MaxName)+5)))
			if err != nil {
				return nil, err
			}
			nl, nw, ok := u32(nb, 0)
			if !ok || int64(nl) > s.Len-int64(nw) || int(nl) > lim.MaxName {
				return nil, bad("custom section name at %d", pos)
			}
			s.Name = string(nb[nw : nw+int(nl)])
			s.NameEnd = s.Body + int64(nw) + int64(nl)
		}
		d.Secs = append(d.Secs, s)
		pos = s.End()
	}
	return d, nil
}

// Find returns the first section with the given id.
func (d *Dir) Find(id byte) (Section, bool) {
	for _, s := range d.Secs {
		if s.ID == id {
			return s, true
		}
	}
	return Section{}, false
}
