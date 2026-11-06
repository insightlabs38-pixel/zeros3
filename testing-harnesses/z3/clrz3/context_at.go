package clrz3

import (
	"bytes"
	"zeros3-z3/idxref"
)

// newCtxAt owns only bounded metadata heaps and compact identity state.
func newCtxAt(f *File, s *clrSource, k0 int) (*Ctx, error) {
	rows := int64(0)
	for _, t := range f.Tables {
		rows += int64(t.Rows)
		if rows > 1<<20 {
			return nil, limit("aggregate identity rows")
		}
	}
	heapBytes := f.Str.Size + f.Blob.Size + f.US.Size + f.GUID.Size
	if heapBytes < 0 || heapBytes > 32<<20 {
		return nil, limit("aggregate metadata heaps")
	}
	c := &Ctx{F: f, source: s, K0: k0, rowIdx: map[[2]int]*idxref.ClassIndex{}, workLimit: f.Size*16 + (64 << 20)}
	var err error
	if f.HasStr {
		c.str, err = s.owned(f.Str.Off, f.Str.Size)
		if err != nil {
			return nil, err
		}
	}
	if err := c.prepareStringBounds(); err != nil {
		return nil, err
	}
	hv := &HeapView{}
	if f.HasBlob {
		hv.Blob, err = s.owned(f.Blob.Off, f.Blob.Size)
		if err != nil {
			return nil, err
		}
		hv.Blobs, err = limitedHeapEntries(hv.Blob)
		if err != nil {
			return nil, err
		}
	}
	if f.HasUS {
		hv.US, err = s.owned(f.US.Off, f.US.Size)
		if err != nil {
			return nil, err
		}
		hv.USes, err = limitedHeapEntries(hv.US)
		if err != nil {
			return nil, err
		}
	}
	if heapBytes+33*rows+94*int64(len(hv.Blobs))+60*int64(len(hv.USes)) > 64<<20 {
		return nil, limit("aggregate context metadata")
	}
	hv.BlobAt = make(map[uint32]int, len(hv.Blobs))
	for i, e := range hv.Blobs {
		hv.BlobAt[e.Off] = i
	}
	hv.USAt = make(map[uint32]int, len(hv.USes))
	for i, e := range hv.USes {
		hv.USAt[e.Off] = i
	}
	c.HV, c.blob, c.us, c.Blobs, c.USes, c.blobAt, c.usAt = hv, hv.Blob, hv.US, hv.Blobs, hv.USes, hv.BlobAt, hv.USAt
	if f.HasGUID {
		c.guid, err = s.owned(f.GUID.Off, f.GUID.Size)
		if err != nil {
			return nil, err
		}
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

// Mechanically adapted canonical heap walk with a checked entry budget.
func limitedHeapEntries(h []byte) (es []BlobEntry, err error) {
	p := 0
	for p < len(h) {
		c := h[p]
		var n, w int
		switch {
		case c&0x80 == 0:
			n, w = int(c), 1
		case c&0xC0 == 0x80 && p+2 <= len(h):
			n, w = int(c&0x3f)<<8|int(h[p+1]), 2
		case c&0xE0 == 0xC0 && p+4 <= len(h):
			n, w = int(c&0x1f)<<24|int(h[p+1])<<16|int(h[p+2])<<8|int(h[p+3]), 4
		default:
			return nil, unsup("heap does not end on an entry boundary")
		}
		if p+w+n > len(h) {
			return nil, unsup("heap does not end on an entry boundary")
		}
		if len(es) >= 1<<18 {
			return nil, limit("heap entry count")
		}
		es = append(es, BlobEntry{uint32(p), uint32(p + w), uint32(n)})
		p += w + n
	}
	return es, nil
}

// Decode retains aliases into its already bounded reconstruction, so reference
// restoration remains visible to identity computation without another image.
func newCtxDecode(f *File, data []byte, k0 int) (*Ctx, error) {
	source := &clrSource{r: bytes.NewReader(data), size: int64(len(data)), lo: -1, borrow: data}
	c, e := newCtxAt(f, source, k0)
	if e != nil {
		return nil, e
	}
	c.D = data
	c.source = nil
	return c, nil
}
func tableSlotCount(f *File) (int, error) {
	n := 0
	for t, tb := range f.Tables {
		for _, cd := range schema[t] {
			if cd.K != KConst {
				if int64(tb.Rows) > int64((2<<20)-n) {
					return 0, limit("table slot metadata")
				}
				n += int(tb.Rows)
			}
		}
	}
	return n, nil
}
