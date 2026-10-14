package tdelta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Z3-10B/C/E: row-aligned exact reuse and the per-tensor hybrid with admission.
//
// A tensor is stored in exactly one of these modes; every one reconstructs the
// exact target bytes and none does arithmetic on values:
//
//	Raw         the bytes themselves
//	Standalone  PXR against zeros (byte-plane coding, no base)
//	Same        bit-identical to the base tensor (no payload)
//	Delta       PXR of target XOR base
//	Rows        recipe of exact-reuse / delta row runs + PXR of the changed rows
//
// The base is the same-name, same-dtype, same-shape tensor of the file's one
// base file. There is no other base search.

type Mode uint8

const (
	ModeRaw Mode = iota + 1
	ModeStandalone
	ModeSame
	ModeDelta
	ModeRows
	numModes
)

var ModeNames = [...]string{"", "raw", "standalone", "same", "delta", "rows"}

func (m Mode) String() string {
	if int(m) < len(ModeNames) {
		return ModeNames[m]
	}
	return fmt.Sprintf("mode%d", m)
}

// Spec describes the tensor being coded.
type Spec struct {
	Shape []int64
	W     int // element width in bytes (1, 2, 4, 8)
}

// RowBytes is the byte length of one native row (the outermost dimension), or 0
// when the tensor has no usable row structure.
func (s Spec) RowBytes(n int) int {
	if len(s.Shape) < 2 || s.Shape[0] < 2 || s.Shape[0] > int64(n) || int64(n)%s.Shape[0] != 0 {
		return 0
	}
	return int(int64(n) / s.Shape[0])
}

// Options steers the encoder; none of it is needed to decode.
type Options struct {
	MinReuse int  // reuse runs shorter than this many rows are folded into the delta (default 1)
	NoRows   bool // do not try the row candidate
	NoDelta  bool // standalone / raw only (no usable base)
	// MaxFrac: a non-raw mode is admitted only if its wire size <= MaxFrac*raw (0 = no limit).
	MaxFrac float64
	Compare bool // also measure the DEFLATE plane-XOR baseline (Z3-09) for reporting
}

// Cand is one measured candidate (wire bytes include the mode byte and length prefix).
type Cand struct {
	Mode  Mode
	Wire  int
	Pay   int // payload bytes inside the record
	Meta  int // recipe bytes inside the payload (Rows only)
	Rows  RowStats
	OK    bool
	Flate int // Compare: DEFLATE plane-XOR (or plane alone for Standalone) payload bytes
}

// ----- row recipe -----

// RowStats describes one row recipe.
type RowStats struct {
	Rows, ReuseRows, DeltaRows, Runs int
	MetaBytes                        int // encoded recipe (run count + run lengths)
	DeltaBytes                       int // PXR payload of the changed rows
}

// rowRuns returns alternating run lengths in rows, reuse first (the first may be 0).
func rowRuns(base, target []byte, rowBytes, minReuse int) []int {
	rows := len(target) / rowBytes
	if minReuse < 1 {
		minReuse = 1
	}
	eq := make([]bool, rows)
	for r := range eq {
		eq[r] = bytes.Equal(base[r*rowBytes:(r+1)*rowBytes], target[r*rowBytes:(r+1)*rowBytes])
	}
	// raw runs
	var kinds []bool
	var lens []int
	for r := 0; r < rows; r++ {
		if len(kinds) > 0 && kinds[len(kinds)-1] == eq[r] {
			lens[len(lens)-1]++
		} else {
			kinds = append(kinds, eq[r])
			lens = append(lens, 1)
		}
	}
	// fold short reuse runs into the delta, then merge neighbours
	for i := range kinds {
		if kinds[i] && lens[i] < minReuse {
			kinds[i] = false
		}
	}
	runs := []int{0}
	cur := true // currently in a reuse run
	for i := range kinds {
		if kinds[i] != cur {
			runs = append(runs, 0)
			cur = kinds[i]
		}
		runs[len(runs)-1] += lens[i]
	}
	return runs
}

