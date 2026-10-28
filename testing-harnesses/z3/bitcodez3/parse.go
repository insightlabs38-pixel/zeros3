// Package bitcodez3 frames LLVM's generic bitstream without interpreting LLVM IR.
package bitcodez3

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

const MaxSource = 128 << 20
const MaxRecords = 2000000
const MaxValues = 8000000
const MaxBlob = 16 << 20

// Record retains bit positions and an encoding-independent diagnostic operand hash.
// That hash is not a semantic IR identity and never supplies reconstruction bytes.
type Record struct {
	Block, Code, Abbrev uint64
	Start, End          uint64
	Values              int
	OperandSHA          [32]byte
}
type Block struct {
	ID               uint64
	Start, Body, End uint64
	Width            uint64
	Records          int
}
type Plan struct {
	Blocks  []Block
	Records []Record
	Abbrevs int
}
type operand struct {
	literal                bool
	value, encoding, width uint64
}
type abbreviation []operand
type reader struct {
	data           []byte
	pos, end       uint64
	err            error
	values         int
	abbrevs        int
	abbrevOperands int
	info           map[uint64][]abbreviation
	plan           Plan
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = fmt.Errorf("unsupported or invalid LLVM bitstream at bit %d", r.pos)
	}
}
func (r *reader) bits(n uint64) uint64 {
	if r.err != nil {
		return 0
	}
	if n > 64 || r.pos > r.end || n > r.end-r.pos {
		r.fail()
		return 0
	}
	var v, shift uint64
	for n > 0 {
		take := min(n, 8-r.pos%8)
		v |= (uint64(r.data[r.pos/8]) >> (r.pos % 8) & ((1 << take) - 1)) << shift
		r.pos += take
		shift += take
		n -= take
	}
	return v
}
func (r *reader) vbr(n uint64) uint64 {
	if n < 2 || n > 32 {
		r.fail()
		return 0
	}
	var v, shift uint64
	for i := 0; i < 64 && r.err == nil; i++ {
		x := r.bits(n)
		part := x & ((1 << (n - 1)) - 1)
		if shift >= 64 || part > (^uint64(0)>>shift) {
			r.fail()
			return 0
		}
		v |= part << shift
		if x>>(n-1) == 0 {
			return v
		}
		shift += n - 1
	}
	r.fail()
	return 0
}
func (r *reader) align() {
	n := (32 - r.pos%32) % 32
	if r.bits(n) != 0 {
		r.fail()
	}
}
func (r *reader) definition() abbreviation {
	n := r.vbr(5)
	if n == 0 || n > 256 {
		r.fail()
		return nil
	}
	r.abbrevOperands += int(n)
	if r.abbrevOperands > 1<<20 {
		r.fail()
		return nil
	}
	a := make(abbreviation, 0, n)
	for i := uint64(0); i < n && r.err == nil; i++ {
		o := operand{literal: r.bits(1) != 0}
		if o.literal {
			o.value = r.vbr(8)
		} else {
			o.encoding = r.bits(3)
			switch o.encoding {
			case 1, 2:
				o.width = r.vbr(5)
				if o.width > 32 || (o.encoding == 2 && o.width < 2) {
					r.fail()
				}
			case 3:
				if i != n-2 {
					r.fail()
				}
			case 4:
			case 5:
				if i != n-1 {
					r.fail()
				}
			default:
				r.fail()
			}
		}
		a = append(a, o)
	}
	for i, o := range a {
		if o.encoding == 3 {
			if i+1 >= len(a) {
				r.fail()
				break
			}
			next := a[i+1]
			if next.literal || next.encoding == 3 || next.encoding == 5 {
				r.fail()
			}
		}
	}
	r.abbrevs++
	if r.abbrevs > 65536 {
		r.fail()
	}
	return a
}
func (r *reader) value(o operand) uint64 {
	if o.literal {
		return o.value
	}
	switch o.encoding {
	case 1:
		return r.bits(o.width)
	case 2:
		return r.vbr(o.width)
	case 4:
		x := r.bits(6)
		switch {
		case x < 26:
			return x + 'a'
		case x < 52:
			return x - 26 + 'A'
		case x < 62:
			return x - 52 + '0'
		case x == 62:
			return '.'
		default:
			return '_'
		}
	}
	r.fail()
	return 0
}
func (r *reader) record(a abbreviation, plain bool) (uint64, int, [32]byte, []uint64) {
	h := sha256.New()
	var scratch [8]byte
	count := 0
	var first []uint64
	add := func(v uint64) {
		r.values++
		count++
		if r.values > min(MaxValues, len(r.data)*32) {
			r.fail()
			return
		}
		binary.LittleEndian.PutUint64(scratch[:], v)
		h.Write(scratch[:])
		if len(first) < 2 {
			first = append(first, v)
		}
	}
	if plain {
		code := r.vbr(6)
		add(code)
		n := r.vbr(6)
		if n > uint64(max(0, min(MaxValues, len(r.data)*32)-r.values)) {
			r.fail()
		}
		for i := uint64(0); i < n && r.err == nil; i++ {
			add(r.vbr(6))
		}
	} else {
		for i := 0; i < len(a) && r.err == nil; i++ {
			o := a[i]
			switch o.encoding {
			case 3:
				n := r.vbr(6)
				if n > uint64(max(0, min(MaxValues, len(r.data)*32)-r.values)) {
					r.fail()
				}
				i++
				for j := uint64(0); j < n && r.err == nil; j++ {
					add(r.value(a[i]))
				}
			case 5:
				n := r.vbr(6)
				r.align()
				if n > uint64(max(0, min(MaxValues, len(r.data)*32)-r.values)) || n > MaxBlob || r.pos > r.end || n > (r.end-r.pos)/8 {
					r.fail()
					break
				}
				for j := uint64(0); j < n && r.err == nil; j++ {
					add(r.bits(8))
				}
				r.align()
			default:
				add(r.value(o))
			}
		}
	}
	if len(first) == 0 {
		r.fail()
		return 0, 0, [32]byte{}, nil
	}
	var digest [32]byte
	copy(digest[:], h.Sum(digest[:0]))
	return first[0], count - 1, digest, first[1:]
}
func (r *reader) block(id, width, end uint64, depth int, top bool) {
	if depth > 32 || width < 2 || width > 32 || len(r.plan.Blocks) > 262144 {
		r.fail()
		return
	}
	oldEnd := r.end
	r.end = end
	defer func() { r.end = oldEnd }()
	abbrevs := append([]abbreviation(nil), r.info[id]...)
	var target uint64
	hasTarget := false
	for r.pos < end && r.err == nil {
		start := r.pos
		code := r.bits(width)
		switch code {
		case 0:
			if top {
				r.fail()
				return
			}
			r.align()
			if r.pos != end {
				r.fail()
			}
			return
		case 1:
			child := r.vbr(8)
			w := r.vbr(4)
			r.align()
			words := r.bits(32)
			body := r.pos
			if words > (end-body)/32 || w < 2 || w > 32 {
				r.fail()
				return
			}
			finish := body + words*32
			index := len(r.plan.Blocks)
			r.plan.Blocks = append(r.plan.Blocks, Block{ID: child, Start: start, Body: body, End: finish, Width: w})
			before := len(r.plan.Records)
			r.block(child, w, finish, depth+1, false)
			r.plan.Blocks[index].Records = len(r.plan.Records) - before
			if r.pos != finish {
				r.fail()
				return
			}
		case 2:
			a := r.definition()
			if id == 0 && !top {
				if !hasTarget {
					r.fail()
					return
				}
				r.info[target] = append(r.info[target], a)
			} else {
				abbrevs = append(abbrevs, a)
				if len(abbrevs) > 4096 {
					r.fail()
				}
			}
		default:
			var a abbreviation
			if code > 3 {
				if code-4 >= uint64(len(abbrevs)) {
					r.fail()
					return
				}
				a = abbrevs[code-4]
			}
			value, n, hash, first := r.record(a, code == 3)
			if len(r.plan.Records) >= MaxRecords {
				r.fail()
				return
			}
			r.plan.Records = append(r.plan.Records, Record{Block: id, Code: value, Abbrev: code, Start: start, End: r.pos, Values: n, OperandSHA: hash})
			if id == 0 && value == 1 {
				if n != 1 || len(first) != 1 {
					r.fail()
					return
				}
				target = first[0]
				hasTarget = true
			}
		}
	}
	if !top {
		r.fail()
	}
}

// Parse validates generic block lengths, abbreviations, arrays, blobs and records.
// Unknown LLVM block/record IDs remain exact; no IR-level validity is claimed.
func Parse(data []byte) (*Plan, error) {
	if len(data) < 8 || len(data) > MaxSource || len(data)%4 != 0 || string(data[:4]) != "BC\xc0\xde" {
		return nil, fmt.Errorf("unsupported LLVM bitcode envelope")
	}
	r := reader{data: data, pos: 32, end: uint64(len(data)) * 8, info: map[uint64][]abbreviation{}}
	r.block(^uint64(0), 2, r.end, 0, true)
	if r.err != nil {
		return nil, r.err
	}
	r.plan.Abbrevs = r.abbrevs
	return &r.plan, nil
}
