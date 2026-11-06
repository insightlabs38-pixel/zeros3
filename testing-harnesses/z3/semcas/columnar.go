package semcas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/parquetz3"
)

// Columnar lane: Arrow IPC (uncompressed) and Parquet (UNCOMPRESSED, PLAIN, required
// columns) share one canonical comp per column: dense little-endian values for fixed-width
// types, or the PLAIN length-prefixed stream for utf8/binary. Everything the lane cannot
// prove (nulls, dictionaries, compression, narrow ints, nested types) stays literal and is
// accounted as such.

func colDesc(dt, n int) Desc {
	return Desc{Kind: KindColumn, Version: 1, DType: uint16(dt), Shape: []uint64{uint64(n)}, Ordered: true}
}

// colSpan is one exact region of the original file backed by a canonical column component.
type colSpan struct {
	off, n int64
	step   Step
}

// ColumnStats reports what a columnar file contributed.
type ColumnStats struct {
	Columns, SharedColumns int
	CanonBytes             int64 // bytes of canonical column payload referenced
	LiteralBytes           int64 // bytes kept as literals/chunks (framing, nulls, unsupported)
	Unsupported            map[string]int
}

func (f *FilePlanner) buildFromSpans(data []byte, spans []colSpan, st *ColumnStats) (*Recipe, error) {
	sort.Slice(spans, func(i, j int) bool { return spans[i].off < spans[j].off })
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	pos := int64(0)
	lit := func(p []byte) error {
		if len(p) == 0 {
			return nil
		}
		st.LiteralBytes += int64(len(p))
		if len(p) < LitChunkMin {
			b.Lit(p)
			return nil
		}
		id, err := f.content(KindBytes, p)
		if err != nil {
			return err
		}
		b.Ref(id, "lit")
		return nil
	}
	for _, sp := range spans {
		if sp.off < pos || sp.off+sp.n > int64(len(data)) {
			return nil, errors.New("semcas: overlapping column spans")
		}
		if err := lit(data[pos:sp.off]); err != nil {
			return nil, err
		}
		b.R.Steps = append(b.R.Steps, sp.step)
		st.CanonBytes += sp.n
		pos = sp.off + sp.n
	}
	if err := lit(data[pos:]); err != nil {
		return nil, err
	}
	rc := b.R
	return &rc, nil
}

// IngestArrow represents an uncompressed Arrow IPC file.
func (f *FilePlanner) IngestArrow(name string, data []byte) (*Recipe, *ColumnStats, error) {
	st := &ColumnStats{Unsupported: map[string]int{}}
	af, err := ParseArrowFile(data)
	if err != nil {
		return nil, nil, err
	}
	var spans []colSpan
	nb := 0
	for _, c := range af.Cols {
		if !c.Supported {
			st.Unsupported[c.Why]++
		}
		if c.DType == DPlainBytes {
			nb += 3
		} else {
			nb += 2
		}
	}
	allKnown := true
	for _, c := range af.Cols {
		if c.Why != "" && c.DType == 0 && c.Width == 0 && !c.Supported {
			allKnown = false // unknown buffer layout: cannot map buffers to fields
		}
	}
	for _, bt := range af.Batches {
		if len(bt.Bufs) != nb {
			allKnown = false
		}
	}
	if allKnown {
		base := 0
		for ci, c := range af.Cols {
			k := 2
			if c.DType == DPlainBytes {
				k = 3
			}
			st.Columns++
			if c.Supported {
				got, err := f.arrowColumn(data, af, ci, base, &spans)
				if err != nil {
					return nil, nil, err
				}
				if got {
					st.SharedColumns++
				}
			}
			base += k
		}
	}
	rc, err := f.buildFromSpans(data, spans, st)
	return rc, st, err
}

