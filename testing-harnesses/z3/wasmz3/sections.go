package wasmz3

import "fmt"

// Section content parsers and the function-body instruction walker. Recognized-but-unsupported
// constructs (GC types, typed references, memory64, multi-memory) set gcOrWide so the caller can
// tell "unsupported" from "malformed".

func (a *an) fail() bool { a.m.gcOrWide = true; return false }

func (a *an) typeSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		if p >= end {
			return false
		}
		if a.d[p] != 0x60 {
			return a.fail()
		}
		t := FuncType{Off: int64(p)}
		p++
		for k := 0; k < 2; k++ {
			cnt, q, ok := a.vecCount(p, end)
			if !ok || q+cnt > end {
				return false
			}
			vt := a.d[q : q+cnt]
			for _, c := range vt {
				if !isValType(c) {
					return a.fail()
				}
			}
			p = q + cnt
			if k == 0 {
				t.Params = vt
			} else {
				t.Results = vt
			}
		}
		t.End = int64(p)
		a.m.Types = append(a.m.Types, t)
	}
	return p == end
}

func (a *an) importSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		im := Import{Off: int64(p)}
		if im.Mod, p, ok = a.name(p, end); !ok {
			return false
		}
		if im.Name, p, ok = a.name(p, end); !ok {
			return false
		}
		if p >= end {
			return false
		}
		im.Kind = a.d[p]
		p++
		ds := p
		switch im.Kind {
		case 0:
			v, w, ok := u32(a.d, p)
			if !ok {
				return false
			}
			a.slot(p, w, SpType, false, v)
			im.TypeIdx = v
			p += w
		case 1:
			if p, ok = a.tableType(p, end); !ok {
				return a.fail()
			}
			im.Desc = a.d[ds:p]
		case 2:
			if p, ok = a.limits(p, end); !ok {
				return a.fail()
			}
			im.Desc = a.d[ds:p]
		case 3:
			if p, ok = a.globalType(p, end); !ok {
				return a.fail()
			}
			im.Desc = a.d[ds:p]
		case 4:
			if p >= end || a.d[p] != 0 {
				return false
			}
			im.Desc = a.d[p : p+1]
			p++
			v, w, ok := u32(a.d, p)
			if !ok {
				return false
			}
			a.slot(p, w, SpType, false, v)
			im.TypeIdx = v
			p += w
		default:
			return false
		}
		im.End = int64(p)
		a.m.NImport[im.Kind]++
		a.m.Imports = append(a.m.Imports, im)
	}
	return p == end
}

func (a *an) funcSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		v, w, ok := u32(a.d, p)
		if !ok || p+w > end {
			return false
		}
		a.slot(p, w, SpType, false, v)
		a.m.Funcs = append(a.m.Funcs, v)
		p += w
	}
	return p == end
}

func (a *an) tableSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		s := p
		if p < end && a.d[p] == 0x40 { // table with an initializer
			if p+2 > end || a.d[p+1] != 0 {
				return false
			}
			if p, ok = a.tableType(p+2, end); !ok {
				return a.fail()
			}
			if p, ok = a.constExpr(p, end); !ok {
				return false
			}
		} else if p, ok = a.tableType(p, end); !ok {
			return a.fail()
		}
		a.m.Tables = append(a.m.Tables, Region{int64(s), int64(p)})
	}
	return p == end
}

func (a *an) memSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		s := p
		if p, ok = a.limits(p, end); !ok {
			return a.fail()
		}
		a.m.Mems = append(a.m.Mems, Region{int64(s), int64(p)})
	}
	return p == end
}

func (a *an) tagSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		if p >= end || a.d[p] != 0 {
			return false
		}
		p++
		v, w, ok := u32(a.d, p)
		if !ok || p+w > end {
			return false
		}
		a.slot(p, w, SpType, false, v)
		a.m.Tags = append(a.m.Tags, v)
		p += w
	}
	return p == end
}

