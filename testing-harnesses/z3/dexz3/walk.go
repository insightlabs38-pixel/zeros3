package dexz3

import "fmt"

// Space is the index space a slot refers to.
type Space uint8

const (
	SpNone Space = iota
	SpString
	SpType
	SpProto
	SpField
	SpMethod
	SpOffset // a file offset of an item (not an index space)
	numSpaces
)

func (s Space) String() string {
	return [...]string{"none", "string", "type", "proto", "field", "method", "offset"}[s]
}

// Enc is how a slot's value is stored in the file.
type Enc uint8

const (
	EncU16   Enc = iota // fixed 2 bytes
	EncU32              // fixed 4 bytes
	EncULEB             // ULEB128, Len bytes
	EncP1               // ULEB128 holding value+1 (0 = none), Len bytes
	EncVal              // encoded_value payload: Len little-endian bytes, zero extended
	EncDelta            // ULEB128 holding the difference to the previous slot of the group
)

// Class names the structure a slot lives in (attribution and per-class switches).
type Class uint8

const (
	ClIDTab Class = iota
	ClClassDef
	ClTypeList
	ClClassData
	ClInsn
	ClHandler
	ClCodeHdr
	ClDebug
	ClAnno
	numClasses
)

func (c Class) String() string {
	return [...]string{"idtab", "classdef", "typelist", "classdata", "insn", "handler", "codehdr", "debug", "anno"}[c]
}

// Slot is one index- or offset-bearing field.
type Slot struct {
	Off   int64
	Val   uint32 // absolute value: index, or file offset (0 when walking masked bytes)
	Prev  int32  // EncDelta: previous slot of the same group, -1 for the first
	Len   uint8
	Enc   Enc
	Space Space
	Cls   Class
	Kind  OffKind // offset slots: which field this is (the target section follows from it)
}

// OffKind names an offset field site; the target is always the start of an item of OffTarget[kind].
type OffKind uint8

const (
	OffNone OffKind = iota
	OffString
	OffParams
	OffIface
	OffClassAnno
	OffClassData
	OffStatic
	OffCode
	OffDebug
	OffDirClass
	OffDirField
	OffDirMethod
	OffDirParam
	OffSetEntry
	OffRefEntry
	numOffKinds
)

// OffTarget is the map type of the items an offset kind points at.
var OffTarget = [numOffKinds]uint16{0, TypeStringData, TypeTypeList, TypeTypeList, TypeAnnoDir, TypeClassData, TypeEncodedArray,
	TypeCode, TypeDebugInfo, TypeAnnoSet, TypeAnnoSet, TypeAnnoSet, TypeAnnoSetRef, TypeAnnotation, TypeAnnoSet}

// Item is one counted data item of a map section (units for structural segmentation).
type Item struct {
	Type uint16
	Off  int64
	Len  int64
}

// Walked is the result of walking one DEX.
type Walked struct {
	Slots []Slot
	Items []Item // code, class_data, debug_info, type_list, annotation..., file order
	// StrOff[i] is the start of string_data item i, StrEnd[i] its end (exclusive).
	StrOff, StrEnd []uint32
}

// WalkOpts selects what the walker enumerates.
type WalkOpts struct {
	Masked  bool // the bytes are a masked payload: do not bound-check values, tolerate zeros
	Offsets bool // enumerate offset slots (needed only to attribute or normalize offsets)
	Debug   bool // enumerate debug_info and annotation structures
}

type cur struct {
	b   []byte
	p   int64
	end int64
	err error
	lim *Limits
}

func (c *cur) fail(f string, a ...any) {
	if c.err == nil {
		c.err = fmt.Errorf("%w: "+f, append([]any{ErrFormat}, a...)...)
	}
}

func (c *cur) need(n int64) bool {
	if c.err != nil {
		return false
	}
	if n < 0 || c.p < 0 || c.p > c.end || n > c.end-c.p {
		c.fail("read %d at %d past %d", n, c.p, c.end)
		return false
	}
	return true
}

func (c *cur) u8() uint32 {
	if !c.need(1) {
		return 0
	}
	v := c.b[c.p]
	c.p++
	return uint32(v)
}
func (c *cur) u16() uint32 {
	if !c.need(2) {
		return 0
	}
	v := le16(c.b[c.p:])
	c.p += 2
	return v
}
func (c *cur) u32() uint32 {
	if !c.need(4) {
		return 0
	}
	v := le32(c.b[c.p:])
	c.p += 4
	return v
}

