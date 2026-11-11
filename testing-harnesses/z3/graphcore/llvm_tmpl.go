package graphcore

import (
	"encoding/binary"
)

// Policy selects which operand classes are replaced by symbolic references. The synthesizer
// searches this space by fully charged cost; each toggle is an independent role class.
type Policy struct {
	Types, Values, Attrs, MD, LocalAbs, LocalMD bool
}

var PolicyAll = Policy{true, true, true, true, true, true}
var PolicyNone = Policy{}

func (p Policy) Bits() int {
	n := 0
	for i, b := range []bool{p.Types, p.Values, p.Attrs, p.MD, p.LocalAbs, p.LocalMD} {
		if b {
			n |= 1 << i
		}
	}
	return n
}
func PolicyFromBits(n int) Policy {
	return Policy{n&1 != 0, n&2 != 0, n&4 != 0, n&8 != 0, n&16 != 0, n&32 != 0}
}

// TmplStats counts operand outcomes while building one template.
type TmplStats struct {
	Ops, Sym, Unusable int
	ExtKeys            [NumTabs]int
	Ext                [NumTabs][]Key // per-template extern lists (not summed)
	ByKind             [8]int
}

func (s *TmplStats) add(o TmplStats) {
	s.Ops += o.Ops
	s.Sym += o.Sym
	s.Unusable += o.Unusable
	for i := range s.ByKind {
		s.ByKind[i] += o.ByKind[i]
	}
	for i := range s.ExtKeys {
		s.ExtKeys[i] += o.ExtKeys[i]
	}
}

type tmplBuilder struct {
	L      *LLVM
	pol    Policy
	out    []byte
	ext    [NumTabs][]Key
	extIdx map[extEnt]int
	info   map[uint64][]Abbrev
	scopes [][]Abbrev // abbreviation lists of the open blocks
	inst   uint32
	nVals  uint32
	nMD    uint64
	fail   bool
	st     TmplStats
	hook   func(*Block) // skeleton mode: called for module-level function blocks
	bidCur uint64       // SETBID target while inside BLOCKINFO
}

func appendLit(b []byte, v uint64) []byte {
	if v < 1<<61 {
		return binary.AppendUvarint(b, v<<3)
	}
	b = binary.AppendUvarint(b, TokEsc)
	return binary.AppendUvarint(b, v)
}

func (b *tmplBuilder) extTok(kind uint64, tab int, idx uint64, tb []byte) ([]byte, bool) {
	if idx >= uint64(len(b.L.Keys[tab])) {
		return tb, false
	}
	k := b.L.Keys[tab][idx]
	if b.L.Dup[tab][k] {
		b.st.Unusable++
		return tb, false
	}
	e := extEnt{tab, k}
	i, ok := b.extIdx[e]
	if !ok {
		i = len(b.ext[tab])
		b.ext[tab] = append(b.ext[tab], k)
		b.extIdx[e] = i
	}
	b.st.ByKind[kind]++
	b.st.Sym++
	return binary.AppendUvarint(tb, (uint64(i)<<2|uint64(tab))<<3|kind), true
}

// cursor walks one record's operands, appending one token per operand.
type cursor struct {
	b    *tmplBuilder
	ops  []uint64
	i    int
	tb   []byte
	ends []int
}

// take records the token tb for the operand at c.i and advances.
func (c *cursor) take(tb []byte) {
	c.tb = tb
	c.ends = append(c.ends, len(tb))
	c.b.st.Ops++
	c.i++
}

