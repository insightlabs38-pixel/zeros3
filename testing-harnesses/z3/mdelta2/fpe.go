package mdelta2

import "encoding/binary"

// FPE codes raw floating-point words (no prediction): the exponent field by static rANS, sign and
// mantissa as raw bits. It is the first-sight coder for weights (a numeric split that plain DEFLATE
// planes cannot express). Only F32/F16/BF16.

func fpeSplit(d DType) (sh uint, eb uint, ok bool) {
	switch d {
	case DF32:
		return 23, 8, true
	case DBF16:
		return 7, 8, true
	case DF16:
		return 10, 5, true
	}
	return 0, 0, false
}

func fpeEncode(d DType, words []byte) ([]byte, bool) {
	sh, eb, ok := fpeSplit(d)
	if !ok {
		return nil, false
	}
	w := d.Width()
	n := len(words) / w
	cnt := make([]uint32, 1<<eb)
	syms := make([]uint8, n)
	rawN := uint(8*w) - eb
	raw := make([]byte, 0, n*int(rawN)/8+8)
	var acc uint64
	var nacc uint
	lowMask := uint32(1)<<sh - 1
	for i := 0; i < n; i++ {
		v := le(words, w, i)
		e := (v >> sh) & (1<<eb - 1)
		syms[i] = uint8(e)
		cnt[e]++
		r := v&lowMask | (v>>(sh+eb))<<sh
		acc |= uint64(r) << nacc
		nacc += rawN
		for nacc >= 8 {
			raw = append(raw, byte(acc))
			acc >>= 8
			nacc -= 8
		}
	}
	if nacc > 0 {
		raw = append(raw, byte(acc))
	}
	f := normFreqs(cnt)
	tab, err := newTab(f)
	if err != nil {
		return nil, false
	}
	out := appendFreqs(nil, f)
	out = binary.AppendUvarint(out, uint64(len(raw)))
	enc := newRansEnc()
	for i := n - 1; i >= 0; i-- {
		s := int(syms[i])
		enc.put(i&1, uint32(tab.freq[s]), uint32(tab.start[s]))
	}
	out = append(out, enc.finish()...)
	return append(out, raw...), true
}

func fpeDecode(d DType, payload []byte, n int) ([]byte, error) {
	sh, eb, ok := fpeSplit(d)
	if !ok {
		return nil, ErrCorrupt
	}
	w := d.Width()
	f, k, err := readFreqs(payload, 1<<eb)
	if err != nil {
		return nil, err
	}
	tab, err := newTab(f)
	if err != nil {
		return nil, err
	}
	pos := k
	rl, k2 := binary.Uvarint(payload[pos:])
	rawN := uint(8*w) - eb
	if k2 <= 0 || rl != (uint64(n)*uint64(rawN)+7)/8 {
		return nil, ErrCorrupt
	}
	pos += k2
	dec, k3, err := newRansDec(payload[pos:])
	if err != nil {
		return nil, err
	}
	pos += k3
	if uint64(len(payload)-pos) != rl {
		return nil, ErrCorrupt
	}
	raw := payload[pos:]
	out := make([]byte, n*w)
	lowMask := uint32(1)<<sh - 1
	var bp uint64
	for i := 0; i < n; i++ {
		e, err := dec.get(i&1, tab)
		if err != nil {
			return nil, err
		}
		byteIdx := int(bp >> 3)
		var v uint64
		if byteIdx+8 <= len(raw) {
			v = binary.LittleEndian.Uint64(raw[byteIdx:])
		} else {
			for j := 0; byteIdx+j < len(raw) && j < 8; j++ {
				v |= uint64(raw[byteIdx+j]) << (8 * uint(j))
			}
		}
		r := uint32(v>>uint(bp&7)) & (1<<rawN - 1)
		bp += uint64(rawN)
		word := r&lowMask | uint32(e)<<sh | (r>>sh)<<(sh+eb)
		putLE(out, w, i, word)
	}
	return out, nil
}
