package graphcore

import (
	"encoding/binary"
	"errors"
)

// Sequence-point blob line normalization (Portable PDB spec, "Sequence Points Blob").
// The first non-hidden record carries absolute StartLine/StartColumn; every other field is
// already a delta. Inserting source lines above a method rewrites only that absolute line,
// so it is replaced by a delta against the previous method's first line in the same document.

func readCInt(b []byte) (v uint64, n int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	switch c := b[0]; {
	case c&0x80 == 0:
		return uint64(c), 1, true
	case c&0xC0 == 0x80:
		if len(b) < 2 {
			return 0, 0, false
		}
		return uint64(c&0x3F)<<8 | uint64(b[1]), 2, true
	case c&0xE0 == 0xC0:
		if len(b) < 4 {
			return 0, 0, false
		}
		return uint64(c&0x1F)<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3]), 4, true
	}
	return 0, 0, false
}

func appendCInt(b []byte, v uint64) ([]byte, bool) {
	switch {
	case v < 0x80:
		return append(b, byte(v)), true
	case v < 0x4000:
		return append(b, byte(v>>8)|0x80, byte(v)), true
	case v < 0x20000000:
		return append(b, byte(v>>24)|0xC0, byte(v>>16), byte(v>>8), byte(v)), true
	}
	return b, false
}

// seqFirstLine locates the first non-hidden record's StartLine inside a blob body (without
// the blob's own length prefix): byte offset/size of the compressed StartLine and its value.
func seqFirstLine(body []byte, hasDocField bool) (off, n int, line uint64, ok bool) {
	p := 0
	skip := func() bool {
		_, k, o := readCInt(body[p:])
		p += k
		return o
	}
	if !skip() { // LocalSignature
		return
	}
	if hasDocField && !skip() { // InitialDocument
		return
	}
	first := true
	for p < len(body) {
		il, k, o := readCInt(body[p:])
		if !o {
			return
		}
		p += k
		if il == 0 && !first { // document-record
			if !skip() {
				return
			}
			continue
		}
		first = false
		dl, k, o := readCInt(body[p:])
		if !o {
			return
		}
		p += k
		dc, k2, o2 := readCInt(body[p:])
		if !o2 {
			return
		}
		p += k2
		if dl == 0 && dc == 0 { // hidden
			continue
		}
		l, k3, o3 := readCInt(body[p:])
		if !o3 {
			return
		}
		return p, k3, l, true
	}
	return
}

var errSeq = errors.New("graphcore: sequence-point blob not normalizable")

// LineNormalizer rewrites referenced sequence-point blobs in a #Blob heap. Entries that do
// not parse as expected are left untouched (and are not rewritten at decode either: the
// per-entry flag stream records which entries were rewritten).
type LineNormalizer struct{}

var _ = binary.LittleEndian

// PDBLineOracle rewrites the #Blob heap with first-line deltas (NOT reversible; measurement only)
// and returns the rewritten heap plus counts of rewritten / unparsed sequence-point blobs.
func PDBLineOracle(d []byte) (heap []byte, rewritten, unparsed int, err error) {
	tables, streams, _, err := parsePDB(d)
	if err != nil {
		return nil, 0, 0, err
	}
	bs, ok := streams["#Blob"]
	if !ok {
		return nil, 0, 0, errPDB
	}
	blob := d[bs[0]:bs[1]]
	var mdi *pdbTable
	for i := range tables {
		if tables[i].Name == "MethodDebugInformation" {
			mdi = &tables[i]
		}
	}
	if mdi == nil {
		return append([]byte(nil), blob...), 0, 0, nil
	}
	type ref struct {
		off, doc uint64
	}
	refs := map[uint64]uint64{} // blob offset -> document row
	w0, w1 := mdi.Cols[0].W, mdi.Cols[1].W
	for r := 0; r < mdi.Rows; r++ {
		o := mdi.Off + r*mdi.W
		var doc, sp uint64
		for k := w0 - 1; k >= 0; k-- {
			doc = doc<<8 | uint64(d[o+k])
		}
		for k := w1 - 1; k >= 0; k-- {
			sp = sp<<8 | uint64(d[o+w0+k])
		}
		if sp != 0 {
			refs[sp] = doc
		}
	}
	var out []byte
	prev := map[uint64]uint64{}
	for p := uint64(0); p < uint64(len(blob)); {
		n, k, ok := readCInt(blob[p:])
		if !ok || p+uint64(k)+n > uint64(len(blob)) {
			out = append(out, blob[p:]...)
			break
		}
		body := blob[p+uint64(k) : p+uint64(k)+n]
		doc, isSeq := refs[p]
		if isSeq {
			off, sz, line, ok := seqFirstLine(body, doc == 0)
			if ok && sz > 0 {
				key := doc
				delta := Zig(int64(line) - int64(prev[key]))
				prev[key] = line
				nb := append([]byte(nil), body[:off]...)
				nb, ok2 := appendCInt(nb, delta)
				if ok2 {
					nb = append(nb, body[off+sz:]...)
					out, _ = appendCInt(out, uint64(len(nb)))
					out = append(out, nb...)
					rewritten++
					p += uint64(k) + n
					continue
				}
			}
			unparsed++
		}
		out = append(out, blob[p:p+uint64(k)+n]...)
		p += uint64(k) + n
	}
	return out, rewritten, unparsed, nil
}