func (c *cursor) more() bool { return c.i < len(c.ops) }
func (c *cursor) lit() {
	if c.i < len(c.ops) {
		c.take(appendLit(c.tb, c.ops[c.i]))
	}
}
func (c *cursor) lits(n int) {
	for ; n > 0 && c.more(); n-- {
		c.lit()
	}
}
func (c *cursor) rest() {
	for c.more() {
		c.lit()
	}
}
func (c *cursor) ty() {
	if !c.more() {
		return
	}
	if c.b.pol.Types {
		if tb, ok := c.b.extTok(TokXAbs, 1, c.ops[c.i], c.tb); ok {
			c.take(tb)
			return
		}
	}
	c.lit()
}
func (c *cursor) attr() {
	if !c.more() {
		return
	}
	if v := c.ops[c.i]; v != 0 && c.b.pol.Attrs {
		if tb, ok := c.b.extTok(TokXAb1, 2, v-1, c.tb); ok {
			c.take(tb)
			return
		}
	}
	c.lit()
}

// v is a relative value operand without trailing type.
func (c *cursor) v() {
	if !c.more() {
		return
	}
	v := c.ops[c.i]
	if c.b.pol.Values && v < 1<<32 {
		valno := c.b.inst - uint32(v)
		if valno < c.b.inst && valno < c.b.nVals {
			if tb, ok := c.b.extTok(TokXRel, 0, uint64(valno), c.tb); ok {
				c.take(tb)
				return
			}
		}
	}
	c.lit()
}

// vt is a relative value operand followed by a type iff it is a forward reference.
func (c *cursor) vt() {
	if !c.more() {
		return
	}
	v := c.ops[c.i]
	fwd := v >= 1<<32 || c.b.inst-uint32(v) >= c.b.inst
	c.v()
	if fwd {
		c.ty()
	}
}

// vs is a signed-VBR relative value (PHI incoming).
func (c *cursor) vs() {
	if !c.more() {
		return
	}
	v := c.ops[c.i]
	if c.b.pol.Values && v&1 == 0 && v>>1 < 1<<32 {
		valno := c.b.inst - uint32(v>>1)
		if valno < c.b.inst && valno < c.b.nVals {
			if tb, ok := c.b.extTok(TokXRlS, 0, uint64(valno), c.tb); ok {
				c.take(tb)
				return
			}
		}
	}
	c.lit()
}

// vabs is an absolute value id (module value => lookup, function-local => offset).
func (c *cursor) vabs() {
	if !c.more() {
		return
	}
	v := c.ops[c.i]
	if v < uint64(c.b.nVals) {
		if c.b.pol.Values {
			if tb, ok := c.b.extTok(TokXAbs, 0, v, c.tb); ok {
				c.take(tb)
				return
			}
		}
	} else if c.b.pol.LocalAbs && v-uint64(c.b.nVals) < 1<<40 {
		c.b.st.ByKind[TokLAbs]++
		c.b.st.Sym++
		c.take(binary.AppendUvarint(c.tb, (v-uint64(c.b.nVals))<<3|TokLAbs))
		return
	}
	c.lit()
}

// md is a metadata id operand; plus1 selects the id+1 (null = 0) convention.
func (c *cursor) md(plus1 bool) {
	if !c.more() {
		return
	}
	v := c.ops[c.i]
	id := v
	if plus1 {
		if v == 0 {
			c.lit()
			return
		}
		id = v - 1
	}
	if !c.b.L.MDBad {
		if id < c.b.nMD {
			if c.b.pol.MD {
				kind := uint64(TokXAbs)
				if plus1 {
					kind = TokXAb1
				}
				if tb, ok := c.b.extTok(kind, 3, id, c.tb); ok {
					c.take(tb)
					return
				}
			}
		} else if c.b.pol.LocalMD && id-c.b.nMD < 1<<40 {
			p := (id - c.b.nMD) << 1
			if plus1 {
				p |= 1
			}
			c.b.st.ByKind[TokLMD]++
			c.b.st.Sym++
			c.take(binary.AppendUvarint(c.tb, p<<3|TokLMD))
			return
		}
	}
	c.lit()
}

func (b *tmplBuilder) curAbbrevs() []Abbrev { return b.scopes[len(b.scopes)-1] }

