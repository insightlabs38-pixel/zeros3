// Package snappyz3 is the Z3-30 exact Snappy block parse/replay substrate. It is a
// laboratory object, not a ZeroS3 production format.
//
// A Snappy block is uvarint(decoded length) followed by tagged commands: literals
// (length in the tag or in 1-4 extra bytes) and copies (1-, 2- or 4-byte offset
// forms). Different encoders and versions choose different commands for the same
// input, so a compressed stream is NOT a function of its decoded bytes. Parse
// therefore decodes a stream strictly and records the commands it contained; Replay
// rebuilds the original compressed bytes from
//
//	the decoded bytes (the "child": literal bytes come from here, never from the recipe)
//	+ the command recipe (kind, length form, length and offset of every command)
//
// and checks, while writing, that every copy really reproduces the child. Streams
// that do not follow the canonical framing (non-minimal length prefix, trailing
// bytes, offset 0, a copy before the start of the output, output that disagrees with
// the declared length) are rejected, never guessed. The package imports only the
// standard library.
package snappyz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	ErrCorrupt  = errors.New("snappyz3: corrupt stream")
	ErrLimit    = errors.New("snappyz3: limit exceeded")
	ErrRecipe   = errors.New("snappyz3: malformed recipe")
	ErrMismatch = errors.New("snappyz3: recipe does not describe the decoded bytes")
	ErrNoncanon = errors.New("snappyz3: non-canonical framing")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}

// Limits bound a parse. The zero value selects the defaults.
type Limits struct {
	MaxDecoded  int64 // decoded bytes of one block (default 64 MiB)
	MaxCommands int   // commands of one block (default 1<<24)
}

func (l Limits) norm() Limits {
	if l.MaxDecoded <= 0 {
		l.MaxDecoded = 64 << 20
	}
	if l.MaxCommands <= 0 {
		l.MaxCommands = 1 << 24
	}
	return l
}

// Command kinds.
const (
	Literal uint8 = 0
	Copy1   uint8 = 1 // 2-byte tag form: length 4..11, offset < 2048
	Copy2   uint8 = 2 // 3-byte form: length 1..64, offset < 65536
	Copy4   uint8 = 3 // 5-byte form: length 1..64, offset < 2^32
)

// Cmd is one command. For a literal Form is the number of extra length bytes (0-4)
// the stream used; for a copy it is unused.
type Cmd struct {
	Kind uint8
	Form uint8
	Len  uint32
	Off  uint32
}

// minForm is the fewest extra length bytes a literal of n bytes can use.
func minForm(n uint32) uint8 {
	switch {
	case n-1 < 60:
		return 0
	case n-1 < 1<<8:
		return 1
	case n-1 < 1<<16:
		return 2
	case n-1 < 1<<24:
		return 3
	}
	return 4
}

