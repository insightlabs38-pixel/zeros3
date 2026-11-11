package graphcore

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/adler32"
)

// DEX adapter (generic-engine control for the specialized dexz3 representation): the header
// checksum/signature are derived, the six fixed-width id tables are OpTables with predictor
// search, and the data section is an opaque component. It does NOT rewrite index operands
// inside code items; that is what dexz3 does and what this control measures the gap to.

var errDEX = errors.New("graphcore: unsupported DEX")

type DEXEncoding struct {
	Program []byte
	Blobs   [][]byte // [0] column streams, [1] data section
	Cols    []ColChoice
}

func EncodeDEX(d []byte, search bool) (*DEXEncoding, error) {
	le := binary.LittleEndian
	if len(d) < 0x70 || string(d[:4]) != "dex\n" || le.Uint32(d[32:]) != uint32(len(d)) || le.Uint32(d[36:]) != 0x70 {
		return nil, errDEX
	}
	type tab struct {
		name   string
		size   uint32
		off    uint32
		widths []int
	}
	tabs := []tab{
		{"string_ids", le.Uint32(d[56:]), le.Uint32(d[60:]), []int{4}},
		{"type_ids", le.Uint32(d[64:]), le.Uint32(d[68:]), []int{4}},
		{"proto_ids", le.Uint32(d[72:]), le.Uint32(d[76:]), []int{4, 4, 4}},
		{"field_ids", le.Uint32(d[80:]), le.Uint32(d[84:]), []int{2, 2, 4}},
		{"method_ids", le.Uint32(d[88:]), le.Uint32(d[92:]), []int{2, 2, 4}},
		{"class_defs", le.Uint32(d[96:]), le.Uint32(d[100:]), []int{4, 4, 4, 4, 4, 4, 4, 4}},
	}
	pos := 0x70
	var prog, cols []byte
	prog = append(prog, OpStream, 0, 0)
	lit := func(lo, hi int) {
		if hi > lo {
			prog = append(prog, OpLit)
			prog = binary.AppendUvarint(prog, uint64(hi-lo))
			prog = append(prog, d[lo:hi]...)
		}
	}
	// header with checksum/signature zeroed (re-derived at the end)
	hdr := append([]byte(nil), d[:0x70]...)
	for i := 8; i < 32; i++ {
		hdr[i] = 0
	}
	prog = append(prog, OpLit)
	prog = binary.AppendUvarint(prog, 0x70)
	prog = append(prog, hdr...)
	enc := &DEXEncoding{}
	for _, t := range tabs {
		if t.size == 0 {
			continue
		}
		rowW := 0
		for _, w := range t.widths {
			rowW += w
		}
		if int(t.off) < pos || uint64(t.off)+uint64(t.size)*uint64(rowW) > uint64(len(d)) {
			return nil, fmt.Errorf("%w: table layout", errDEX)
		}
		lit(pos, int(t.off))
		prog = append(prog, OpTable)
		prog = binary.AppendUvarint(prog, uint64(t.size))
		prog = binary.AppendUvarint(prog, uint64(len(t.widths)))
		off := int(t.off)
		vals := make([][]uint64, len(t.widths))
		for c := range vals {
			vals[c] = make([]uint64, t.size)
		}
		for r := 0; r < int(t.size); r++ {
			for c, w := range t.widths {
				var v uint64
				for k := w - 1; k >= 0; k-- {
					v = v<<8 | uint64(d[off+k])
				}
				vals[c][r] = v
				off += w
			}
		}
		for c, w := range t.widths {
			raw := uvarints(vals[c])
			deltas := make([]uint64, t.size)
			var prev uint64
			for i, v := range vals[c] {
				deltas[i] = Zig(int64(v - prev))
				prev = v
			}
			dl := uvarints(deltas)
			ch := ColChoice{Table: t.name, Col: fmt.Sprint(c), RawBytes: len(raw), DeltaBytes: len(dl)}
			use, body := PredRaw, raw
			if search && packedLen(dl) < packedLen(raw) {
				use, body = PredDelta, dl
			}
			ch.Chosen = use
			enc.Cols = append(enc.Cols, ch)
			prog = append(prog, byte(w), byte(use))
			cols = append(cols, body...)
		}
		pos = off
	}
	// data section: everything from pos to the end (strings, code, maps...)
	prog = append(prog, OpCopy)
	prog = binary.AppendUvarint(prog, 1)
	prog = binary.AppendUvarint(prog, 0)
	prog = binary.AppendUvarint(prog, uint64(len(d)-pos))
	prog = append(prog, OpDerive, 2)
	prog = binary.AppendUvarint(prog, 12)
	prog = binary.AppendUvarint(prog, 32)
	prog = append(prog, OpDerive, 0)
	prog = binary.AppendUvarint(prog, 8)
	prog = binary.AppendUvarint(prog, 12)
	enc.Program = prog
	enc.Blobs = [][]byte{cols, d[pos:]}
	m := NewByteMachine()
	m.Blobs = enc.Blobs
	if err := m.Exec(prog); err != nil {
		return nil, fmt.Errorf("graphcore: dex self-replay: %w", err)
	}
	if err := m.Finish(); err != nil {
		return nil, err
	}
	if !bytes.Equal(m.Output(), d) {
		// checksum/signature not derivable (non-standard producer): fall back is the caller's
		out := m.Output()
		first := -1
		for i := range out {
			if i >= len(d) || out[i] != d[i] {
				first = i
				break
			}
		}
		return nil, fmt.Errorf("%w: replay differs at byte %d (len %d vs %d)", errDEX, first, len(out), len(d))
	}
	return enc, nil
}

var _ = sha1.Sum
var _ = adler32.Checksum
