package gitz3

import (
	"bytes"
	"math/rand"
	"sort"

	"zeros3-z3/lab"
)

// Range is [Off, Off+Len) of a file.
type Range struct{ Off, Len int64 }

// LookupRanges returns the byte ranges of the index a lookup of id reads: the
// two fanout words, each binary-search probe's id, and the CRC and offset of
// the hit (CRC is read to verify).
func (x *Idx) LookupRanges(id []byte) []Range {
	var rs []Range
	hs := int64(x.Algo.Size())
	b := int(id[0])
	lo := 0
	if b > 0 {
		lo = int(x.Fanout[b-1])
		rs = append(rs, Range{8 + 4*int64(b-1), 4})
	}
	rs = append(rs, Range{8 + 4*int64(b), 4})
	hi := int(x.Fanout[b])
	for lo < hi {
		m := (lo + hi) / 2
		rs = append(rs, Range{int64(x.idsOff) + int64(m)*hs, hs})
		c := bytes.Compare(x.ID(m), id)
		switch {
		case c == 0:
			rs = append(rs, Range{int64(x.crcOff) + 4*int64(m), 4}, Range{int64(x.offOff) + 4*int64(m), 4})
			return rs
		case c < 0:
			lo = m + 1
		default:
			hi = m
		}
	}
	return rs
}

// chunkSpans returns the (offset,len) of each piece of a Concat plan.
func chunkSpans(p *lab.Plan) []Range {
	var out []Range
	var pos int64
	for _, pc := range p.Pieces {
		out = append(out, Range{pos, pc.Len})
		pos += pc.Len
	}
	return out
}

// coveredBytes is the number of bytes of chunks that must be fetched to serve
// the ranges: the union of the chunks they touch.
func coveredBytes(spans []Range, rs []Range) int64 {
	seen := map[int]bool{}
	var n int64
	for _, r := range rs {
		i := sort.Search(len(spans), func(i int) bool { return spans[i].Off+spans[i].Len > r.Off })
		for ; i < len(spans) && spans[i].Off < r.Off+r.Len; i++ {
			if !seen[i] {
				seen[i] = true
				n += spans[i].Len
			}
		}
	}
	return n
}

// ReadCosts is the mean bytes fetched to read one object by native id.
type ReadCosts struct {
	Sample              int
	MeanDepth           float64
	ObjectBytes         float64 // mean resolved object size (what the caller wants)
	CDCPackIdx          float64 // CDC chunks covering the idx probe + the entry chain in the pack
	CDCIdxPart          float64
	EntryComponents     float64 // gp-entry: idx via CDC + the chain's entry components
	SetSections         float64 // gp-set: table sections of the chain + plaintext/delta components (packed sizes)
	SetDirectoryBytes   int64   // per-pack section directory (first-id prefix per section) kept in the recipe
	NativeObject        float64 // the floor: the object's own canonical component (packed size)
	FullPackBytesCDC    int64
	SampleDepth0Share   float64
	DeepestChainFetch   float64 // SetSections at the deepest sampled chain
	DeepestChainDepth   int
	CDCPackIdxAtDeepest float64
}

// MeasureReads samples objects uniformly (seeded) and accounts the fetch for
// each representation. packFile/idxFile are the original bytes (the CDC view).
func MeasureReads(an *PackAnalysis, packFile, idxFile []byte, sample int, seed int64, o RepOpts) (ReadCosts, error) {
	p := an.P
	var rc ReadCosts
	x, err := ParseIdx(idxFile, p.Algo, Limits{})
	if err != nil {
		return rc, err
	}
	cdcPack, err := baseline("cdc", File{Name: "p.pack", Kind: "pack", Data: packFile})
	if err != nil {
		return rc, err
	}
	cdcIdx, err := baseline("cdc", File{Name: "p.idx", Kind: "idx", Data: idxFile})
	if err != nil {
		return rc, err
	}
	pSpans, iSpans := chunkSpans(cdcPack), chunkSpans(cdcIdx)
	rc.FullPackBytesCDC = int64(len(packFile))
	// per-entry sizes
	n := len(p.Entries)
	entryBytes := func(i int) int64 { return p.Entries[i].End - p.Entries[i].Off }
	packedPlain := make([]int64, n)
	_, ss := an.SetPlan(o)
	sectionOf, sectionBytes := ss.SectionOf, ss.SectionBytes
	rc.SetDirectoryBytes = int64(len(sectionBytes)) * 4
	for i := range an.Entries {
		packedPlain[i] = lab.PackedLen(an.Entries[i].Comp)
	}
	rng := rand.New(rand.NewSource(seed))
	var depthSum, objSum, cdcSum, cdcIdxSum, entSum, setSum, natSum float64
	var d0 int
	for s := 0; s < sample; s++ {
		i := rng.Intn(n)
		e := &p.Entries[i]
		idxRs := x.LookupRanges(e.ID)
		idxCov := coveredBytes(iSpans, idxRs)
		var packRs []Range
		var entFetch int64
		secs := map[int]bool{}
		var setFetch int64
		depth := 0
		for j := i; ; j = p.Entries[j].Base {
			ej := &p.Entries[j]
			packRs = append(packRs, Range{ej.Off, ej.End - ej.Off})
			entFetch += entryBytes(j)
			if !secs[sectionOf[j]] {
				secs[sectionOf[j]] = true
				setFetch += sectionBytes[sectionOf[j]]
			}
			setFetch += packedPlain[j]
			if !ej.IsDelta() {
				break
			}
			depth++
		}
		cdcPackCov := coveredBytes(pSpans, packRs)
		depthSum += float64(depth)
		if depth == 0 {
			d0++
		}
		objSum += float64(e.RSize)
		cdcSum += float64(idxCov + cdcPackCov)
		cdcIdxSum += float64(idxCov)
		entSum += float64(idxCov + entFetch)
		setSum += float64(setFetch)
		natSum += float64(lab.PackedLen(Canonical(e.Type, mustObj(p, i))))
		if depth > rc.DeepestChainDepth {
			rc.DeepestChainDepth = depth
			rc.DeepestChainFetch = float64(setFetch)
			rc.CDCPackIdxAtDeepest = float64(idxCov + cdcPackCov)
		}
	}
	f := float64(sample)
	rc.Sample = sample
	rc.MeanDepth = depthSum / f
	rc.ObjectBytes = objSum / f
	rc.CDCPackIdx = cdcSum / f
	rc.CDCIdxPart = cdcIdxSum / f
	rc.EntryComponents = entSum / f
	rc.SetSections = setSum / f
	rc.NativeObject = natSum / f
	rc.SampleDepth0Share = float64(d0) / f
	return rc, nil
}

var objCache = map[*Pack]map[int][]byte{}

func mustObj(p *Pack, i int) []byte {
	c := objCache[p]
	if c == nil {
		c = map[int][]byte{}
		objCache[p] = c
	}
	b, _ := p.Object(i, c)
	if len(c) > 512 {
		objCache[p] = map[int][]byte{}
	}
	return b
}
