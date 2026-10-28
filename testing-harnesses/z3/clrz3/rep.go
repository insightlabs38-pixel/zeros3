package clrz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/idxref"
)

// Representation of one assembly (the "indexed-reference" candidate):
//
//	Meta   the exact header regions (PE headers, CLI header, metadata root and stream headers, the
//	       table-stream header) with their positions, the file size and the identity prefix length
//	Comps  groups of the *masked payload* (the rest of the file with every reference slot
//	       overwritten by a width-preserving mask), each followed by eight byte strings:
//	       the reference streams of the slots that start inside the group, one per resolution
//	       stage (stage order is dependency order, see slots.go), and the layout stream
//	         reference   prefix of the target's semantic Identity (K0 bytes + the implied extra bytes,
//	                     plus an occurrence number when several targets share one identity)
//	         layout      uvarints: child counts instead of list starts, deltas instead of RVAs and
//	                     nested-class pairs, self-reference selectors, signature marks, body codes
//
// Decode needs only Meta and the component bytes: it reassembles the masked file, parses the
// exact headers, restores the layout state, then resolves the reference stages in dependency
// order (computing each table's identities from what is already restored) and writes every slot
// back with the width the mask preserved.

const repMagic = "CLR1"

// Config selects the representation parameters.
type Config struct {
	K0   int // identity prefix bytes, 1..4 (default 3)
	Auto int // >0: prefix length chosen per candidate table (idxref.AutoK0 with this collision percentage)
	// Cut returns the exclusive end offsets of the payload groups; secs describe the payload.
	Cut func(payload []byte, secs []idxref.Sec) []int
}

// Reference classes (analysis).
const (
	ClsTable  = iota // simple table index
	ClsCoded         // coded index
	ClsStr           // #Strings
	ClsBlob          // #Blob
	ClsGuid          // #GUID
	ClsSigRef        // type reference embedded in a signature
	ClsToken         // CIL metadata token
	ClsUS            // ldstr
	ClsLayout        // layout items
	NumClasses
)

// ClassName names an analysis class.
var ClassName = [NumClasses]string{"table", "coded", "string", "blob", "guid", "sigref", "token", "ldstr", "layout"}

// Stats are the encoder-side measurements of one representation.
type Stats struct {
	Slots      [NumClasses]int
	RefBytes   [NumClasses]int
	StreamLen  [NumStreams]int
	Payload    int
	Comps      int
	Marked     int // signature blobs normalized
	Blobs      int
	OpaqueBody int
	Bodies     int
	Dups       [4]int // duplicate-identity entries in the string / blob / row / us candidate indexes
	Collisions int
	Excised    int
	RVA        [3]int // method RVAs: predicted from the previous body, null, explicit delta
}

// Rep is an encoded assembly.
type Rep struct {
	Meta  []byte
	Comps [][]byte
	Stats Stats
	Cuts  []int
	Secs  []idxref.Sec
	Refs  []idxref.RefRec
}

func slotClass(s *Slot) int {
	switch s.Sp {
	case SpStr:
		return ClsStr
	case SpBlob:
		return ClsBlob
	case SpGuid:
		return ClsGuid
	case SpUS:
		return ClsUS
	case SpLayout:
		return ClsLayout
	}
	switch {
	case s.Table == TabEmb:
		return ClsSigRef
	case s.Table == TabIL:
		return ClsToken
	case s.Set < NumTables:
		return ClsTable
	}
	return ClsCoded
}

// posMap converts file offsets to payload offsets (the excised header regions removed).
type posMap struct{ regs []Region }

func (m posMap) payload(off int64) int {
	n := off
	for _, r := range m.regs {
		if r.End() <= off {
			n -= r.Len
		}
	}
	return int(n)
}

func zz(v int64) uint64 { return idxref.Zigzag(v) }

// stage orders (table indexes).
var (
	stageG0a = []int{TModule, TModuleRef, TAssembly, TAssemblyRef, TFile}
	stageG0b = []int{TTypeRef, TTypeDef, TExportedType, TManifestResource}
	stageG1  = []int{TTypeSpec}
	stageG2  = []int{TField, TMethodDef, TParam, TInterfaceImpl, TMemberRef, TDeclSecurity, TStandAloneSig, TEventMap, TEvent, TPropertyMap, TProperty, TGenericParam, TMethodSpec, TGenericParamConstraint}
)

