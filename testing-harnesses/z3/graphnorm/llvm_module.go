package graphnorm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// LLVM IR block ids and record codes used by the adapter (LLVM 18/19 numbering).
const (
	blkModule   = 8
	blkParamAtt = 9
	blkParamGrp = 10
	blkConst    = 11
	blkFunc     = 12
	blkVST      = 14
	blkUseList  = 18
	blkMeta     = 15
	blkMetaAtt  = 16
	blkType     = 17
	blkStrtab   = 23
)

type ltype struct {
	code uint64
	ops  []uint64
	name string
}

type lval struct {
	kind  byte // 'g' var, 'f' function, 'a' alias, 'i' ifunc, 'c' constant
	rec   *Record
	cty   int // constant: type id
	name  string
	proto bool
}

type lfunc struct {
	blk   *Block
	val   int // value number of the function
	fnTy  int
	nargs int
}

// LLVM is the semantic view of one bitcode module needed to canonicalize references.
type LLVM struct {
	BC       *Bitcode
	Mod      *Block
	Strtab   []byte
	Types    []ltype
	Groups   []*Record // PARAMATTR_GROUP entries
	groupIdx map[uint64]int
	Lists    []*Record // PARAMATTR entries
	Vals     []lval
	MDs      []mdEnt
	MDBad    bool // module metadata contains records whose ids we cannot model
	Funcs    []lfunc
	Keys     [NumTabs][]Key
	Dup      [NumTabs]map[Key]bool
	Graph    *Graph
	Ident    *Idents
	nVals    int
	info     map[uint64][]Abbrev
}

type mdEnt struct {
	code uint64
	rec  *Record
	str  []byte
	bad  bool
}

var errLLVM = errors.New("graphnorm: unsupported LLVM module shape")

func recStr(ops []uint64) string {
	b := make([]byte, len(ops))
	for i, o := range ops {
		b[i] = byte(o)
	}
	return string(b)
}

// AnalyzeLLVM extracts module-level numbering domains and computes graph identities.
func AnalyzeLLVM(bc *Bitcode) (*LLVM, error) {
	L := &LLVM{BC: bc, groupIdx: map[uint64]int{}}
	for _, b := range bc.Blocks {
		switch b.ID {
		case blkModule:
			if L.Mod != nil {
				return nil, fmt.Errorf("%w: multiple modules", errLLVM)
			}
			L.Mod = b
		case blkStrtab:
			for _, it := range b.Items {
				if it.Kind == ItemRec && it.Rec.Code == 1 {
					L.Strtab = it.Rec.Blob
				}
			}
		}
	}
	if L.Mod == nil {
		return nil, fmt.Errorf("%w: no module", errLLVM)
	}
	version := uint64(0)
	var pendName string
	var fnProto []bool
	for _, it := range L.Mod.Items {
		switch it.Kind {
		case ItemRec:
			r := it.Rec
			switch r.Code {
			case 1: // VERSION
				if len(r.Ops) > 0 {
					version = r.Ops[0]
				}
			case 7, 8, 14, 15:
				v := lval{rec: r}
				v.kind = map[uint64]byte{7: 'g', 8: 'f', 14: 'a', 15: 'i'}[r.Code]
				if version >= 2 && len(r.Ops) >= 2 {
					off, n := r.Ops[0], r.Ops[1]
					if off+n > uint64(len(L.Strtab)) || off+n < off {
						return nil, fmt.Errorf("%w: strtab range", errLLVM)
					}
					v.name = string(L.Strtab[off : off+n])
				}
				if r.Code == 8 {
					if len(r.Ops) < 5 {
						return nil, fmt.Errorf("%w: short function record", errLLVM)
					}
					v.proto = r.Ops[4] != 0
					if !v.proto {
						fnProto = append(fnProto, true)
						L.Funcs = append(L.Funcs, lfunc{val: len(L.Vals), fnTy: int(r.Ops[2])})
					}
				}
				L.Vals = append(L.Vals, v)
			}
		case ItemBlock:
			b := it.Blk
			switch b.ID {
			case blkType:
				for _, ti := range b.Items {
					if ti.Kind != ItemRec {
						continue
					}
					r := ti.Rec
					switch r.Code {
					case 1:
					case 19:
						pendName = recStr(r.Ops)
					default:
						t := ltype{code: r.Code, ops: r.Ops}
						if r.Code == 20 || r.Code == 6 {
							t.name = pendName
						}
						pendName = ""
						L.Types = append(L.Types, t)
					}
				}
			case blkParamGrp:
				for _, gi := range b.Items {
					if gi.Kind == ItemRec && gi.Rec.Code == 3 && len(gi.Rec.Ops) >= 2 {
						L.groupIdx[gi.Rec.Ops[0]] = len(L.Groups)
						L.Groups = append(L.Groups, gi.Rec)
					}
				}
			case blkParamAtt:
				for _, gi := range b.Items {
					if gi.Kind == ItemRec && gi.Rec.Code == 2 {
						L.Lists = append(L.Lists, gi.Rec)
					}
				}
			case blkConst:
				cty := -1
				for _, ci := range b.Items {
					if ci.Kind != ItemRec {
						continue
					}
					if ci.Rec.Code == 1 {
						if len(ci.Rec.Ops) < 1 {
							return nil, fmt.Errorf("%w: SETTYPE", errLLVM)
						}
						cty = int(ci.Rec.Ops[0])
						continue
					}
					L.Vals = append(L.Vals, lval{kind: 'c', rec: ci.Rec, cty: cty})
				}
			case blkMeta:
				L.addModuleMD(b)
			}
		}
	}
	L.nVals = len(L.Vals)
	L.info = bc.BlockInfo()
	// function blocks, in order
	fi := 0
	for _, it := range L.Mod.Items {
		if it.Kind == ItemBlock && it.Blk.ID == blkFunc {
			if fi >= len(L.Funcs) {
				return nil, fmt.Errorf("%w: more function blocks than definitions", errLLVM)
			}
			L.Funcs[fi].blk = it.Blk
			fi++
		}
	}
	if fi != len(L.Funcs) {
		return nil, fmt.Errorf("%w: function blocks %d != definitions %d", errLLVM, fi, len(L.Funcs))
	}
	for i := range L.Funcs {
		t := L.typeAt(L.Funcs[i].fnTy)
		if t == nil || t.code != 21 || len(t.ops) < 2 {
			return nil, fmt.Errorf("%w: function type", errLLVM)
		}
		L.Funcs[i].nargs = len(t.ops) - 2
	}
	_ = fnProto
	L.buildGraph()
	return L, nil
}

