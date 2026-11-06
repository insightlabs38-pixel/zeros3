package clrz3

import (
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/idxref"
)

// Semantic identities. Every identity is built from native metadata content — heap bytes, names,
// signatures with their embedded type references replaced by the identities they name — never
// from a row number, heap offset or token. Inheritance and interface lists are not part of a
// type's identity: a type is its (namespace, name, enclosing type), so the identity graph has no
// cycles except the explicit self references of TypeRef scopes and nested types, which are
// resolved by a bounded, cycle-checked recursion.

const (
	tagStr   = 0x51
	tagGuid  = 0x52
	tagUS    = 0x53
	tagBlob  = 0x54
	tagSig   = 0x55
	tagNull  = 0x56
	tagRow   = 0x60 // + table
	maxStrHp = 8 << 20
)

// Ctx is the identity context of one file: the bytes (original, or progressively restored by the
// decoder), heap views, per-table identity arrays and the candidate indexes.
type Ctx struct {
	strEnds             []uint32
	workLimit, workUsed int64
	idDepth             int
	F                   *File
	D                   []byte
	source              *clrSource
	K0                  int
	Auto                int

	str, blob, us, guid []byte
	HV                  *HeapView
	Blobs               []BlobEntry
	USes                []BlobEntry
	Marks               []byte
	blobAt              map[uint32]int
	usAt                map[uint32]int

	IDs       [NumTables][]idxref.Identity
	state     [NumTables][]uint8 // 0 new, 1 computing, 2 done
	ready     [NumTables]bool
	computing [NumTables]bool
	blobID    []idxref.Identity
	blobDone  []uint8
	sigReady  bool

	fieldOwner, methodOwner, paramOwner []uint32
	nestedOf                            []uint32
	evOwner, propOwner                  []uint32
	evBuilt, propBuilt                  bool

	rowIdx   map[[2]int]*idxref.ClassIndex
	StrExtra []int
	strIdx   *idxref.ClassIndex
	usIdx    *idxref.ClassIndex
	guidIdx  *idxref.ClassIndex
	blobPre  *idxref.ClassIndex
	blobAll  *idxref.ClassIndex

	err error
}

var nullID = idxref.Hash(tagNull)

// NewCtx builds the context over d (the file bytes the identities read).
func NewCtx(f *File, d []byte, k0 int) (*Ctx, error) {
	c := &Ctx{F: f, D: d, K0: k0, rowIdx: map[[2]int]*idxref.ClassIndex{}}
	if f.HasStr {
		if f.Str.Size > maxStrHp {
			return nil, limit("#Strings of %d bytes", f.Str.Size)
		}
		c.str = d[f.Str.Off : f.Str.Off+f.Str.Size]
	}
	hv, err := NewHeapView(f, d)
	if err != nil {
		return nil, err
	}
	c.HV, c.blob, c.us, c.Blobs, c.USes, c.blobAt, c.usAt = hv, hv.Blob, hv.US, hv.Blobs, hv.USes, hv.BlobAt, hv.USAt
	if f.HasGUID {
		c.guid = d[f.GUID.Off : f.GUID.Off+f.GUID.Size]
		if len(c.guid)%16 != 0 {
			return nil, unsup("#GUID size %d", len(c.guid))
		}
	}
	for t := 0; t < NumTables; t++ {
		n := int(f.Tables[t].Rows)
		c.IDs[t] = make([]idxref.Identity, n)
		c.state[t] = make([]uint8, n)
	}
	c.Marks = make([]byte, len(c.Blobs))
	c.blobID = make([]idxref.Identity, len(c.Blobs))
	c.blobDone = make([]uint8, len(c.Blobs))
	return c, nil
}

func (c *Ctx) fail(f string, a ...any) {
	if c.err == nil {
		c.err = unsup(f, a...)
	}
}

func lp(b []byte) []byte { return append(binary.AppendUvarint(nil, uint64(len(b))), b...) }

// strAt returns the NUL-terminated string at heap offset off.
func (c *Ctx) strAt(off uint32) []byte {
	if c.err != nil {
		return nil
	}
	if uint64(off) >= uint64(len(c.str)) {
		if off == 0 && len(c.str) == 0 {
			return nil
		}
		c.fail("#Strings offset %d outside the heap", off)
		return nil
	}
	if c.strEnds != nil {
		j := sort.Search(len(c.strEnds), func(i int) bool { return c.strEnds[i] >= off })
		if j == len(c.strEnds) {
			c.fail("unterminated #Strings entry at %d", off)
			return nil
		}
		n := int(c.strEnds[j] - off)
		if !c.chargeIdentity(n) {
			return nil
		}
		return c.str[off:c.strEnds[j]]
	}
	b := c.str[off:]
	for i, x := range b {
		if x == 0 {
			return b[:i]
		}
	}
	c.fail("unterminated #Strings entry at %d", off)
	return nil
}

