package clrz3

// Heap views and the signature marking shared by the representation and the byte attribution.

// HeapView is the entry structure of #Blob and #US.
type HeapView struct {
	Blob, US     []byte
	Blobs, USes  []BlobEntry
	BlobAt, USAt map[uint32]int
}

// NewHeapView walks the length-prefixed heaps of f in d.
func NewHeapView(f *File, d []byte) (*HeapView, error) {
	h := &HeapView{}
	if f.HasBlob {
		h.Blob = d[f.Blob.Off : f.Blob.Off+f.Blob.Size]
		var rag bool
		if h.Blobs, rag = HeapEntries(h.Blob); rag {
			return nil, unsup("#Blob does not end on an entry boundary")
		}
		h.BlobAt = make(map[uint32]int, len(h.Blobs))
		for i, e := range h.Blobs {
			h.BlobAt[e.Off] = i
		}
	}
	if f.HasUS {
		h.US = d[f.US.Off : f.US.Off+f.US.Size]
		var rag bool
		if h.USes, rag = HeapEntries(h.US); rag {
			return nil, unsup("#US does not end on an entry boundary")
		}
		h.USAt = make(map[uint32]int, len(h.USes))
		for i, e := range h.USes {
			h.USAt[e.Off] = i
		}
	}
	return h, nil
}

// DeriveMarks returns, per blob entry, the signature kind it is normalized as (BlobOpaque if it is
// not a signature, does not parse exactly or names a reference that does not exist).
func DeriveMarks(f *File, d []byte, h *HeapView) []byte {
	marks := make([]byte, len(h.Blobs))
	conflict := make([]bool, len(h.Blobs))
	for t := 0; t < NumTables; t++ {
		for ci, cd := range schema[t] {
			if cd.K != KBlob || cd.Sig == SigNone {
				continue
			}
			for r := uint32(1); r <= f.Rows(t); r++ {
				off := f.Col(d, t, r, ci)
				i, ok := h.BlobAt[off]
				if !ok {
					continue
				}
				e := h.Blobs[i]
				k := KindOf(cd.Sig, h.Blob[e.DataOff:e.DataOff+e.Len])
				if k == BlobOpaque {
					continue
				}
				if marks[i] != BlobOpaque && marks[i] != k {
					conflict[i] = true
				}
				marks[i] = k
			}
		}
	}
	for i, e := range h.Blobs {
		if marks[i] == BlobOpaque {
			continue
		}
		sl, ok := ParseSig(marks[i], h.Blob[e.DataOff:e.DataOff+e.Len], true)
		if ok {
			for _, s := range sl {
				t, r, ok2 := RowOf(SetEmbType, s.Val)
				if !ok2 || t < 0 || r > f.Rows(t) {
					ok = false
					break
				}
			}
		}
		if !ok || conflict[i] {
			marks[i] = BlobOpaque
		}
	}
	return marks
}

// ValidateBodies marks a body opaque when a token operand names nothing that exists.
func ValidateBodies(f *File, d []byte, h *HeapView, bodies []BodyInfo) {
	okTok := func(set uint8, v uint32, us bool) bool {
		if us {
			_, ok := h.USAt[v&0xffffff]
			return ok
		}
		t, r, ok := RowOf(set, v)
		return ok && (t < 0 || r <= f.Rows(t))
	}
	for i := range bodies {
		b := &bodies[i]
		if b.Opaque {
			continue
		}
		good := true
		if b.Body.LocalOff != 0 {
			good = okTok(SetTokSig, le32(d[b.Body.LocalOff:]), false)
		}
		for _, t := range b.Body.Toks {
			switch t.Class {
			case TkString:
				good = good && okTok(0, t.Val, true)
			case TkMethod:
				good = good && okTok(SetTokMethod, t.Val, false)
			case TkField:
				good = good && okTok(SetTokField, t.Val, false)
			case TkType:
				good = good && okTok(SetTokType, t.Val, false)
			case TkSig:
				good = good && okTok(SetTokSig, t.Val, false)
			default:
				good = good && okTok(SetTokAny, t.Val, false)
			}
		}
		for _, cl := range b.Body.Clauses {
			if cl.TokOff != 0 {
				good = good && okTok(SetTokType, cl.TokVal, false)
			}
		}
		b.Opaque = !good
	}
}

// BlobBytes returns the content of the blob entry at heap offset off.
func (h *HeapView) BlobBytes(off uint32) ([]byte, bool) {
	i, ok := h.BlobAt[off]
	if !ok {
		return nil, false
	}
	e := h.Blobs[i]
	return h.Blob[e.DataOff : e.DataOff+e.Len], true
}