// emitRec writes: 0x80|def<<6|abbrev (abbrev 63 = explicit uvarint follows), then for
// unabbreviated records code and operand count, else only non-literal operands (array length
// and blob length explicit). Literal abbreviation operands and the abbreviation's literal code
// are implied by the abbreviation definition the VM tracks.
func (b *tmplBuilder) emitRec(rec *Record, c *cursor, def bool) {
	for c.i < len(c.ops) { // any unconsumed operands are literal
		c.lit()
	}
	hb := byte(0x80)
	if def {
		hb |= 0x40
	}
	if rec.AbbrevID < 63 {
		hb |= byte(rec.AbbrevID)
	} else {
		hb |= 63
	}
	b.out = append(b.out, hb)
	if rec.AbbrevID >= 63 {
		b.out = binary.AppendUvarint(b.out, rec.AbbrevID)
	}
	tok := func(i int) []byte {
		lo := 0
		if i > 0 {
			lo = c.ends[i-1]
		}
		return c.tb[lo:c.ends[i]]
	}
	if rec.AbbrevID == 3 {
		b.out = binary.AppendUvarint(b.out, rec.Code)
		b.out = binary.AppendUvarint(b.out, uint64(len(rec.Ops)))
		b.out = append(b.out, c.tb...)
	} else {
		a := b.curAbbrevs()[rec.AbbrevID-4]
		if !a[0].Lit {
			b.out = binary.AppendUvarint(b.out, rec.Code)
		}
		k := 0
		for i := 1; i < len(a); i++ {
			o := a[i]
			switch {
			case !o.Lit && o.Enc == encArray:
				b.out = binary.AppendUvarint(b.out, uint64(len(rec.Ops)-k))
				for ; k < len(rec.Ops); k++ {
					b.out = append(b.out, tok(k)...)
				}
				i = len(a) // the element type op is not an operand
			case !o.Lit && o.Enc == encBlob:
				b.out = binary.AppendUvarint(b.out, uint64(len(rec.Blob)))
				b.out = append(b.out, rec.Blob...)
			case o.Lit:
				k++
			default:
				b.out = append(b.out, tok(k)...)
				k++
			}
		}
	}
	if def {
		b.inst++
	}
}

func (b *tmplBuilder) emitDef(a Abbrev, blkID uint64) {
	if blkID != 0 {
		b.scopes[len(b.scopes)-1] = append(b.scopes[len(b.scopes)-1], a)
	} else if b.bidCur != ^uint64(0) {
		b.info[b.bidCur] = append(b.info[b.bidCur], a)
	}
	b.out = append(b.out, OpDefAbb)
	b.out = binary.AppendUvarint(b.out, uint64(len(a)))
	for _, o := range a {
		if o.Lit {
			b.out = append(b.out, 0)
			b.out = binary.AppendUvarint(b.out, o.Val)
		} else {
			b.out = append(b.out, o.Enc)
			b.out = binary.AppendUvarint(b.out, o.Width)
		}
	}
}