// leb reads a ULEB128 of at most 5 bytes; it returns the value and its byte length.
func (c *cur) leb() (v uint32, n int) {
	var sh uint
	for n < c.lim.MaxLEB {
		if !c.need(1) {
			return 0, n
		}
		b := c.b[c.p]
		c.p++
		n++
		if sh == 28 && b&0xf0 != 0 {
			c.fail("ULEB128 overflows 32 bits")
			return 0, n
		}
		v |= uint32(b&0x7f) << sh
		if b&0x80 == 0 {
			return v, n
		}
		sh += 7
	}
	c.fail("ULEB128 longer than %d bytes", c.lim.MaxLEB)
	return 0, n
}

func (c *cur) sleb() int32 {
	var v uint32
	var sh uint
	for n := 0; n < c.lim.MaxLEB; n++ {
		if !c.need(1) {
			return 0
		}
		b := c.b[c.p]
		c.p++
		v |= uint32(b&0x7f) << sh
		sh += 7
		if b&0x80 == 0 {
			if sh < 32 && b&0x40 != 0 {
				v |= ^uint32(0) << sh
			}
			return int32(v)
		}
	}
	c.fail("SLEB128 too long")
	return 0
}

type walker struct {
	data []byte
	f    *File
	o    WalkOpts
	w    *Walked
	cnt  [numSpaces]uint32
	err  error
}

func (k *walker) add(s Slot) error {
	if len(k.w.Slots) >= k.f.Lim.MaxSlots {
		return fmt.Errorf("%w: slots", ErrLimit)
	}
	if !k.o.Masked && s.Space != SpOffset && s.Space != SpNone && s.Val >= k.cnt[s.Space] {
		return bad("%s index %d >= %d at %d", s.Space, s.Val, k.cnt[s.Space], s.Off)
	}
	if !k.o.Masked && s.Space == SpOffset && (int64(s.Val) < HeaderSize || int64(s.Val) >= k.f.Size) {
		return bad("offset %d outside the file at %d", s.Val, s.Off)
	}
	k.w.Slots = append(k.w.Slots, s)
	return nil
}

// fixed adds a fixed-width slot reading the value from the bytes (value NoIndex = absent).
func (k *walker) fixed(off int64, enc Enc, sp Space, cl Class, optional bool, kind ...OffKind) error {
	var v uint32
	if enc == EncU16 {
		v = le16(k.data[off:])
	} else {
		v = le32(k.data[off:])
	}
	if sp == SpOffset {
		if v == 0 {
			return nil
		}
		if !k.o.Offsets {
			return nil
		}
	} else if enc == EncU32 && v == NoIndex {
		if !optional {
			return bad("NO_INDEX where an index is required at %d", off)
		}
		return nil
	}
	n := uint8(2)
	if enc == EncU32 {
		n = 4
	}
	if k.o.Masked {
		v = 0
	}
	sl := Slot{Off: off, Val: v, Prev: -1, Len: n, Enc: enc, Space: sp, Cls: cl}
	if len(kind) > 0 {
		sl.Kind = kind[0]
	}
	return k.add(sl)
}

func (k *walker) sectionCur(s Span) *cur {
	return &cur{b: k.data, p: s.Off, end: s.Off + s.Len, lim: &k.f.Lim}
}

