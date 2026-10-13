package corpus

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Structured-data characterisation controls. These are shaped like page
// databases, column stores, chunked arrays and length-delimited records; they
// make no compatibility claim with any real format.

const pageSize = 4096

func pageDBRender(pages [][]byte, counter uint64) []byte {
	h := make([]byte, pageSize)
	copy(h, "ZPDB")
	binary.LittleEndian.PutUint32(h[4:], pageSize)
	binary.LittleEndian.PutUint32(h[8:], uint32(len(pages)))
	binary.LittleEndian.PutUint64(h[16:], counter)
	return append(h, bytes.Join(pages, nil)...)
}

func newPage(r *RNG, firstID uint64) []byte {
	p := make([]byte, pageSize)
	const rows = (pageSize - 8) / 64
	binary.LittleEndian.PutUint16(p, rows)
	for i := 0; i < rows; i++ {
		row := p[8+64*i:]
		binary.LittleEndian.PutUint64(row, firstID+uint64(i))
		copy(row[8:64], Text(r, 56))
	}
	return p
}

func init() {
	pg := func(name string, fn func(r *RNG, pages [][]byte, next *uint64) ([][]byte, string)) {
		register(Family{Name: name, Format: "pagedb", Workload: "structured-data", Ext: ".zpdb",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				var pages [][]byte
				var next uint64
				for len(pages)+1 < c.size/pageSize {
					pages = append(pages, newPage(r, next))
					next += 63
				}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i > 0 {
						pages, desc = fn(r, pages, &next)
					}
					return pageDBRender(pages, uint64(i)), desc, map[string][]string{"page_size": {"4096"}}
				})
			}})
	}
	pg("pagedb-update", func(r *RNG, pages [][]byte, next *uint64) ([][]byte, string) {
		for k := 0; k < 3; k++ {
			j := r.Intn(len(pages))
			p := append([]byte(nil), pages[j]...)
			copy(p[8+64*r.Intn(63)+8:], Text(r, 56))
			pages[j] = p
		}
		return pages, "rewrite rows in up to 3 pages; header counter bumps"
	})
	pg("pagedb-append", func(r *RNG, pages [][]byte, next *uint64) ([][]byte, string) {
		for k := 0; k < 8; k++ {
			pages = append(pages, newPage(r, *next))
			*next += 63
		}
		return pages, "append 8 pages"
	})
	pg("pagedb-vacuum", func(r *RNG, pages [][]byte, next *uint64) ([][]byte, string) {
		out := append([][]byte(nil), pages...)
		for k := 0; k < len(out)/20+1; k++ {
			a := r.Intn(len(out))
			b := (a + 1 + r.Intn(len(out)-1)) % len(out)
			out[a], out[b] = out[b], out[a]
		}
		return out, "swap ~5% of page positions"
	})
}

// ---- column store ----------------------------------------------------------

func colChunk(r *RNG, n int) []byte {
	b := make([]byte, n-n%4)
	v := uint32(r.U64())
	for i := 0; i < len(b); i += 4 {
		v += uint32(r.Intn(16))
		binary.LittleEndian.PutUint32(b[i:], v)
	}
	return b
}

// colRender lays out row group major; chunks[rg][col].
func colRender(chunks [][][]byte) []byte {
	var out bytes.Buffer
	le := binary.LittleEndian
	out.WriteString("ZCL1")
	type ent struct{ rg, col, off, n int }
	var es []ent
	for rg, cols := range chunks {
		for col, ch := range cols {
			es = append(es, ent{rg, col, out.Len(), len(ch)})
			out.Write(ch)
		}
	}
	foot := out.Len()
	binary.Write(&out, le, uint32(len(es)))
	for _, e := range es {
		binary.Write(&out, le, uint32(e.rg))
		binary.Write(&out, le, uint32(e.col))
		binary.Write(&out, le, uint64(e.off))
		binary.Write(&out, le, uint64(e.n))
	}
	binary.Write(&out, le, uint32(out.Len()-foot))
	out.WriteString("ZCL1")
	return out.Bytes()
}

func init() {
	cs := func(name string, fn func(c *Ctx, r *RNG, chunks [][][]byte, csz int) ([][][]byte, string)) {
		register(Family{Name: name, Format: "colstore", Workload: "structured-data", Ext: ".zcl",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				const rgs, cols = 4, 4
				csz := c.size / (rgs * cols)
				chunks := make([][][]byte, rgs)
				for g := range chunks {
					for k := 0; k < cols; k++ {
						chunks[g] = append(chunks[g], colChunk(r, csz))
					}
				}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i > 0 {
						chunks, desc = fn(c, r, chunks, csz)
					}
					return colRender(chunks), desc, nil
				})
			}})
	}
	cs("colstore-update-col", func(c *Ctx, r *RNG, ch [][][]byte, csz int) ([][][]byte, string) {
		g := r.Intn(len(ch))
		k := r.Intn(len(ch[g]))
		ch[g][k] = colChunk(r, len(ch[g][k]))
		return ch, fmt.Sprintf("replace column chunk rg%d col%d (same length)", g, k)
	})
	cs("colstore-append-rg", func(c *Ctx, r *RNG, ch [][][]byte, csz int) ([][][]byte, string) {
		var row [][]byte
		for range ch[0] {
			row = append(row, colChunk(r, csz))
		}
		return append(ch, row), "append a row group"
	})
	cs("colstore-add-col", func(c *Ctx, r *RNG, ch [][][]byte, csz int) ([][][]byte, string) {
		for g := range ch {
			ch[g] = append(ch[g], colChunk(r, csz))
		}
		return ch, "add a column to every row group; later chunk offsets shift"
	})
}

