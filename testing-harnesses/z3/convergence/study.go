// Package convergence measures physical sharing without defining a persistent
// format. Span and pack records below are transient accounting/proof structures.
package convergence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"index/suffixarray"
	"sort"
	"zeros3-z3/admission"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

type Span struct {
	SHA            repz3.SHA
	Offset, Length int
}
type chunk struct {
	id         repz3.SHA
	start, end int
}
type Row struct {
	Case, Stage, Class, Variant                                                   string
	Components, ExactComponents, SpanComponents                                   int
	Raw, ExactRaw, SpanRaw                                                        int64
	CDC, Additive, Internal64K, Internal16K, InternalAll, Slices, Packs           int64
	Descriptor64K, Descriptor16K, DescriptorAll, SpanDescriptors, PackIndex       int64
	SpanBackingRead, SpanRequested, PackBackingRead, PackRequested, PackWorstDead int64
	SpanCount                                                                     int
}
type state struct {
	spanSeen    map[repz3.SHA]bool
	costs       [7]map[repz3.SHA]bool
	descriptors [4]map[repz3.SHA]bool
	totals      [7]int64
	desc        [4]int64
	seen        map[repz3.SHA]bool
}
type Study struct {
	states  map[string]*state
	current string
	data    []byte
	chunks  []chunk
	backing map[repz3.SHA][]byte
	index   *suffixarray.Index
	Rows    []Row
}

func New() *Study    { return &Study{states: map[string]*state{}} }
func uv(n int) int64 { var b [10]byte; return int64(binary.PutUvarint(b[:], uint64(n))) }
func descriptor(size int, refs []Span, slice bool) int64 {
	n := int64(32) + uv(size) + uv(len(refs))
	for _, r := range refs {
		n += 32 + uv(r.Length)
		if slice {
			n += uv(r.Offset)
		}
	}
	return n
}
func chunksOf(data []byte) ([]chunk, map[repz3.SHA][]byte, error) {
	var cs []chunk
	bs := map[repz3.SHA][]byte{}
	err := lab.ChunkCDC(bytes.NewReader(data), func(off int64, b []byte) error {
		id := sha256.Sum256(b)
		cs = append(cs, chunk{id, int(off), int(off) + len(b)})
		bs[id] = append([]byte(nil), b...)
		return nil
	})
	return cs, bs, err
}