func (a *an) globalSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		g := Global{Off: int64(p)}
		var q int
		if q, ok = a.globalType(p, end); !ok {
			return a.fail()
		}
		g.ValType, g.Mutable = a.d[p], a.d[p+1]
		g.InitOff = int64(q)
		if p, ok = a.constExpr(q, end); !ok {
			return false
		}
		g.InitEnd, g.End = int64(p), int64(p)
		a.m.Globals = append(a.m.Globals, g)
	}
	return p == end
}

func (a *an) exportSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		e := Export{Off: int64(p)}
		if e.Name, p, ok = a.name(p, end); !ok {
			return false
		}
		if p >= end {
			return false
		}
		e.Kind = a.d[p]
		p++
		sp := [...]Space{SpFunc, SpTable, SpMem, SpGlobal, SpTag}
		if e.Kind > 4 {
			return false
		}
		v, w, ok := u32(a.d, p)
		if !ok || p+w > end {
			return false
		}
		a.slot(p, w, sp[e.Kind], false, v)
		e.Idx = v
		p += w
		e.End = int64(p)
		a.m.Exports = append(a.m.Exports, e)
	}
	return p == end
}

func (a *an) funcVec(p, end int) (int, int, bool) {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return 0, p, false
	}
	for i := 0; i < n; i++ {
		if p, ok = a.idx(p, SpFunc); !ok || p > end {
			return 0, p, false
		}
	}
	return n, p, true
}

func (a *an) exprVec(p, end int) (int, int, bool) {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return 0, p, false
	}
	for i := 0; i < n; i++ {
		if p, ok = a.constExpr(p, end); !ok {
			return 0, p, false
		}
	}
	return n, p, true
}

func (a *an) elemSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		s := Seg{Off: int64(p)}
		fl, w, ok := u32(a.d, p)
		if !ok || fl > 7 {
			return false
		}
		s.Flags = fl
		p += w
		s.Active = fl&1 == 0
		if fl&3 == 2 { // explicit table index
			if p, ok = a.idx(p, SpTable); !ok {
				return false
			}
		}
		if s.Active {
			s.ExprOff = int64(p)
			if p, ok = a.constExpr(p, end); !ok {
				return false
			}
			s.ExprEnd = int64(p)
		}
		if fl&3 != 0 { // elemkind or reftype byte
			if p >= end {
				return false
			}
			if fl&4 == 0 && a.d[p] != 0 || fl&4 != 0 && !isValType(a.d[p]) {
				return a.fail()
			}
			p++
		}
		s.EntryStart = int64(p)
		if fl&4 == 0 {
			s.NFuncs, p, ok = a.funcVec(p, end)
		} else {
			s.ExprFuncs = true
			s.NFuncs, p, ok = a.exprVec(p, end)
		}
		if !ok {
			return false
		}
		s.End = int64(p)
		a.m.Elems = append(a.m.Elems, s)
	}
	return p == end
}

func (a *an) dataSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		s := Seg{Off: int64(p)}
		fl, w, ok := u32(a.d, p)
		if !ok || fl > 2 {
			return false
		}
		s.Flags = fl
		p += w
		s.Active = fl != 1
		if fl == 2 {
			if p, ok = a.idx(p, SpMem); !ok {
				return false
			}
		}
		if s.Active {
			s.ExprOff = int64(p)
			if p, ok = a.constExpr(p, end); !ok {
				return false
			}
			s.ExprEnd = int64(p)
		}
		l, w, ok := u32(a.d, p)
		if !ok || int64(l) > int64(end-p-w) {
			return false
		}
		s.DataOff, s.DataLen = int64(p+w), int64(l)
		p += w + int(l)
		s.End = int64(p)
		a.m.Datas = append(a.m.Datas, s)
	}
	return p == end
}

func (a *an) codeSec(p, end int) bool {
	n, p, ok := a.vecCount(p, end)
	if !ok {
		return false
	}
	for i := 0; i < n; i++ {
		l, w, ok := u32(a.d, p)
		if !ok || int64(l) > maxBodyLen || int64(l) > int64(end-p-w) {
			return false
		}
		b := Body{SizeOff: int64(p), SizeW: w, Start: int64(p + w), End: int64(p+w) + int64(l)}
		a.body = int32(i)
		keep := len(a.m.Slots)
		if !a.walkBody(int(b.Start), int(b.End)) {
			a.m.Slots = a.m.Slots[:keep]
			b.Opaque = true
			a.m.Opaque++
			a.m.Why[a.why]++
		}
		a.body = -1
		a.m.Bodies = append(a.m.Bodies, b)
		p = int(b.End)
	}
	return p == end
}

