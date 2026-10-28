package nativerescue

import (
	"bytes"
	"fmt"
	"zeros3-z3/binref"
	"zeros3-z3/idxref"
)

func regionSizes(b []byte) ([]int, error) {
	r := &reader{b: b}
	r.uv()
	r.uv()
	n := r.uv()
	if n > 65536 || n > uint64(len(r.b)/2) {
		return nil, fmt.Errorf("native: region count")
	}
	sizes := make([]int, 0, n)
	total := uint64(0)
	for i := uint64(0); i < n; i++ {
		size := r.uv()
		p := r.take(1)
		if r.err != nil {
			return nil, r.err
		}
		if p[0] > 3 || p[0] == 2 || size > MaxNative-total {
			return nil, fmt.Errorf("native: region bound")
		}
		total += size
		sizes = append(sizes, int(size))
		if p[0]&1 != 0 {
			r.uv()
		}
		if p[0]&2 != 0 {
			r.uv()
		}
	}
	if r.err != nil || len(r.b) != 0 {
		return nil, fmt.Errorf("native: region framing")
	}
	return sizes, nil
}

// Decode is independent of analysis state. It reconstructs binref's existing
// streams and delegates exact address semantics to the unchanged engine.
func Decode(meta []byte, comps [][]byte) ([]byte, error) {
	if len(meta) > 32<<20 || len(meta) < 6 || !bytes.Equal(meta[:4], []byte("NBR1")) {
		return nil, fmt.Errorf("native: recipe magic/bound")
	}
	r := &reader{b: meta[4:]}
	size := r.uv()
	flag := r.take(1)
	if r.err != nil || size > MaxNative {
		return nil, fmt.Errorf("native: output size")
	}
	if flag[0] > 1 {
		return nil, fmt.Errorf("native: recipe flags")
	}
	compact := flag[0] == 1
	count := r.uv()
	if count > maxParts || count != uint64(len(comps)) {
		return nil, fmt.Errorf("native: recipe count")
	}
	ds := make([]descriptor, 0, count)
	total := 0
	for i := uint64(0); i < count; i++ {
		tag := r.take(1)
		ri, off, n := r.uv(), r.uv(), r.uv()
		if r.err != nil {
			return nil, r.err
		}
		if tag[0] > 4 || ri >= 65536 || off > MaxNative || n > MaxNative-off {
			return nil, fmt.Errorf("native: component descriptor")
		}
		if len(comps[i]) > 32<<20 || len(comps[i]) > 2*MaxNative-total {
			return nil, fmt.Errorf("native: component budget")
		}
		total += len(comps[i])
		ds = append(ds, descriptor{tag[0], int(ri), int(off), int(n)})
	}
	if len(r.b) != 0 {
		return nil, fmt.Errorf("native: recipe trailing bytes")
	}
	type key struct {
		tag    byte
		region int
	}
	streams := map[key][]byte{}
	appendPart := func(k key, off, n int, b []byte) error {
		if off != len(streams[k]) || n != len(b) || len(b) > MaxNative-len(streams[k]) {
			return fmt.Errorf("native: component order/size")
		}
		streams[k] = append(streams[k], b...)
		return nil
	}
	for i, d := range ds {
		if d.tag == 2 {
			continue
		}
		if d.tag < 2 && d.region != 0 {
			return nil, fmt.Errorf("native: metadata region")
		}
		if e := appendPart(key{d.tag, d.region}, d.off, d.n, comps[i]); e != nil {
			return nil, e
		}
	}
	hdr, ut := streams[key{0, 0}], streams[key{1, 0}]
	sizes, e := regionSizes(hdr)
	if e != nil {
		return nil, e
	}
	sum := 0
	for _, n := range sizes {
		sum += n
	}
	if uint64(sum) != size {
		return nil, fmt.Errorf("native: total region size")
	}
	ids, pi, _, e := unitIndex(ut)
	if e != nil {
		return nil, e
	}
	refRecords := map[int][]record{}
	grouped := map[int]bool{}
	for i, d := range ds {
		if d.tag != 2 {
			continue
		}
		if d.region >= len(sizes) || d.off > sizes[d.region] || d.n > sizes[d.region]-d.off {
			return nil, fmt.Errorf("native: group region")
		}
		if _, mixed := streams[key{4, d.region}]; mixed {
			return nil, fmt.Errorf("native: mixed reference modes")
		}
		if _, mixed := streams[key{3, d.region}]; mixed && !grouped[d.region] {
			return nil, fmt.Errorf("native: mixed payload modes")
		}
		grouped[d.region] = true
		p, ss, e := idxref.SplitComp(comps[i], 1)
		if e != nil {
			return nil, e
		}
		if e := appendPart(key{3, d.region}, d.off, d.n, p); e != nil {
			return nil, e
		}
		rs, e := parseRecords(ss[0], len(p), compact, pi, ids)
		if e != nil {
			return nil, e
		}
		for j := range rs {
			rs[j].off += d.off
		}
		refRecords[d.region] = append(refRecords[d.region], rs...)
	}
	for ri, rs := range refRecords {
		raw, e := putRecords(rs, 0, false, nil, nil)
		if e != nil {
			return nil, e
		}
		streams[key{4, ri}] = raw
	}
	norm := []binref.Stream{{Kind: "regions", Data: hdr}, {Kind: "units", Data: ut}}
	for k := range streams {
		if k.tag >= 3 && k.region >= len(sizes) {
			return nil, fmt.Errorf("native: surplus region")
		}
	}
	for ri, size := range sizes {
		p := streams[key{3, ri}]
		if len(p) != size {
			return nil, fmt.Errorf("native: incomplete payload")
		}
		norm = append(norm, binref.Stream{Kind: "payload", Region: ri, Data: p})
		if rb, ok := streams[key{4, ri}]; ok {
			if _, e := parseRecords(rb, size, false, nil, nil); e != nil {
				return nil, e
			}
			norm = append(norm, binref.Stream{Kind: "refs", Region: ri, Data: rb})
		}
	}
	out, e := binref.Reconstruct(norm)
	if e != nil {
		return nil, e
	}
	if uint64(len(out)) != size {
		return nil, fmt.Errorf("native: reconstructed size")
	}
	return out, nil
}
