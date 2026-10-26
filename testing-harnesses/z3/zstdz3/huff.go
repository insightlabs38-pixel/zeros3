package zstdz3

import "math/bits"

// Zstandard Huffman literal coding. The tree is the canonical code defined by a weight
// per symbol: symbols are placed in a 2^maxBits decoding table in increasing weight
// order (then symbol order), each taking 2^(weight-1) entries.

const maxHuffBits = 11

type huffDec struct{ sym, nb uint8 }

type huffTable struct {
	desc    []byte // the verbatim tree description (for checkpoints)
	nsym    int
	maxBits int
	nb      [256]uint8
	code    [256]uint16
	dec     []huffDec
}

// buildHuff builds the table from the listed weights; the final symbol's weight is
// implied by the requirement that the sum of 2^(w-1) is a power of two.
func buildHuff(listed []uint8) (*huffTable, error) {
	if len(listed) == 0 || len(listed) > 255 {
		return nil, corrupt("huffman tree with %d weights", len(listed))
	}
	var total uint32
	for _, w := range listed {
		if w > maxHuffBits {
			return nil, corrupt("huffman weight %d", w)
		}
		if w > 0 {
			total += 1 << (w - 1)
		}
	}
	if total == 0 {
		return nil, corrupt("huffman tree without weight")
	}
	mb := bits.Len32(total) // floor(log2 total) + 1
	if mb > maxHuffBits {
		return nil, corrupt("huffman max bits %d", mb)
	}
	rest := uint32(1)<<uint(mb) - total
	if rest == 0 || rest&(rest-1) != 0 {
		return nil, corrupt("huffman weights do not complete a prefix code")
	}
	t := &huffTable{nsym: len(listed) + 1, maxBits: mb}
	weights := append(append(make([]uint8, 0, 256), listed...), uint8(bits.Len32(rest)))
	if weights[len(weights)-1] > maxHuffBits {
		return nil, corrupt("huffman implied weight")
	}
	// A complete code has an even number (at least two) of leaves at the deepest
	// level, i.e. of weight-1 symbols; the reference decoder rejects anything else.
	ones := 0
	for _, w := range weights {
		if w == 1 {
			ones++
		}
	}
	if ones < 2 || ones&1 != 0 {
		return nil, corrupt("huffman tree with %d symbols of the longest code", ones)
	}
	t.dec = make([]huffDec, 1<<uint(mb))
	pos := 0
	for w := 1; w <= mb; w++ {
		nbits := uint8(mb + 1 - w)
		for s, sw := range weights {
			if int(sw) != w {
				continue
			}
			n := 1 << uint(w-1)
			for k := 0; k < n; k++ {
				t.dec[pos+k] = huffDec{uint8(s), nbits}
			}
			t.nb[s] = nbits
			t.code[s] = uint16(pos >> uint(mb-int(nbits)))
			pos += n
		}
	}
	if pos != 1<<uint(mb) {
		return nil, corrupt("huffman table not full")
	}
	return t, nil
}

// readHuffTree parses a Huffman tree description at the start of b (RFC 8878 section
// 4.2.1) and returns the tree and the description's byte length.
func readHuffTree(b []byte) (*huffTable, int, error) {
	if len(b) == 0 {
		return nil, 0, corrupt("missing huffman tree description")
	}
	hb := int(b[0])
	var w []uint8
	var n int
	if hb >= 128 {
		cnt := hb - 127
		n = 1 + (cnt+1)/2
		if len(b) < n {
			return nil, 0, corrupt("truncated direct huffman weights")
		}
		w = make([]uint8, cnt)
		for i := range w {
			if i&1 == 0 {
				w[i] = b[1+i/2] >> 4
			} else {
				w[i] = b[1+i/2] & 15
			}
		}
	} else {
		n = 1 + hb
		if hb == 0 || len(b) < n {
			return nil, 0, corrupt("bad compressed huffman weights size %d", hb)
		}
		var err error
		if w, err = decodeHuffWeights(b[1:n]); err != nil {
			return nil, 0, err
		}
	}
	t, err := buildHuff(w)
	if err != nil {
		return nil, 0, err
	}
	t.desc = append([]byte(nil), b[:n]...)
	return t, n, nil
}

// decodeHuffWeights decodes the FSE-compressed weights: two interleaved states over one
// backward bitstream, accuracy log at most 6, ending when a state update overruns.
func decodeHuffWeights(b []byte) ([]uint8, error) {
	t, k, err := readFSEDesc(b, 6, 255)
	if err != nil {
		return nil, err
	}
	r, err := newRevReader(b[k:])
	if err != nil {
		return nil, err
	}
	acc := int(t.acc)
	v1, ok1 := r.read(acc)
	v2, ok2 := r.read(acc)
	if !ok1 || !ok2 {
		return nil, corrupt("truncated huffman weight states")
	}
	s := [2]int{int(v1), int(v2)}
	var out []uint8
	for i := 0; ; i++ {
		if len(out) >= 255 {
			return nil, corrupt("too many huffman weights")
		}
		st := i & 1
		d := t.dec[s[st]]
		out = append(out, d.sym)
		n := int(d.nb)
		if n > r.pos { // the update overran the stream: the other state's symbol is the last one
			out = append(out, t.dec[s[st^1]].sym)
			break
		}
		s[st] = int(d.base) + int(r.get(n))
		r.pos -= n
	}
	return out, nil
}

// decodeStream decodes len(out) symbols from one backward Huffman stream.
func (t *huffTable) decodeStream(data []byte, out []byte) error {
	r, err := newRevReader(data)
	if err != nil {
		return err
	}
	mb := t.maxBits
	for i := range out {
		e := t.dec[r.peek(mb)]
		if !r.skip(int(e.nb)) {
			return corrupt("huffman stream overrun")
		}
		out[i] = e.sym
	}
	if r.pos != 0 {
		return corrupt("huffman stream has %d unused bits", r.pos)
	}
	return nil
}

// encodeStream appends the backward stream for lit (the last symbol is written first,
// so the decoder meets the first symbol first). It fails for a symbol outside the tree.
func (t *huffTable) encodeStream(dst []byte, lit []byte) ([]byte, bool) {
	var w revWriter
	w.buf = dst
	for i := len(lit) - 1; i >= 0; i-- {
		nb := t.nb[lit[i]]
		if nb == 0 {
			return dst, false
		}
		w.write(uint32(t.code[lit[i]]), int(nb))
	}
	return w.finish(), true
}