func (a *an) nameSection(p, end int) {
	if a.m.Names == nil {
		a.m.Names = map[uint32]string{}
	}
	for p < end {
		id := a.d[p]
		sz, w, ok := u32(a.d, p+1)
		if !ok || int64(sz) > int64(end-p-1-w) {
			return
		}
		q, se := p+1+w, p+1+w+int(sz)
		if id == 1 {
			n, q2, ok := a.vecCount(q, se)
			if !ok {
				return
			}
			q = q2
			for i := 0; i < n; i++ {
				idx, w, ok := u32(a.d, q)
				if !ok {
					return
				}
				var nm string
				if nm, q, ok = a.name(q+w, se); !ok {
					return
				}
				a.m.Names[idx] = nm
			}
		}
		p = se
	}
}

// ---------------------------------------------------------------------------------------------
// function bodies

type cur struct {
	a   *an
	p   int
	end int
	bad bool
	op  byte
	sub uint32
}

func (c *cur) u32() uint32 {
	v, w, ok := u32(c.a.d, c.p)
	if !ok || c.p+w > c.end {
		c.bad = true
		return 0
	}
	c.p += w
	return v
}

func (c *cur) skipLEB(max int) {
	w, ok := lebWidth(c.a.d, c.p, max)
	if !ok || c.p+w > c.end {
		c.bad = true
		return
	}
	c.p += w
}

func (c *cur) skip(n int) {
	if c.p+n > c.end {
		c.bad = true
		return
	}
	c.p += n
}

func (c *cur) idx(sp Space) {
	if c.bad {
		return
	}
	p, ok := c.a.idx(c.p, sp)
	if !ok || p > c.end {
		c.bad = true
		return
	}
	c.p = p
}

func (c *cur) blockType() {
	if c.p >= c.end {
		c.bad = true
		return
	}
	b := c.a.d[c.p]
	if b&0x80 == 0 && b&0x40 != 0 { // one-byte negative: empty (0x40) or a value type
		c.p++
		return
	}
	v, w, ok := leb(c.a.d, c.p, 5) // non-negative s33: a type index
	if !ok || c.p+w > c.end || (c.a.d[c.p+w-1]&0x40 != 0) {
		c.bad = true
		return
	}
	c.a.slot(c.p, w, SpType, true, uint32(v))
	c.p += w
}

func (c *cur) memarg() {
	al := c.u32()
	if al&0x40 != 0 { // multi-memory: explicit memory index
		c.idx(SpMem)
	}
	c.skipLEB(5)
}

func (a *an) walkBody(p, end int) bool {
	c := &cur{a: a, p: p, end: end}
	ok := c.walk(p, end)
	if !ok {
		a.why = fmt.Sprintf("op %#x sub %#x", c.op, c.sub)
	}
	return ok
}

