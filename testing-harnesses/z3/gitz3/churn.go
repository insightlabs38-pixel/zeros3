package gitz3

import (
	"bytes"
	"sort"
)

// Churn attributes the bytes of pack B to what changed relative to pack A for
// the same objects. Every entry of B falls in exactly one class.
type Churn struct {
	EntriesB, EntriesA int
	BytesB             int64 // sum of B entry bytes
	Shared, OnlyB      int   // objects present in both / only in B
	SharedBytesRes     int64 // resolved bytes of shared objects
	TotalBytesRes      int64 // resolved bytes of all B objects
	// classes: entry bytes of B
	New         int64 // object not in A
	Same        int64 // entry bytes identical to A's entry of the same object
	OFSShift    int64 // same zlib bytes and base object, only the OFS distance differs
	ZlibOnly    int64 // same plaintext (and base), different compressed bytes
	DeltaChange int64 // different base and/or different delta program / full<->delta
	// counts per class
	NNew, NSame, NOFS, NZlib, NDelta int
	// ordering: bytes of Same+OFSShift entries whose predecessor in B is not
	// their predecessor in A (adjacency lost), and the length-weighted longest
	// preserved run
	AdjBroken      int64
	DeltaBoth      int // objects that are delta entries in both
	DeltaSameBase  int
	DeltaSameProg  int // same base and same delta program bytes
	DeltaProgBytes int64
}

// Attribute compares two resolved packs of (partly) the same objects.
func Attribute(a, b *Pack) (Churn, error) {
	var c Churn
	c.EntriesA, c.EntriesB = len(a.Entries), len(b.Entries)
	aIdx := map[string]int{}
	for i := range a.Entries {
		aIdx[string(a.Entries[i].ID)] = i
	}
	prevA := -2
	for i := range b.Entries {
		eb := &b.Entries[i]
		sz := eb.End - eb.Off
		c.BytesB += sz
		c.TotalBytesRes += eb.RSize
		j, ok := aIdx[string(eb.ID)]
		if !ok {
			c.New += sz
			c.NNew++
			c.OnlyB++
			prevA = -2
			continue
		}
		c.Shared++
		c.SharedBytesRes += eb.RSize
		ea := &a.Entries[j]
		adj := j == prevA+1
		prevA = j
		sameBase := false
		switch {
		case !ea.IsDelta() && !eb.IsDelta():
			sameBase = true
		case ea.IsDelta() && eb.IsDelta():
			sameBase = bytes.Equal(a.Entries[ea.Base].ID, b.Entries[eb.Base].ID)
		}
		za, err := a.ZlibBytes(j)
		if err != nil {
			return c, err
		}
		zb, err := b.ZlibBytes(i)
		if err != nil {
			return c, err
		}
		sameZ := bytes.Equal(za, zb)
		ra, err := a.EntryBytes(j)
		if err != nil {
			return c, err
		}
		rb, err := b.EntryBytes(i)
		if err != nil {
			return c, err
		}
		switch {
		case bytes.Equal(ra, rb):
			c.Same += sz
			c.NSame++
			if !adj {
				c.AdjBroken += sz
			}
		case sameBase && sameZ:
			c.OFSShift += sz
			c.NOFS++
			if !adj {
				c.AdjBroken += sz
			}
		case sameBase:
			pa, pb, err := inflatePair(a, j, b, i)
			if err != nil {
				return c, err
			}
			if bytes.Equal(pa, pb) {
				c.ZlibOnly += sz
				c.NZlib++
			} else {
				c.DeltaChange += sz
				c.NDelta++
			}
		default:
			c.DeltaChange += sz
			c.NDelta++
		}
		if ea.IsDelta() && eb.IsDelta() {
			c.DeltaBoth++
			if sameBase {
				c.DeltaSameBase++
				pa, pb, err := inflatePair(a, j, b, i)
				if err != nil {
					return c, err
				}
				if bytes.Equal(pa, pb) {
					c.DeltaSameProg++
					c.DeltaProgBytes += int64(len(pb))
				}
			}
		}
	}
	return c, nil
}

func inflatePair(a *Pack, j int, b *Pack, i int) ([]byte, []byte, error) {
	pa, err := a.Inflate(j)
	if err != nil {
		return nil, nil, err
	}
	pb, err := b.Inflate(i)
	return pa, pb, err
}

// DeltaStats summarises the delta structure of a resolved pack.
type PackShape struct {
	Entries, Full, OFS, REF int
	MaxDepth                int
	DepthHist               map[int]int
	EntryBytes, ZlibBytes   int64
	InflatedBytes           int64
	ResolvedBytes           int64
	HeaderBytes             int64 // type/size/base encodings
	ByType                  map[ObjType]int
	SmallEntries            int // zlib stream < 100 B
}

func Shape(p *Pack) PackShape {
	s := PackShape{DepthHist: map[int]int{}, ByType: map[ObjType]int{}}
	s.Entries = len(p.Entries)
	for i := range p.Entries {
		e := &p.Entries[i]
		switch e.Kind {
		case ofsDelta:
			s.OFS++
		case refDelta:
			s.REF++
		default:
			s.Full++
		}
		s.DepthHist[e.Depth]++
		if e.Depth > s.MaxDepth {
			s.MaxDepth = e.Depth
		}
		s.EntryBytes += e.End - e.Off
		s.ZlibBytes += e.ZEnd - e.ZOff
		s.InflatedBytes += e.Size
		s.ResolvedBytes += e.RSize
		s.HeaderBytes += e.ZOff - e.Off
		s.ByType[e.Type]++
		if e.ZEnd-e.ZOff < 100 {
			s.SmallEntries++
		}
	}
	return s
}

func SortedKeys(m map[int]int) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}