func hasIdentity(t int) bool { return t != TEventMap && t != TPropertyMap }

// computeIdentities fills every identity array in stage order.
func computeIdentities(c *Ctx) error {
	for _, t := range stageG0a {
		if err := c.ComputeTable(t); err != nil {
			return err
		}
	}
	for _, t := range stageG0b {
		if err := c.ComputeTable(t); err != nil {
			return err
		}
	}
	c.SigReady()
	for _, t := range stageG1 {
		if err := c.ComputeTable(t); err != nil {
			return err
		}
	}
	for _, t := range stageG2 {
		if hasIdentity(t) {
			if err := c.ComputeTable(t); err != nil {
				return err
			}
		}
	}
	return c.err
}

// bodyPred is the RVA the layout predicts for the next body: the end of the previous body, past
// zero padding.
func bodyPred(f *File, d []byte, prevEnd int64) uint32 {
	p := prevEnd
	for k := 0; k < 4 && p < int64(len(d)) && d[p] == 0; k++ {
		p++
	}
	return offToRVA(f, p)
}

func offToRVA(f *File, off int64) uint32 {
	for _, s := range f.Secs {
		if s.RawSize > 0 && off >= int64(s.RawOff) && off < int64(s.RawOff)+int64(s.RawSize) {
			return uint32(off-int64(s.RawOff)) + s.RVA
		}
	}
	return 0
}

type litem struct {
	off int64
	v   uint64
}