func (c *Ctx) col(t int, r uint32, ci int) uint32 {
	if c.source != nil {
		v := c.source.col(c.F, t, r, ci)
		if c.source.err != nil && c.err == nil {
			c.err = c.source.err
		}
		return v
	}
	return c.F.Col(c.D, t, r, ci)
}

func (c *Ctx) strCol(t int, r uint32, ci int) []byte { return lp(c.strAt(c.col(t, r, ci))) }

// blobCol returns the identity of the blob entry a column points to.
func (c *Ctx) blobCol(t int, r uint32, ci int) []byte {
	id := c.blobIdentity(c.col(t, r, ci))
	return id[:]
}

// BlobBytes returns the content of the blob entry at heap offset off.
func (c *Ctx) BlobBytes(off uint32) ([]byte, bool) {
	i, ok := c.blobAt[off]
	if !ok {
		return nil, false
	}
	e := c.Blobs[i]
	return c.blob[e.DataOff : e.DataOff+e.Len], true
}

// blobIdentity: an opaque blob is named by its bytes; a marked signature by its bytes with each
// embedded type reference replaced by the identity of the type it names.
func (c *Ctx) blobIdentity(off uint32) idxref.Identity {
	i, ok := c.blobAt[off]
	if !ok {
		c.fail("#Blob offset %d is not an entry", off)
		return idxref.Identity{}
	}
	return c.entryIdentity(i)
}

func (c *Ctx) entryIdentity(i int) idxref.Identity {
	if c.blobDone[i] == 2 {
		return c.blobID[i]
	}
	e := c.Blobs[i]
	b := c.blob[e.DataOff : e.DataOff+e.Len]
	if !c.chargeIdentity(len(b)) {
		return idxref.Identity{}
	}
	m := c.Marks[i]
	if m == BlobOpaque {
		c.blobID[i] = idxref.Hash(tagBlob, b)
		c.blobDone[i] = 2
		return c.blobID[i]
	}
	if !c.sigReady {
		c.fail("signature identity before the signature references are resolved")
		return idxref.Identity{}
	}
	sl, ok := ParseSig(m, b, false)
	if !ok {
		c.fail("marked blob %d does not parse", i)
		return idxref.Identity{}
	}
	if !c.chargeIdentity(32 * len(sl)) {
		return idxref.Identity{}
	}
	var n []byte
	p := 0
	for _, s := range sl {
		n = append(n, b[p:s.Off]...)
		t, r, ok := RowOf(SetEmbType, s.Val)
		if !ok || t < 0 {
			c.fail("embedded reference %#x", s.Val)
			return idxref.Identity{}
		}
		id := c.id(t, r)
		n = append(n, id[:]...)
		p = s.Off + s.Width
	}
	n = append(n, b[p:]...)
	c.blobID[i] = idxref.Hash(tagSig, []byte{m}, n)
	c.blobDone[i] = 2
	return c.blobID[i]
}

// id returns the identity of row r (1-based) of table t, computing it on demand.
func (c *Ctx) id(t int, r uint32) idxref.Identity {
	if c.err != nil {
		return idxref.Identity{}
	}
	if t < 0 || t >= NumTables || r == 0 || r > c.F.Tables[t].Rows {
		c.fail("row %d of table %d", r, t)
		return idxref.Identity{}
	}
	if !c.ready[t] && !c.computing[t] {
		c.fail("identity of %s needed before its stage", TableName(t))
		return idxref.Identity{}
	}
	switch c.state[t][r-1] {
	case 2:
		return c.IDs[t][r-1]
	case 1:
		c.fail("identity cycle at %s row %d", TableName(t), r)
		return idxref.Identity{}
	}
	if c.idDepth >= 256 {
		c.err = limit("identity dependency depth")
		return idxref.Identity{}
	}
	c.idDepth++
	defer func() { c.idDepth-- }()
	c.state[t][r-1] = 1
	id := c.key(t, r)
	c.state[t][r-1] = 2
	c.IDs[t][r-1] = id
	return id
}

