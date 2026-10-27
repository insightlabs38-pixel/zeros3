package jvmz3

import "fmt"

func (w *walker) utf8Name(idx uint32) string {
	if idx == 0 || int(idx) >= w.c.CPCount {
		return ""
	}
	e := w.c.CP[idx]
	if e.Tag != TagUtf8 || e.End-e.Off < 3 {
		return ""
	}
	return string(w.r.b[e.Off+3 : e.End])
}

func (w *walker) stat(name string, bytes int64, deep bool) {
	s := w.c.Attrs[name]
	if s == nil {
		s = &AttrStat{Name: name, Deep: deep}
		w.c.Attrs[name] = s
	}
	s.Count++
	s.Bytes += bytes
}

func (w *walker) classBody() error {
	r := w.r
	r.u2() // access_flags
	w.slot2(SpClass, ClHdr, false)
	w.slot2(SpClass, ClHdr, true)
	ni := int(r.u2())
	for i := 0; i < ni && r.err == nil; i++ {
		w.slot2(SpClass, ClHdr, false)
	}
	for kind := 0; kind < 2 && r.err == nil; kind++ { // fields, methods
		n := int(r.u2())
		if n > w.lim.MaxMembers {
			return fmt.Errorf("%w: members %d", ErrLimit, n)
		}
		for i := 0; i < n && r.err == nil; i++ {
			r.u2() // access_flags
			w.slot2(SpUtf8, ClMember, false)
			w.slot2(SpUtf8, ClMember, false)
			if kind == 1 {
				w.c.Methods++
			}
			w.attributes(kind == 1, 0)
		}
	}
	w.attributes(false, 0)
	if r.err != nil {
		return r.err
	}
	if r.p != int64(len(r.b)) {
		return bad("%d trailing bytes", int64(len(r.b))-r.p)
	}
	return nil
}

// attributes walks an attribute table. inMethod selects the Code attribute handler.
func (w *walker) attributes(inMethod bool, depth int) {
	r := w.r
	n := int(r.u2())
	if n > w.lim.MaxAttrs {
		r.fail("attribute count %d", n)
		return
	}
	for i := 0; i < n && r.err == nil; i++ {
		start := r.p
		nameIdx := w.slot2(SpAttr, ClAttrName, false)
		if r.err != nil {
			return
		}
		ln := r.u4()
		if r.err != nil {
			return
		}
		if int64(ln) > int64(len(r.b))-r.p {
			r.fail("attribute length %d overruns the file", ln)
			return
		}
		end := r.p + int64(ln)
		name := w.utf8Name(nameIdx)
		deep := w.attr(name, inMethod, end, depth)
		if r.err != nil {
			return
		}
		if deep && r.p != end {
			r.fail("attribute %s consumed %d of %d bytes", name, r.p-(end-int64(ln)), ln)
			return
		}
		r.p = end
		w.stat(name, end-start, deep)
		w.c.Ranges = append(w.c.Ranges, AttrRange{name, start, end})
	}
}

// attr walks one attribute body; it returns false when the attribute is kept as opaque bytes.
func (w *walker) attr(name string, inMethod bool, end int64, depth int) bool {
	r := w.r
	switch name {
	case "Code":
		if !inMethod {
			return false
		}
		w.code(end, depth)
	case "ConstantValue":
		w.slot2(SpLoad, ClAttr, false)
	case "Signature", "SourceFile":
		w.slot2(SpUtf8, ClAttr, false)
	case "Exceptions", "NestMembers", "PermittedSubclasses":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.slot2(SpClass, ClAttr, false)
		}
	case "NestHost":
		w.slot2(SpClass, ClAttr, false)
	case "InnerClasses":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.slot2(SpClass, ClAttr, false)
			w.slot2(SpClass, ClAttr, true)
			w.slot2(SpUtf8, ClAttr, true)
			r.u2()
		}
	case "EnclosingMethod":
		w.slot2(SpClass, ClAttr, false)
		w.slot2(SpNat, ClAttr, true)
	case "BootstrapMethods":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.slot2(SpLoad, ClAttr, false)
			for k := int(r.u2()); k > 0 && r.err == nil; k-- {
				w.slot2(SpLoad, ClAttr, false)
			}
		}
	case "MethodParameters":
		for n := int(r.u1()); n > 0 && r.err == nil; n-- {
			w.slot2(SpUtf8, ClAttr, true)
			r.u2()
		}
	case "LineNumberTable":
		n := int64(r.u2())
		r.skip(4 * n)
	case "Deprecated", "Synthetic":
	case "LocalVariableTable":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			r.skip(4)
			w.slot2(SpUtf8, ClLocals, false)
			w.slot2(SpUtf8, ClLocals, false)
			r.u2()
		}
	case "LocalVariableTypeTable":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			r.skip(4)
			w.slot2(SpUtf8, ClLocals, false)
			w.slot2(SpUtf8, ClLocals, false)
			r.u2()
		}
	case "StackMapTable":
		w.stackMap()
	case "RuntimeVisibleAnnotations", "RuntimeInvisibleAnnotations":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.annotation(0)
		}
	case "RuntimeVisibleParameterAnnotations", "RuntimeInvisibleParameterAnnotations":
		for p := int(r.u1()); p > 0 && r.err == nil; p-- {
			for n := int(r.u2()); n > 0 && r.err == nil; n-- {
				w.annotation(0)
			}
		}
	case "AnnotationDefault":
		w.elementValue(0)
	case "Record":
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.slot2(SpUtf8, ClAttr, false)
			w.slot2(SpUtf8, ClAttr, false)
			if depth > w.lim.MaxDepth {
				r.fail("attribute nesting")
				return true
			}
			w.attributes(false, depth+1)
		}
	default:
		return false
	}
	return true
}