// Encode builds the representation of data (one assembly in memory). The result is verified by a
// full Decode before it is returned; a mismatch is ErrUnsupported (the caller keeps CDC).
func Encode(f *File, data []byte, cfg Config) (*Rep, error) {
	k0 := cfg.K0
	if k0 == 0 {
		k0 = 3
	}
	if k0 < 1 || k0 > 4 {
		return nil, fmt.Errorf("clrz3: K0 %d", k0)
	}
	if f.TabKind != '~' || f.HasPtr {
		return nil, unsup("uncompressed table stream or indirection tables")
	}
	if !f.HasStr || !f.HasBlob {
		return nil, unsup("missing #Strings or #Blob")
	}
	c, err := NewCtx(f, data, k0)
	if err != nil {
		return nil, err
	}
	c.Auto = cfg.Auto
	c.Marks = DeriveMarks(f, data, c.HV)
	bodies := FindBodies(f, data, true)
	ValidateBodies(f, data, c.HV, bodies)
	slots := TableSlots(f, data)
	blobSlots := BlobSlots(f, data, c.Blobs, c.Marks)
	ilSlots := ILSlots(data, bodies)
	if err := c.BuildOwners(); err != nil {
		return nil, err
	}
	extra, err := strExtra(c, slots)
	if err != nil {
		return nil, err
	}
	if err := c.SetStrExtra(extra); err != nil {
		return nil, err
	}
	if err := computeIdentities(c); err != nil {
		return nil, err
	}
	var st Stats
	st.Blobs = len(c.Blobs)
	for _, m := range c.Marks {
		if m != BlobOpaque {
			st.Marked++
		}
	}
	st.Bodies = len(bodies)
	for _, b := range bodies {
		if b.Opaque {
			st.OpaqueBody++
		}
	}
	pm := posMap{f.Excised}
	for _, r := range f.Excised {
		st.Excised += int(r.Len)
	}
	all := append(append(append([]Slot(nil), slots...), blobSlots...), ilSlots...)
	sortSlots(all)
	if err := checkSlots(f, all); err != nil {
		return nil, err
	}

	type item struct {
		pos    int
		stream uint8
		class  uint8
		b      []byte
		off    int64
	}
	var items []item
	emit := func(s *Slot, stream uint8, cls int, b []byte) {
		items = append(items, item{pm.payload(s.Off), stream, uint8(cls), b, s.Off})
		st.RefBytes[cls] += len(b)
		st.StreamLen[stream] += len(b)
	}

	// layout: child counts, deltas, selectors, marks, body codes
	rvaPrevEnd := f.CLIOff + 72
	var prevA, prevFR, prevRes int64
	bodyOf := map[uint32]*BodyInfo{}
	for i := range bodies {
		bodyOf[bodies[i].Row] = &bodies[i]
	}
	selfDelta := map[int64]bool{}
	listNext := func(s *Slot) (uint32, error) {
		cd := schema[s.Table][s.Col]
		tb := &f.Tables[s.Table]
		if s.Row == 1 && s.Val != 1 {
			return 0, unsup("%s.%s starts at %d", TableName(int(s.Table)), cd.Name, s.Val)
		}
		if s.Row < tb.Rows {
			return f.Col(data, int(s.Table), s.Row+1, int(s.Col)), nil
		}
		return f.Rows(cd.T) + 1, nil
	}
	for i := range slots {
		s := &slots[i]
		if s.Sp == SpLayout {
			var v uint64
			switch s.Lay {
			case LList:
				nx, err := listNext(s)
				if err != nil {
					return nil, err
				}
				if nx < s.Val {
					return nil, unsup("descending child list")
				}
				v = uint64(nx - s.Val)
			case LNestedA:
				v = zz(int64(s.Val) - prevA)
				prevA = int64(s.Val)
			case LNestedB:
				v = zz(int64(f.Col(data, TNestedClass, s.Row, 0)) - int64(s.Val))
			case LFieldRVA:
				v = zz(int64(s.Val) - prevFR)
				prevFR = int64(s.Val)
			case LResOff:
				v = zz(int64(s.Val) - prevRes)
				prevRes = int64(s.Val)
			case LRVA:
				var code uint64
				bi := bodyOf[s.Row]
				switch {
				case s.Val == 0:
					code = 1 << 1
					st.RVA[1]++
				default:
					pred := bodyPred(f, data, rvaPrevEnd)
					rc := uint64(0)
					if s.Val != pred {
						rc = 2 + zz(int64(s.Val)-int64(pred))
						st.RVA[2]++
					} else {
						st.RVA[0]++
					}
					code = rc << 1
					if bi == nil || bi.Opaque {
						code |= 1
					} else {
						rvaPrevEnd = bi.Body.End
					}
				}
				v = code
			}
			st.Slots[ClsLayout]++
			b := binary.AppendUvarint(nil, v)
			emit(s, StL, ClsLayout, b)
			continue
		}
		if s.Self >= 0 && s.Sp == SpRow {
			t, r, ok := RowOf(s.Set, s.Val)
			if ok && t == int(s.Self) {
				sel := binary.AppendUvarint(nil, 1+zz(int64(s.Row)-int64(r)))
				selfDelta[s.Off] = true
				st.Slots[ClsLayout]++
				emit(s, StL, ClsLayout, sel)
				continue
			}
			sel := []byte{0}
			st.Slots[ClsLayout]++
			emit(s, StL, ClsLayout, sel)
		}
	}
	// the referenced suffix offsets of #Strings: count, then ascending deltas, at the heap start
	{
		b := binary.AppendUvarint(nil, uint64(len(extra)))
		prev := 0
		for _, o := range extra {
			b = binary.AppendUvarint(b, uint64(o-prev))
			prev = o
		}
		s := &Slot{Off: f.Str.Off}
		st.Slots[ClsLayout]++
		emit(s, StL, ClsLayout, b)
	}
	// signature marks (one item per blob entry, at the entry start)
	for i, e := range c.Blobs {
		s := &Slot{Off: f.Blob.Off + int64(e.Off)}
		st.Slots[ClsLayout]++
		emit(s, StL, ClsLayout, binary.AppendUvarint(nil, uint64(c.Marks[i])))
	}
	// references
	for i := range all {
		s := &all[i]
		if s.Sp == SpLayout || selfDelta[s.Off] {
			continue
		}
		b, err := c.Put(nil, s, s.Stream >= StG1)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		cls := slotClass(s)
		st.Slots[cls]++
		emit(s, s.Stream, cls, b)
	}
	if c.err != nil {
		return nil, c.err
	}
	st.Dups[0] = c.StrIndex().Duplicates()
	st.Dups[1] = c.BlobIndex(true).Duplicates()
	st.Dups[3] = c.USIndex().Duplicates()
	if ix, ok := c.rowIdx[[2]int{SetCoded0 + CHasCustomAttribute, -1}]; ok {
		st.Dups[2] = ix.Duplicates()
	}

	// masked file
	masked := append([]byte(nil), data...)
	for i := range all {
		s := &all[i]
		switch s.Enc {
		case EncFixed, EncToken:
			clear(masked[s.Off : s.Off+int64(s.Len)])
		case EncCompressed:
			maskCompressed(masked[s.Off:s.Off+int64(s.Len)], int(s.Len))
		}
	}
	payload := make([]byte, 0, len(data))
	pos := int64(0)
	for _, r := range f.Excised {
		payload = append(payload, masked[pos:r.Off]...)
		pos = r.End()
	}
	payload = append(payload, masked[pos:]...)
	st.Payload = len(payload)

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].pos != items[j].pos {
			return items[i].pos < items[j].pos
		}
		return items[i].off < items[j].off
	})
	secs := buildSecs(f, data, c.HV, bodies, pm, len(payload))
	// item hashes: payload bytes plus the reference bytes of the slots inside
	ri := 0
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for k, a := range sc.Items {
			end := sc.End
			if k+1 < len(sc.Items) {
				end = sc.Items[k+1]
			}
			h := idxref.FNV64(idxref.FNVInit, payload[a:end])
			for ri < len(items) && items[ri].pos < a {
				ri++
			}
			for j := ri; j < len(items) && items[j].pos < end; j++ {
				h = idxref.FNV64(h^uint64(items[j].stream), items[j].b)
			}
			sc.Hash[k] = h
		}
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts = cfg.Cut(payload, secs)
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != len(payload) {
		cuts = append(cuts, len(payload))
	}
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	ri, start := 0, 0
	for _, end := range cuts {
		if end < start || end > len(payload) {
			return nil, fmt.Errorf("clrz3: bad cut %d", end)
		}
		var streams [NumStreams][]byte
		for ri < len(items) && items[ri].pos < end {
			streams[items[ri].stream] = append(streams[items[ri].stream], items[ri].b...)
			rep.Refs = append(rep.Refs, idxref.RefRec{Comp: len(rep.Comps), Stream: items[ri].stream, Class: items[ri].class, B: items[ri].b})
			ri++
		}
		rep.Comps = append(rep.Comps, idxref.PackComp(payload[start:end], streams[:]))
		start = end
	}
	rep.Stats.Comps = len(rep.Comps)
	// meta
	m := append([]byte(repMagic), byte(k0), byte(cfg.Auto))
	m = binary.AppendUvarint(m, uint64(len(data)))
	m = binary.AppendUvarint(m, uint64(len(f.Excised)))
	for _, r := range f.Excised {
		m = binary.AppendUvarint(m, uint64(r.Off))
		m = binary.AppendUvarint(m, uint64(r.Len))
	}
	for _, r := range f.Excised {
		m = append(m, data[r.Off:r.End()]...)
	}
	rep.Meta = m
	got, err := Decode(rep.Meta, rep.Comps)
	if err != nil {
		return nil, fmt.Errorf("%w: self-check decode: %v", ErrUnsupported, err)
	}
	if !bytes.Equal(got, data) {
		return nil, fmt.Errorf("%w: self-check mismatch", ErrUnsupported)
	}
	return rep, nil
}