func (L *LLVM) typeAt(i int) *ltype {
	if i < 0 || i >= len(L.Types) {
		return nil
	}
	return &L.Types[i]
}

func (L *LLVM) addModuleMD(b *Block) {
	for _, mi := range b.Items {
		if mi.Kind != ItemRec {
			continue
		}
		r := mi.Rec
		switch r.Code {
		case 35: // STRINGS [count, offsetToChars] blob
			strs, ok := parseMDStrings(r)
			if !ok {
				L.MDBad = true
				continue
			}
			for _, s := range strs {
				L.MDs = append(L.MDs, mdEnt{code: 35, str: s})
			}
		case 2, 3, 5: // VALUE, NODE, DISTINCT_NODE
			L.MDs = append(L.MDs, mdEnt{code: r.Code, rec: r})
		case 4, 10, 6, 36, 38, 39, 11: // NAME, NAMED_NODE, KIND, GLOBAL_DECL_ATTACHMENT, INDEX_OFFSET, INDEX, ATTACHMENT
		default:
			// any other record defines a metadata id we do not model
			L.MDs = append(L.MDs, mdEnt{code: r.Code, rec: r, bad: true})
			L.MDBad = true
		}
	}
}

// parseMDStrings decodes METADATA_STRINGS: vbr6 lengths (bit-packed) then the character data.
func parseMDStrings(r *Record) ([][]byte, bool) {
	if len(r.Ops) != 2 || r.Blob == nil {
		return nil, false
	}
	n, off := r.Ops[0], r.Ops[1]
	if off > uint64(len(r.Blob)) || n > uint64(len(r.Blob))*8 {
		return nil, false
	}
	br := &bitReader{d: r.Blob[:off], end: off * 8}
	lens := make([]uint64, 0, n)
	for i := uint64(0); i < n; i++ {
		lens = append(lens, br.vbr(6))
		if br.err != nil {
			return nil, false
		}
	}
	pos := off
	var out [][]byte
	for _, l := range lens {
		if l > uint64(len(r.Blob))-pos {
			return nil, false
		}
		out = append(out, r.Blob[pos:pos+l])
		pos += l
	}
	if pos != uint64(len(r.Blob)) {
		return nil, false
	}
	return out, true
}

type attrScan struct {
	lit  []uint64
	refs []int // type ids referenced
	ok   bool
}