// Restore validates backing hashes, bounds and the complete logical digest.
// A valid backing chunk alone does not authenticate a logical span mapping.
func Restore(id repz3.SHA, size int, spans []Span, backing map[repz3.SHA][]byte) ([]byte, error) {
	if size <= 0 || size > 128<<20 || len(spans) == 0 || len(spans) > 65536 {
		return nil, fmt.Errorf("span bounds")
	}
	out := make([]byte, 0, size)
	for _, s := range spans {
		b, ok := backing[s.SHA]
		if !ok || sha256.Sum256(b) != s.SHA || s.Offset < 0 || s.Length <= 0 || s.Offset > len(b) || s.Length > len(b)-s.Offset || s.Length > size-len(out) {
			return nil, fmt.Errorf("span backing/bounds")
		}
		out = append(out, b[s.Offset:s.Offset+s.Length]...)
	}
	if len(out) != size || sha256.Sum256(out) != id {
		return nil, fmt.Errorf("logical hash")
	}
	return out, nil
}
func (s *Study) spans(raw []byte) []Span {
	if len(raw) == 0 {
		return nil
	}
	hits := s.index.Lookup(raw, 1)
	if len(hits) == 0 {
		return nil
	}
	pos, end := hits[0], hits[0]+len(raw)
	i := sort.Search(len(s.chunks), func(i int) bool { return s.chunks[i].end > pos })
	var out []Span
	for ; i < len(s.chunks) && pos < end; i++ {
		c := s.chunks[i]
		n := min(end, c.end) - pos
		out = append(out, Span{c.id, pos - c.start, n})
		pos += n
	}
	return out
}
func add(st *state, mechanism int, id repz3.SHA, b []byte) {
	if !st.costs[mechanism][id] {
		st.costs[mechanism][id] = true
		st.totals[mechanism] += lab.PackedSize(b) + 64
	}
}
func newState() *state {
	st := &state{seen: map[repz3.SHA]bool{}, spanSeen: map[repz3.SHA]bool{}}
	for i := range st.costs {
		st.costs[i] = map[repz3.SHA]bool{}
	}
	for i := range st.descriptors {
		st.descriptors[i] = map[repz3.SHA]bool{}
	}
	return st
}
func (s *Study) Observe(o admission.Observation) error {
	selected := map[string]string{"sqlite": "sqlite", "parquet": "parquet-8192", "rocksdb": "ukv", "pebble": "ukv", "fdroid-apk": "indexed-4096", "clr": "indexed-4096", "model": "model-delta", "git": "gp-zlib"}
	if selected[o.Class] != o.Variant {
		return nil
	}
	if len(o.Data) > 128<<20 {
		return fmt.Errorf("input bound")
	}
	key := o.Case + "/" + o.Stage
	if s.current != key {
		var err error
		s.chunks, s.backing, err = chunksOf(o.Data)
		if err != nil {
			return err
		}
		s.data = o.Data
		s.index = suffixarray.New(o.Data)
		s.current = key
	}
	st := s.states[o.Case]
	if st == nil {
		st = newState()
		s.states[o.Case] = st
	}
	for _, c := range s.chunks {
		for i := range st.costs {
			add(st, i, c.id, s.backing[c.id])
		}
	}
	row := Row{Case: o.Case, Stage: o.Stage, Class: o.Class, Variant: o.Variant}
	var fresh [][]byte
	for _, id := range o.Reach.Order {
		raw := o.Store.Bytes(id)
		if sha256.Sum256(raw) != id {
			return fmt.Errorf("component hash")
		}
		row.Components++
		row.Raw += int64(len(raw))
		if _, ok := s.backing[id]; ok {
			row.ExactComponents++
			row.ExactRaw += int64(len(raw))
		}
		// Production baseline virtualizes components only above 256 KiB. The root
		// descriptor itself is charged separately, with logical SHA and each ref.
		cs, bs, e := chunksOf(raw)
		if e != nil {
			return e
		}
		refs := make([]Span, len(cs))
		for i, c := range cs {
			refs[i] = Span{c.id, 0, c.end - c.start}
		}
		for m, threshold := range []int{256 << 10, 64 << 10, 16 << 10, 0} {
			slot := m + 1
			if len(raw) > threshold && len(refs) > 1 {
				for _, r := range refs {
					add(st, slot, r.SHA, bs[r.SHA])
				}
				if !st.descriptors[m][id] {
					st.descriptors[m][id] = true
					d := descriptor(len(raw), refs, false)
					st.desc[m] += d
					st.totals[slot] += d
				}
			} else {
				add(st, slot, id, raw)
			}
		}
		spans := s.spans(raw)
		if len(spans) > 0 {
			rebuilt, e := Restore(id, len(raw), spans, s.backing)
			if e != nil || !bytes.Equal(rebuilt, raw) {
				return fmt.Errorf("span proof: %v", e)
			}
			row.SpanComponents++
			row.SpanRaw += int64(len(raw))
			row.SpanCount += len(spans)
			if !st.costs[5][id] && !st.spanSeen[id] {
				st.spanSeen[id] = true
				d := descriptor(len(raw), spans, true)
				st.totals[5] += d
				row.SpanDescriptors += d
			}
			// First 4KiB of every component, cold verified chunk reads; whole logical
			// verification is additional work explicitly not hidden in this metric.
			requested := min(4096, len(raw))
			row.SpanRequested += int64(requested)
			left := requested
			for _, r := range spans {
				if left <= 0 {
					break
				}
				row.SpanBackingRead += int64(len(s.backing[r.SHA]))
				left -= min(left, r.Length)
			}
		} else {
			add(st, 5, id, raw)
		}
		if !st.seen[id] {
			st.seen[id] = true
			if !st.costs[6][id] {
				fresh = append(fresh, raw)
			}
		}
	}
	// D: first-seen logical components, 64KiB soft-cap physical compression
	// groups. Large individual objects stay intact. Lookup adds SHA+location
	// (48B) per object; pack SHA and 64B overhead are fully charged.
	var pack []byte
	var members [][]byte
	flush := func() error {
		if len(members) == 0 {
			return nil
		}
		id := sha256.Sum256(pack)
		add(st, 6, id, pack)
		st.totals[6] += int64(len(members)) * 48
		row.PackIndex += int64(len(members)) * 48
		smallest := len(pack)
		pos := 0
		for _, b := range members {
			end := pos + len(b)
			rebuilt, e := Restore(sha256.Sum256(b), len(b), []Span{{id, pos, len(b)}}, map[repz3.SHA][]byte{id: pack})
			if e != nil || !bytes.Equal(rebuilt, b) {
				return fmt.Errorf("pack proof: %v", e)
			}
			smallest = min(smallest, len(b))
			row.PackRequested += int64(min(4096, len(b)))
			row.PackBackingRead += int64(len(pack))
			pos = end
		}
		row.PackWorstDead += int64(len(pack) - smallest)
		pack = nil
		members = nil
		return nil
	}
	for _, raw := range fresh {
		if len(pack) > 0 && len(pack)+len(raw) > 64<<10 {
			if e := flush(); e != nil {
				return e
			}
		}
		pack = append(pack, raw...)
		members = append(members, raw)
	}
	if e := flush(); e != nil {
		return e
	}
	row.CDC = st.totals[0]
	row.Additive = st.totals[1]
	row.Internal64K = st.totals[2]
	row.Internal16K = st.totals[3]
	row.InternalAll = st.totals[4]
	row.Slices = st.totals[5]
	row.Packs = st.totals[6]
	row.Descriptor64K = st.desc[1]
	row.Descriptor16K = st.desc[2]
	row.DescriptorAll = st.desc[3]
	s.Rows = append(s.Rows, row)
	return nil
}
