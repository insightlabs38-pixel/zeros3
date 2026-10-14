package eraez3

import "bytes"

// Block is one block's components as plaintext (RLP header/body/slim
// receipts) plus the 32-byte total difficulty (nil when absent).
type Block struct {
	Header, Body, Receipts, TD []byte
}

// WriteOpts shapes a synthetic re-export of the same blocks.
type WriteOpts struct {
	Interleaved bool // Era1 style: header, body, receipts, TD per block (default: grouped by type)
	NoTD        bool
	Frame       func([]byte) []byte // default FrameEncodeRef
	IndexStart  uint64
	Accumulator []byte
	NoIndex     bool
	// ShiftIndex adds a bogus first offset (a different implementation's index
	// convention): the index then no longer derives from the positions.
	ShiftIndex int64
}

// Write assembles an EraE file from blocks.
func Write(blocks []Block, o WriteOpts) []byte {
	frame := o.Frame
	if frame == nil {
		frame = FrameEncodeRef
	}
	var out []byte
	var pos []map[uint16]int64
	out = appendEntry(out, TypeVersion, nil)
	add := func(i int, t uint16, payload []byte) {
		for len(pos) <= i {
			pos = append(pos, map[uint16]int64{})
		}
		pos[i][t] = int64(len(out))
		out = appendEntry(out, t, payload)
	}
	comp := []uint16{TypeHeader, TypeBody, TypeSlimReceipts}
	if !o.NoTD {
		comp = append(comp, TypeTD)
	}
	payload := func(b Block, t uint16) []byte {
		switch t {
		case TypeHeader:
			return frame(b.Header)
		case TypeBody:
			return frame(b.Body)
		case TypeSlimReceipts:
			return frame(b.Receipts)
		}
		return b.TD
	}
	if o.Interleaved {
		for i, b := range blocks {
			for _, t := range comp {
				add(i, t, payload(b, t))
			}
		}
	} else {
		for _, t := range comp {
			for i, b := range blocks {
				add(i, t, payload(b, t))
			}
		}
	}
	if o.Accumulator != nil {
		out = appendEntry(out, TypeAccumulator, o.Accumulator)
	}
	if !o.NoIndex {
		ip := int64(len(out))
		var idx []byte
		idx = appendU64(idx, o.IndexStart)
		for i := range blocks {
			for _, t := range comp {
				idx = appendU64(idx, uint64(pos[i][t]-ip+o.ShiftIndex))
			}
		}
		idx = appendU64(idx, uint64(len(comp)))
		idx = appendU64(idx, uint64(len(blocks)))
		out = appendEntry(out, TypeComponentIndex, idx)
	}
	return out
}

func appendU64(b []byte, v uint64) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24), byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

// ExtractBlocks decodes a file's framed entries into plaintext blocks
// (grouped or interleaved) and returns the accumulator and index start.
func ExtractBlocks(file []byte) (blocks []Block, acc []byte, start uint64, err error) {
	ents, err := Parse(bytes.NewReader(file), int64(len(file)), DefaultLimits())
	if err != nil {
		return nil, nil, 0, err
	}
	var hs, bs, rs, tds [][]byte
	for _, e := range ents {
		p := file[e.Off+8 : e.End()]
		switch e.Type {
		case TypeHeader, TypeBody, TypeSlimReceipts:
			pl, err := FrameDecode(p)
			if err != nil {
				return nil, nil, 0, err
			}
			switch e.Type {
			case TypeHeader:
				hs = append(hs, pl)
			case TypeBody:
				bs = append(bs, pl)
			default:
				rs = append(rs, pl)
			}
		case TypeTD:
			tds = append(tds, p)
		case TypeAccumulator:
			acc = p
		case TypeComponentIndex:
			if len(p) >= 8 {
				start = uint64(p[0]) | uint64(p[1])<<8 | uint64(p[2])<<16 | uint64(p[3])<<24 | uint64(p[4])<<32 | uint64(p[5])<<40 | uint64(p[6])<<48 | uint64(p[7])<<56
			}
		}
	}
	for i := range hs {
		b := Block{Header: hs[i]}
		if i < len(bs) {
			b.Body = bs[i]
		}
		if i < len(rs) {
			b.Receipts = rs[i]
		}
		if i < len(tds) {
			b.TD = tds[i]
		}
		blocks = append(blocks, b)
	}
	return blocks, acc, start, nil
}
