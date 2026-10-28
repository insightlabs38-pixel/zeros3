package wasmz3

import "sort"

// Space is a module-level index space. Function-local indexes (locals, labels) are not spaces.
type Space uint8

const (
	SpType Space = iota + 1
	SpFunc
	SpTable
	SpMem
	SpGlobal
	SpTag
	SpElem
	SpData
	SpAddr    // linear-memory address constant (padded 5-byte i32.const): a layout reference, not an index
	NumSpaces // one past the last space
)

// SpaceName names an index space.
var SpaceName = [NumSpaces]string{"", "type", "func", "table", "mem", "global", "tag", "elem", "data", "addr"}

// Slot is one LEB128 number naming a module-level index. W is its exact encoded width.
type Slot struct {
	Off    int64
	W      uint8
	Sp     Space
	Signed bool // s33 block type
	Val    uint32
	Sec    byte  // section id the slot lives in
	Body   int32 // function body ordinal (code section) or -1
}

// FuncType is a function type.
type FuncType struct {
	Params, Results []byte
	Off, End        int64
}

// Import is one import entry.
type Import struct {
	Mod, Name string
	Kind      byte // 0 func, 1 table, 2 memory, 3 global, 4 tag
	Off, End  int64
	Desc      []byte // raw bytes of the descriptor without the (type index) LEB
	TypeIdx   uint32
}

// Body is one function body.
type Body struct {
	SizeOff int64 // offset of the size LEB
	SizeW   int
	Start   int64 // first byte of the locals vector
	End     int64
	Opaque  bool // not walked: unknown instruction or construct (no slots)
}

// Seg is an element or data segment.
type Seg struct {
	Off, End   int64
	DataOff    int64 // data payload (data segments)
	DataLen    int64
	Flags      uint32
	Active     bool
	ExprOff    int64 // offset expression (active), 0 if none
	ExprEnd    int64
	NFuncs     int
	ExprFuncs  bool // element entries are expressions
	EntryStart int64
}

// Global is a defined global.
type Global struct {
	Off, End         int64
	ValType, Mutable byte
	InitOff, InitEnd int64
}

// Module is the analysed content of a module.
type Module struct {
	Dir          *Dir
	Types        []FuncType
	Imports      []Import
	Funcs        []uint32 // type index of each defined function (value as in the file)
	Tables, Mems []Region
	Tags         []uint32
	Globals      []Global
	Exports      []Export
	Start        int64 // offset of the start function slot, 0 if none
	Elems        []Seg
	Datas        []Seg
	Bodies       []Body
	Slots        []Slot
	NImport      [5]int // imported func, table, mem, global, tag counts
	DataCount    int
	Names        map[uint32]string // function names from the name section (hint)
	Opaque       int
	Why          map[string]int // opaque bodies by first unsupported opcode
	gcOrWide     bool
}

// Region is a byte range.
type Region struct{ Off, End int64 }

// Export is one export entry.
type Export struct {
	Name     string
	Kind     byte
	Idx      uint32
	Off, End int64
}

const (
	maxLocals  = 50000
	maxVec     = 1 << 22
	maxBodyLen = 1 << 26
)

type an struct {
	why  string
	d    []byte
	m    *Module
	sec  byte
	body int32
	lim  Limits
}

func (a *an) slot(p int, w int, sp Space, signed bool, v uint32) {
	a.m.Slots = append(a.m.Slots, Slot{Off: int64(p), W: uint8(w), Sp: sp, Signed: signed, Val: v, Sec: a.sec, Body: a.body})
}

// uleb reads a u32 at p.
func (a *an) uleb(p int) (uint32, int, bool) { return u32(a.d, p) }

// idx reads a u32 index at p and records it as a slot of space sp.
func (a *an) idx(p int, sp Space) (int, bool) {
	v, w, ok := u32(a.d, p)
	if !ok {
		return p, false
	}
	a.slot(p, w, sp, false, v)
	return p + w, true
}

func (a *an) name(p, end int) (string, int, bool) {
	n, w, ok := u32(a.d, p)
	if !ok || int(n) > a.lim.MaxName || p+w+int(n) > end {
		return "", p, false
	}
	return string(a.d[p+w : p+w+int(n)]), p + w + int(n), true
}

func isValType(c byte) bool {
	switch c {
	case 0x7F, 0x7E, 0x7D, 0x7C, 0x7B, 0x70, 0x6F:
		return true
	}
	return false
}

