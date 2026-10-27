// Package jvmz3 is the Z3-32 strict, bounded JVM class-file reader and constant-pool-index
// normalizer. Parse validates the constant pool and the class body (fields, methods, Code,
// attributes) with checked arithmetic and hard limits and enumerates every *slot*: a u1/u2 field
// that holds a constant-pool index. Encode replaces each slot by a short digest of the target
// entry's semantic identity (tag + literal bytes + identities of the entries it references) and
// Decode rebuilds the exact bytes from that representation alone. The constant-pool order, every
// attribute the walker does not model and every length stay exact bytes. Stdlib only.
package jvmz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	ErrFormat      = errors.New("jvmz3: malformed class file")
	ErrUnsupported = errors.New("jvmz3: unsupported class file feature")
	ErrLimit       = errors.New("jvmz3: limit exceeded")
)

// Limits bound every parse; zero fields take the defaults.
type Limits struct {
	MaxClass   int64 // class file bytes: 64 MiB
	MaxCP      int   // constant_pool_count: 65535 (format maximum)
	MaxMembers int   // fields or methods: 65535
	MaxCode    int   // code_length: 65535 (format maximum)
	MaxAttrs   int   // attributes per structure: 65535
	MaxDepth   int   // annotation / element_value nesting: 32
	MaxSlots   int   // 16 Mi
	MaxFrames  int   // StackMapTable entries / verification types: 65535
}

func (l Limits) norm() Limits {
	if l.MaxClass <= 0 {
		l.MaxClass = 64 << 20
	}
	d := func(v *int, x int) {
		if *v <= 0 {
			*v = x
		}
	}
	d(&l.MaxCP, 65535)
	d(&l.MaxMembers, 65535)
	d(&l.MaxCode, 65535)
	d(&l.MaxAttrs, 65535)
	d(&l.MaxDepth, 32)
	d(&l.MaxSlots, 16<<20)
	d(&l.MaxFrames, 65535)
	return l
}

// Constant-pool tags.
const (
	TagUtf8               = 1
	TagInteger            = 3
	TagFloat              = 4
	TagLong               = 5
	TagDouble             = 6
	TagClass              = 7
	TagString             = 8
	TagFieldref           = 9
	TagMethodref          = 10
	TagInterfaceMethodref = 11
	TagNameAndType        = 12
	TagMethodHandle       = 15
	TagMethodType         = 16
	TagDynamic            = 17
	TagInvokeDynamic      = 18
	TagModule             = 19
	TagPackage            = 20
)

// Space is the set of constant-pool tags a slot may refer to.
type Space uint8

const (
	SpNone Space = iota
	SpUtf8
	SpClass
	SpNat
	SpField   // Fieldref
	SpMethod  // Methodref | InterfaceMethodref
	SpMember  // Fieldref | Methodref | InterfaceMethodref (MethodHandle target)
	SpLoad    // ldc family targets: Integer Float Long Double String Class MethodHandle MethodType Dynamic
	SpInvDyn  // InvokeDynamic
	SpModule  // Module
	SpPackage // Package
	SpAttr    // attribute_name_index: a Utf8, resolved on the fly while walking
	SpInt     // Integer (annotation const_value for B C I S Z)
	SpLong    // Long
	SpFloat   // Float
	SpDouble  // Double
	numSpaces
)

func (s Space) String() string {
	return [...]string{"none", "utf8", "class", "nat", "field", "method", "member", "load", "invdyn", "module", "package", "attr", "int", "long", "float", "double"}[s]
}

// tags a space accepts.
func (s Space) accepts(tag byte) bool {
	switch s {
	case SpUtf8, SpAttr:
		return tag == TagUtf8
	case SpClass:
		return tag == TagClass
	case SpNat:
		return tag == TagNameAndType
	case SpField:
		return tag == TagFieldref
	case SpMethod:
		return tag == TagMethodref || tag == TagInterfaceMethodref
	case SpMember:
		return tag == TagFieldref || tag == TagMethodref || tag == TagInterfaceMethodref
	case SpLoad:
		switch tag {
		case TagInteger, TagFloat, TagLong, TagDouble, TagString, TagClass, TagMethodHandle, TagMethodType, TagDynamic:
			return true
		}
	case SpInvDyn:
		return tag == TagInvokeDynamic
	case SpModule:
		return tag == TagModule
	case SpPackage:
		return tag == TagPackage
	case SpInt:
		return tag == TagInteger
	case SpLong:
		return tag == TagLong
	case SpFloat:
		return tag == TagFloat
	case SpDouble:
		return tag == TagDouble
	}
	return false
}

// Class names the structure a slot lives in (attribution and coverage).
type Class uint8

