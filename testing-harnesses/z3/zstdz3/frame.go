package zstdz3

import (
	"encoding/binary"
	"math"
)

// frameHdr is a parsed standard-frame header. raw is the exact header (from the Frame
// Header Descriptor to the end of Frame_Content_Size), kept verbatim in the skeleton.
type frameHdr struct {
	raw      []byte
	single   bool
	checksum bool
	dictID   uint32
	hasFCS   bool
	fcs      uint64
	window   int64
}

func fhdLen(fhd byte) int {
	n := 1
	if fhd>>5&1 == 0 {
		n++
	}
	n += [4]int{0, 1, 2, 4}[fhd&3]
	switch fhd >> 6 {
	case 0:
		if fhd>>5&1 == 1 {
			n++
		}
	case 1:
		n += 2
	case 2:
		n += 4
	default:
		n += 8
	}
	return n
}

// parseFrameHeader validates raw (exactly fhdLen bytes) against the format and lim.
func parseFrameHeader(raw []byte, lim Limits) (*frameHdr, error) {
	return parseFrameHeaderX(raw, lim, false)
}

func parseFrameHeaderX(raw []byte, lim Limits, allowDict bool) (*frameHdr, error) {
	if len(raw) == 0 || len(raw) != fhdLen(raw[0]) {
		return nil, corrupt("frame header length")
	}
	fhd := raw[0]
	if fhd>>3&1 != 0 {
		return nil, corrupt("reserved frame header bit set")
	}
	h := &frameHdr{raw: raw, single: fhd>>5&1 == 1, checksum: fhd>>2&1 == 1}
	p := 1
	if !h.single {
		wd := raw[1]
		p = 2
		wlog := 10 + uint(wd>>3)
		base := int64(1) << wlog
		h.window = base + base/8*int64(wd&7)
	}
	switch fhd & 3 {
	case 1:
		h.dictID, p = uint32(raw[p]), p+1
	case 2:
		h.dictID, p = uint32(binary.LittleEndian.Uint16(raw[p:])), p+2
	case 3:
		h.dictID, p = binary.LittleEndian.Uint32(raw[p:]), p+4
	}
	switch fhd >> 6 {
	case 0:
		if h.single {
			h.hasFCS, h.fcs = true, uint64(raw[p])
		}
	case 1:
		h.hasFCS, h.fcs = true, uint64(binary.LittleEndian.Uint16(raw[p:]))+256
	case 2:
		h.hasFCS, h.fcs = true, uint64(binary.LittleEndian.Uint32(raw[p:]))
	default:
		h.hasFCS, h.fcs = true, binary.LittleEndian.Uint64(raw[p:])
	}
	if h.hasFCS && h.fcs > math.MaxInt64 {
		return nil, corrupt("frame content size %d", h.fcs)
	}
	if h.single {
		h.window = int64(h.fcs)
	}
	if h.window > lim.MaxWindow {
		return nil, limit("window size %d > %d", h.window, lim.MaxWindow)
	}
	if h.hasFCS && int64(h.fcs) > lim.MaxFrameDecoded {
		return nil, limit("frame content size %d > %d", h.fcs, lim.MaxFrameDecoded)
	}
	if h.dictID != 0 && !allowDict {
		return nil, unsupported("dictionary id %d", h.dictID)
	}
	return h, nil
}

// blockMax is Block_Maximum_Size for the frame.
func (h *frameHdr) blockMax() int { return int(min(h.window, maxBlock)) }