// hasReuse reports whether a run list contains any reuse run (runs[0] may be empty).
func hasReuse(runs []int) bool { return runs[0] > 0 || len(runs) > 2 }

func appendRows(runs []int) []byte {
	b := binary.AppendUvarint(nil, uint64(len(runs)))
	for _, r := range runs {
		b = binary.AppendUvarint(b, uint64(r))
	}
	return b
}

// EncodeRows builds the Rows payload: recipe, then PXR of the changed rows
// against the matching base rows.
func EncodeRows(base, target []byte, rowBytes, w, minReuse int) ([]byte, RowStats, error) {
	if rowBytes <= 0 || len(target)%rowBytes != 0 || len(base) != len(target) {
		return nil, RowStats{}, errors.New("tdelta: rows: bad geometry")
	}
	runs := rowRuns(base, target, rowBytes, minReuse)
	rs := RowStats{Rows: len(target) / rowBytes, Runs: len(runs)}
	var cb, ct []byte
	r := 0
	for i, n := range runs {
		if i%2 == 1 {
			cb = append(cb, base[r*rowBytes:(r+n)*rowBytes]...)
			ct = append(ct, target[r*rowBytes:(r+n)*rowBytes]...)
			rs.DeltaRows += n
		} else {
			rs.ReuseRows += n
		}
		r += n
	}
	out := appendRows(runs)
	rs.MetaBytes = len(out)
	if len(ct) > 0 {
		p, err := EncodePXR(cb, ct, w)
		if err != nil {
			return nil, rs, err
		}
		rs.DeltaBytes = len(p)
		out = append(out, p...)
	}
	return out, rs, nil
}

func uv(b []byte) (uint64, []byte, bool) {
	v, k := binary.Uvarint(b)
	if k <= 0 {
		return 0, nil, false
	}
	return v, b[k:], true
}

// DecodeRows rebuilds n bytes from base and a Rows payload.
func DecodeRows(base, payload []byte, n, rowBytes, w int) ([]byte, error) {
	if rowBytes <= 0 || n%rowBytes != 0 || len(base) != n {
		return nil, ErrCorrupt
	}
	rows := n / rowBytes
	nr, rest, ok := uv(payload)
	if !ok || nr == 0 || nr > uint64(rows)+1 {
		return nil, ErrCorrupt
	}
	runs := make([]int, nr)
	total := 0
	for i := range runs {
		var v uint64
		if v, rest, ok = uv(rest); !ok || v > uint64(rows-total) || (v == 0 && i > 0) {
			return nil, ErrCorrupt
		}
		runs[i] = int(v)
		total += runs[i]
	}
	if total != rows {
		return nil, ErrCorrupt
	}
	var cb []byte
	r := 0
	for i, k := range runs {
		if i%2 == 1 {
			cb = append(cb, base[r*rowBytes:(r+k)*rowBytes]...)
		}
		r += k
	}
	var ct []byte
	if len(cb) > 0 {
		var err error
		if ct, err = DecodePXR(cb, rest, len(cb), w); err != nil {
			return nil, err
		}
	} else if len(rest) != 0 {
		return nil, ErrCorrupt
	}
	out := make([]byte, n)
	r, c := 0, 0
	for i, k := range runs {
		if i%2 == 1 {
			copy(out[r*rowBytes:], ct[c:c+k*rowBytes])
			c += k * rowBytes
		} else {
			copy(out[r*rowBytes:], base[r*rowBytes:(r+k)*rowBytes])
		}
		r += k
	}
	return out, nil
}

// ----- tensor wire form -----

func wire(m Mode, payload []byte) []byte {
	switch m {
	case ModeRaw:
		return append([]byte{byte(m)}, payload...)
	case ModeSame:
		return []byte{byte(m)}
	}
	return append(binary.AppendUvarint([]byte{byte(m)}, uint64(len(payload))), payload...)
}

// WireLen is the size of a record carrying payload bytes in mode m.
func WireLen(m Mode, payload int) int {
	switch m {
	case ModeRaw:
		return 1 + payload
	case ModeSame:
		return 1
	}
	return 1 + len(binary.AppendUvarint(nil, uint64(payload))) + payload
}

