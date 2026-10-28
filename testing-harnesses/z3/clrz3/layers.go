package clrz3

import "zeros3-z3/idxref"

// Normalization layers for the layer-by-layer oracle (what each class of reference slot buys).
const (
	LayerIdx    = 1 << iota // table / coded / heap indexes and references inside signatures
	LayerTok                // CIL metadata tokens and ldstr offsets
	LayerLayout             // RVAs, child-list starts, nested-class pairs
	LayerAll    = LayerIdx | LayerTok | LayerLayout
)

// LayerPayload returns the payload (header regions removed) with only the selected classes of
// slot masked, and the section description of that payload. It is an ORACLE: nothing is stored
// for the masked values, so it prices what the layer could save, not a representation.
func LayerPayload(f *File, d []byte, layers int) ([]byte, []idxref.Sec, error) {
	hv, err := NewHeapView(f, d)
	if err != nil {
		return nil, nil, err
	}
	marks := DeriveMarks(f, d, hv)
	bodies := FindBodies(f, d, true)
	ValidateBodies(f, d, hv, bodies)
	slots := append(append(TableSlots(f, d), BlobSlots(f, d, hv.Blobs, marks)...), ILSlots(d, bodies)...)
	m := append([]byte(nil), d...)
	for i := range slots {
		s := &slots[i]
		layer := LayerIdx
		switch {
		case s.Sp == SpLayout:
			layer = LayerLayout
		case s.Table == TabIL || s.Sp == SpUS:
			layer = LayerTok
		}
		if layers&layer == 0 {
			continue
		}
		switch s.Enc {
		case EncFixed, EncToken:
			clear(m[s.Off : s.Off+int64(s.Len)])
		case EncCompressed:
			maskCompressed(m[s.Off:s.Off+int64(s.Len)], int(s.Len))
		}
	}
	var payload []byte
	pos := int64(0)
	for _, r := range f.Excised {
		payload = append(payload, m[pos:r.Off]...)
		pos = r.End()
	}
	payload = append(payload, m[pos:]...)
	return payload, buildSecs(f, d, hv, bodies, posMap{f.Excised}, len(payload)), nil
}

// RawSecs describes the unmasked file for the structure-only baselines (same sections, file
// coordinates, nothing excised).
func RawSecs(f *File, d []byte) ([]idxref.Sec, error) {
	hv, err := NewHeapView(f, d)
	if err != nil {
		return nil, err
	}
	bodies := FindBodies(f, d, false)
	return buildSecs(f, d, hv, bodies, posMap{}, len(d)), nil
}