// ---- chunked N-d array -----------------------------------------------------

func ndRender(chunks [][]byte) []byte {
	le := binary.LittleEndian
	var payload [][]byte
	for _, ch := range chunks {
		payload = append(payload, deflateBytes(ch))
	}
	var out bytes.Buffer
	out.WriteString("ZND1")
	binary.Write(&out, le, uint32(2))
	binary.Write(&out, le, [2]uint64{uint64(len(chunks) / 2 * 128), 512})
	binary.Write(&out, le, [2]uint64{128, 256})
	binary.Write(&out, le, uint32(len(chunks)))
	off := out.Len() + 16*len(chunks)
	for _, p := range payload {
		binary.Write(&out, le, uint64(off))
		binary.Write(&out, le, uint64(len(p)))
		off += len(p)
	}
	for _, p := range payload {
		out.Write(p)
	}
	return out.Bytes()
}

func ndChunk(r *RNG) []byte {
	b := make([]byte, 128*256*4)
	v := uint32(r.U64())
	for i := 0; i < len(b); i += 4 {
		v += uint32(r.Intn(64))
		binary.LittleEndian.PutUint32(b[i:], v)
	}
	return b
}

func init() {
	nd := func(name string, fn func(r *RNG, chunks [][]byte) ([][]byte, string)) {
		register(Family{Name: name, Format: "ndarray", Workload: "structured-data", Ext: ".znd", ToolDependent: true,
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				// ~45% DEFLATE ratio on the random-walk chunks.
				n := max(4, c.size/(128*256*4*45/100)) &^ 1
				var chunks [][]byte
				for i := 0; i < n; i++ {
					chunks = append(chunks, ndChunk(r))
				}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i > 0 {
						chunks, desc = fn(r, chunks)
					}
					return ndRender(chunks), desc, nil
				})
			}})
	}
	nd("ndarray-region-update", func(r *RNG, ch [][]byte) ([][]byte, string) {
		k := r.Intn(len(ch))
		c := append([]byte(nil), ch[k]...)
		for row := 0; row < 16; row++ {
			copy(c[(row*256+r.Intn(200))*4:], r.Bytes(64))
		}
		ch[k] = c
		return ch, fmt.Sprintf("modify a 16-row region inside chunk %d", k)
	})
	nd("ndarray-append", func(r *RNG, ch [][]byte) ([][]byte, string) {
		return append(ch, ndChunk(r), ndChunk(r)), "extend dimension 0 by one chunk row; index grows"
	})
}

// ---- length-delimited records ----------------------------------------------

type protoRec struct {
	id   uint64
	body []byte
}

func protoEnc(rs []protoRec) []byte {
	var out bytes.Buffer
	for _, p := range rs {
		var m []byte
		m = append(m, 0x08)
		m = binary.AppendUvarint(m, p.id)
		name := fmt.Sprintf("tensor_%d", p.id)
		m = append(m, 0x12)
		m = binary.AppendUvarint(m, uint64(len(name)))
		m = append(m, name...)
		m = append(m, 0x1a)
		m = binary.AppendUvarint(m, uint64(len(p.body)))
		m = append(m, p.body...)
		out.Write(binary.AppendUvarint(nil, uint64(len(m))))
		out.Write(m)
	}
	return out.Bytes()
}

func init() {
	pr := func(name string, fn func(r *RNG, rs []protoRec, next *uint64) ([]protoRec, string)) {
		register(Family{Name: name, Format: "protorec", Workload: "structured-data", Ext: ".zpb",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				var rs []protoRec
				var next uint64 = 1
				for sz := 0; sz < c.size; {
					b := randWeights(dtFP32, 2000+r.Intn(4000), r)
					rs = append(rs, protoRec{next, b})
					next++
					sz += len(b) + 16
				}
				prev := map[string]string{}
				for _, p := range rs {
					prev[fmt.Sprint("id:", p.id)] = contentKey(p.body)
				}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					var truth map[string][]string
					if i > 0 {
						rs, desc = fn(r, rs, &next)
						cur := map[string]string{}
						for _, p := range rs {
							cur[fmt.Sprint("id:", p.id)] = contentKey(p.body)
						}
						truth = truthSets(prev, cur)
						prev = cur
					}
					return protoEnc(rs), desc, truth
				})
			}})
	}
	pr("protorec-update", func(r *RNG, rs []protoRec, next *uint64) ([]protoRec, string) {
		k := r.Intn(len(rs))
		b := append([]byte(nil), rs[k].body...)
		perturb(b, 4, 0.05, r)
		rs[k].body = b
		return rs, fmt.Sprintf("perturb record id %d", rs[k].id)
	})
	pr("protorec-insert-delete", func(r *RNG, rs []protoRec, next *uint64) ([]protoRec, string) {
		n := protoRec{*next, randWeights(dtFP32, 3000, r)}
		*next++
		k := len(rs) / 2
		rs = append(rs[:k:k], append([]protoRec{n}, rs[k:]...)...)
		d := r.Intn(len(rs))
		rs = append(rs[:d:d], rs[d+1:]...)
		return rs, "insert a record mid-stream, delete another"
	})
	pr("protorec-reorder", func(r *RNG, rs []protoRec, next *uint64) ([]protoRec, string) {
		a := r.Intn(len(rs))
		b := (a + 1 + r.Intn(len(rs)-1)) % len(rs)
		rs[a], rs[b] = rs[b], rs[a]
		return rs, "swap two records"
	})
}