func (a *an) limits(p, end int) (int, bool) {
	if p >= end {
		return p, false
	}
	fl := a.d[p]
	p++
	if fl > 3 { // 4 and above: memory64 / page sizes
		return p, false
	}
	_, w, ok := u32(a.d, p)
	if !ok {
		return p, false
	}
	p += w
	if fl&1 != 0 {
		_, w, ok = u32(a.d, p)
		if !ok {
			return p, false
		}
		p += w
	}
	return p, p <= end
}

func (a *an) tableType(p, end int) (int, bool) {
	if p >= end || !isValType(a.d[p]) || (a.d[p] != 0x70 && a.d[p] != 0x6F) {
		return p, false
	}
	return a.limits(p+1, end)
}

func (a *an) globalType(p, end int) (int, bool) {
	if p+2 > end || !isValType(a.d[p]) || a.d[p+1] > 1 {
		return p, false
	}
	return p + 2, true
}

// constExpr walks a constant expression up to and including its end opcode.
func (a *an) constExpr(p, end int) (int, bool) {
	for p < end {
		op := a.d[p]
		p++
		switch op {
		case 0x0B:
			return p, true
		case 0x41, 0x42:
			mx := 5
			if op == 0x42 {
				mx = 10
			}
			w, ok := lebWidth(a.d, p, mx)
			if !ok {
				return p, false
			}
			p += w
		case 0x43:
			p += 4
		case 0x44:
			p += 8
		case 0x23:
			var ok bool
			if p, ok = a.idx(p, SpGlobal); !ok {
				return p, false
			}
		case 0xD2:
			var ok bool
			if p, ok = a.idx(p, SpFunc); !ok {
				return p, false
			}
		case 0xD0:
			if p >= end || !isValType(a.d[p]) {
				return p, false
			}
			p++
		case 0x6A, 0x6B, 0x6C, 0x7C, 0x7D, 0x7E:
		default:
			return p, false
		}
	}
	return p, false
}

func (a *an) vecCount(p, end int) (int, int, bool) {
	n, w, ok := u32(a.d, p)
	if !ok || int64(n) > maxVec || int64(n) > a.lim.MaxEntries || p+w > end {
		return 0, p, false
	}
	return int(n), p + w, true
}

// Analyze walks the whole module (d is the module bytes, dir its directory).
func Analyze(dir *Dir, d []byte, lim Limits) (*Module, error) {
	lim.fill()
	m := &Module{Dir: dir, Why: map[string]int{}}
	a := &an{d: d, m: m, lim: lim, body: -1}
	for _, s := range dir.Secs {
		a.sec = s.ID
		p, end := int(s.Body), int(s.End())
		var ok bool
		switch s.ID {
		case SecCustom:
			if s.Name == "name" {
				a.nameSection(int(s.NameEnd), end)
			}
			continue
		case SecType:
			ok = a.typeSec(p, end)
		case SecImport:
			ok = a.importSec(p, end)
		case SecFunction:
			ok = a.funcSec(p, end)
		case SecTable:
			ok = a.tableSec(p, end)
		case SecMemory:
			ok = a.memSec(p, end)
		case SecTag:
			ok = a.tagSec(p, end)
		case SecGlobal:
			ok = a.globalSec(p, end)
		case SecExport:
			ok = a.exportSec(p, end)
		case SecStart:
			var q int
			m.Start = int64(p)
			q, ok = a.idx(p, SpFunc)
			ok = ok && q == end
		case SecElement:
			ok = a.elemSec(p, end)
		case SecDataCount:
			v, w, k := u32(d, p)
			ok = k && p+w == end
			m.DataCount = int(v)
		case SecCode:
			ok = a.codeSec(p, end)
		case SecData:
			ok = a.dataSec(p, end)
		}
		if !ok {
			if m.gcOrWide {
				return nil, unsup("section %d uses a feature this package does not represent", s.ID)
			}
			return nil, bad("section %d content", s.ID)
		}
	}
	if len(m.Funcs) != len(m.Bodies) {
		return nil, bad("%d function declarations, %d bodies", len(m.Funcs), len(m.Bodies))
	}
	if _, ok := dir.Find(SecDataCount); ok && m.DataCount != len(m.Datas) {
		return nil, bad("data count %d, %d segments", m.DataCount, len(m.Datas))
	}
	sort.SliceStable(m.Slots, func(i, j int) bool { return m.Slots[i].Off < m.Slots[j].Off })
	return m, nil
}