// checkSlots rejects slot sets that overlap one another or a header region (a hostile layout: a
// method body inside the metadata root, two bodies sharing bytes).
func checkSlots(f *File, all []Slot) error {
	end := int64(0)
	ri := 0
	for i := range all {
		s := &all[i]
		if s.Len == 0 {
			continue
		}
		if s.Off < end {
			return unsup("overlapping reference slots at %d", s.Off)
		}
		end = s.Off + int64(s.Len)
		for ri < len(f.Excised) && f.Excised[ri].End() <= s.Off {
			ri++
		}
		if ri < len(f.Excised) && f.Excised[ri].Off < end {
			return unsup("reference slot at %d inside a header region", s.Off)
		}
	}
	return nil
}

// strExtra lists, ascending, the #Strings offsets the table slots reference that are not the start
// of a physical entry.
func strExtra(c *Ctx, slots []Slot) ([]int, error) {
	seen := map[int]bool{}
	for i := range slots {
		s := &slots[i]
		if s.Sp != SpStr {
			continue
		}
		o := int(s.Val)
		if o >= len(c.str) {
			return nil, unsup("#Strings offset %d outside the heap", o)
		}
		if o != 0 && c.str[o-1] != 0 {
			seen[o] = true
		}
	}
	out := make([]int, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Ints(out)
	return out, nil
}
