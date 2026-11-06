// Package simdelta contains research-only authenticated similarity deltas.
// It does not define a production ZeroS3 storage format.
package simdelta

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/gitz3"
)

const MaxBytes = 16 << 20
const MaxBases = 4

var Algorithms = []string{"direct", "xor", "sparse-xor", "changed", "rolling", "git", "adaptive"}
var bad = errors.New("invalid similarity delta")

func zip(b []byte) []byte {
	var out bytes.Buffer
	w, _ := flate.NewWriter(&out, 6)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

// Physical charges production-style independent record header and locator bytes.
func Physical(b []byte) int {
	z := zip(b)
	if len(z) < len(b) {
		return len(z) + 96
	}
	return len(b) + 96
}
func uv(b []byte, n int) []byte { return binary.AppendUvarint(b, uint64(n)) }

type op struct {
	base, off, n int
	lit          []byte
}

// Encode emits a self-authenticating envelope. Only bases actually used by the
// selected program appear in its dependency list, in input order.
func Encode(algorithm string, bases [][]byte, target []byte) ([]byte, error) {
	if len(target) > MaxBytes || len(bases) > MaxBases {
		return nil, bad
	}
	for _, b := range bases {
		if len(b) > MaxBytes {
			return nil, bad
		}
	}
	kind := -1
	for i, a := range Algorithms {
		if a == algorithm {
			kind = i
		}
	}
	if kind < 0 {
		return nil, bad
	}
	var p []byte
	used := make([]bool, len(bases))
	switch algorithm {
	case "direct":
		p = target
	case "xor", "sparse-xor", "changed":
		if len(bases) == 0 {
			return nil, bad
		}
		used[0] = true
		x := make([]byte, len(target))
		copy(x, bases[0])
		for i := range x {
			x[i] ^= target[i]
		}
		if algorithm == "xor" {
			p = x
			break
		}
		// A zero-filled XOR residual or base-prefilled changed-range program.
		for i := 0; i < len(x); {
			if x[i] == 0 {
				i++
				continue
			}
			start := i
			end := i + 1
			for end < len(x) {
				if x[end] != 0 {
					end++
					continue
				}
				j := end
				for j < len(x) && j-end < 8 && x[j] == 0 {
					j++
				}
				if j < len(x) && j-end < 8 {
					end = j + 1
					continue
				}
				break
			}
			p = uv(p, start)
			p = uv(p, end-start)
			if algorithm == "changed" {
				p = append(p, target[start:end]...)
			} else {
				p = append(p, x[start:end]...)
			}
			i = end
		}
	case "rolling", "git", "adaptive":
		anchor, stride, slots := 24, 8, 1
		if algorithm == "git" {
			anchor, stride, slots = 16, 4, 2
		}
		if algorithm == "adaptive" {
			anchor, stride, slots = 8, 4, 4
		}
		ops := match(bases, target, anchor, stride, slots)
		for _, o := range ops {
			if o.lit == nil {
				used[o.base] = true
			}
		}
		if algorithm == "git" {
			// This independently written encoder uses the existing exact Git decoder.
			if len(bases) != 1 {
				return nil, bad
			}
			used[0] = true
			p = uv(p, len(bases[0]))
			p = uv(p, len(target))
			for _, o := range ops {
				if o.lit != nil {
					for s := o.lit; len(s) > 0; {
						n := len(s)
						if n > 127 {
							n = 127
						}
						p = append(p, byte(n))
						p = append(p, s[:n]...)
						s = s[n:]
					}
					continue
				}
				off, n := o.off, o.n
				for n > 0 {
					k := n
					if k > 0xffffff {
						k = 0xffffff
					}
					c := byte(0x80)
					var args []byte
					for j := 0; j < 4; j++ {
						v := byte(off >> (8 * j))
						if v != 0 {
							c |= 1 << j
							args = append(args, v)
						}
					}
					for j := 0; j < 3; j++ {
						v := byte(k >> (8 * j))
						if v != 0 {
							c |= 0x10 << j
							args = append(args, v)
						}
					}
					p = append(p, c)
					p = append(p, args...)
					off += k
					n -= k
				}
			}
		} else {
			mapping := make([]int, len(used))
			v := 0
			for i, u := range used {
				mapping[i] = v
				if u {
					v++
				}
			}
			for _, o := range ops {
				if o.lit != nil {
					p = append(p, 0)
					p = uv(p, len(o.lit))
					p = append(p, o.lit...)
				} else {
					p = append(p, 1)
					p = uv(p, mapping[o.base])
					p = uv(p, o.off)
					p = uv(p, o.n)
				}
			}
		}
	}
	h := append([]byte("B1D1"), byte(kind))
	h = uv(h, len(target))
	sum := sha256.Sum256(target)
	h = append(h, sum[:]...)
	count := 0
	for _, u := range used {
		if u {
			count++
		}
	}
	h = append(h, byte(count))
	for i, u := range used {
		if u {
			s := sha256.Sum256(bases[i])
			h = append(h, s[:]...)
			h = uv(h, len(bases[i]))
		}
	}
	z := zip(p)
	if len(z) < len(p) {
		h = append(h, 1)
		p = z
	} else {
		h = append(h, 0)
	}
	h = uv(h, len(p))
	return append(h, p...), nil
}

type cursor struct {
	b   []byte
	err error
}

func (r *cursor) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.b) {
		r.err = bad
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
func (r *cursor) num() int {
	v, n := binary.Uvarint(r.b)
	if r.err != nil || n <= 0 || v > MaxBytes*8 {
		r.err = bad
		return 0
	}
	r.b = r.b[n:]
	return int(v)
}
func (r *cursor) byte() byte {
	b := r.take(1)
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

type Header struct {
	Algorithm string
	Size      int
	Hash      [32]byte
	Bases     [][32]byte
	Lengths   []int
}

func parse(wire []byte) (Header, []byte, error) {
	var h Header
	if len(wire) > MaxBytes*8 {
		return h, nil, bad
	}
	r := cursor{b: wire}
	if string(r.take(4)) != "B1D1" {
		return h, nil, bad
	}
	k := int(r.byte())
	if k >= len(Algorithms) {
		return h, nil, bad
	}
	h.Algorithm = Algorithms[k]
	h.Size = r.num()
	if h.Size > MaxBytes {
		return h, nil, bad
	}
	copy(h.Hash[:], r.take(32))
	n := int(r.byte())
	if n > MaxBases {
		return h, nil, bad
	}
	for i := 0; i < n; i++ {
		var s [32]byte
		copy(s[:], r.take(32))
		h.Bases = append(h.Bases, s)
		sz := r.num()
		if sz > MaxBytes {
			return h, nil, bad
		}
		h.Lengths = append(h.Lengths, sz)
	}
	mode := r.byte()
	sz := r.num()
	p := r.take(sz)
	if r.err != nil || len(r.b) != 0 || mode > 1 {
		return h, nil, bad
	}
	if mode == 1 {
		br := bytes.NewReader(p)
		f := flate.NewReader(br)
		out, e := io.ReadAll(io.LimitReader(f, MaxBytes*8+1))
		ce := f.Close()
		if e != nil || ce != nil || len(out) > MaxBytes*8 || br.Len() != 0 {
			return h, nil, bad
		}
		p = out
	}
	return h, p, nil
}
func Inspect(wire []byte) (Header, error) { h, _, e := parse(wire); return h, e }

// Decode verifies every base and the final target. The caller controls base
// retrieval; Resolve adds graph depth, cycle and cumulative-work limits.
func Decode(wire []byte, get func([32]byte) ([]byte, error)) ([]byte, error) {
	h, p, e := parse(wire)
	if e != nil {
		return nil, e
	}
	bases := make([][]byte, len(h.Bases))
	for i, s := range h.Bases {
		if get == nil {
			return nil, bad
		}
		b, e := get(s)
		if e != nil {
			return nil, e
		}
		if len(b) != h.Lengths[i] || sha256.Sum256(b) != s {
			return nil, bad
		}
		bases[i] = b
	}
	var out []byte
	switch h.Algorithm {
	case "direct":
		if len(bases) != 0 || len(p) != h.Size {
			return nil, bad
		}
		out = append([]byte{}, p...)
	case "xor", "sparse-xor", "changed":
		if len(bases) != 1 {
			return nil, bad
		}
		out = make([]byte, h.Size)
		copy(out, bases[0])
		if h.Algorithm == "xor" {
			if len(p) != len(out) {
				return nil, bad
			}
			for i := range out {
				out[i] ^= p[i]
			}
			break
		}
		r := cursor{b: p}
		end := 0
		for len(r.b) > 0 && r.err == nil {
			off, n := r.num(), r.num()
			if off < end || n == 0 || off > len(out) || n > len(out)-off {
				return nil, bad
			}
			s := r.take(n)
			if r.err != nil {
				return nil, bad
			}
			for i, v := range s {
				if h.Algorithm == "changed" {
					out[off+i] = v
				} else {
					out[off+i] ^= v
				}
			}
			end = off + n
		}
		if r.err != nil {
			return nil, bad
		}
	case "git":
		if len(bases) != 1 {
			return nil, bad
		}
		out, e = gitz3.ApplyDelta(bases[0], p, int64(MaxBytes))
		if e != nil {
			return nil, e
		}
	default:
		out = make([]byte, 0, h.Size)
		r := cursor{b: p}
		for len(r.b) > 0 && r.err == nil {
			switch r.byte() {
			case 0:
				n := r.num()
				if n == 0 || n > h.Size-len(out) {
					return nil, bad
				}
				s := r.take(n)
				out = append(out, s...)
			case 1:
				b, off, n := r.num(), r.num(), r.num()
				if b >= len(bases) || n == 0 || off > len(bases[b]) || n > len(bases[b])-off || n > h.Size-len(out) {
					return nil, bad
				}
				out = append(out, bases[b][off:off+n]...)
			default:
				return nil, bad
			}
		}
		if r.err != nil {
			return nil, bad
		}
	}
	if len(out) != h.Size || sha256.Sum256(out) != h.Hash {
		return nil, fmt.Errorf("%w: target hash/length", bad)
	}
	return out, nil
}

// Resolve is deliberately uncached so cold-read amplification includes shared
// edges. Work is charged for every expanded target, including repeats.
func Resolve(root [32]byte, get func([32]byte) ([]byte, error), maxDepth int, budget int64) ([]byte, error) {
	if maxDepth < 0 || maxDepth > 4 || budget < 0 {
		return nil, bad
	}
	active := map[[32]byte]bool{}
	var walk func([32]byte, int) ([]byte, error)
	walk = func(s [32]byte, d int) ([]byte, error) {
		if d > maxDepth || active[s] {
			return nil, bad
		}
		w, e := get(s)
		if e != nil {
			return nil, e
		}
		h, e := Inspect(w)
		if e != nil || h.Hash != s || int64(h.Size) > budget {
			return nil, bad
		}
		budget -= int64(h.Size)
		active[s] = true
		defer delete(active, s)
		return Decode(w, func(b [32]byte) ([]byte, error) { return walk(b, d+1) })
	}
	return walk(root, 0)
}