// refKey is the identity of the row a stored number names (null = the null identity).
func (c *Ctx) refKey(set uint8, v uint32) []byte {
	t, r, ok := RowOf(set, v)
	if !ok {
		c.fail("reference %#x outside set %d", v, set)
		return nullID[:]
	}
	if t < 0 {
		return nullID[:]
	}
	id := c.id(t, r)
	return id[:]
}

func (c *Ctx) codedKey(fam int, t int, r uint32, ci int) []byte {
	return c.refKey(uint8(SetCoded0+fam), c.col(t, r, ci))
}

func (c *Ctx) simpleKey(target int, t int, r uint32, ci int) []byte {
	return c.refKey(uint8(target), c.col(t, r, ci))
}

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// key is the semantic identity definition of each table.
func (c *Ctx) key(t int, r uint32) idxref.Identity {
	tag := byte(tagRow + t)
	switch t {
	case TModule:
		return idxref.Hash(tag, c.strCol(t, r, 1))
	case TModuleRef, TFile:
		ci := 0
		if t == TFile {
			ci = 1
		}
		return idxref.Hash(tag, c.strCol(t, r, ci))
	case TAssembly:
		return idxref.Hash(tag, c.strCol(t, r, 7), c.strCol(t, r, 8), c.blobCol(t, r, 6))
	case TAssemblyRef:
		return idxref.Hash(tag, c.strCol(t, r, 6), c.strCol(t, r, 7), c.blobCol(t, r, 5))
	case TTypeRef:
		return idxref.Hash(tag, c.codedKey(CResolutionScope, t, r, 0), c.strCol(t, r, 2), c.strCol(t, r, 1))
	case TTypeDef:
		var enc []byte
		if e := c.nested(r); e != 0 {
			id := c.id(TTypeDef, e)
			enc = id[:]
		}
		return idxref.Hash(tag, c.strCol(t, r, 2), c.strCol(t, r, 1), enc)
	case TExportedType:
		return idxref.Hash(tag, c.strCol(t, r, 3), c.strCol(t, r, 2), c.codedKey(CImplementation, t, r, 4))
	case TManifestResource:
		return idxref.Hash(tag, c.strCol(t, r, 2), c.codedKey(CImplementation, t, r, 3))
	case TTypeSpec, TStandAloneSig:
		return idxref.Hash(tag, c.blobCol(t, r, 0))
	case TField:
		o := c.id(TTypeDef, c.owner(c.fieldOwner, r))
		return idxref.Hash(tag, o[:], c.strCol(t, r, 1), c.blobCol(t, r, 2))
	case TMethodDef:
		o := c.id(TTypeDef, c.owner(c.methodOwner, r))
		return idxref.Hash(tag, o[:], c.strCol(t, r, 3), c.blobCol(t, r, 4))
	case TParam:
		o := c.id(TMethodDef, c.owner(c.paramOwner, r))
		return idxref.Hash(tag, o[:], u32(c.col(t, r, 1)))
	case TInterfaceImpl:
		return idxref.Hash(tag, c.simpleKey(TTypeDef, t, r, 0), c.codedKey(CTypeDefOrRef, t, r, 1))
	case TMemberRef:
		return idxref.Hash(tag, c.codedKey(CMemberRefParent, t, r, 0), c.strCol(t, r, 1), c.blobCol(t, r, 2))
	case TEvent:
		c.buildMap(TEventMap, TEvent, &c.evOwner, &c.evBuilt)
		o := c.id(TTypeDef, c.owner(c.evOwner, r))
		return idxref.Hash(tag, o[:], c.strCol(t, r, 1), c.codedKey(CTypeDefOrRef, t, r, 2))
	case TProperty:
		c.buildMap(TPropertyMap, TProperty, &c.propOwner, &c.propBuilt)
		o := c.id(TTypeDef, c.owner(c.propOwner, r))
		return idxref.Hash(tag, o[:], c.strCol(t, r, 1), c.blobCol(t, r, 2))
	case TGenericParam:
		return idxref.Hash(tag, c.codedKey(CTypeOrMethodDef, t, r, 2), u32(c.col(t, r, 0)))
	case TGenericParamConstraint:
		return idxref.Hash(tag, c.simpleKey(TGenericParam, t, r, 0), c.codedKey(CTypeDefOrRef, t, r, 1))
	case TMethodSpec:
		return idxref.Hash(tag, c.codedKey(CMethodDefOrRef, t, r, 0), c.blobCol(t, r, 1))
	case TDeclSecurity:
		return idxref.Hash(tag, u32(c.col(t, r, 0)), c.codedKey(CHasDeclSecurity, t, r, 1), c.blobCol(t, r, 2))
	}
	c.fail("no identity for table %s", TableName(t))
	return idxref.Identity{}
}