func (w *walker) annotation(depth int) {
	r := w.r
	if depth > w.lim.MaxDepth {
		r.fail("annotation nesting")
		return
	}
	w.slot2(SpUtf8, ClAnno, false)
	for n := int(r.u2()); n > 0 && r.err == nil; n-- {
		w.slot2(SpUtf8, ClAnno, false)
		w.elementValue(depth + 1)
	}
}

func (w *walker) elementValue(depth int) {
	r := w.r
	if depth > w.lim.MaxDepth {
		r.fail("element_value nesting")
		return
	}
	switch tag := byte(r.u1()); tag {
	case 'B', 'C', 'I', 'S', 'Z':
		w.slot2(SpInt, ClAnno, false)
	case 'D':
		w.slot2(SpDouble, ClAnno, false)
	case 'F':
		w.slot2(SpFloat, ClAnno, false)
	case 'J':
		w.slot2(SpLong, ClAnno, false)
	case 's':
		w.slot2(SpUtf8, ClAnno, false)
	case 'e':
		w.slot2(SpUtf8, ClAnno, false)
		w.slot2(SpUtf8, ClAnno, false)
	case 'c':
		w.slot2(SpUtf8, ClAnno, false)
	case '@':
		w.annotation(depth + 1)
	case '[':
		for n := int(r.u2()); n > 0 && r.err == nil; n-- {
			w.elementValue(depth + 1)
		}
	default:
		if r.err == nil {
			r.fail("element_value tag %#x", tag)
		}
	}
}

func (w *walker) stackMap() {
	r := w.r
	vt := func() {
		switch t := r.u1(); {
		case t <= 6:
		case t == 7:
			w.slot2(SpClass, ClStack, false)
		case t == 8:
			r.u2()
		default:
			r.fail("verification type %d", t)
		}
	}
	n := int(r.u2())
	if n > w.lim.MaxFrames {
		r.fail("StackMapTable entries %d", n)
		return
	}
	for i := 0; i < n && r.err == nil; i++ {
		switch ft := r.u1(); {
		case ft <= 63:
		case ft <= 127:
			vt()
		case ft == 247:
			r.u2()
			vt()
		case ft >= 248 && ft <= 251:
			r.u2()
		case ft >= 252 && ft <= 254:
			r.u2()
			for k := int(ft) - 251; k > 0 && r.err == nil; k-- {
				vt()
			}
		case ft == 255:
			r.u2()
			for k := int(r.u2()); k > 0 && r.err == nil; k-- {
				vt()
			}
			for k := int(r.u2()); k > 0 && r.err == nil; k-- {
				vt()
			}
		default:
			r.fail("frame type %d", ft)
		}
	}
}

// opcode lengths: >0 fixed, 0 variable (tableswitch, lookupswitch, wide), -1 invalid
var opLen [256]int8

var opSpace [256]Space

