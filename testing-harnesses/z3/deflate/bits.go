package deflate

import "io"

// bitReader reads an LSB-first bit stream from memory, or (streaming mode,
// src != nil) from a reader through a reusable buffer.
type bitReader struct {
	data []byte
	pos  int // next byte to load
	acc  uint64
	nacc uint

	src  io.Reader // streaming source; data is then a refillable buffer
	base int64     // bytes of src preceding data[0]
	rerr error
}

func (b *bitReader) fill() {
	for b.nacc <= 56 {
		if b.pos >= len(b.data) {
			if b.src == nil || !b.refill() {
				return
			}
		}
		b.acc |= uint64(b.data[b.pos]) << b.nacc
		b.pos++
		b.nacc += 8
	}
}

// refill replaces the exhausted buffer with the next chunk of the source.
func (b *bitReader) refill() bool {
	if b.rerr != nil {
		return false
	}
	b.base += int64(len(b.data))
	b.data, b.pos = b.data[:cap(b.data)], 0
	for {
		n, err := b.src.Read(b.data)
		if n > 0 {
			b.data = b.data[:n]
			return true
		}
		if err != nil {
			b.rerr = err
			b.data = b.data[:0]
			return false
		}
	}
}

// bitPos is the absolute number of bits consumed.
func (b *bitReader) bitPos() int64 { return (b.base+int64(b.pos))*8 - int64(b.nacc) }

// readStream consumes n whole bytes (the reader must be byte aligned) and
// passes them to emit in chunks no larger than the buffer.
func (b *bitReader) readStream(n int, emit func([]byte) error) error {
	if b.nacc%8 != 0 {
		panic("deflate: unaligned readStream")
	}
	var tmp [8]byte
	k := 0
	for b.nacc > 0 && n > 0 {
		tmp[k] = byte(b.acc)
		b.acc >>= 8
		b.nacc -= 8
		k++
		n--
	}
	if k > 0 {
		if err := emit(tmp[:k]); err != nil {
			return err
		}
	}
	for n > 0 {
		if b.pos >= len(b.data) && (b.src == nil || !b.refill()) {
			return ErrTruncated
		}
		m := min(n, len(b.data)-b.pos)
		if err := emit(b.data[b.pos : b.pos+m]); err != nil {
			return err
		}
		b.pos += m
		n -= m
	}
	return nil
}

// leftover returns the whole bytes read ahead but not consumed.
func (b *bitReader) leftover() []byte {
	var out []byte
	for b.nacc >= 8 {
		out = append(out, byte(b.acc))
		b.acc >>= 8
		b.nacc -= 8
	}
	return append(out, b.data[b.pos:]...)
}

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