// Parse strictly decodes one Snappy block. It returns the decoded bytes and the
// commands that produce them. Nothing is allocated from the declared length beyond
// lim.MaxDecoded and the remaining input (a literal-free copy bomb is bounded by
// MaxDecoded; a declared length larger than what the input can possibly expand to
// is rejected up front).
func Parse(src []byte, lim Limits) (decoded []byte, cmds []Cmd, err error) {
	lim = lim.norm()
	ulen, n := binary.Uvarint(src)
	if n <= 0 || n > 5 || ulen > 1<<32-1 {
		return nil, nil, corrupt("length prefix")
	}
	if n != uvLen(ulen) {
		return nil, nil, fmt.Errorf("%w: length prefix is not minimal", ErrNoncanon)
	}
	if int64(ulen) > lim.MaxDecoded {
		return nil, nil, fmt.Errorf("%w: declared length %d", ErrLimit, ulen)
	}
	// every input byte can produce at most 64 output bytes (a 2-byte copy1 yields 11, a
	// 3-byte copy2 yields 64): this rejects absurd declared lengths before allocating.
	if ulen > uint64(len(src)-n)*64 {
		return nil, nil, corrupt("declared length %d cannot come from %d input bytes", ulen, len(src)-n)
	}
	out := make([]byte, 0, ulen)
	s := n
	for s < len(src) {
		if len(cmds) >= lim.MaxCommands {
			return nil, nil, fmt.Errorf("%w: commands", ErrLimit)
		}
		tag := src[s]
		s++
		var c Cmd
		switch tag & 3 {
		case 0:
			ln := uint32(tag >> 2)
			form := uint8(0)
			if ln >= 60 {
				form = uint8(ln - 59)
				if s+int(form) > len(src) {
					return nil, nil, corrupt("truncated literal length")
				}
				ln = 0
				for i := 0; i < int(form); i++ {
					ln |= uint32(src[s+i]) << (8 * i)
				}
				s += int(form)
			}
			if ln == 1<<32-1 {
				return nil, nil, corrupt("literal length overflow")
			}
			ln++
			if uint64(ln) > uint64(len(src)-s) {
				return nil, nil, corrupt("literal of %d bytes with %d input left", ln, len(src)-s)
			}
			if uint64(len(out))+uint64(ln) > ulen {
				return nil, nil, corrupt("literal overruns the declared length")
			}
			out = append(out, src[s:s+int(ln)]...)
			s += int(ln)
			c = Cmd{Kind: Literal, Form: form, Len: ln}
		case 1:
			if s >= len(src) {
				return nil, nil, corrupt("truncated copy1")
			}
			c = Cmd{Kind: Copy1, Len: 4 + uint32(tag>>2)&7, Off: uint32(tag>>5)<<8 | uint32(src[s])}
			s++
		case 2:
			if s+2 > len(src) {
				return nil, nil, corrupt("truncated copy2")
			}
			c = Cmd{Kind: Copy2, Len: 1 + uint32(tag>>2), Off: uint32(binary.LittleEndian.Uint16(src[s:]))}
			s += 2
		default:
			if s+4 > len(src) {
				return nil, nil, corrupt("truncated copy4")
			}
			c = Cmd{Kind: Copy4, Len: 1 + uint32(tag>>2), Off: binary.LittleEndian.Uint32(src[s:])}
			s += 4
		}
		if c.Kind != Literal {
			if c.Off == 0 || uint64(c.Off) > uint64(len(out)) {
				return nil, nil, corrupt("copy offset %d with %d bytes produced", c.Off, len(out))
			}
			if uint64(len(out))+uint64(c.Len) > ulen {
				return nil, nil, corrupt("copy overruns the declared length")
			}
			for i := uint32(0); i < c.Len; i++ { // byte by byte: copies may overlap
				out = append(out, out[len(out)-int(c.Off)])
			}
		}
		cmds = append(cmds, c)
	}
	if uint64(len(out)) != ulen {
		return nil, nil, corrupt("decoded %d bytes, declared %d", len(out), ulen)
	}
	return out, cmds, nil
}

func uvLen(v uint64) int {
	var b [binary.MaxVarintLen64]byte
	return binary.PutUvarint(b[:], v)
}

// Marshal serialises commands compactly. Each command is uvarint(len<<3 | code) and,
// for copies, uvarint(offset). Codes: 0 literal in its minimal form, 1/2/3 copy1/2/4,
// 4-7 a literal using a non-minimal form of 1-4 extra length bytes. The decoded length
// is not stored (the caller knows it), and neither is the command count: the list ends
// when the lengths sum to the decoded length.
func Marshal(cmds []Cmd) []byte {
	b := make([]byte, 0, len(cmds)*3)
	for _, c := range cmds {
		code := uint64(c.Kind)
		if c.Kind == Literal && c.Form != minForm(c.Len) {
			code = 3 + uint64(c.Form)
		}
		b = binary.AppendUvarint(b, uint64(c.Len)<<3|code)
		if c.Kind != Literal {
			b = binary.AppendUvarint(b, uint64(c.Off))
		}
	}
	return b
}

// Unmarshal parses a recipe describing exactly decodedLen bytes; it must be consumed
// exactly. Counts and lengths are validated before any allocation.
func Unmarshal(b []byte, decodedLen int64, lim Limits) ([]Cmd, error) {
	lim = lim.norm()
	if decodedLen < 0 || decodedLen > lim.MaxDecoded {
		return nil, fmt.Errorf("%w: decoded length %d", ErrLimit, decodedLen)
	}
	var cmds []Cmd
	var total int64
	for total < decodedLen {
		if len(cmds) >= lim.MaxCommands {
			return nil, fmt.Errorf("%w: commands", ErrLimit)
		}
		t, n := binary.Uvarint(b)
		if n <= 0 || n != uvLen(t) {
			return nil, fmt.Errorf("%w: command header", ErrRecipe)
		}
		b = b[n:]
		code, ln := uint8(t&7), t>>3
		if ln == 0 || ln > 1<<32-1 || int64(ln) > decodedLen-total {
			return nil, fmt.Errorf("%w: command length %d", ErrRecipe, ln)
		}
		c := Cmd{Len: uint32(ln)}
		switch {
		case code == 0:
			c.Kind, c.Form = Literal, minForm(c.Len)
		case code <= 3:
			c.Kind = code
		default:
			c.Kind, c.Form = Literal, code-3
			if c.Form == minForm(c.Len) {
				return nil, fmt.Errorf("%w: redundant literal form", ErrRecipe)
			}
			if c.Form < minForm(c.Len) {
				return nil, fmt.Errorf("%w: literal length %d does not fit %d extra bytes", ErrRecipe, c.Len, c.Form)
			}
		}
		if c.Kind != Literal {
			off, m := binary.Uvarint(b)
			if m <= 0 || m != uvLen(off) || off == 0 || off > 1<<32-1 {
				return nil, fmt.Errorf("%w: offset", ErrRecipe)
			}
			b = b[m:]
			c.Off = uint32(off)
			if err := checkCopyForm(c); err != nil {
				return nil, err
			}
		}
		total += int64(c.Len)
		cmds = append(cmds, c)
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrRecipe, len(b))
	}
	return cmds, nil
}

