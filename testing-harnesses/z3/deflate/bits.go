package deflate

// bitReader reads an LSB-first bit stream from memory.
type bitReader struct {
	data []byte
	pos  int // next byte to load
	acc  uint64
	nacc uint
}

func (b *bitReader) fill() {
	for b.nacc <= 56 && b.pos < len(b.data) {
		b.acc |= uint64(b.data[b.pos]) << b.nacc
		b.pos++
		b.nacc += 8
	}
}

// bitPos is the absolute number of bits consumed.
func (b *bitReader) bitPos() int64 { return int64(b.pos)*8 - int64(b.nacc) }

// bits consumes n <= 32 bits.
func (b *bitReader) bits(n uint) (uint32, error) {
	if n == 0 {
		return 0, nil
	}
	if b.nacc < n {
		b.fill()
		if b.nacc < n {
			return 0, ErrTruncated
		}
	}
	v := uint32(b.acc & (1<<n - 1))
	b.acc >>= n
	b.nacc -= n
	return v, nil
}

// readBytes consumes n whole bytes; the reader must be byte aligned.
func (b *bitReader) readBytes(n int) ([]byte, error) {
	if b.nacc%8 != 0 {
		panic("deflate: unaligned readBytes")
	}
	// bytes already buffered in acc are the next bytes of the stream
	start := b.pos - int(b.nacc/8)
	if start+n > len(b.data) {
		return nil, ErrTruncated
	}
	out := b.data[start : start+n]
	b.acc, b.nacc, b.pos = 0, 0, start+n
	return out, nil
}

// bitWriter writes an LSB-first bit stream, flushing to a sink in chunks.
type bitWriter struct {
	buf   []byte
	acc   uint64
	nacc  uint
	flush func([]byte) error
	err   error
}

const flushAt = 32 << 10

func (w *bitWriter) put(v uint32, n uint) {
	w.acc |= uint64(v) << w.nacc
	w.nacc += n
	for w.nacc >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
	if len(w.buf) >= flushAt {
		w.drain()
	}
}

func (w *bitWriter) drain() {
	if w.err == nil && len(w.buf) > 0 {
		w.err = w.flush(w.buf)
	}
	w.buf = w.buf[:0]
}

func (w *bitWriter) bytes(b []byte) { // writer must be byte aligned
	w.drain()
	if w.err == nil {
		w.err = w.flush(b)
	}
}