// scanAttrs normalizes one attribute-group payload: type ids become edges.
func scanAttrs(ops []uint64) attrScan {
	var s attrScan
	s.ok = true
	i := 0
	for i < len(ops) && s.ok {
		kind := ops[i]
		s.lit = append(s.lit, kind)
		i++
		switch kind {
		case 0, 5:
			if i >= len(ops) {
				s.ok = false
				break
			}
			s.lit = append(s.lit, ops[i])
			i++
		case 1:
			if i+1 >= len(ops) {
				s.ok = false
				break
			}
			s.lit = append(s.lit, ops[i], ops[i+1])
			i += 2
		case 3, 4:
			// key\0 [value\0]
			for z := 0; z < int(kind)-2; z++ {
				for i < len(ops) && ops[i] != 0 {
					s.lit = append(s.lit, ops[i])
					i++
				}
				if i >= len(ops) {
					s.ok = false
					break
				}
				s.lit = append(s.lit, 0)
				i++
			}
		case 6:
			if i+1 >= len(ops) {
				s.ok = false
				break
			}
			s.lit = append(s.lit, ops[i])
			s.refs = append(s.refs, int(ops[i+1]))
			i += 2
		default:
			s.ok = false
		}
	}
	return s
}

func lits(vs ...uint64) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

// node-index domains inside the unified module graph
type gdom struct{ ty, grp, lst, val, md, null int }

func (L *LLVM) buildGraph() {
	g := &Graph{}
	d := gdom{}
	d.ty = 0
	d.grp = d.ty + len(L.Types)
	d.lst = d.grp + len(L.Groups)
	d.val = d.lst + len(L.Lists)
	d.md = d.val + len(L.Vals)
	d.null = d.md + len(L.MDs)
	g.Nodes = make([]Node, d.null+1)
	tyE := func(kind uint16, id uint64) Edge {
		if id >= uint64(len(L.Types)) {
			return Edge{kind, -1}
		}
		return Edge{kind, int32(d.ty) + int32(id)}
	}
	valE := func(kind uint16, id uint64) Edge {
		if id >= uint64(len(L.Vals)) {
			return Edge{kind, -1}
		}
		return Edge{kind, int32(d.val) + int32(id)}
	}
	// types
	for i, t := range L.Types {
		n := Node{Class: 100 + uint32(t.code)}
		ops := t.ops
		switch t.code {
		case 2, 3, 4, 5, 10, 13, 14, 15, 16, 17, 22, 23, 24:
			n.Lit = lits(t.code)
		case 7, 25:
			n.Lit = lits(t.code, ops[0])
			if len(ops) != 1 {
				n.Flags = FlagUnknown
			}
		case 8:
			if len(ops) >= 1 {
				n.Out = []Edge{tyE(0, ops[0])}
				n.Lit = lits(append([]uint64{t.code}, ops[1:]...)...)
			}
		case 11, 12:
			if len(ops) >= 2 {
				n.Lit = lits(append([]uint64{t.code, ops[0]}, ops[2:]...)...)
				n.Out = []Edge{tyE(0, ops[1])}
			} else {
				n.Flags = FlagUnknown
			}
		case 18, 20, 21:
			if len(ops) >= 1 {
				n.Lit = append(lits(t.code, ops[0]), t.name...)
				for _, o := range ops[1:] {
					n.Out = append(n.Out, tyE(1, o))
				}
			} else {
				n.Flags = FlagUnknown
			}
		case 6:
			n.Lit = append(lits(t.code), t.name...)
		default:
			n.Lit = lits(append([]uint64{t.code}, ops...)...)
			n.Flags = FlagOpaque | FlagUnknown
		}
		g.Nodes[d.ty+i] = n
	}
	// attribute groups and lists
	for i, r := range L.Groups {
		sc := scanAttrs(r.Ops[2:])
		n := Node{Class: 200, Lit: lits(append([]uint64{r.Ops[1]}, sc.lit...)...)}
		for _, tr := range sc.refs {
			n.Out = append(n.Out, tyE(0, uint64(tr)))
		}
		if !sc.ok {
			n.Flags = FlagUnknown
		}
		g.Nodes[d.grp+i] = n
	}
	for i, r := range L.Lists {
		n := Node{Class: 201}
		for _, gid := range r.Ops {
			gi, ok := L.groupIdx[gid]
			if !ok {
				n.Out = append(n.Out, Edge{0, -1})
				continue
			}
			n.Out = append(n.Out, Edge{0, int32(d.grp + gi)})
		}
		g.Nodes[d.lst+i] = n
	}
	// values
	for i, v := range L.Vals {
		g.Nodes[d.val+i] = L.valNode(v, d, tyE, valE)
	}
	// metadata
	g.Nodes[d.null] = Node{Class: 3999, Lit: []byte("null")}
	for i, m := range L.MDs {
		n := Node{Class: 3000 + uint32(m.code)}
		mdE := func(id uint64) Edge {
			if id == 0 {
				return Edge{2, int32(d.null)}
			}
			if id-1 >= uint64(len(L.MDs)) {
				return Edge{2, -1}
			}
			return Edge{2, int32(d.md) + int32(id-1)}
		}
		switch {
		case m.bad:
			n.Flags = FlagUnknown
			n.Lit = lits(m.code)
		case m.code == 35:
			n.Lit = m.str
		case m.code == 2 && len(m.rec.Ops) == 2:
			n.Out = []Edge{tyE(0, m.rec.Ops[0]), valE(0, m.rec.Ops[1])}
		case m.code == 3 || m.code == 5:
			for _, o := range m.rec.Ops {
				n.Out = append(n.Out, mdE(o))
			}
		default:
			n.Flags = FlagUnknown
		}
		g.Nodes[d.md+i] = n
	}
	L.Graph = g
	L.Ident = g.SCCIdentities()
	// per-table keys
	fill := func(tab, base, n int) {
		L.Keys[tab] = make([]Key, n)
		L.Dup[tab] = map[Key]bool{}
		seen := map[Key]int{}
		for i := 0; i < n; i++ {
			var k Key
			copy(k[:], L.Ident.ID[base+i][:6])
			L.Keys[tab][i] = k
			seen[k]++
			if seen[k] > 1 || L.Ident.Ambig[base+i] != AmbigNone {
				L.Dup[tab][k] = true
			}
		}
	}
	fill(0, d.val, len(L.Vals))
	fill(1, d.ty, len(L.Types))
	fill(2, d.lst, len(L.Lists))
	fill(3, d.md, len(L.MDs))
}

