package simdelta

import (
	"encoding/binary"
	"io"
	"sort"
)

type Candidate struct {
	ID   uint32
	Hits int
}

// Index bounds hot postings to the newest 64 IDs. This intentionally trades
// recall for bounded query work, independently of corpus size.
type Index struct {
	Buckets map[uint64][]uint32
	Dead    map[uint32]bool
}

func NewIndex() *Index { return &Index{map[uint64][]uint32{}, map[uint32]bool{}} }
func (x *Index) Add(id uint32, f []uint64) {
	for _, h := range f {
		p := x.Buckets[h]
		if len(p) > 0 && p[len(p)-1] == id {
			continue
		}
		if len(p) == 64 {
			copy(p, p[1:])
			p = p[:63]
		}
		x.Buckets[h] = append(p, id)
	}
}
func (x *Index) Delete(id uint32) { x.Dead[id] = true }
func (x *Index) Query(f []uint64, k int) []Candidate {
	if k <= 0 || k > 64 || len(f) > 64 {
		return nil
	}
	scores := map[uint32]int{}
	seen := map[uint64]bool{}
	for _, h := range f {
		if seen[h] {
			continue
		}
		seen[h] = true
		for _, id := range x.Buckets[h] {
			if !x.Dead[id] {
				scores[id]++
			}
		}
	}
	out := make([]Candidate, 0, len(scores))
	for id, n := range scores {
		out = append(out, Candidate{id, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}
func (x *Index) Rebuild() {
	for h, p := range x.Buckets {
		q := p[:0]
		for _, id := range p {
			if !x.Dead[id] {
				q = append(q, id)
			}
		}
		if len(q) == 0 {
			delete(x.Buckets, h)
		} else {
			x.Buckets[h] = q
		}
	}
	x.Dead = map[uint32]bool{}
}

// Posting is 16 bytes resident, 12 bytes serialized; no payload is required.
type Posting struct {
	Feature uint64
	ID      uint32
}

func Sort(p []Posting) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Feature != p[j].Feature {
			return p[i].Feature < p[j].Feature
		}
		return p[i].ID < p[j].ID
	})
}
func WriteTable(w io.Writer, p []Posting) error {
	var b [12]byte
	for _, v := range p {
		binary.LittleEndian.PutUint64(b[:], v.Feature)
		binary.LittleEndian.PutUint32(b[8:], v.ID)
		n, e := w.Write(b[:])
		if e != nil {
			return e
		}
		if n != 12 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// DiskLookup uses bounded ReaderAt binary search. Immutable levels can be
// partitioned by feature prefix. Production needs authenticated level metadata.
func DiskLookup(r io.ReaderAt, count int64, feature uint64) ([]uint32, error) {
	if count < 0 || count > 1<<40 {
		return nil, bad
	}
	var b [12]byte
	at := func(i int64) (Posting, error) {
		_, e := r.ReadAt(b[:], i*12)
		return Posting{binary.LittleEndian.Uint64(b[:]), binary.LittleEndian.Uint32(b[8:])}, e
	}
	lo, hi := int64(0), count
	for lo < hi {
		m := lo + (hi-lo)/2
		v, e := at(m)
		if e != nil {
			return nil, e
		}
		if v.Feature <= feature {
			lo = m + 1
		} else {
			hi = m
		}
	}
	var out []uint32
	for i := lo - 1; i >= 0 && len(out) < 64; i-- {
		v, e := at(i)
		if e != nil {
			return nil, e
		}
		if v.Feature != feature {
			break
		}
		out = append(out, v.ID)
	}
	return out, nil
}
