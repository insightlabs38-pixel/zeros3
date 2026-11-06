package clrz3

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"zeros3-z3/idxref"
)

type bodyGeom struct {
	Row      uint32
	Off, End int64
	Opaque   bool
}

// EncodeAt owns bounded metadata, reads source windows and emits exact components.
func EncodeAt(f *File, r io.ReaderAt, cfg StreamConfig) (*Rep, error) {
	if f.Size <= 0 || f.Size > 32<<20 {
		return nil, fmt.Errorf("%w: managed creation source size", ErrLimit)
	}
	data := &clrSource{r: r, size: f.Size, lo: -1}
	if cfg.Emit == nil {
		return nil, bad("missing component sink")
	}
	source := data
	scratch := make([]byte, 64<<10)
	spool, err := idxref.NewRefSpool(cfg.TempDir, 256<<20)
	if err != nil {
		return nil, err
	}
	defer spool.Close()

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
	c, err := newCtxAt(f, data, k0)
	if err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("context")
	}
	c.Auto = cfg.Auto
	c.Marks = deriveMarksAt(c)
	bodies, err := compactFindBodiesAt(f, data, true)
	if err != nil {
		return nil, err
	}
	validateBodiesAt(f, data, c.HV, bodies)
	capacity, err := countCompactSlotsAt(f, data, c.blob, c.Blobs, c.Marks, bodies)
	if err != nil {
		return nil, err
	}
	all, err := compactTableSlotsAt(f, data, make([]Slot, 0, capacity))
	if err != nil {
		return nil, err
	}
	all = compactBlobSlotsAt(f, data, c.blob, c.Blobs, c.Marks, all)
	all, err = compactILSlotsAt(data, bodies, all)
	if err != nil {
		return nil, err
	}
	geoms := make([]bodyGeom, len(bodies))
	for i, b := range bodies {
		geoms[i] = bodyGeom{Row: b.Row, Off: b.Body.Off, End: b.Body.End, Opaque: b.Opaque}
	}
	bodies = nil
	if cfg.Observe != nil {
		cfg.Observe("slots")
	}
	if err := c.BuildOwners(); err != nil {
		return nil, err
	}
	extra, err := strExtra(c, all)
	if err != nil {
		return nil, err
	}
	if err := c.SetStrExtra(extra); err != nil {
		return nil, err
	}
	if err := computeIdentities(c); err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("identities")
	}
	var st Stats
	st.Blobs = len(c.Blobs)
	for _, m := range c.Marks {
		if m != BlobOpaque {
			st.Marked++
		}
	}
	st.Bodies = len(geoms)
	for _, b := range geoms {
		if b.Opaque {
			st.OpaqueBody++
		}
	}
	pm := posMap{f.Excised}
	for _, r := range f.Excised {
		st.Excised += int(r.Len)
	}
	if cfg.Observe != nil {
		cfg.Observe("all-slots")
	}
	sortSlots(all)
	if err := checkSlots(f, all); err != nil {
		return nil, err
	}

	maxReferences := len(all) + len(c.Blobs) + 1
	for _, s := range all {
		if s.Self >= 0 && s.Sp == SpRow {
			maxReferences++
		}
	}
	sorted, err := idxref.NewSortedRefSpool(cfg.TempDir, maxReferences, 256<<20)
	if err != nil {
		return nil, err
	}
	defer sorted.Close()
	var emitErr error
	emit := func(s *Slot, stream uint8, cls int, b []byte) {
		if emitErr != nil {
			return
		}
		emitErr = sorted.Append(int64(pm.payload(s.Off)), s.Off, stream, uint8(cls), b)
		st.RefBytes[cls] += len(b)
		st.StreamLen[stream] += len(b)
	}
	// layout: child counts, deltas, selectors, marks, body codes
	layoutSource := &clrSource{r: data.r, size: data.size, lo: -1}
	rvaPrevEnd := f.CLIOff + 72
	var prevA, prevFR, prevRes int64
	bodyOf := map[uint32]*bodyGeom{}
	for i := range geoms {
		bodyOf[geoms[i].Row] = &geoms[i]
	}
	selfDelta := map[int64]bool{}
	listNext := func(s *Slot) (uint32, error) {
		cd := schema[s.Table][s.Col]
		tb := &f.Tables[s.Table]
		if s.Row == 1 && s.Val != 1 {
			return 0, unsup("%s.%s starts at %d", TableName(int(s.Table)), cd.Name, s.Val)
		}
		if s.Row < tb.Rows {
			v := data.col(f, int(s.Table), s.Row+1, int(s.Col))
			return v, data.err
		}
		return f.Rows(cd.T) + 1, nil
	}
	for i := range all {
		s := &all[i]
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
				v = zz(int64(data.col(f, TNestedClass, s.Row, 0)) - int64(s.Val))
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
					pred, e := bodyPredAt(f, layoutSource, rvaPrevEnd)
					if e != nil {
						return nil, e
					}
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
						rvaPrevEnd = bi.End
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
	if cfg.Observe != nil {
		cfg.Observe("layout")
	}
	// Resolve one bounded candidate set at a time; spool order restores the exact
	// legacy positional reference order. Avoid retaining overlapping token sets.
	clearIndexes := func() {
		c.rowIdx = map[[2]int]*idxref.ClassIndex{}
		c.strIdx = nil
		c.usIdx = nil
		c.guidIdx = nil
		c.blobPre = nil
		c.blobAll = nil
	}
	refKey := func(s *Slot) uint64 {
		flag := uint64(0)
		if s.Sp == SpBlob && s.Stream >= StG1 {
			flag = 1
		}
		return uint64(s.Sp)<<24 | uint64(s.Set)<<16 | uint64(uint8(s.Self+1))<<8 | flag
	}
	refOrder := make([]uint32, 0, len(all))
	for i, s := range all {
		if s.Sp != SpLayout && !selfDelta[s.Off] {
			refOrder = append(refOrder, uint32(i))
		}
	}
	sort.Slice(refOrder, func(i, j int) bool { return refKey(&all[refOrder[i]]) < refKey(&all[refOrder[j]]) })
	lastKey := ^uint64(0)
	// references
	for _, i := range refOrder {
		s := &all[i]
		key := refKey(s)
		if key != lastKey {
			clearIndexes()
			lastKey = key
		}
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
		if ix, ok := c.rowIdx[[2]int{SetCoded0 + CHasCustomAttribute, -1}]; ok {
			st.Dups[2] = ix.Duplicates()
		}
	}
	if cfg.Observe != nil {
		cfg.Observe("references")
	}
	if data.err != nil {
		return nil, data.err
	}
	if c.err != nil {
		return nil, c.err
	}
	clearIndexes()
	st.Dups[0] = c.StrIndex().Duplicates()
	clearIndexes()
	st.Dups[1] = c.BlobIndex(true).Duplicates()
	clearIndexes()
	st.Dups[3] = c.USIndex().Duplicates()
	clearIndexes()

	if emitErr != nil {
		return nil, emitErr
	}
	if err := sorted.CopySorted(spool); err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("sorted-spool")
	}
	patches := make([]idxref.Patch, 0, len(all))
	for _, s := range all {
		if s.Len == 0 {
			continue
		}
		if s.Len > 5 {
			return nil, unsup("mask width")
		}
		var b [5]byte
		if s.Enc == EncCompressed {
			maskCompressed(b[:s.Len], int(s.Len))
		}
		patches = append(patches, idxref.Patch{Off: s.Off, Len: s.Len, Bytes: b})
	}
	excised := make([]idxref.Span, len(f.Excised))
	for i, r := range f.Excised {
		excised[i] = idxref.Span{Off: r.Off, Len: r.Len}
	}
	payload, err := idxref.NewWindowView(source, data.size, excised, patches)
	if err != nil {
		return nil, err
	}
	st.Payload = int(payload.Size())
	secs := buildSecsAt(f, c.str, c.HV, geoms, pm, int(payload.Size()))
	// item hashes: payload bytes plus the reference bytes of the slots inside
	if err := spool.Rewind(); err != nil {
		return nil, err
	}
	ref, readErr := spool.Next()
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for k, a := range sc.Items {
			end := sc.End
			if k+1 < len(sc.Items) {
				end = sc.Items[k+1]
			}
			h, err := idxref.FNVRange(payload, int64(a), int64(end), scratch)
			if err != nil {
				return nil, err
			}
			for readErr == nil && ref.Pos < int64(a) {
				ref, readErr = spool.Next()
			}
			for readErr == nil && ref.Pos < int64(end) {
				h = idxref.FNV64(h^uint64(ref.Stream), ref.Bytes)
				ref, readErr = spool.Next()
			}
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			sc.Hash[k] = h
		}
	}
	if cfg.Observe != nil {
		cfg.Observe("hashes")
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts, err = cfg.Cut(payload, secs)
		if err != nil {
			return nil, err
		}
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != int(payload.Size()) {
		cuts = append(cuts, int(payload.Size()))
	}
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	if err := spool.Rewind(); err != nil {
		return nil, err
	}
	ref, readErr = spool.Next()
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	start := 0
	for _, end := range cuts {
		if end < start || end > int(payload.Size()) {
			return nil, fmt.Errorf("clrz3: bad cut %d", end)
		}
		if end-start > 16<<20 {
			return nil, bad("component payload limit")
		}
		var streams [NumStreams][]byte
		refBytes := 0
		for readErr == nil && ref.Pos < int64(end) {
			if len(ref.Bytes) > (16<<20)-(end-start)-refBytes-128 {
				return nil, bad("component reference limit")
			}
			refBytes += len(ref.Bytes)
			streams[ref.Stream] = append(streams[ref.Stream], ref.Bytes...)
			ref, readErr = spool.Next()
		}
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		p := make([]byte, end-start)
		if _, err := payload.ReadAt(p, int64(start)); err != nil {
			return nil, err
		}
		component := idxref.PackComp(p, streams[:])
		if len(component) > 16<<20 {
			return nil, bad("component limit")
		}
		if err := cfg.Emit(component); err != nil {
			return nil, err
		}
		rep.Stats.Comps++
		start = end
	}

	if cfg.Observe != nil {
		cfg.Observe("emitted")
	}
	// meta
	m := append([]byte(repMagic), byte(k0), byte(cfg.Auto))
	m = binary.AppendUvarint(m, uint64(data.size))
	m = binary.AppendUvarint(m, uint64(len(f.Excised)))
	for _, r := range f.Excised {
		m = binary.AppendUvarint(m, uint64(r.Off))
		m = binary.AppendUvarint(m, uint64(r.Len))
	}
	for _, r := range f.Excised {
		m = append(m, data.span(r.Off, r.Len)...)
	}
	rep.Meta = m
	if data.err != nil {
		return nil, data.err
	}
	// Caller must perform an independent exact full proof before publication.
	return rep, nil
}