func (c *Ctx) owner(a []uint32, r uint32) uint32 {
	if int(r) > len(a) || a[r-1] == 0 {
		c.fail("row %d has no owner", r)
		return 0
	}
	return a[r-1]
}

func (c *Ctx) nested(r uint32) uint32 {
	if int(r) > len(c.nestedOf) {
		return 0
	}
	return c.nestedOf[r-1]
}

// BuildOwners derives the owner of every Field, MethodDef and Param row and the enclosing type of
// every nested TypeDef from the (layout) list-start and NestedClass columns.
func (c *Ctx) BuildOwners() error {
	f := c.F
	spread := func(parent, list, child int) ([]uint32, error) {
		out := make([]uint32, f.Rows(child))
		np := f.Rows(parent)
		for p := uint32(1); p <= np; p++ {
			lo := c.col(parent, p, list)
			hi := uint32(f.Rows(child)) + 1
			if p < np {
				hi = c.col(parent, p+1, list)
			}
			if lo < 1 || hi < lo || hi > f.Rows(child)+1 {
				return nil, unsup("%s list %d..%d of %d", TableName(parent), lo, hi, f.Rows(child))
			}
			for x := lo; x < hi; x++ {
				out[x-1] = p
			}
		}
		return out, nil
	}
	var err error
	if c.fieldOwner, err = spread(TTypeDef, 4, TField); err != nil {
		return err
	}
	if c.methodOwner, err = spread(TTypeDef, 5, TMethodDef); err != nil {
		return err
	}
	if c.paramOwner, err = spread(TMethodDef, 5, TParam); err != nil {
		return err
	}
	c.nestedOf = make([]uint32, f.Rows(TTypeDef))
	for r := uint32(1); r <= f.Rows(TNestedClass); r++ {
		n, e := c.col(TNestedClass, r, 0), c.col(TNestedClass, r, 1)
		if n == 0 || n > f.Rows(TTypeDef) || e == 0 || e > f.Rows(TTypeDef) || c.nestedOf[n-1] != 0 {
			return unsup("NestedClass row %d", r)
		}
		c.nestedOf[n-1] = e
	}
	return nil
}

// buildMap derives event / property owners from EventMap / PropertyMap (resolved key columns).
func (c *Ctx) buildMap(mt, child int, dst *[]uint32, built *bool) {
	if *built {
		return
	}
	*built = true
	f := c.F
	out := make([]uint32, f.Rows(child))
	n := f.Rows(mt)
	for p := uint32(1); p <= n; p++ {
		lo := c.col(mt, p, 1)
		hi := f.Rows(child) + 1
		if p < n {
			hi = c.col(mt, p+1, 1)
		}
		if lo < 1 || hi < lo || hi > f.Rows(child)+1 {
			c.fail("%s list %d..%d", TableName(mt), lo, hi)
			return
		}
		par := c.col(mt, p, 0)
		for x := lo; x < hi; x++ {
			out[x-1] = par
		}
	}
	*dst = out
}

// ComputeTable fills the identity array of table t (every key column it reads must be restored).
func (c *Ctx) ComputeTable(t int) error {
	c.computing[t] = true
	for r := uint32(1); r <= c.F.Tables[t].Rows; r++ {
		c.id(t, r)
		if c.err != nil {
			break
		}
	}
	c.computing[t] = false
	c.ready[t] = true
	return c.err
}

// SetReady marks a table whose identities nobody asks for as available (no key columns).
func (c *Ctx) SetReady(t int) { c.ready[t] = true }

// ---------------------------------------------------------------------------------------------
// Candidate indexes.

func (c *Ctx) classIndex(handles []int, ids []idxref.Identity) *idxref.ClassIndex {
	k := c.K0
	if c.Auto > 0 {
		k = idxref.AutoK0(len(ids), c.Auto)
	}
	return idxref.NewClassIndex(handles, ids, k)
}

// RowIndex is the candidate index of a row reference set (the null reference plus every row of
// the set's tables).
func (c *Ctx) RowIndex(set uint8) (*idxref.ClassIndex, error) { return c.rowIndexEx(set, -1) }

