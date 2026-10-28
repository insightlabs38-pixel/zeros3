package bitcodez3

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Encode groups exact target bytes; IDs and abbreviations are never rewritten.
func Encode(data []byte, p *Plan, mode string) (*kinds.Bundle, error) {
	var spans [][2]int64
	switch mode {
	case "functions":
		for _, b := range p.Blocks {
			if b.ID == 12 {
				spans = append(spans, [2]int64{int64(b.Body / 8), int64((b.End - b.Body) / 8)})
			}
		}
	case "leaves":
		// Blocks are emitted in preorder, so the next block identifies descendants.
		for i, b := range p.Blocks {
			if i+1 == len(p.Blocks) || p.Blocks[i+1].Body >= b.End {
				spans = append(spans, [2]int64{int64(b.Body / 8), int64((b.End - b.Body) / 8)})
			}
		}
	case "records":
		// Byte cuts are rounded down; boundary bits stay in adjacent exact bytes.
		// Group at most 4 KiB of consecutive records from the same block ID.
		var start, end uint64
		var block uint64
		flush := func() {
			if end > start {
				spans = append(spans, [2]int64{int64(start), int64(end - start)})
			}
		}
		for _, r := range p.Records {
			a, z := r.Start/8, r.End/8
			if z <= a {
				continue
			}
			if end > start && (r.Block != block || z-start > 4096 || a > end+32) {
				flush()
				start = end
			}
			if end <= start {
				start = a
			}
			end = z
			block = r.Block
		}
		flush()
	default:
		return nil, fmt.Errorf("unknown bitcode grouping")
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	for _, s := range spans {
		if s[1] > 16<<20 {
			return nil, fmt.Errorf("bitcode region limit")
		}
	}
	return regions(data, spans)
}
func regions(data []byte, spans [][2]int64) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objects [][]byte
	add := func(b []byte) {
		if len(b) == 0 {
			return
		}
		objects = append(objects, b)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(b), Size: int64(len(b))})
	}
	gap := func(b []byte) {
		for _, p := range lab.ChunkScaled(b, 1<<10, 4<<10, 16<<10) {
			add(p)
		}
	}
	off := int64(0)
	for _, s := range spans {
		if s[0] < off || s[1] <= 0 || s[0] > int64(len(data)) || s[1] > int64(len(data))-s[0] {
			return nil, fmt.Errorf("native span bounds")
		}
		gap(data[off:s[0]])
		add(data[s[0] : s[0]+s[1]])
		off = s[0] + s[1]
	}
	gap(data[off:])
	return kinds.NewBundle(r, objects)
}