func (c *cur) walk(p, end int) bool {
	a := c.a
	nl := c.u32()
	var total uint64
	for i := uint32(0); i < nl && !c.bad; i++ {
		total += uint64(c.u32())
		if c.p >= end || !isValType(a.d[c.p]) || total > maxLocals {
			return false
		}
		c.p++
	}
	depth := 1
	for c.p < end && !c.bad {
		op := a.d[c.p]
		c.p++
		c.op = op
		switch op {
		case 0x00, 0x01, 0x05, 0x0F, 0x1A, 0x1B, 0x19, 0x0A:
		case 0x02, 0x03, 0x04, 0x06:
			depth++
			c.blockType()
		case 0x1F: // try_table
			depth++
			c.blockType()
			n := c.u32()
			for i := uint32(0); i < n && !c.bad; i++ {
				if c.p >= end {
					return false
				}
				k := a.d[c.p]
				c.p++
				switch k {
				case 0, 1:
					c.idx(SpTag)
					c.skipLEB(5)
				case 2, 3:
					c.skipLEB(5)
				default:
					return false
				}
			}
		case 0x0B:
			depth--
			if depth == 0 {
				return !c.bad && c.p == end
			}
		case 0x07, 0x08:
			c.idx(SpTag)
		case 0x09, 0x0C, 0x0D:
			c.skipLEB(5)
		case 0x18: // delegate ends a legacy try block
			c.skipLEB(5)
			depth--
			if depth == 0 {
				return false
			}
		case 0x0E:
			n := c.u32()
			if int64(n) > int64(end-c.p) {
				return false
			}
			for i := uint32(0); i <= n && !c.bad; i++ {
				c.skipLEB(5)
			}
		case 0x10, 0x12:
			c.idx(SpFunc)
		case 0x11, 0x13:
			c.idx(SpType)
			c.idx(SpTable)
		case 0x1C:
			n := c.u32()
			if int64(n) > int64(end-c.p) {
				return false
			}
			for i := uint32(0); i < n && !c.bad; i++ {
				if c.p >= end || !isValType(a.d[c.p]) {
					return false
				}
				c.p++
			}
		case 0x20, 0x21, 0x22:
			c.skipLEB(5)
		case 0x23, 0x24:
			c.idx(SpGlobal)
		case 0x25, 0x26:
			c.idx(SpTable)
		case 0x3F, 0x40:
			c.idx(SpMem)
		case 0x41:
			// a linker-relocated address is emitted as a padded 5-byte signed LEB
			if w, ok := lebWidth(a.d, c.p, 5); ok && w == 5 && a.d[c.p+4]&0x78 == 0 && a.d[c.p]&0x80 != 0 {
				var v uint32
				for i := 0; i < 5; i++ {
					v |= uint32(a.d[c.p+i]&0x7f) << (7 * uint(i))
				}
				a.slot(c.p, 5, SpAddr, true, v)
			}
			c.skipLEB(5)
		case 0x42:
			c.skipLEB(10)
		case 0x43:
			c.skip(4)
		case 0x44:
			c.skip(8)
		case 0xD0:
			if c.p >= end || !isValType(a.d[c.p]) {
				return false
			}
			c.p++
		case 0xD1:
		case 0xD2:
			c.idx(SpFunc)
		default:
			switch {
			case op >= 0x28 && op <= 0x3E:
				c.memarg()
			case op >= 0x45 && op <= 0xC4:
			case op == 0xFC:
				c.fc()
			case op == 0xFD:
				c.simd()
			case op == 0xFE:
				c.atomic()
			default:
				return false
			}
		}
	}
	return false
}

func (c *cur) fc() {
	sub := c.u32()
	c.sub = sub
	switch {
	case sub <= 7:
	case sub == 8:
		c.idx(SpData)
		c.idx(SpMem)
	case sub == 9:
		c.idx(SpData)
	case sub == 10:
		c.idx(SpMem)
		c.idx(SpMem)
	case sub == 11:
		c.idx(SpMem)
	case sub == 12:
		c.idx(SpElem)
		c.idx(SpTable)
	case sub == 13:
		c.idx(SpElem)
	case sub == 14:
		c.idx(SpTable)
		c.idx(SpTable)
	case sub >= 15 && sub <= 17:
		c.idx(SpTable)
	default:
		c.bad = true
	}
}

func (c *cur) simd() {
	sub := c.u32()
	c.sub = sub
	switch {
	case sub <= 11, sub == 92, sub == 93:
		c.memarg()
	case sub == 12, sub == 13:
		c.skip(16)
	case sub >= 21 && sub <= 34:
		c.skip(1)
	case sub >= 84 && sub <= 91:
		c.memarg()
		c.skip(1)
	case sub <= 0x113: // arithmetic, comparisons, conversions, relaxed simd: no immediates
	default:
		c.bad = true
	}
}

func (c *cur) atomic() {
	sub := c.u32()
	c.sub = sub
	switch {
	case sub <= 2, sub >= 0x10 && sub <= 0x4E:
		c.memarg()
	case sub == 3:
		c.skip(1)
	default:
		c.bad = true
	}
}