// rowIndexEx is RowIndex without the rows of table excl (a self-capable coded slot names its own
// table through the layout stream, never through the candidate index).
func (c *Ctx) rowIndexEx(set uint8, excl int) (*idxref.ClassIndex, error) {
	key := [2]int{int(set), excl}
	if ix, ok := c.rowIdx[key]; ok {
		return ix, nil
	}
	candidates := 1
	for _, t := range SetTables(set) {
		if t != excl {
			candidates += int(c.F.Tables[t].Rows)
		}
	}
	handles := make([]int, 1, candidates)
	ids := make([]idxref.Identity, 1, candidates)
	ids[0] = nullID
	for _, t := range SetTables(set) {
		if t == excl {
			continue
		}
		if !c.ready[t] {
			return nil, unsup("candidate set %d needs table %s", set, TableName(t))
		}
		for r := uint32(1); r <= c.F.Tables[t].Rows; r++ {
			handles = append(handles, t<<24|int(r))
			ids = append(ids, c.IDs[t][r-1])
		}
	}
	ix := c.classIndex(handles, ids)
	c.rowIdx[key] = ix
	return ix, nil
}

// StrIndex names the candidates of #Strings: every physical entry (the NUL-delimited string) and
// the suffix offsets the file really references (StrExtra, carried in the layout stream so the
// decoder knows them before it reads a reference). A suffix-shared name is "the tail of this
// entry from byte k"; it does not move when unrelated entries are inserted and needs no occurrence
// number. The candidate table is therefore the size of the entry list, not of the heap.
func (c *Ctx) StrIndex() *idxref.ClassIndex {
	if c.strIdx != nil {
		return c.strIdx
	}
	var handles []int
	var ids []idxref.Identity
	start, xi := 0, 0
	for i := 0; i <= len(c.str); i++ {
		if i < len(c.str) && c.str[i] != 0 {
			continue
		}
		entry := c.str[start:i]
		handles = append(handles, start)
		if !c.chargeIdentity(len(entry)) {
			return idxref.NewClassIndex(nil, nil, c.K0)
		}
		ids = append(ids, idxref.Hash(tagStr, lp(entry), []byte{0}))
		for xi < len(c.StrExtra) && c.StrExtra[xi] <= i {
			if o := c.StrExtra[xi]; o > start {
				handles = append(handles, o)
				if !c.chargeIdentity(len(entry)) {
					return idxref.NewClassIndex(nil, nil, c.K0)
				}
				ids = append(ids, idxref.Hash(tagStr, lp(entry), binary.AppendUvarint(nil, uint64(o-start))))
			}
			xi++
		}
		start = i + 1
	}
	c.strIdx = c.classIndex(handles, ids)
	return c.strIdx
}

// SetStrExtra declares the referenced suffix offsets (ascending, each inside an entry, none at an
// entry start). The decoder takes them from the layout stream.
func (c *Ctx) SetStrExtra(extra []int) error {
	prev := 0
	for _, o := range extra {
		if o <= prev || o > len(c.str) || (o < len(c.str) && c.str[o]&0xC0 == 0x80) || o == 0 || c.str[o-1] == 0 {
			return bad("string suffix offset %d", o)
		}
		prev = o
	}
	c.StrExtra, c.strIdx = extra, nil
	return nil
}

// USIndex names the #US entries by their exact bytes.
func (c *Ctx) USIndex() *idxref.ClassIndex {
	if c.usIdx != nil {
		return c.usIdx
	}
	handles := make([]int, len(c.USes))
	ids := make([]idxref.Identity, len(c.USes))
	for i, e := range c.USes {
		handles[i] = int(e.Off)
		ids[i] = idxref.Hash(tagUS, c.us[e.DataOff:e.DataOff+e.Len])
	}
	c.usIdx = c.classIndex(handles, ids)
	return c.usIdx
}

// GuidIndex names #GUID ordinals (0 = null) by their bytes.
func (c *Ctx) GuidIndex() *idxref.ClassIndex {
	if c.guidIdx != nil {
		return c.guidIdx
	}
	n := len(c.guid) / 16
	handles := []int{0}
	ids := []idxref.Identity{nullID}
	for i := 0; i < n; i++ {
		handles = append(handles, i+1)
		ids = append(ids, idxref.Hash(tagGuid, c.guid[16*i:16*i+16]))
	}
	c.guidIdx = c.classIndex(handles, ids)
	return c.guidIdx
}

