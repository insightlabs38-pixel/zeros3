package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// CARv1 with CIDv1/sha2-256 blocks: raw leaves (0x55) and dag-cbor link
// nodes (0x71). Hand-encoded; no multiformats dependency.

type carBlock struct {
	cid  []byte
	data []byte
	kids []int // indices into the block list (for DFS emission)
}

func cidOf(codec byte, data []byte) []byte {
	h := sha256.Sum256(data)
	return append([]byte{0x01, codec, 0x12, 0x20}, h[:]...)
}

func uvarint(n uint64) []byte { return binary.AppendUvarint(nil, n) }

func cborLink(cid []byte) []byte {
	return append([]byte{0xd8, 0x2a, 0x58, byte(len(cid) + 1), 0x00}, cid...)
}

func cborLinks(cids [][]byte) []byte {
	b := []byte{0x80 | byte(len(cids))}
	for _, c := range cids {
		b = append(b, cborLink(c)...)
	}
	return b
}

// carTree builds a fan-out-4 DAG over leaves and returns blocks in DFS
// pre-order plus the root CID.
func carTree(leaves [][]byte) ([]carBlock, []byte) {
	var blocks []carBlock
	type node struct {
		cid  []byte
		emit func()
	}
	level := make([]node, len(leaves))
	for i := range leaves {
		d := leaves[i]
		cid := cidOf(0x55, d)
		level[i] = node{cid, func() { blocks = append(blocks, carBlock{cid: cid, data: d}) }}
	}
	for len(level) > 1 {
		var up []node
		for i := 0; i < len(level); i += 4 {
			grp := level[i:min(i+4, len(level))]
			var cs [][]byte
			for _, g := range grp {
				cs = append(cs, g.cid)
			}
			d := cborLinks(cs)
			cid := cidOf(0x71, d)
			up = append(up, node{cid, func() {
				blocks = append(blocks, carBlock{cid: cid, data: d})
				for _, g := range grp {
					g.emit()
				}
			}})
		}
		level = up
	}
	level[0].emit()
	return blocks, level[0].cid
}

func writeCAR(roots [][]byte, blocks []carBlock, order []int) []byte {
	hdr := []byte{0xa2, 0x65}
	hdr = append(hdr, "roots"...)
	hdr = append(hdr, cborLinks(roots)...)
	hdr = append(hdr, 0x67)
	hdr = append(hdr, "version"...)
	hdr = append(hdr, 0x01)
	var b bytes.Buffer
	b.Write(uvarint(uint64(len(hdr))))
	b.Write(hdr)
	for _, i := range order {
		bl := blocks[i]
		b.Write(uvarint(uint64(len(bl.cid) + len(bl.data))))
		b.Write(bl.cid)
		b.Write(bl.data)
	}
	return b.Bytes()
}

func init() {
	car := func(name string, fn func(c *Ctx, r *RNG, i int, base, leaves [][]byte) ([][]byte, string)) {
		register(Family{Name: name, Format: "car", Workload: "content-addressed", Ext: ".car",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				n := max(8, c.size/(36*1024))
				var base [][]byte
				for i := 0; i < n; i++ {
					base = append(base, r.Bytes(8192+r.Intn(57344)))
				}
				leaves := base
				prev := map[string]bool{}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i > 0 {
						leaves, desc = fn(c, r, i, base, leaves)
					}
					blocks, root := carTree(leaves)
					roots := [][]byte{root}
					order := identity(len(blocks))
					if name == "car-reorder" && i > 0 {
						order = r.Perm(len(blocks))
					}
					if name == "car-roots" {
						roots = append(roots, blocks[len(blocks)/2].cid)
						roots = roots[:1+i%2]
					}
					cur := map[string]bool{}
					shared := 0
					for _, b := range blocks {
						k := string(b.cid)
						cur[k] = true
						if prev[k] {
							shared++
						}
					}
					prev = cur
					truth := map[string][]string{
						"blocks_total":              {strconv.Itoa(len(blocks))},
						"blocks_shared_with_parent": {strconv.Itoa(shared)},
					}
					return writeCAR(roots, blocks, order), desc, truth
				})
			}})
	}
	car("car-reorder", func(c *Ctx, r *RNG, i int, base, l [][]byte) ([][]byte, string) {
		return l, "same blocks and roots, shuffled section order"
	})
	car("car-roots", func(c *Ctx, r *RNG, i int, base, l [][]byte) ([][]byte, string) {
		return l, "same blocks, root set changes"
	})
	car("car-add", func(c *Ctx, r *RNG, i int, base, l [][]byte) ([][]byte, string) {
		l = append(l[:len(l):len(l)], r.Bytes(20000), r.Bytes(30000), r.Bytes(40000))
		return l, "append three leaf blocks"
	})
	car("car-remove", func(c *Ctx, r *RNG, i int, base, l [][]byte) ([][]byte, string) {
		for k := 0; k < 3 && len(l) > 4; k++ {
			j := r.Intn(len(l))
			l = append(l[:j:j], l[j+1:]...)
		}
		return l, "drop up to three leaf blocks"
	})
	car("car-overlap", func(c *Ctx, r *RNG, i int, base, l [][]byte) ([][]byte, string) {
		var out [][]byte
		for _, j := range r.Perm(len(base))[:len(base)/2] {
			out = append(out, base[j])
		}
		for len(out) < len(base) {
			out = append(out, r.Bytes(8192+r.Intn(57344)))
		}
		return out, "half of the base blocks plus fresh blocks"
	})
}
