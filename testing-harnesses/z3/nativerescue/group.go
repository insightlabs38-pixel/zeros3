package nativerescue

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"zeros3-z3/binref"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
	"zeros3-z3/lab"
)

const MaxNative = 128 << 20
const maxParts = 1 << 18

// Options selects independent reference classes and grouping, never correctness.
// Target -1 is production CDC, zero is sections; positive sizes are sensitivity.
type Options struct {
	Identity                     string
	Target                       int
	Selection                    string
	Functions, Compact, Separate bool
}
type Statistics struct {
	Identity                string
	Native                  binref.Stats
	Parts, RecipeBytes      int
	Payload, ReferenceBytes int64
}
type record struct {
	off, width int
	kb         byte
	id         uint32
	tr, add    uint64
}
type reader struct {
	b   []byte
	err error
}

func (r *reader) uv() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = fmt.Errorf("native: malformed varint")
		return 0
	}
	r.b = r.b[n:]
	return v
}
func (r *reader) take(n int) []byte {
	if n < 0 || n > len(r.b) {
		r.err = fmt.Errorf("native: short record")
		return nil
	}
	p := r.b[:n]
	r.b = r.b[n:]
	return p
}
func uv(b []byte, v int) []byte { return binary.AppendUvarint(b, uint64(v)) }
func parseRecords(b []byte, size int, compact bool, pi *idxref.PrefixIndex, ids []uint32) ([]record, error) {
	r := &reader{b: b}
	n := r.uv()
	if r.err != nil || n > uint64(size/4) {
		return nil, fmt.Errorf("native: reference count")
	}
	out := make([]record, 0, n)
	at := 0
	for i := uint64(0); i < n; i++ {
		delta := r.uv()
		if delta > uint64(size-at) {
			return nil, fmt.Errorf("native: reference offset")
		}
		at += int(delta)
		raw := r.take(1)
		if r.err != nil {
			return nil, r.err
		}
		v := record{off: at, kb: raw[0], width: 4}
		enc := v.kb & 7
		if enc < 1 || enc > 4 || v.kb&0x40 != 0 {
			return nil, fmt.Errorf("native: encoding")
		}
		if enc == 2 {
			v.width = 8
		}
		if v.width > size-at {
			return nil, fmt.Errorf("native: crossing reference")
		}
		if v.kb&0x80 != 0 {
			v.tr = r.uv()
		} else if compact {
			if pi == nil {
				return nil, fmt.Errorf("native: missing identity table")
			}
			index, rest, e := pi.Get(r.b)
			if e != nil {
				return nil, e
			}
			if int(index) >= len(ids) {
				return nil, fmt.Errorf("native: identity")
			}
			v.id = ids[index]
			r.b = rest
		} else {
			p := r.take(4)
			if r.err != nil {
				return nil, r.err
			}
			v.id = binary.LittleEndian.Uint32(p)
		}
		v.add = r.uv()
		if r.err != nil {
			return nil, r.err
		}
		out = append(out, v)
		at += v.width
	}
	if len(r.b) != 0 {
		return nil, fmt.Errorf("native: trailing references")
	}
	return out, nil
}
func putRecords(rs []record, start int, compact bool, pi *idxref.PrefixIndex, byID map[uint32]uint32) ([]byte, error) {
	b := uv(nil, len(rs))
	at := start
	for _, r := range rs {
		if r.off < at {
			return nil, fmt.Errorf("native: unordered references")
		}
		b = uv(b, r.off-at)
		b = append(b, r.kb)
		if r.kb&0x80 != 0 {
			b = binary.AppendUvarint(b, r.tr)
		} else if compact {
			i, ok := byID[r.id]
			if !ok {
				return nil, fmt.Errorf("native: unknown unit")
			}
			var e error
			b, e = pi.Put(b, i)
			if e != nil {
				return nil, e
			}
		} else {
			b = binary.LittleEndian.AppendUint32(b, r.id)
		}
		b = binary.AppendUvarint(b, r.add)
		at = r.off + r.width
	}
	return b, nil
}
func unitIndex(data []byte) ([]uint32, *idxref.PrefixIndex, map[uint32]uint32, error) {
	r := &reader{b: data}
	n := r.uv()
	if n > 1<<20 || n > uint64(len(r.b)/5) {
		return nil, nil, nil, fmt.Errorf("native: unit count")
	}
	ids := make([]uint32, 0, n)
	hashes := make([]idxref.Identity, 0, n)
	byID := map[uint32]uint32{}
	var va uint64
	for i := uint64(0); i < n; i++ {
		p := r.take(4)
		if r.err != nil {
			return nil, nil, nil, r.err
		}
		id := binary.LittleEndian.Uint32(p)
		d := r.uv()
		if ^uint64(0)-va < d {
			return nil, nil, nil, fmt.Errorf("native: VA overflow")
		}
		va += d
		if _, ok := byID[id]; ok {
			return nil, nil, nil, fmt.Errorf("native: duplicate unit identity")
		}
		byID[id] = uint32(len(ids))
		ids = append(ids, id)
		hashes = append(hashes, sha256.Sum256(p))
	}
	if r.err != nil || len(r.b) != 0 {
		return nil, nil, nil, fmt.Errorf("native: unit framing")
	}
	return ids, idxref.NewPrefixIndex(hashes, 2), byID, nil
}
func selectSet(name string) (slotSet, error) {
	switch name {
	case "reloc":
		return setReloc, nil
	case "struct":
		return setStruct, nil
	case "x86":
		return setX86, nil
	case "full", "":
		return setFull, nil
	case "branch":
		return slotSet{Name: name, Branch: true}, nil
	case "rip":
		return slotSet{Name: name, RIP: true}, nil
	case "ptr":
		return slotSet{Name: name, Ptr: true}, nil
	case "table":
		return slotSet{Name: name, Table: true}, nil
	case "go":
		return slotSet{Name: name, Go: true}, nil
	}
	return slotSet{}, fmt.Errorf("native: unknown reference class %q", name)
}