const (
	ClCP       Class = iota // references between constant-pool entries
	ClHdr                   // this_class, super_class, interfaces
	ClMember                // field / method name and descriptor
	ClAttrName              // attribute_name_index
	ClCode                  // bytecode operands
	ClExc                   // exception table catch types
	ClStack                 // StackMapTable verification types
	ClLocals                // LocalVariable(Type)Table
	ClAttr                  // Signature, SourceFile, Exceptions, InnerClasses, EnclosingMethod, Nest*, ConstantValue, BootstrapMethods, MethodParameters
	ClAnno                  // annotations
	numClasses
)

func (c Class) String() string {
	return [...]string{"cp", "header", "member", "attrname", "code", "exctable", "stackmap", "locals", "attr", "anno"}[c]
}

// Slot is one constant-pool index field.
type Slot struct {
	Off   int64
	Val   uint16 // the original index (0 when walking masked bytes)
	Len   uint8  // 1 (ldc) or 2
	Space Space
	Cls   Class
	Opt   bool // zero means "absent" (catch_type, super_class, ...)
}

// Entry is one constant-pool entry: its tag, its byte range and the first CP index it occupies.
type Entry struct {
	Tag      byte
	Off, End int64 // [Off, End) includes the tag byte
}

// AttrStat classifies the attributes met while walking.
type AttrStat struct {
	Name  string
	Bytes int64
	Count int
	Deep  bool // true: indexes inside were normalized (NORMALIZED), false: OPAQUE_PRESERVED
}

// AttrRange is the byte range of one attribute (header included).
type AttrRange struct {
	Name     string
	Off, End int64
}

// File is a parsed class file.
type File struct {
	Size    int64
	Major   uint16
	Minor   uint16
	CPCount int
	CP      []Entry // index 1..CPCount-1 (nil entry for 0 and for the second slot of Long/Double)
	CPEnd   int64   // first byte after the constant pool
	Slots   []Slot
	Attrs   map[string]*AttrStat
	Methods int
	Code    int64 // bytes of Code attribute bytecode
	Ranges  []AttrRange
	Lim     Limits
}

type reader struct {
	b   []byte
	p   int64
	err error
}

func (r *reader) fail(f string, a ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: "+f, append([]any{ErrFormat}, a...)...)
	}
}

func (r *reader) need(n int64) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || r.p+n > int64(len(r.b)) || r.p+n < r.p {
		r.fail("read %d at %d past %d", n, r.p, len(r.b))
		return false
	}
	return true
}
func (r *reader) u1() uint32 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.p]
	r.p++
	return uint32(v)
}
func (r *reader) u2() uint32 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.p:])
	r.p += 2
	return uint32(v)
}
func (r *reader) u4() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.p:])
	r.p += 4
	return v
}
func (r *reader) skip(n int64) {
	if r.need(n) {
		r.p += n
	}
}

func bad(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrFormat}, a...)...) }

// Parse reads and validates one class file.
func Parse(src io.ReaderAt, size int64, lim Limits) (*File, []byte, error) {
	lim = lim.norm()
	if size < 24 {
		return nil, nil, bad("short file %d", size)
	}
	if size > lim.MaxClass {
		return nil, nil, fmt.Errorf("%w: class file %d bytes", ErrLimit, size)
	}
	b := make([]byte, size)
	if _, err := src.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, nil, err
	}
	c, err := ParseBytes(b, lim, false)
	return c, b, err
}

// ParseBytes walks class bytes. With masked set, slot values are not read or range-checked (the
// bytes are a masked payload) and attribute names come from the resolve callback of the walker
// configured by the caller (see WalkMasked).
func ParseBytes(b []byte, lim Limits, masked bool) (*File, error) {
	return walk(b, lim.norm(), masked, nil)
}

// resolver lets a masked walk learn an attribute's name while it walks: it receives the slot of
// an attribute_name_index and must write the resolved index into b (or fail).
type resolver func(s *Slot) error

type walker struct {
	r      *reader
	c      *File
	lim    Limits
	masked bool
	res    resolver
	utf8   func(idx uint32) ([]byte, bool) // content of a Utf8 entry
	depth  int
}

