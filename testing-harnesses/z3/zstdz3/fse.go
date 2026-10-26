package zstdz3

import (
	"math/bits"
	"sort"
)

// A Zstandard FSE (tANS) table. The decoding table is built from the normalized
// distribution exactly as the format specifies; the encoding side is its inverse
// (find, for a symbol and the following state, the unique state whose baseline range
// contains it), so replay needs no separate encoder construction.

type fseDec struct {
	sym  uint8
	nb   uint8
	base uint16
}

type fseTable struct {
	kind   uint8  // 0 predefined, 1 RLE, 2 FSE-compressed: how the stream introduced it
	desc   []byte // RLE: the symbol; FSE: the verbatim description (for checkpoints)
	acc    uint8
	counts []int16 // normalized distribution; -1 is "less than one"
	dec    []fseDec
	// Encoding index, built on first use: for each symbol the states carrying it, once
	// ascending by state number (rank order) and once ascending by baseline.
	symOff  []int32
	symList []uint16
	byBase  []uint16
}

const (
	maxAccLL = 9
	maxAccML = 9
	maxAccOF = 8
	maxSymLL = 35
	maxSymML = 52
	maxSymOF = 31
)

// Predefined distributions (RFC 8878 section 3.1.1.3.2.2).
var (
	predLL = []int16{4, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 2, 1, 1, 1, 1, 1, -1, -1, -1, -1}
	predML = []int16{1, 4, 3, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1, -1, -1}
	predOF = []int16{1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1}
)

var predTables [3]*fseTable // LL, OF, ML

func init() {
	for i, d := range []struct {
		c   []int16
		acc int
	}{{predLL, 6}, {predOF, 5}, {predML, 6}} {
		t, err := buildFSE(d.c, d.acc)
		if err != nil {
			panic("zstdz3: predefined table: " + err.Error())
		}
		predTables[i] = t
	}
}

func rleTable(sym uint8) *fseTable {
	c := make([]int16, int(sym)+1)
	c[sym] = 1
	return &fseTable{kind: 1, desc: []byte{sym}, acc: 0, counts: c, dec: []fseDec{{sym: sym}}}
}

// buildFSE constructs the decoding table; counts must sum (counting -1 as 1) to 1<<acc.
func buildFSE(counts []int16, acc int) (*fseTable, error) {
	size := 1 << uint(acc)
	if acc > 15 || len(counts) > 256 {
		return nil, corrupt("fse table too large")
	}
	var sum int
	for _, c := range counts {
		switch {
		case c == -1:
			sum++
		case c < 0:
			return nil, corrupt("fse count %d", c)
		default:
			sum += int(c)
		}
	}
	if sum != size {
		return nil, corrupt("fse distribution sums to %d, want %d", sum, size)
	}
	t := &fseTable{acc: uint8(acc), counts: counts, dec: make([]fseDec, size)}
	next := make([]uint32, len(counts))
	high := size - 1
	for s, c := range counts {
		if c == -1 {
			t.dec[high].sym = uint8(s)
			high--
			next[s] = 1
		} else {
			next[s] = uint32(c)
		}
	}
	step := size>>1 + size>>3 + 3
	mask := size - 1
	pos := 0
	for s, c := range counts {
		for i := 0; i < int(c); i++ {
			t.dec[pos].sym = uint8(s)
			pos = (pos + step) & mask
			for pos > high {
				pos = (pos + step) & mask
			}
		}
	}
	if pos != 0 {
		return nil, corrupt("fse spread did not close")
	}
	for u := range t.dec {
		s := t.dec[u].sym
		n := next[s]
		next[s]++
		nb := acc - (bits.Len32(n) - 1)
		t.dec[u].nb = uint8(nb)
		t.dec[u].base = uint16(int(n)<<uint(nb) - size)
	}
	return t, nil
}