// Walk enumerates the slots and items of a DEX held in data (one file in memory).
func Walk(f *File, data []byte, o WalkOpts) (*Walked, error) {
	if int64(len(data)) != f.Size {
		return nil, bad("walk: data length %d != %d", len(data), f.Size)
	}
	k := &walker{data: data, f: f, o: o, w: &Walked{}}
	k.cnt = [numSpaces]uint32{0, f.Hdr.StringIDs, f.Hdr.TypeIDs, f.Hdr.ProtoIDs, f.Hdr.FieldIDs, f.Hdr.MethodIDs, 0}
	for _, s := range f.Spans {
		var err error
		switch s.Type {
		case TypeStringID:
			err = k.idTable(s, 4, func(off int64) error { return k.fixed(off, EncU32, SpOffset, ClIDTab, false, OffString) })
		case TypeTypeID:
			err = k.idTable(s, 4, func(off int64) error { return k.fixed(off, EncU32, SpString, ClIDTab, false) })
		case TypeProtoID:
			err = k.idTable(s, 12, func(off int64) error {
				if e := k.fixed(off, EncU32, SpString, ClIDTab, false); e != nil {
					return e
				}
				if e := k.fixed(off+4, EncU32, SpType, ClIDTab, false); e != nil {
					return e
				}
				return k.fixed(off+8, EncU32, SpOffset, ClIDTab, false, OffParams)
			})
		case TypeFieldID:
			err = k.idTable(s, 8, func(off int64) error {
				if e := k.fixed(off, EncU16, SpType, ClIDTab, false); e != nil {
					return e
				}
				if e := k.fixed(off+2, EncU16, SpType, ClIDTab, false); e != nil {
					return e
				}
				return k.fixed(off+4, EncU32, SpString, ClIDTab, false)
			})
		case TypeMethodID:
			err = k.idTable(s, 8, func(off int64) error {
				if e := k.fixed(off, EncU16, SpType, ClIDTab, false); e != nil {
					return e
				}
				if e := k.fixed(off+2, EncU16, SpProto, ClIDTab, false); e != nil {
					return e
				}
				return k.fixed(off+4, EncU32, SpString, ClIDTab, false)
			})
		case TypeClassDef:
			err = k.idTable(s, 32, func(off int64) error {
				for _, a := range []struct {
					d    int64
					sp   Space
					opt  bool
					kind OffKind
				}{{0, SpType, false, 0}, {8, SpType, true, 0}, {12, SpOffset, true, OffIface}, {16, SpString, true, 0}, {20, SpOffset, true, OffClassAnno}, {24, SpOffset, true, OffClassData}, {28, SpOffset, true, OffStatic}} {
					if e := k.fixed(off+a.d, EncU32, a.sp, ClClassDef, a.opt, a.kind); e != nil {
						return e
					}
				}
				return nil
			})
		case TypeTypeList:
			err = k.typeLists(s)
		case TypeClassData:
			err = k.classData(s)
		case TypeCode:
			err = k.codeItems(s)
		case TypeStringData:
			err = k.stringData(s)
		case TypeDebugInfo, TypeAnnotation, TypeEncodedArray, TypeAnnoSet, TypeAnnoSetRef, TypeAnnoDir:
			if o.Debug {
				err = k.dataItems(s)
			} else {
				err = k.itemsOnly(s)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return k.w, nil
}

func (k *walker) idTable(s Span, w int64, one func(off int64) error) error {
	for i := int64(0); i < int64(s.Count); i++ {
		if err := one(s.Off + i*w); err != nil {
			return err
		}
	}
	return nil
}

func align(p, a int64) int64 { return (p + a - 1) &^ (a - 1) }

func (k *walker) typeLists(s Span) error {
	c := k.sectionCur(s)
	for i := uint32(0); i < s.Count; i++ {
		c.p = align(c.p, 4)
		start := c.p
		n := c.u32()
		if c.err != nil {
			return c.err
		}
		if int64(n) > (c.end-c.p)/2 {
			return bad("type_list size %d", n)
		}
		for j := uint32(0); j < n; j++ {
			if err := k.fixed(c.p, EncU16, SpType, ClTypeList, false); err != nil {
				return err
			}
			c.p += 2
		}
		k.w.Items = append(k.w.Items, Item{TypeTypeList, start, c.p - start})
	}
	return c.err
}

func (k *walker) stringData(s Span) error {
	c := k.sectionCur(s)
	k.w.StrOff = make([]uint32, 0, s.Count)
	k.w.StrEnd = make([]uint32, 0, s.Count)
	for i := uint32(0); i < s.Count; i++ {
		start := c.p
		c.leb()
		for {
			if !c.need(1) {
				return c.err
			}
			if c.p-start > int64(k.f.Lim.MaxStrLen) {
				return fmt.Errorf("%w: string length", ErrLimit)
			}
			b := c.b[c.p]
			c.p++
			if b == 0 {
				break
			}
		}
		k.w.StrOff = append(k.w.StrOff, uint32(start))
		k.w.StrEnd = append(k.w.StrEnd, uint32(c.p))
	}
	return c.err
}

func (k *walker) classData(s Span) error {
	c := k.sectionCur(s)
	for i := uint32(0); i < s.Count; i++ {
		start := c.p
		var sizes [4]uint32
		for j := range sizes {
			sizes[j], _ = c.leb()
		}
		if c.err != nil {
			return c.err
		}
		tot := uint64(sizes[0]) + uint64(sizes[1]) + uint64(sizes[2]) + uint64(sizes[3])
		if tot > uint64(k.f.Lim.MaxMembers) {
			return fmt.Errorf("%w: class_data members %d", ErrLimit, tot)
		}
		for g := 0; g < 4; g++ {
			prev := int32(-1)
			var abs uint32
			for m := uint32(0); m < sizes[g]; m++ {
				off := c.p
				d, n := c.leb()
				if c.err != nil {
					return c.err
				}
				sp := SpField
				if g >= 2 {
					sp = SpMethod
				}
				if !k.o.Masked {
					if m > 0 && d == 0 {
						return bad("class_data member delta 0 at %d", off)
					}
					if uint64(abs)+uint64(d) > 1<<32-1 {
						return bad("class_data index overflow")
					}
					abs += d
				}
				if err := k.add(Slot{Off: off, Val: abs, Prev: prev, Len: uint8(n), Enc: EncDelta, Space: sp, Cls: ClClassData}); err != nil {
					return err
				}
				prev = int32(len(k.w.Slots) - 1)
				c.leb() // access_flags
				if g >= 2 {
					coff := c.p
					v, n := c.leb()
					if c.err != nil {
						return c.err
					}
					if v != 0 && k.o.Offsets {
						if err := k.add(Slot{Off: coff, Val: v, Prev: -1, Len: uint8(n), Enc: EncULEB, Space: SpOffset, Cls: ClClassData, Kind: OffCode}); err != nil {
							return err
						}
					}
				}
			}
		}
		if c.err != nil {
			return c.err
		}
		k.w.Items = append(k.w.Items, Item{TypeClassData, start, c.p - start})
	}
	return c.err
}

func (k *walker) codeItems(s Span) error {
	c := k.sectionCur(s)
	lim := &k.f.Lim
	for i := uint32(0); i < s.Count; i++ {
		c.p = align(c.p, 4)
		start := c.p
		if !c.need(16) {
			return c.err
		}
		tries := le16(k.data[start+6:])
		if err := k.fixed(start+8, EncU32, SpOffset, ClCodeHdr, false, OffDebug); err != nil {
			return err
		}
		isz := le32(k.data[start+12:])
		if int64(isz) > int64(lim.MaxCodeUnit) {
			return fmt.Errorf("%w: insns_size %d", ErrLimit, isz)
		}
		c.p = start + 16
		if !c.need(int64(isz) * 2) {
			return c.err
		}
		ib := k.data[c.p : c.p+int64(isz)*2]
		base := c.p
		if err := walkInsns(ib, int(isz), func(u int, sp Space, wide bool) error {
			n, enc := uint8(2), EncU16
			var v uint32
			if wide {
				n, enc = 4, EncU32
				v = le32(ib[2*u:])
			} else {
				v = le16(ib[2*u:])
			}
			if k.o.Masked {
				v = 0
			}
			return k.add(Slot{Off: base + int64(2*u), Val: v, Prev: -1, Len: n, Enc: enc, Space: sp, Cls: ClInsn})
		}); err != nil {
			return err
		}
		c.p += int64(isz) * 2
		if tries != 0 {
			if int(tries) > lim.MaxTries {
				return fmt.Errorf("%w: tries %d", ErrLimit, tries)
			}
			if isz%2 == 1 {
				c.p += 2
			}
			if !c.need(int64(tries) * 8) {
				return c.err
			}
			c.p += int64(tries) * 8
			hn, _ := c.leb()
			if int(hn) > lim.MaxHandlers {
				return fmt.Errorf("%w: handlers %d", ErrLimit, hn)
			}
			for h := uint32(0); h < hn; h++ {
				sz := c.sleb()
				if c.err != nil {
					return c.err
				}
				np := int64(sz)
				if np < 0 {
					np = -np
				}
				if np > int64(lim.MaxHandlers) {
					return fmt.Errorf("%w: handler pairs %d", ErrLimit, np)
				}
				for j := int64(0); j < np; j++ {
					off := c.p
					v, n := c.leb()
					if c.err != nil {
						return c.err
					}
					if k.o.Masked {
						v = 0
					}
					if err := k.add(Slot{Off: off, Val: v, Prev: -1, Len: uint8(n), Enc: EncULEB, Space: SpType, Cls: ClHandler}); err != nil {
						return err
					}
					c.leb()
				}
				if sz <= 0 {
					c.leb()
				}
			}
		}
		if c.err != nil {
			return c.err
		}
		k.w.Items = append(k.w.Items, Item{TypeCode, start, c.p - start})
	}
	return c.err
}

// itemsOnly records item boundaries of sections whose slots are not enumerated (opaque payload).
func (k *walker) itemsOnly(s Span) error { return k.dataItems2(s, false) }
func (k *walker) dataItems(s Span) error { return k.dataItems2(s, true) }

func (k *walker) dataItems2(s Span, slots bool) error {
	c := k.sectionCur(s)
	for i := uint32(0); i < s.Count; i++ {
		switch s.Type {
		case TypeAnnoSet, TypeAnnoSetRef, TypeAnnoDir:
			c.p = align(c.p, 4)
		}
		start := c.p
		switch s.Type {
		case TypeDebugInfo:
			k.debugInfo(c, slots)
		case TypeAnnotation:
			c.u8()
			k.encAnnotation(c, 0, slots)
		case TypeEncodedArray:
			k.encArray(c, 0, slots)
		case TypeAnnoSet, TypeAnnoSetRef:
			n := c.u32()
			if int64(n) > (c.end-c.p)/4 {
				c.fail("annotation set size %d", n)
			}
			for j := uint32(0); j < n && c.err == nil; j++ {
				if slots && k.o.Offsets {
					kd := OffSetEntry
					if s.Type == TypeAnnoSetRef {
						kd = OffRefEntry
					}
					if err := k.fixed(c.p, EncU32, SpOffset, ClAnno, false, kd); err != nil {
						return err
					}
				}
				c.p += 4
			}
		case TypeAnnoDir:
			if !c.need(16) {
				return c.err
			}
			if slots && k.o.Offsets {
				if err := k.fixed(c.p, EncU32, SpOffset, ClAnno, false, OffDirClass); err != nil {
					return err
				}
			}
			nf, nm, np := le32(k.data[c.p+4:]), le32(k.data[c.p+8:]), le32(k.data[c.p+12:])
			c.p += 16
			tot := uint64(nf) + uint64(nm) + uint64(np)
			if tot > uint64(c.end-c.p)/8 {
				c.fail("annotation directory size")
			}
			for j := uint64(0); j < tot && c.err == nil; j++ {
				if slots {
					sp := SpField
					if j >= uint64(nf) {
						sp = SpMethod
					}
					if err := k.fixed(c.p, EncU32, sp, ClAnno, false); err != nil {
						return err
					}
					if k.o.Offsets {
						kd := OffDirField
						if j >= uint64(nf) {
							kd = OffDirMethod
						}
						if j >= uint64(nf)+uint64(nm) {
							kd = OffDirParam
						}
						if err := k.fixed(c.p+4, EncU32, SpOffset, ClAnno, false, kd); err != nil {
							return err
						}
					}
				}
				c.p += 8
			}
		}
		if c.err != nil {
			return c.err
		}
		k.w.Items = append(k.w.Items, Item{s.Type, start, c.p - start})
	}
	return c.err
}

// p1 adds a ULEB128p1 string/type slot (value 0 = none).
func (k *walker) leb1(c *cur, sp Space, cl Class, slots bool) {
	off := c.p
	v, n := c.leb()
	if c.err != nil || !slots {
		return
	}
	if v == 0 && (n == 1 || !k.o.Masked) {
		if n != 1 {
			c.fail("non-canonical NO_INDEX at %d", off)
		}
		return
	}
	if k.o.Masked {
		v = 0
	} else {
		v--
	}
	if err := k.add(Slot{Off: off, Val: v, Prev: -1, Len: uint8(n), Enc: EncP1, Space: sp, Cls: cl}); err != nil {
		c.fail("%v", err)
	}
}

func (k *walker) lebSlot(c *cur, sp Space, cl Class, slots bool) {
	off := c.p
	v, n := c.leb()
	if c.err != nil || !slots {
		return
	}
	if k.o.Masked {
		v = 0
	}
	if err := k.add(Slot{Off: off, Val: v, Prev: -1, Len: uint8(n), Enc: EncULEB, Space: sp, Cls: cl}); err != nil {
		c.fail("%v", err)
	}
}

func (k *walker) debugInfo(c *cur, slots bool) {
	c.leb()
	np, _ := c.leb()
	if int64(np) > c.end-c.p {
		c.fail("debug parameters")
		return
	}
	for i := uint32(0); i < np && c.err == nil; i++ {
		k.leb1(c, SpString, ClDebug, slots)
	}
	for ops := 0; c.err == nil; ops++ {
		if ops > k.f.Lim.MaxDebugOps {
			c.fail("debug opcodes")
			return
		}
		switch op := c.u8(); {
		case c.err != nil:
		case op == 0:
			return
		case op == 1, op == 5, op == 6:
			c.leb()
		case op == 2:
			c.sleb()
		case op == 3, op == 4:
			c.leb()
			k.leb1(c, SpString, ClDebug, slots)
			k.leb1(c, SpType, ClDebug, slots)
			if op == 4 {
				k.leb1(c, SpString, ClDebug, slots)
			}
		case op == 9:
			k.leb1(c, SpString, ClDebug, slots)
		}
	}
}

func (k *walker) encAnnotation(c *cur, depth int, slots bool) {
	if depth > k.f.Lim.MaxDepth {
		c.fail("annotation nesting")
		return
	}
	k.lebSlot(c, SpType, ClAnno, slots)
	n, _ := c.leb()
	if int64(n) > c.end-c.p {
		c.fail("annotation elements")
		return
	}
	for i := uint32(0); i < n && c.err == nil; i++ {
		k.lebSlot(c, SpString, ClAnno, slots)
		k.encValue(c, depth+1, slots)
	}
}

func (k *walker) encArray(c *cur, depth int, slots bool) {
	if depth > k.f.Lim.MaxDepth {
		c.fail("array nesting")
		return
	}
	n, _ := c.leb()
	if int64(n) > c.end-c.p {
		c.fail("array size")
		return
	}
	for i := uint32(0); i < n && c.err == nil; i++ {
		k.encValue(c, depth+1, slots)
	}
}

func (k *walker) encValue(c *cur, depth int, slots bool) {
	hb := c.u8()
	if c.err != nil {
		return
	}
	t, arg := hb&0x1f, int64(hb>>5)
	size := arg + 1
	sp := SpNone
	switch t {
	case 0x00:
		if arg != 0 {
			c.fail("byte value arg")
		}
	case 0x02, 0x03:
		if arg > 1 {
			c.fail("short/char arg")
		}
	case 0x04, 0x10:
		if arg > 3 {
			c.fail("int/float arg")
		}
	case 0x06, 0x11:
	case 0x15:
		sp = SpProto
	case 0x16:
		// method handle table not modelled: raw
		if arg > 3 {
			c.fail("handle arg")
		}
	case 0x17:
		sp = SpString
	case 0x18:
		sp = SpType
	case 0x19, 0x1b:
		sp = SpField
	case 0x1a:
		sp = SpMethod
	case 0x1c, 0x1d, 0x1e, 0x1f:
		if (t != 0x1f && arg != 0) || (t == 0x1f && arg > 1) {
			c.fail("value arg")
		}
		switch t {
		case 0x1c:
			k.encArray(c, depth, slots)
		case 0x1d:
			k.encAnnotation(c, depth, slots)
		}
		return
	default:
		c.fail("encoded value type %#x", t)
	}
	if sp != SpNone && arg > 3 {
		c.fail("index value arg")
	}
	if c.err != nil || !c.need(size) {
		return
	}
	if sp != SpNone && slots {
		var v uint32
		if !k.o.Masked {
			for i := int64(0); i < size; i++ {
				v |= uint32(c.b[c.p+i]) << (8 * uint(i))
			}
		}
		if err := k.add(Slot{Off: c.p, Val: v, Prev: -1, Len: uint8(size), Enc: EncVal, Space: sp, Cls: ClAnno}); err != nil {
			c.fail("%v", err)
		}
	}
	c.p += size
}