func (f *FilePlanner) arrowColumn(data []byte, af *ArrowFile, ci, base int, spans *[]colSpan) (bool, error) {
	c := af.Cols[ci]
	// require zero nulls in every batch; the validity buffer must be absent/empty
	for _, bt := range af.Batches {
		nn := bt.Nulls
		if ci < len(nn) && nn[ci] != 0 {
			return false, nil
		}
	}
	if c.DType != DPlainBytes {
		var dense []byte
		var runs []arrowBuf
		for _, bt := range af.Batches {
			v := bt.Bufs[base+1]
			need := bt.Rows * int64(c.Width)
			if v.len < need { // longer buffers carry alignment padding that stays literal
				return false, nil
			}
			v.len = need
			dense = append(dense, data[v.off:v.off+v.len]...)
			runs = append(runs, v)
		}
		r, err := f.S.PutRaw(colDesc(c.DType, len(dense)/c.Width), dense)
		if err != nil {
			return false, err
		}
		off := int64(0)
		for _, v := range runs {
			*spans = append(*spans, colSpan{v.off, v.len, Step{Kind: StepSpan, CID: r.CID, Off: uint64(off), Len: uint64(v.len), Label: c.Name}})
			off += v.len
		}
		return true, nil
	}
	// utf8/binary: rebuild the length-prefixed stream, then derive each batch's buffers
	var stream []byte
	type bspan struct{ so, sl, rows, dl int64 }
	var bs []bspan
	for _, bt := range af.Batches {
		ob, db := bt.Bufs[base+1], bt.Bufs[base+2]
		if ob.len < (bt.Rows+1)*4 {
			return false, nil
		}
		ob.len = (bt.Rows + 1) * 4
		offs := data[ob.off : ob.off+ob.len]
		if binary.LittleEndian.Uint32(offs) != 0 {
			return false, nil
		}
		start := int64(len(stream))
		prev := uint32(0)
		for i := int64(0); i < bt.Rows; i++ {
			nx := binary.LittleEndian.Uint32(offs[4*(i+1):])
			if nx < prev || int64(nx) > db.len {
				return false, nil
			}
			stream = binary.LittleEndian.AppendUint32(stream, nx-prev)
			stream = append(stream, data[db.off+int64(prev):db.off+int64(nx)]...)
			prev = nx
		}
		if int64(prev) > db.len {
			return false, nil
		}
		bs = append(bs, bspan{start, int64(len(stream)) - start, bt.Rows, int64(prev)})
	}
	var rows int64
	for _, x := range bs {
		rows += x.rows
	}
	sr, err := f.S.PutRaw(colDesc(DPlainBytes, int(rows)), stream)
	if err != nil {
		return false, err
	}
	for i, bt := range af.Batches {
		ob, db := bt.Bufs[base+1], bt.Bufs[base+2]
		ob.len = (bt.Rows + 1) * 4
		db.len = bs[i].dl
		slice := Op{Kind: OpSlice, Off: uint64(bs[i].so), Len: uint64(bs[i].sl)}
		o, err := f.S.PutDerived(colDesc(DI32, int(bt.Rows)+1), []ID{sr.CID}, []Op{slice, {Kind: OpPlainOffsets}}, nil, data[ob.off:ob.off+ob.len])
		if err != nil {
			return false, err
		}
		d, err := f.S.PutDerived(colDesc(DU8, int(db.len)), []ID{sr.CID}, []Op{slice, {Kind: OpPlainData}}, nil, data[db.off:db.off+db.len])
		if err != nil {
			return false, err
		}
		*spans = append(*spans,
			colSpan{ob.off, ob.len, Step{Kind: StepRef, CID: o.CID, Label: c.Name + ".offsets"}},
			colSpan{db.off, db.len, Step{Kind: StepRef, CID: d.CID, Label: c.Name + ".data"}})
	}
	return true, nil
}

