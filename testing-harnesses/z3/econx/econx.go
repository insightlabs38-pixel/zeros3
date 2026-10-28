// Package econx is the representation-economics glue the Z3-32 driver (cmd/z3mb) kept in package
// main — the grouping cutters over idxref.Sec, component/piece packing and the index-node
// ("Treeify") plan wrapper — extracted mechanically (sed over cmd/z3mb/econ.go, identifiers
// exported, the DEX section type replaced by idxref.Sec) so the CLR and Wasm drivers import it instead of copying it.
// cmd/z3mb is left unchanged.
package econx

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/bits"

	"zeros3-z3/idxref"
	"zeros3-z3/lab"
)

// Cutters choose the physical grouping of the masked payload (or of the raw file for baselines).
type Cutter struct {
	Name string
	Cut  func(payload []byte, secs []idxref.Sec) []int
}

func CutsOfChunks(chunks [][]byte, base int) []int {
	var out []int
	p := base
	for _, c := range chunks {
		p += len(c)
		out = append(out, p)
	}
	return out
}

func CDCProd() Cutter {
	return Cutter{"cdc-prod", func(p []byte, _ []idxref.Sec) []int {
		var out []int
		lab.ChunkCDC(bytes.NewReader(p), func(off int64, c []byte) error { out = append(out, int(off)+len(c)); return nil })
		return out
	}}
}

func CDCScaled(kib int) Cutter {
	return Cutter{fmt.Sprintf("cdc-%dk", kib), func(p []byte, _ []idxref.Sec) []int {
		t := kib << 10
		return CutsOfChunks(lab.ChunkScaled(p, t/4, t, t*4), 0)
	}}
}

// SectionCDC restarts the scaled CDC at every section boundary.
func SectionCDC(kib int) Cutter {
	return Cutter{fmt.Sprintf("seccdc-%dk", kib), func(p []byte, secs []idxref.Sec) []int {
		t := kib << 10
		var out []int
		for _, s := range secs {
			out = append(out, CutsOfChunks(lab.ChunkScaled(p[s.Off:s.End], t/4, t, t*4), s.Off)...)
		}
		return out
	}}
}

func Sections() Cutter {
	return Cutter{"sections", func(p []byte, secs []idxref.Sec) []int {
		var out []int
		for _, s := range secs {
			out = append(out, s.End)
		}
		return out
	}}
}

func FNV(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

// UnitCut groups consecutive items of an item section by a content-defined rule (cut after an
// item whose hash is 0 mod M, M = target / mean item size); sections without item boundaries
// (ID tables, gaps) use scaled CDC.
func UnitCut(kib int) Cutter {
	return Cutter{fmt.Sprintf("unit-%dk", kib), UnitCutBytes(kib << 10)}
}

func UnitCutBytes(t int) func(p []byte, secs []idxref.Sec) []int {
	return func(p []byte, secs []idxref.Sec) []int {
		var out []int
		for _, s := range secs {
			if len(s.Items) < 2 {
				out = append(out, CutsOfChunks(lab.ChunkScaled(p[s.Off:s.End], t/4, t, t*4), s.Off)...)
				continue
			}
			m := uint64(t) * uint64(len(s.Items)) / uint64(s.End-s.Off)
			if m < 1 {
				m = 1
			}
			// Z3-33: round the modulus to a power of two. The extracted Z3-32 rule used the raw
			// quotient, so a one-item change moved it (136 -> 135) and re-drew every cut of a
			// small section.
			m = 1 << uint(bits.Len64(m*3/2)-1)
			start := s.Off
			for i, st := range s.Items {
				end := s.End
				if i+1 < len(s.Items) {
					end = s.Items[i+1]
				}
				h := uint64(0)
				if s.Hash != nil {
					h = s.Hash[i]
				} else {
					h = FNV(p[st:end])
				}
				if h%m == 0 || end-start >= 4*t {
					out = append(out, end)
					start = end
				}
			}
			if start < s.End {
				out = append(out, s.End)
			}
		}
		return out
	}
}

// PerItem cuts at every item start (one component per code item / string / class_data...).
func PerItem(types ...uint16) Cutter {
	return Cutter{"per-item", func(p []byte, secs []idxref.Sec) []int {
		var out []int
		for _, s := range secs {
			use := false
			for _, t := range types {
				use = use || t == s.Type
			}
			if !use || len(s.Items) == 0 {
				out = append(out, s.End)
				continue
			}
			for i := range s.Items {
				if i+1 < len(s.Items) {
					out = append(out, s.Items[i+1])
				} else {
					out = append(out, s.End)
				}
			}
		}
		return out
	}}
}

func Sum(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

func MkComp(b []byte, class string) *lab.Comp {
	return &lab.Comp{ID: Sum(b), Size: int64(len(b)), Class: class, Data: func() ([]byte, error) { return b, nil }}
}

// PackPieces turns a meta record and its components into plan pieces (flat).
func PackPieces(meta []byte, comps [][]byte, class string) []lab.Piece {
	ps := []lab.Piece{{Inline: meta}}
	for _, c := range comps {
		ps = append(ps, lab.Piece{Comp: MkComp(c, class), Len: int64(len(c))})
	}
	return ps
}

// UnpackPieces fetches the meta record and component bytes back from a flat piece list.
func UnpackPieces(pieces []lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, [][]byte, error) {
	if len(pieces) == 0 || pieces[0].Comp != nil {
		return nil, nil, fmt.Errorf("missing meta piece")
	}
	var cs [][]byte
	for _, pc := range pieces[1:] {
		if pc.Comp == nil {
			return nil, nil, fmt.Errorf("unexpected inline piece")
		}
		b, err := get(pc.Comp.ID)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, b)
	}
	return pieces[0].Inline, cs, nil
}

// that every node lists exactly the digests its children have, then runs the original rebuild.
func Treeify(p *lab.Plan, fan int) *lab.Plan {
	if fan <= 0 || p == nil {
		return p
	}
	np := *p
	np.Concat = false
	np.Pieces = append([]lab.Piece(nil), p.Pieces...)
	var elig []int
	for i, pc := range np.Pieces {
		if pc.Comp != nil && !pc.Via && !pc.Derived {
			elig = append(elig, i)
		}
	}
	if len(elig) < 8 {
		return p
	}
	type node struct {
		comp *lab.Comp
		ids  []byte
	}
	var nodes []node
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			nodes = append(nodes, node{MkComp(cur, "node"), cur})
			cur = nil
		}
	}
	n := 0
	for _, i := range elig {
		id := np.Pieces[i].Comp.ID
		cur = append(cur, id[:]...)
		np.Pieces[i].Via = true
		n++
		if (int(id[0])<<8|int(id[1]))%fan == 0 || n >= 4*fan {
			flush()
			n = 0
		}
	}
	flush()
	for _, nd := range nodes {
		np.Pieces = append(np.Pieces, lab.Piece{Comp: nd.comp, Len: nd.comp.Size})
	}
	inner := p.Rebuild
	np.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		for _, nd := range nodes {
			b, err := get(nd.comp.ID)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, nd.ids) {
				return fmt.Errorf("index node differs")
			}
		}
		return inner(w, get)
	}
	return &np
}