// defines reports whether an instruction record defines an SSA value.
func (b *tmplBuilder) instruction(rec *Record) (def bool) {
	c := &cursor{b: b, ops: rec.Ops}
	L := b.L
	switch rec.Code {
	case 1: // DECLAREBLOCKS
		c.rest()
	case 33, 35: // DEBUG_LOC_AGAIN, DEBUG_LOC
		c.rest()
	case 2: // BINOP
		c.vt()
		c.v()
		c.rest()
		def = true
	case 56: // UNOP
		c.vt()
		c.rest()
		def = true
	case 3: // CAST
		c.vt()
		c.ty()
		c.rest()
		def = true
	case 43: // GEP
		c.lits(1)
		c.ty()
		for c.more() {
			c.vt()
		}
		def = true
	case 26: // EXTRACTVAL
		c.vt()
		c.rest()
		def = true
	case 27: // INSERTVAL
		c.vt()
		c.vt()
		c.rest()
		def = true
	case 29: // VSELECT
		c.vt()
		c.v()
		c.vt()
		c.rest()
		def = true
	case 6:
		c.vt()
		c.vt()
		c.rest()
		def = true
	case 7:
		c.vt()
		c.v()
		c.vt()
		c.rest()
		def = true
	case 8:
		c.vt()
		c.v()
		c.v()
		c.rest()
		def = true
	case 9, 28: // CMP, CMP2
		c.vt()
		c.v()
		c.rest()
		def = true
	case 10: // RET
		if c.more() {
			c.vt()
		}
		c.rest()
	case 11: // BR
		c.lits(2)
		c.v()
		c.rest()
	case 12: // SWITCH
		c.ty()
		c.v()
		c.lits(1)
		for c.more() {
			c.vabs()
			c.lits(1)
		}
	case 15: // UNREACHABLE
		c.rest()
	case 16: // PHI
		c.ty()
		for len(c.ops)-c.i >= 2 {
			c.vs()
			c.lits(1)
		}
		c.rest()
		def = true
	case 19: // ALLOCA
		c.ty()
		c.ty()
		c.vabs()
		c.rest()
		def = true
	case 20, 41: // LOAD, LOADATOMIC
		c.vt()
		c.ty()
		c.rest()
		def = true
	case 44, 45: // STORE, STOREATOMIC
		c.vt()
		c.vt()
		c.rest()
	case 36: // FENCE
		c.rest()
	case 58: // FREEZE
		c.vt()
		c.rest()
		def = true
	case 59: // ATOMICRMW
		c.vt()
		c.vt()
		c.rest()
		def = true
	case 46: // CMPXCHG
		c.vt()
		c.vt()
		c.v()
		c.rest()
		def = true
	case 34: // CALL
		c.attr()
		if !c.more() {
			b.fail = true
			break
		}
		cc := c.ops[c.i]
		c.lit()
		if cc>>17&1 != 0 {
			c.lits(1)
		}
		if cc>>15&1 == 0 || !c.more() {
			b.fail = true
			c.rest()
			break
		}
		fnty := int(min(c.ops[c.i], 1<<30))
		c.ty()
		ft := L.typeAt(fnty)
		if ft == nil || ft.code != 21 || len(ft.ops) < 2 {
			b.fail = true
			c.rest()
			break
		}
		c.vt()
		for _, pt := range ft.ops[2:] {
			if !c.more() {
				break
			}
			if t := L.typeAt(int(min(pt, 1<<30))); t != nil && t.code == 5 {
				c.lit() // label argument: basic block id
			} else {
				c.v()
			}
		}
		if ft.ops[0] != 0 { // vararg
			for c.more() {
				c.vt()
			}
		}
		c.rest()
		if rt := L.typeAt(int(min(ft.ops[1], 1<<30))); rt != nil {
			def = rt.code != 2
		} else {
			b.fail = true
		}
	default:
		b.fail = true
		c.rest()
		def = true
	}
	b.emitRec(rec, c, def)
	return def
}

func (b *tmplBuilder) constant(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	def := true
	switch rec.Code {
	case 1: // SETTYPE
		c.ty()
		def = false
	case 7: // AGGREGATE
		for c.more() {
			c.vabs()
		}
	case 10:
		c.lits(1)
		c.vabs()
		c.vabs()
	case 11:
		c.lits(1)
		c.ty()
		c.vabs()
	case 12, 20, 24:
		if rec.Code == 24 {
			c.ty()
			c.lits(1)
		} else if len(rec.Ops)%2 == 1 {
			c.ty()
		}
		for len(c.ops)-c.i >= 2 {
			c.ty()
			c.vabs()
		}
	case 13:
		c.vabs()
		c.vabs()
		c.vabs()
	}
	c.rest()
	b.emitRec(rec, c, def)
}

func (b *tmplBuilder) metadata(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	switch rec.Code {
	case 2: // VALUE
		c.ty()
		c.vabs()
	case 3, 5:
		for c.more() {
			c.md(true)
		}
	}
	c.rest()
	b.emitRec(rec, c, false)
}