// IngestParquet represents a Parquet file; columns that are UNCOMPRESSED, PLAIN, required
// and of a supported physical type share canonical column components.
func (f *FilePlanner) IngestParquet(name string, data []byte) (*Recipe, *ColumnStats, error) {
	st := &ColumnStats{Unsupported: map[string]int{}}
	pf, err := parquetz3.Open(bytes.NewReader(data), int64(len(data)), parquetz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	type pageRef struct{ hdrOff, hdrLen, payOff, payLen int64 }
	type chunkInfo struct {
		pages []pageRef
		n     int64
	}
	colChunks := map[int][]*chunkInfo{}
	colDT := map[int]int{}
	bad := map[int]string{}
	for ci := range pf.Columns {
		col := pf.Columns[ci]
		st.Columns++
		dt, why := parquetDType(col)
		if why == "" && (col.MaxDef > 1 || col.MaxRep != 0) {
			why = "nested"
		}
		if why != "" {
			bad[ci] = why
			continue
		}
		colDT[ci] = dt
	}
	for _, rg := range pf.RowGroups {
		for i := range rg.Chunks {
			c := &rg.Chunks[i]
			if bad[c.Col] != "" {
				continue
			}
			if c.Codec != 0 || c.DictOff >= 0 {
				bad[c.Col] = "compressed or dictionary chunk"
				continue
			}
			ck := &chunkInfo{}
			off := c.Start
			ok := true
			for off < c.End {
				end := off + 256
				if end > c.End {
					end = c.End
				}
				pg, err := parquetz3.ParsePageHeader(data[off:end], parquetz3.Limits{})
				if err != nil || (pg.Type != 0 && pg.Type != 3) || pg.Enc != 0 || pg.Comp != pg.Uncomp || pg.RepLen != 0 {
					ok = false
					break
				}
				hl := int64(pg.HdrLen)
				if off+hl+int64(pg.Comp) > c.End {
					ok = false
					break
				}
				skip := int64(0) // definition-level bytes stay literal; only the values are shared
				if pf.Columns[c.Col].MaxDef > 0 {
					if pg.Type == 3 {
						skip = int64(pg.DefLen)
					} else if pg.Comp >= 4 {
						skip = 4 + int64(binary.LittleEndian.Uint32(data[off+hl:]))
					}
					if skip > int64(pg.Comp) {
						ok = false
						break
					}
				}
				ck.pages = append(ck.pages, pageRef{off, hl, off + hl + skip, int64(pg.Comp) - skip})
				ck.n += int64(pg.NumValues)
				off += hl + int64(pg.Comp)
			}
			if !ok {
				bad[c.Col] = "non-PLAIN or compressed page"
				continue
			}
			colChunks[c.Col] = append(colChunks[c.Col], ck)
		}
	}
	for ci, why := range bad {
		_ = ci
		st.Unsupported[why]++
	}
	var spans []colSpan
	for ci, chunks := range colChunks {
		if bad[ci] != "" {
			continue
		}
		var dense []byte
		var total int64
		for _, ck := range chunks {
			for _, p := range ck.pages {
				dense = append(dense, data[p.payOff:p.payOff+p.payLen]...)
			}
			total += ck.n
		}
		_ = total
		r, err := f.S.PutRaw(colDesc(colDT[ci], countValues(colDT[ci], dense)), dense)
		if err != nil {
			return nil, nil, err
		}
		off := uint64(0)
		for _, ck := range chunks {
			for _, p := range ck.pages {
				spans = append(spans, colSpan{p.payOff, p.payLen, Step{Kind: StepSpan, CID: r.CID, Off: off, Len: uint64(p.payLen), Label: pf.Columns[ci].Name}})
				off += uint64(p.payLen)
			}
		}
		st.SharedColumns++
	}
	rc, err := f.buildFromSpans(data, spans, st)
	return rc, st, err
}

func parquetDType(c parquetz3.Column) (int, string) {
	switch c.Phys {
	case 1: // INT32
		switch c.Converted {
		case -1, 17:
			return DI32, ""
		case 13:
			return DU32, ""
		}
		return 0, fmt.Sprintf("int32 converted type %d", c.Converted)
	case 2: // INT64
		switch c.Converted {
		case -1, 18:
			return DI64, ""
		case 14:
			return DU64, ""
		}
		return 0, fmt.Sprintf("int64 converted type %d", c.Converted)
	case 4:
		return DF32, ""
	case 5:
		return DF64, ""
	case 6:
		return DPlainBytes, ""
	}
	return 0, fmt.Sprintf("physical type %d", c.Phys)
}

// parquetReplay runs the existing exact Parquet codec: the recipe names components by
// SHA-256 and every one must be among the parents' payloads.
func parquetReplay(recipe []byte, in [][]byte) ([]byte, error) {
	by := make(map[[32]byte][]byte, len(in))
	for _, b := range in {
		by[sha256.Sum256(b)] = b
	}
	var out bytes.Buffer
	err := parquetz3.Decode(&out, recipe, func(id parquetz3.ID) ([]byte, error) {
		if b, ok := by[id]; ok {
			return b, nil
		}
		return nil, ErrMissing
	}, parquetz3.Limits{})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// IngestParquetExact represents any Parquet file the existing exact codec accepts
// (Snappy/Zstd/Gzip pages, dictionaries, levels): every column's whole logical value stream
// is stored as the same canonical column component Arrow and PLAIN Parquet use; the codec's
// recipe and its small framing components make up the file-specific residual.
func (f *FilePlanner) IngestParquetExact(name string, data []byte) (*Recipe, *ColumnStats, error) {
	st := &ColumnStats{Unsupported: map[string]int{}}
	pf, err := parquetz3.Open(bytes.NewReader(data), int64(len(data)), parquetz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	colType := map[string]int{}
	for _, c := range pf.Columns {
		dt, why := parquetDType(c)
		if why == "" && c.MaxDef <= 1 && c.MaxRep == 0 {
			colType[c.Name] = dt
		}
	}
	type comp struct {
		class string
		id    [32]byte
		data  []byte
	}
	var comps []comp
	recipe, _, _, err := parquetz3.Encode(bytes.NewReader(data), int64(len(data)),
		parquetz3.Options{Target: 1 << 30, Cut: parquetz3.CutFixed, FixedCount: 1 << 30, ColumnClasses: true},
		func(class string, id parquetz3.ID, b []byte) error {
			comps = append(comps, comp{class, id, append([]byte(nil), b...)})
			return nil
		})
	if err != nil {
		return nil, nil, err
	}
	var parents []ID
	seen := map[[32]byte]bool{}
	for _, c := range comps {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		var d Desc
		if strings.HasPrefix(c.class, "val:") {
			if dt, ok := colType[c.class[4:]]; ok {
				d = colDesc(dt, countValues(dt, c.data))
				st.SharedColumns++
			} else {
				d = bytesDesc(KindBytes, len(c.data))
				st.Unsupported["nullable or nested value stream"]++
			}
		} else {
			d = bytesDesc(KindBytes, len(c.data))
			st.LiteralBytes += int64(len(c.data))
		}
		if d.PayloadLen() >= 0 && int(d.PayloadLen()) != len(c.data) {
			d = bytesDesc(KindBytes, len(c.data)) // count/width disagree: stay opaque
		}
		r, err := f.S.PutRaw(d, c.data)
		if err != nil {
			return nil, nil, err
		}
		parents = append(parents, r.CID)
		if strings.HasPrefix(c.class, "val:") {
			st.CanonBytes += int64(len(c.data))
		}
	}
	st.Columns = len(pf.Columns)
	if len(parents) > MaxFanout {
		return nil, nil, fmt.Errorf("semcas: %s has %d components, above the fanout cap", name, len(parents))
	}
	r, err := f.S.PutDerived(bytesDesc(KindBytes, len(data)), parents, []Op{{Kind: OpParquet, Aux: recipe}}, nil, data)
	if err != nil {
		return nil, nil, err
	}
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	b.Ref(r.CID, "parquet")
	st.LiteralBytes += int64(len(recipe))
	rc := b.R
	return &rc, st, nil
}

// countValues is the number of values in a canonical dense column payload.
func countValues(dt int, b []byte) int {
	if w := ElemBytes(dt); w > 0 {
		return len(b) / w
	}
	n, pos := 0, 0
	for pos+4 <= len(b) {
		pos += 4 + int(binary.LittleEndian.Uint32(b[pos:]))
		n++
	}
	return n
}