// ParseWire splits one tensor record off buf; n is the tensor's byte length.
func ParseWire(buf []byte, n int) (m Mode, payload, rest []byte, err error) {
	if len(buf) == 0 {
		return 0, nil, nil, ErrCorrupt
	}
	m = Mode(buf[0])
	buf = buf[1:]
	switch m {
	case ModeSame:
		return m, nil, buf, nil
	case ModeRaw:
		if len(buf) < n {
			return 0, nil, nil, ErrCorrupt
		}
		return m, buf[:n], buf[n:], nil
	case ModeStandalone, ModeDelta, ModeRows:
		l, r, ok := uv(buf)
		if !ok || l > uint64(len(r)) {
			return 0, nil, nil, ErrCorrupt
		}
		return m, r[:l], r[l:], nil
	}
	return 0, nil, nil, ErrCorrupt
}

// EncodeTensor tries the admissible candidates and returns the cheapest wire
// record. base may be nil (no usable base).
func EncodeTensor(sp Spec, base, target []byte, opt Options) ([]byte, []Cand, error) {
	n := len(target)
	w := sp.W
	var cands []Cand
	best := wire(ModeRaw, target)
	bestMode := ModeRaw
	cands = append(cands, Cand{Mode: ModeRaw, Wire: len(best), Pay: n, OK: true})
	try := func(m Mode, payload []byte, c Cand) {
		c.Mode, c.Wire, c.Pay, c.OK = m, WireLen(m, len(payload)), len(payload), true
		cands = append(cands, c)
		if c.Wire < len(best) && (opt.MaxFrac <= 0 || float64(c.Wire) <= opt.MaxFrac*float64(n+1)) {
			best, bestMode = wire(m, payload), m
		}
	}
	if p, err := EncodePXR(nil, target, w); err == nil {
		c := Cand{}
		if opt.Compare {
			c.Flate = len(must(Encode(ReplacePlane, nil, target, w)))
		}
		try(ModeStandalone, p, c)
	} else {
		return nil, nil, err
	}
	if base != nil && !opt.NoDelta && len(base) == n {
		if bytes.Equal(base, target) {
			try(ModeSame, nil, Cand{})
		} else {
			p, err := EncodePXR(base, target, w)
			if err != nil {
				return nil, nil, err
			}
			c := Cand{}
			if opt.Compare {
				c.Flate = len(must(Encode(Plane, base, target, w)))
			}
			try(ModeDelta, p, c)
			// rows can only win when whole rows repeat, which the cheap equality scan shows first
			if rb := sp.RowBytes(n); rb > 0 && !opt.NoRows && hasReuse(rowRuns(base, target, rb, opt.MinReuse)) {
				rp, rs, err := EncodeRows(base, target, rb, w, opt.MinReuse)
				if err != nil {
					return nil, nil, err
				}
				try(ModeRows, rp, Cand{Meta: rs.MetaBytes, Rows: rs})
			}
		}
	}
	_ = bestMode
	return best, cands, nil
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// DecodeTensor rebuilds the n target bytes from a parsed record.
func DecodeTensor(sp Spec, base []byte, m Mode, payload []byte, n int) ([]byte, error) {
	w := sp.W
	switch m {
	case ModeRaw:
		if len(payload) != n {
			return nil, ErrCorrupt
		}
		return append([]byte(nil), payload...), nil
	case ModeStandalone:
		return DecodePXR(nil, payload, n, w)
	case ModeSame:
		if base == nil || len(base) != n {
			return nil, ErrCorrupt
		}
		return append([]byte(nil), base...), nil
	case ModeDelta:
		if base == nil {
			return nil, ErrCorrupt
		}
		return DecodePXR(base, payload, n, w)
	case ModeRows:
		if base == nil {
			return nil, ErrCorrupt
		}
		rb := sp.RowBytes(n)
		return DecodeRows(base, payload, n, rb, w)
	}
	return nil, ErrCorrupt
}