func (b *tmplBuilder) attachment(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	if rec.Code == 11 {
		if len(rec.Ops)%2 == 1 {
			c.lits(1)
		}
		for c.more() {
			c.lits(1)
			c.md(false)
		}
	}
	c.rest()
	b.emitRec(rec, c, false)
}

func (b *tmplBuilder) vst(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	if rec.Code == 1 {
		c.vabs()
	}
	c.rest()
	b.emitRec(rec, c, false)
}

// uselist: USELIST_CODE_ENTRY = [permutation..., value id]; USELIST_CODE_BB = [permutation..., bb id].
func (b *tmplBuilder) uselist(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	if rec.Code == 1 && len(rec.Ops) > 0 {
		c.lits(len(rec.Ops) - 1)
		c.vabs()
	}
	c.rest()
	b.emitRec(rec, c, false)
}

func (b *tmplBuilder) literalRec(rec *Record) {
	c := &cursor{b: b, ops: rec.Ops}
	c.rest()
	b.emitRec(rec, c, false)
}

func (b *tmplBuilder) block(blk *Block, depth int) {
	if b.hook != nil && depth == 1 && blk.ID == blkFunc {
		b.hook(blk)
		return
	}
	b.out = append(b.out, OpEnter)
	b.out = binary.AppendUvarint(b.out, blk.ID)
	b.out = binary.AppendUvarint(b.out, blk.Width)
	b.scopes = append(b.scopes, append([]Abbrev(nil), b.info[blk.ID]...))
	saveBID := b.bidCur
	b.bidCur = ^uint64(0)
	for _, it := range blk.Items {
		switch it.Kind {
		case ItemBlock:
			b.block(it.Blk, depth+1)
		case ItemDef:
			b.emitDef(it.Def, blk.ID)
		default:
			switch blk.ID {
			case blkFunc:
				b.instruction(it.Rec)
			case blkConst:
				b.constant(it.Rec)
			case blkMeta:
				b.metadata(it.Rec)
			case blkMetaAtt:
				b.attachment(it.Rec)
			case blkVST:
				b.vst(it.Rec)
			case blkUseList:
				b.uselist(it.Rec)
			default:
				if blk.ID == 0 && it.Rec.Code == 1 && len(it.Rec.Ops) > 0 {
					b.bidCur = it.Rec.Ops[0]
				}
				b.literalRec(it.Rec)
			}
		}
	}
	b.bidCur = saveBID
	b.scopes = b.scopes[:len(b.scopes)-1]
	b.out = append(b.out, OpEnd)
}

// BuildFuncTemplate returns the serialized template for one function block. ok=false means
// an unsupported instruction was met: the caller must use the literal template instead.
func (L *LLVM) BuildFuncTemplate(f lfunc, pol Policy) (tmpl []byte, st TmplStats, ok bool) {
	b := &tmplBuilder{L: L, pol: pol, extIdx: map[extEnt]int{}, nVals: uint32(L.nVals), nMD: uint64(len(L.MDs)), info: L.info}
	b.inst = b.nVals + uint32(f.nargs)
	b.scopes = [][]Abbrev{nil}
	b.block(f.blk, 0)
	body := b.out
	for t := range b.ext {
		b.st.ExtKeys[t] = len(b.ext[t])
		b.st.Ext[t] = b.ext[t]
	}
	var head []byte
	head = append(head, OpExt)
	for t := 0; t < NumTabs; t++ {
		head = binary.AppendUvarint(head, uint64(len(b.ext[t])))
		for _, k := range b.ext[t] {
			head = append(head, k[:]...)
		}
	}
	head = append(head, OpSetInst)
	head = binary.AppendUvarint(head, uint64(f.nargs))
	return append(head, body...), b.st, !b.fail
}

// LiteralTemplate emits the function block with every operand literal (always exact).
func (L *LLVM) LiteralTemplate(f lfunc) []byte {
	t, _, _ := L.BuildFuncTemplate(f, PolicyNone)
	return t
}