func (w *walker) add(off int64, n uint8, sp Space, cl Class, opt bool) uint32 {
	if len(w.c.Slots) >= w.lim.MaxSlots {
		w.r.fail("slots limit")
		return 0
	}
	var v uint32
	if !w.masked {
		if n == 1 {
			v = uint32(w.r.b[off])
		} else {
			v = uint32(binary.BigEndian.Uint16(w.r.b[off:]))
		}
		if v == 0 && opt {
			return 0
		}
		if v == 0 || int(v) >= w.c.CPCount || w.c.CP[v].Tag == 0 || !sp.accepts(w.c.CP[v].Tag) {
			w.r.fail("slot at %d: index %d is not a valid %s entry", off, v, sp)
			return 0
		}
	} else {
		// masked: optional slots are present iff non-zero (the mask is 1)
		var raw uint32
		if n == 1 {
			raw = uint32(w.r.b[off])
		} else {
			raw = uint32(binary.BigEndian.Uint16(w.r.b[off:]))
		}
		if raw == 0 && opt {
			return 0
		}
	}
	s := Slot{Off: off, Val: uint16(v), Len: n, Space: sp, Cls: cl, Opt: opt}
	if w.res != nil && sp == SpAttr {
		if err := w.res(&s); err != nil {
			w.r.fail("%v", err)
			return 0
		}
		if n == 2 {
			v = uint32(binary.BigEndian.Uint16(w.r.b[off:]))
		}
		s.Val = uint16(v)
	}
	w.c.Slots = append(w.c.Slots, s)
	return v
}

// slot2 reads a two-byte slot at the cursor and advances.
func (w *walker) slot2(sp Space, cl Class, opt bool) uint32 {
	if !w.r.need(2) {
		return 0
	}
	off := w.r.p
	w.r.p += 2
	return w.add(off, 2, sp, cl, opt)
}

func walk(b []byte, lim Limits, masked bool, res resolver) (*File, error) {
	return walkWith(b, lim, masked, res)
}

func walkWith(b []byte, lim Limits, masked bool, res resolver) (*File, error) {
	c := &File{Size: int64(len(b)), Lim: lim, Attrs: map[string]*AttrStat{}}
	r := &reader{b: b}
	w := &walker{r: r, c: c, lim: lim, masked: masked, res: res}
	if r.u4() != 0xCAFEBABE {
		return nil, bad("magic")
	}
	c.Minor, c.Major = uint16(r.u2()), uint16(r.u2())
	if r.err != nil {
		return nil, r.err
	}
	if c.Major < 45 || c.Major > 70 {
		return nil, fmt.Errorf("%w: class file version %d.%d", ErrUnsupported, c.Major, c.Minor)
	}
	n := int(r.u2())
	if n < 1 || n > lim.MaxCP {
		return nil, bad("constant_pool_count %d", n)
	}
	c.CPCount = n
	c.CP = make([]Entry, n)
	for i := 1; i < n && r.err == nil; i++ {
		start := r.p
		tag := byte(r.u1())
		two := false
		switch tag {
		case TagUtf8:
			l := r.u2()
			r.skip(int64(l))
		case TagInteger, TagFloat:
			r.skip(4)
		case TagLong, TagDouble:
			r.skip(8)
			two = true
		case TagClass, TagString, TagMethodType, TagModule, TagPackage:
			r.skip(2)
		case TagFieldref, TagMethodref, TagInterfaceMethodref, TagNameAndType, TagDynamic, TagInvokeDynamic:
			r.skip(4)
		case TagMethodHandle:
			r.skip(3)
		default:
			if r.err == nil {
				return nil, fmt.Errorf("%w: constant pool tag %d at index %d", ErrUnsupported, tag, i)
			}
		}
		c.CP[i] = Entry{Tag: tag, Off: start, End: r.p}
		if two {
			i++
			if i >= n {
				return nil, bad("two-slot constant at the end of the pool")
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	c.CPEnd = r.p
	// CP-internal reference slots
	for i := 1; i < n; i++ {
		e := c.CP[i]
		if e.Tag == 0 {
			continue
		}
		p := e.Off + 1
		var sps [2]Space
		var offs [2]int64
		k := 0
		switch e.Tag {
		case TagClass, TagString:
			sps[0], offs[0], k = SpUtf8, p, 1
			if e.Tag == TagClass {
				sps[0] = SpUtf8
			}
		case TagMethodType:
			sps[0], offs[0], k = SpUtf8, p, 1
		case TagModule, TagPackage:
			sps[0], offs[0], k = SpUtf8, p, 1
		case TagNameAndType:
			sps[0], offs[0], sps[1], offs[1], k = SpUtf8, p, SpUtf8, p+2, 2
		case TagFieldref, TagMethodref, TagInterfaceMethodref:
			sps[0], offs[0], sps[1], offs[1], k = SpClass, p, SpNat, p+2, 2
		case TagDynamic, TagInvokeDynamic:
			sps[0], offs[0], k = SpNat, p+2, 1 // bootstrap_method_attr_index at p stays raw
		case TagMethodHandle:
			sps[0], offs[0], k = SpMember, p+1, 1
			if kind := b[p]; kind < 1 || kind > 9 {
				return nil, bad("method handle kind %d", kind)
			}
		}
		for j := 0; j < k; j++ {
			w.add(offs[j], 2, sps[j], ClCP, false)
			if r.err != nil {
				return nil, r.err
			}
		}
	}
	if err := w.classBody(); err != nil {
		return nil, err
	}
	return c, nil
}