func checkCopyForm(c Cmd) error {
	switch c.Kind {
	case Copy1:
		if c.Len < 4 || c.Len > 11 || c.Off >= 2048 {
			return fmt.Errorf("%w: copy1 length %d offset %d", ErrRecipe, c.Len, c.Off)
		}
	case Copy2:
		if c.Len > 64 || c.Off > 0xffff {
			return fmt.Errorf("%w: copy2 length %d offset %d", ErrRecipe, c.Len, c.Off)
		}
	case Copy4:
		if c.Len > 64 {
			return fmt.Errorf("%w: copy4 length %d", ErrRecipe, c.Len)
		}
	}
	return nil
}

// Replay writes the original compressed block from the decoded child and the
// commands. It verifies that each copy equals the child at that position (so the
// replayed stream decodes to exactly child) and that the commands cover the child
// exactly. Output is written progressively; on error the caller discards it.
func Replay(w io.Writer, child []byte, cmds []Cmd) error {
	var hdr [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(hdr[:], uint64(len(child)))
	if uint64(len(child)) > 1<<32-1 {
		return fmt.Errorf("%w: child of %d bytes", ErrLimit, len(child))
	}
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	pos := 0
	var buf [8]byte
	for i, c := range cmds {
		if c.Len == 0 || uint64(c.Len) > uint64(len(child)-pos) {
			return fmt.Errorf("%w: command %d overruns the child", ErrMismatch, i)
		}
		end := pos + int(c.Len)
		switch c.Kind {
		case Literal:
			if c.Form > 4 || c.Form < minForm(c.Len) {
				return fmt.Errorf("%w: command %d literal form", ErrRecipe, i)
			}
			l1 := c.Len - 1
			var h []byte
			if c.Form == 0 {
				buf[0] = byte(l1) << 2
				h = buf[:1]
			} else {
				buf[0] = byte(59+c.Form) << 2
				for k := 0; k < int(c.Form); k++ {
					buf[1+k] = byte(l1 >> (8 * k))
				}
				h = buf[:1+c.Form]
			}
			if _, err := w.Write(h); err != nil {
				return err
			}
			if _, err := w.Write(child[pos:end]); err != nil {
				return err
			}
		case Copy1, Copy2, Copy4:
			if err := checkCopyForm(c); err != nil {
				return err
			}
			if c.Off == 0 || uint64(c.Off) > uint64(pos) {
				return fmt.Errorf("%w: command %d offset %d at %d", ErrMismatch, i, c.Off, pos)
			}
			for k := pos; k < end; k++ {
				if child[k] != child[k-int(c.Off)] {
					return fmt.Errorf("%w: command %d copy differs from the child at %d", ErrMismatch, i, k)
				}
			}
			var h []byte
			switch c.Kind {
			case Copy1:
				buf[0] = byte(c.Off>>8)<<5 | byte(c.Len-4)<<2 | 1
				buf[1] = byte(c.Off)
				h = buf[:2]
			case Copy2:
				buf[0] = byte(c.Len-1)<<2 | 2
				binary.LittleEndian.PutUint16(buf[1:], uint16(c.Off))
				h = buf[:3]
			default:
				buf[0] = byte(c.Len-1)<<2 | 3
				binary.LittleEndian.PutUint32(buf[1:], c.Off)
				h = buf[:5]
			}
			if _, err := w.Write(h); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: command %d kind %d", ErrRecipe, i, c.Kind)
		}
		pos = end
	}
	if pos != len(child) {
		return fmt.Errorf("%w: commands cover %d of %d bytes", ErrMismatch, pos, len(child))
	}
	return nil
}