type descriptor struct {
	tag            byte
	region, off, n int
}

func cutsFor(b []byte, target int, sec *idxref.Sec) []int {
	if len(b) == 0 {
		return nil
	}
	if target == 0 {
		return []int{len(b)}
	}
	if sec != nil && target > 0 {
		return econx.UnitCutBytes(target)(b, []idxref.Sec{*sec})
	}
	var cuts []int
	if target < 0 {
		lab.ChunkCDC(bytes.NewReader(b), func(off int64, c []byte) error { cuts = append(cuts, int(off)+len(c)); return nil })
	} else {
		at := 0
		for _, c := range lab.ChunkScaled(b, target/4, target, target*4) {
			at += len(c)
			cuts = append(cuts, at)
		}
	}
	return cuts
}

// Encode reuses binref normalization, pairs masked payload and local references,
// and codes stable target IDs through idxref's table-implied prefix mechanism.
// Its recipe is research-only and allocates no production kind number.
func Encode(data []byte, o Options) ([]byte, [][]byte, Statistics, error) {
	var st Statistics
	if len(data) > MaxNative || o.Target < -1 || o.Target > 1<<20 || (o.Target > 0 && o.Target < 256) {
		return nil, nil, st, fmt.Errorf("native: size/target bound")
	}
	b, e := loadBin("native", data)
	if e != nil {
		return nil, nil, st, e
	}
	set, e := selectSet(o.Selection)
	if e != nil {
		return nil, nil, st, e
	}
	ident := binref.IdentSymbol
	// Stripped non-Go binaries have no stable symbol names. Reuse binref's
	// masked-content identities instead of silently falling back to address offsets.
	if !o.Separate && len(b.Im.Syms) == 0 && b.Pcln == nil {
		ident = binref.IdentContent
	}
	switch o.Identity {
	case "", "auto":
	case "symbol":
		ident = binref.IdentSymbol
	case "content":
		ident = binref.IdentContent
	default:
		return nil, nil, st, fmt.Errorf("native: identity option")
	}
	st.Identity = ident.String()
	n, _, e := normalize(b, cfg{set, ident, "auto"})
	if e != nil {
		return nil, nil, st, e
	}
	st.Native = n.Stat
	var unitBytes []byte
	payload := map[int][]byte{}
	refs := map[int][]byte{}
	for _, s := range n.Streams {
		if s.Kind == "units" {
			unitBytes = s.Data
		}
		if s.Kind == "payload" {
			payload[s.Region] = s.Data
		}
		if s.Kind == "refs" {
			refs[s.Region] = s.Data
		}
	}
	ids, pi, byID, e := unitIndex(unitBytes)
	if e != nil {
		return nil, nil, st, e
	}
	_ = ids
	var ds []descriptor
	var comps [][]byte
	add := func(tag byte, region, off, n int, c []byte) {
		ds = append(ds, descriptor{tag, region, off, n})
		comps = append(comps, c)
	}
	for _, s := range n.Streams {
		tag := byte(0)
		switch s.Kind {
		case "regions":
			tag = 0
		case "units":
			tag = 1
		case "payload":
			tag = 3
		case "refs":
			tag = 4
		}
		if !o.Separate && (tag == 3 || tag == 4) {
			continue
		}
		at := 0
		for _, end := range cutsFor(s.Data, o.Target, nil) {
			add(tag, s.Region, at, end-at, append([]byte(nil), s.Data[at:end]...))
			at = end
		}
	}
	if !o.Separate {
		units := b.units("auto")
		for ri, r := range b.Im.Regions {
			p := payload[ri]
			var records []record
			if len(refs[ri]) > 0 {
				records, e = parseRecords(refs[ri], len(p), false, nil, nil)
				if e != nil {
					return nil, nil, st, e
				}
			}
			var sec *idxref.Sec
			if o.Functions && r.Exec {
				s := idxref.Sec{Off: 0, End: len(p), Items: []int{0}}
				for _, u := range units {
					if u.VA > r.VA && u.VA < r.VA+uint64(r.Len) {
						s.Items = append(s.Items, int(u.VA-r.VA))
					}
				}
				sort.Ints(s.Items)
				s.Items = unique(s.Items)
				sec = &s
			}
			cuts := cutsFor(p, o.Target, sec)
			start, recordAt := 0, 0
			for _, end := range cuts {
				if end <= start {
					continue
				}
				for j := recordAt; j < len(records) && records[j].off < end; j++ {
					v := records[j]
					if v.off+v.width > end {
						end = v.off + v.width
					}
				}
				end = min(end, len(p))
				next := recordAt
				for next < len(records) && records[next].off < end {
					next++
				}
				rb, e := putRecords(records[recordAt:next], start, o.Compact, pi, byID)
				if e != nil {
					return nil, nil, st, e
				}
				c := idxref.PackComp(p[start:end], [][]byte{rb})
				add(2, ri, start, end-start, c)
				st.Payload += int64(end - start)
				st.ReferenceBytes += int64(len(rb))
				start = end
				recordAt = next
			}
			if start != len(p) || recordAt != len(records) {
				return nil, nil, st, fmt.Errorf("native: incomplete groups")
			}
		}
	}
	if len(ds) > maxParts {
		return nil, nil, st, fmt.Errorf("native: too many components")
	}
	meta := []byte("NBR1")
	meta = uv(meta, len(data))
	var flags byte
	if o.Compact {
		flags = 1
	}
	meta = append(meta, flags)
	meta = uv(meta, len(ds))
	for _, d := range ds {
		meta = append(meta, d.tag)
		meta = uv(meta, d.region)
		meta = uv(meta, d.off)
		meta = uv(meta, d.n)
	}
	st.Parts = len(ds)
	st.RecipeBytes = len(meta)
	return meta, comps, st, nil
}
func unique(xs []int) []int {
	out := xs[:0]
	for _, v := range xs {
		if len(out) == 0 || v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// Plan uses existing physical/index-node economics and a decode callback that
// sees serialized bytes only. No Image, Norm or source closure reaches decode.
func Plan(data []byte, o Options) (*lab.Plan, Statistics, error) {
	m, cs, st, e := Encode(data, o)
	if e != nil {
		return nil, st, e
	}
	if e := Proof(m, cs, data); e != nil {
		return nil, st, e
	}
	p := econx.RepPlan(m, cs, "native-group", Decode)
	p.MetaBytes = 32 // envelope/object overhead beyond serialized recipe
	return p, st, nil
}