func (L *LLVM) valNode(v lval, d gdom, tyE func(uint16, uint64) Edge, valE func(uint16, uint64) Edge) Node {
	r := v.rec
	switch v.kind {
	case 'g', 'f', 'a', 'i':
		private := v.kind == 'g' && len(r.Ops) > 5 && r.Ops[5] == 9
		if !private && v.name != "" {
			return Node{Class: 1000 + uint32(v.kind), Lit: []byte(v.name)}
		}
		// private/unnamed globals: content identity (the numeric suffix of their names is unstable)
		n := Node{Class: 1100 + uint32(v.kind)}
		if v.kind != 'g' || len(r.Ops) < 15 {
			n.Flags = FlagUnknown
			n.Lit = lits(r.Ops...)
			return n
		}
		n.Lit = lits(append([]uint64{r.Ops[3]}, r.Ops[5:14]...)...)
		n.Out = []Edge{tyE(0, r.Ops[2])}
		if r.Ops[4] != 0 {
			n.Out = append(n.Out, valE(1, r.Ops[4]-1))
		} else {
			n.Out = append(n.Out, Edge{1, int32(d.null)})
		}
		return n
	}
	// constants
	n := Node{Class: 2000 + uint32(r.Code)}
	n.Out = []Edge{tyE(0, uint64(max(v.cty, 0)))}
	if v.cty < 0 {
		n.Flags = FlagUnknown
	}
	ops := r.Ops
	switch r.Code {
	case 2, 3, 26:
	case 4, 5, 6, 8, 9, 22:
		n.Lit = lits(ops...)
	case 7:
		for _, o := range ops {
			n.Out = append(n.Out, valE(1, o))
		}
	case 10:
		if len(ops) >= 3 {
			n.Lit = lits(append([]uint64{ops[0]}, ops[3:]...)...)
			n.Out = append(n.Out, valE(1, ops[1]), valE(1, ops[2]))
		} else {
			n.Flags = FlagUnknown
		}
	case 11:
		if len(ops) == 3 {
			n.Lit = lits(ops[0])
			n.Out = append(n.Out, tyE(2, ops[1]), valE(1, ops[2]))
		} else {
			n.Flags = FlagUnknown
		}
	case 12, 20, 24:
		i := 0
		var pre []uint64
		if r.Code == 24 {
			if len(ops) < 2 {
				n.Flags = FlagUnknown
				break
			}
			pre = []uint64{ops[1]}
			n.Out = append(n.Out, tyE(3, ops[0]))
			i = 2
		} else if len(ops)%2 == 1 {
			n.Out = append(n.Out, tyE(3, ops[0]))
			i = 1
		}
		n.Lit = lits(append([]uint64{r.Code}, pre...)...)
		for ; i+1 < len(ops); i += 2 {
			n.Out = append(n.Out, tyE(2, ops[i]), valE(1, ops[i+1]))
		}
		if i != len(ops) {
			n.Flags = FlagUnknown
		}
	case 13:
		if len(ops) == 3 {
			n.Out = append(n.Out, valE(1, ops[0]), valE(1, ops[1]), valE(1, ops[2]))
		} else {
			n.Flags = FlagUnknown
		}
	default:
		n.Flags = FlagUnknown
		n.Lit = lits(append([]uint64{r.Code}, ops...)...)
	}
	return n
}
