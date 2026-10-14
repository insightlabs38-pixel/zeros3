package ir

// Exported view of the small adaptive range coder for other research packages
// (the Z3-10 tensor-delta codec). It adds no coder: these are thin wrappers
// over the same bitModel / encoder / decoder the DEFLATE IR uses.

// BitModel is one adaptive binary probability.
type BitModel = bitModel

// NewBitModels returns n freshly initialised models.
func NewBitModels(n int) []BitModel { return newModels(n) }

// RCEncoder is the range encoder.
type RCEncoder struct{ e *encoder }

func NewRCEncoder() *RCEncoder { return &RCEncoder{newEncoder()} }

// Bit codes one bit under m and adapts m.
func (r *RCEncoder) Bit(m *BitModel, bit int) { r.e.encode(m, bit) }

// Finish flushes and returns the (trailing-zero trimmed) stream.
func (r *RCEncoder) Finish() []byte { return r.e.finish() }

// RCDecoder is the range decoder; it reads zeros past the end of its input.
type RCDecoder struct{ d *decoder }

func NewRCDecoder(in []byte) *RCDecoder { return &RCDecoder{newDecoder(in)} }

// Bit decodes one bit under m and adapts m.
func (r *RCDecoder) Bit(m *BitModel) int { return r.d.decode(m) }

// Exhausted reports whether every input byte was consumed. A stream produced
// by Finish always is (the encoder emits exactly what the decoder reads, less
// trimmed zeros), so false means trailing junk after the coded symbols.
func (r *RCDecoder) Exhausted() bool { return r.d.pos >= len(r.d.in) }

// Direct codes a bit at probability 1/2 with no model (LZMA "direct bits"): for
// noise-like low bits that a model would only slow down.
func (r *RCEncoder) Direct(bit int) {
	e := r.e
	e.rng >>= 1
	if bit != 0 {
		e.low += uint64(e.rng)
	}
	for e.rng < rcTop {
		e.rng <<= 8
		e.shiftLow()
	}
}

// Direct decodes a bit coded by RCEncoder.Direct.
func (r *RCDecoder) Direct() int {
	d := r.d
	d.rng >>= 1
	bit := 0
	if d.code >= d.rng {
		d.code -= d.rng
		bit = 1
	}
	for d.rng < rcTop {
		d.rng <<= 8
		d.code = d.code<<8 | uint32(d.next())
	}
	return bit
}