// readFSEDesc parses a normalized-count description (RFC 8878 section 4.1.1) at the start
// of b and returns the table and the bytes the description occupies.
func readFSEDesc(b []byte, maxAcc, maxSym int) (*fseTable, int, error) {
	r := fwdReader{b: b}
	v, ok := r.read(4)
	if !ok {
		return nil, 0, corrupt("truncated fse description")
	}
	acc := int(v) + 5
	if acc > maxAcc {
		return nil, 0, corrupt("fse accuracy log %d > %d", acc, maxAcc)
	}
	remaining := 1<<uint(acc) + 1
	threshold := 1 << uint(acc)
	nbBits := acc + 1
	var counts []int16
	prev0 := false
	for remaining > 1 && len(counts) <= maxSym {
		if prev0 {
			for {
				f, ok := r.read(2)
				if !ok {
					return nil, 0, corrupt("truncated fse zero run")
				}
				for k := 0; k < int(f); k++ {
					counts = append(counts, 0)
				}
				if f != 3 {
					break
				}
				if len(counts) > maxSym+1 {
					return nil, 0, corrupt("fse zero run past the last symbol")
				}
			}
			if len(counts) > maxSym+1 {
				return nil, 0, corrupt("fse zero run past the last symbol")
			}
			prev0 = false
			if len(counts) > maxSym {
				break
			}
		}
		max := 2*threshold - 1 - remaining
		low, ok := r.read(nbBits - 1)
		if !ok {
			return nil, 0, corrupt("truncated fse count")
		}
		var count int
		if int(low) < max {
			count = int(low)
		} else {
			hi, ok := r.read(1)
			if !ok {
				return nil, 0, corrupt("truncated fse count")
			}
			count = int(low) | int(hi)<<uint(nbBits-1)
			if count >= threshold {
				count -= max
			}
		}
		count--
		if count < 0 {
			remaining--
		} else {
			remaining -= count
		}
		if remaining < 0 {
			return nil, 0, corrupt("fse distribution overshoots")
		}
		counts = append(counts, int16(count))
		prev0 = count == 0
		for remaining < threshold && threshold > 1 {
			nbBits--
			threshold >>= 1
		}
	}
	if remaining != 1 {
		return nil, 0, corrupt("fse distribution does not close")
	}
	if len(counts) > maxSym+1 {
		return nil, 0, corrupt("fse symbol %d > %d", len(counts)-1, maxSym)
	}
	t, err := buildFSE(counts, acc)
	if err != nil {
		return nil, 0, err
	}
	return t, r.bytes(), nil
}

// initEnc builds the per-symbol state lists used by replay.
func (t *fseTable) initEnc() {
	if t.symList != nil {
		return
	}
	n := len(t.counts)
	t.symOff = make([]int32, n+1)
	for _, d := range t.dec {
		t.symOff[int(d.sym)+1]++
	}
	for i := 0; i < n; i++ {
		t.symOff[i+1] += t.symOff[i]
	}
	t.symList = make([]uint16, len(t.dec))
	fill := make([]int32, n)
	for u, d := range t.dec {
		t.symList[t.symOff[d.sym]+fill[d.sym]] = uint16(u)
		fill[d.sym]++
	}
	t.byBase = append([]uint16(nil), t.symList...)
	for s := 0; s < n; s++ {
		seg := t.byBase[t.symOff[s]:t.symOff[s+1]]
		sort.Slice(seg, func(i, j int) bool { return t.dec[seg[i]].base < t.dec[seg[j]].base })
	}
}

// rank returns the position of state u among the states that carry its symbol.
func (t *fseTable) rank(u int) int {
	t.initEnc()
	s := t.dec[u].sym
	seg := t.symList[t.symOff[s]:t.symOff[s+1]]
	return sort.Search(len(seg), func(i int) bool { return int(seg[i]) >= u })
}

// stateAt is the inverse of rank; ok is false for an impossible rank or symbol.
func (t *fseTable) stateAt(sym int, rank uint32) (int, bool) {
	t.initEnc()
	if sym >= len(t.counts) {
		return 0, false
	}
	seg := t.symList[t.symOff[sym]:t.symOff[sym+1]]
	if uint64(rank) >= uint64(len(seg)) {
		return 0, false
	}
	return int(seg[rank]), true
}

// prev finds the state that carries sym and whose baseline range contains next; the
// bits the decoder must read to get from it to next are next-base (nb bits).
func (t *fseTable) prev(sym int, next int) (state int, bits uint32, nb int, ok bool) {
	t.initEnc()
	if sym >= len(t.counts) {
		return 0, 0, 0, false
	}
	seg := t.byBase[t.symOff[sym]:t.symOff[sym+1]]
	i := sort.Search(len(seg), func(i int) bool { return int(t.dec[seg[i]].base) > next }) - 1
	if i < 0 {
		return 0, 0, 0, false
	}
	d := t.dec[seg[i]]
	if next-int(d.base) >= 1<<d.nb {
		return 0, 0, 0, false
	}
	return int(seg[i]), uint32(next - int(d.base)), int(d.nb), true
}