// BlobIndex names blob entries. Before the signature references exist (stages that run first)
// only unmarked entries, named by their bytes, can be targets; afterwards every entry.
func (c *Ctx) BlobIndex(all bool) *idxref.ClassIndex {
	if !all && c.blobPre != nil {
		return c.blobPre
	}
	if all && c.blobAll != nil {
		return c.blobAll
	}
	var handles []int
	var ids []idxref.Identity
	for i, e := range c.Blobs {
		if !all && c.Marks[i] != BlobOpaque {
			continue
		}
		handles = append(handles, int(e.Off))
		ids = append(ids, c.entryIdentity(i))
	}
	ix := c.classIndex(handles, ids)
	if all {
		c.blobAll = ix
	} else {
		c.blobPre = ix
	}
	return ix
}

// SigReady declares every embedded signature reference restored: marked blobs can be named.
func (c *Ctx) SigReady() { c.sigReady = true }

// Put appends the reference for slot s (encoder side; s.Val is the original number).
func (c *Ctx) Put(dst []byte, s *Slot, allBlobs bool) ([]byte, error) {
	if c.err != nil {
		return dst, c.err
	}
	switch s.Sp {
	case SpStr:
		ix := c.StrIndex()
		if c.err != nil {
			return dst, c.err
		}
		return ix.Put(dst, int(s.Val))
	case SpGuid:
		ix := c.GuidIndex()
		if c.err != nil {
			return dst, c.err
		}
		return ix.Put(dst, int(s.Val))
	case SpUS:
		ix := c.USIndex()
		if c.err != nil {
			return dst, c.err
		}
		return ix.Put(dst, int(s.Val&0xffffff))
	case SpBlob:
		ix := c.BlobIndex(allBlobs)
		if c.err != nil {
			return dst, c.err
		}
		return ix.Put(dst, int(s.Val))
	case SpRow:
		t, r, ok := RowOf(s.Set, s.Val)
		if !ok {
			return dst, bad("slot at %d: value %#x is outside set %d", s.Off, s.Val, s.Set)
		}
		ix, err := c.rowIndexEx(s.Set, int(s.Self))
		if err != nil {
			return dst, err
		}
		h := 0
		if t >= 0 {
			if r > c.F.Tables[t].Rows {
				return dst, bad("slot at %d: row %d of %s beyond %d", s.Off, r, TableName(t), c.F.Tables[t].Rows)
			}
			h = t<<24 | int(r)
		}
		return ix.Put(dst, h)
	}
	return dst, fmt.Errorf("clrz3: Put of space %d", s.Sp)
}

// Get reads one reference for slot s (decoder side) and returns the number to store.
func (c *Ctx) Get(src []byte, s *Slot, allBlobs bool) (uint32, []byte, error) {
	switch s.Sp {
	case SpStr:
		h, rest, err := c.StrIndex().Get(src)
		return uint32(h), rest, err
	case SpGuid:
		h, rest, err := c.GuidIndex().Get(src)
		return uint32(h), rest, err
	case SpUS:
		h, rest, err := c.USIndex().Get(src)
		return 0x70000000 | uint32(h), rest, err
	case SpBlob:
		h, rest, err := c.BlobIndex(allBlobs).Get(src)
		return uint32(h), rest, err
	case SpRow:
		ix, err := c.rowIndexEx(s.Set, int(s.Self))
		if err != nil {
			return 0, nil, err
		}
		h, rest, err := ix.Get(src)
		if err != nil {
			return 0, nil, err
		}
		if h == 0 {
			return 0, rest, nil
		}
		v, ok := ValOf(s.Set, h>>24, uint32(h&0xffffff))
		if !ok {
			return 0, nil, bad("target %d/%d is not in set %d", h>>24, h&0xffffff, s.Set)
		}
		return v, rest, nil
	}
	return 0, nil, fmt.Errorf("clrz3: Get of space %d", s.Sp)
}

// Identity work is charged by canonical content bytes, including repeated suffix
// names. A shared long string cannot force quadratic hashing or allocation.
func (c *Ctx) chargeIdentity(n int) bool {
	if c.err != nil {
		return false
	}
	if c.workLimit == 0 {
		return true
	}
	cost := int64(n) + 1
	if cost > c.workLimit-c.workUsed {
		c.err = limit("aggregate identity byte work")
		return false
	}
	c.workUsed += cost
	return true
}
func (c *Ctx) prepareStringBounds() error {
	count := 0
	for _, b := range c.str {
		if b == 0 {
			count++
			if count > 1<<18 {
				return limit("string entry count")
			}
		}
	}
	c.strEnds = make([]uint32, 0, count)
	for i, b := range c.str {
		if b == 0 {
			c.strEnds = append(c.strEnds, uint32(i))
		}
	}
	return nil
}