func init() {
	for i := range opLen {
		opLen[i] = -1
	}
	set := func(lo, hi int, n int8) {
		for i := lo; i <= hi; i++ {
			opLen[i] = n
		}
	}
	set(0x00, 0x0f, 1)
	set(0x10, 0x10, 2)
	set(0x11, 0x11, 3)
	set(0x12, 0x12, 2)
	set(0x13, 0x14, 3)
	set(0x15, 0x19, 2)
	set(0x1a, 0x35, 1)
	set(0x36, 0x3a, 2)
	set(0x3b, 0x83, 1)
	set(0x84, 0x84, 3)
	set(0x85, 0x98, 1)
	set(0x99, 0xa8, 3)
	set(0xa9, 0xa9, 2)
	set(0xaa, 0xab, 0)
	set(0xac, 0xb1, 1)
	set(0xb2, 0xb8, 3)
	set(0xb9, 0xba, 5)
	set(0xbb, 0xbb, 3)
	set(0xbc, 0xbc, 2)
	set(0xbd, 0xbd, 3)
	set(0xbe, 0xbf, 1)
	set(0xc0, 0xc1, 3)
	set(0xc2, 0xc3, 1)
	set(0xc4, 0xc4, 0)
	set(0xc5, 0xc5, 4)
	set(0xc6, 0xc7, 3)
	set(0xc8, 0xc9, 5)
	set(0xca, 0xca, 1)
	set(0xfe, 0xff, 1)
	opSpace[0x12], opSpace[0x13], opSpace[0x14] = SpLoad, SpLoad, SpLoad
	for _, o := range []int{0xb2, 0xb3, 0xb4, 0xb5} {
		opSpace[o] = SpField
	}
	for _, o := range []int{0xb6, 0xb7, 0xb8, 0xb9} {
		opSpace[o] = SpMethod
	}
	opSpace[0xba] = SpInvDyn
	for _, o := range []int{0xbb, 0xbd, 0xc0, 0xc1, 0xc5} {
		opSpace[o] = SpClass
	}
}

func (w *walker) code(end int64, depth int) {
	r := w.r
	r.u2() // max_stack
	r.u2() // max_locals
	cl := int64(r.u4())
	if r.err != nil {
		return
	}
	if cl < 1 || cl > int64(w.lim.MaxCode) || cl > end-r.p {
		r.fail("code_length %d", cl)
		return
	}
	base := r.p
	w.c.Code += cl
	for pc := int64(0); pc < cl && r.err == nil; {
		op := r.b[base+pc]
		n := int64(opLen[op])
		if n < 0 {
			r.fail("invalid opcode %#x at pc %d", op, pc)
			return
		}
		if n == 0 {
			switch op {
			case 0xc4: // wide
				if pc+1 >= cl {
					r.fail("wide at the end of the code")
					return
				}
				switch r.b[base+pc+1] {
				case 0x84:
					n = 6
				case 0x15, 0x16, 0x17, 0x18, 0x19, 0x36, 0x37, 0x38, 0x39, 0x3a, 0xa9:
					n = 4
				default:
					r.fail("bad wide opcode %#x", r.b[base+pc+1])
					return
				}
			default: // tableswitch / lookupswitch
				p := (pc + 4) &^ 3
				if p+12 > cl {
					r.fail("switch overruns the code")
					return
				}
				if op == 0xaa {
					lo, hi := int32(be32(r.b[base+p+4:])), int32(be32(r.b[base+p+8:]))
					if hi < lo || int64(hi)-int64(lo)+1 > int64(cl) {
						r.fail("tableswitch range")
						return
					}
					n = p + 12 + (int64(hi)-int64(lo)+1)*4 - pc
				} else {
					np := int64(int32(be32(r.b[base+p+4:])))
					if np < 0 || np > int64(cl) {
						r.fail("lookupswitch pairs")
						return
					}
					n = p + 8 + np*8 - pc
				}
			}
		}
		if pc+n > cl {
			r.fail("instruction at pc %d overruns the code", pc)
			return
		}
		if sp := opSpace[op]; sp != SpNone {
			if op == 0x12 {
				w.add(base+pc+1, 1, sp, ClCode, false)
			} else {
				w.add(base+pc+1, 2, sp, ClCode, false)
			}
		}
		pc += n
	}
	r.p = base + cl
	// exception table
	ne := int(r.u2())
	for i := 0; i < ne && r.err == nil; i++ {
		r.skip(6)
		w.slot2(SpClass, ClExc, true)
	}
	if depth > w.lim.MaxDepth {
		r.fail("attribute nesting")
		return
	}
	w.attributes(false, depth+1)
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
