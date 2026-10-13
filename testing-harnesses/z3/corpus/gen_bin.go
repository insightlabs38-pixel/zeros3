package corpus

import (
	"encoding/binary"
	"strconv"
)

// Synthetic relocatable image ("ZBIN"). Text is a sequence of 16-byte
// bundles: opaque code, rel32 calls (0xE8) and abs64 address loads (0xB8).
// This isolates the reference-shift problem: moving code or the image base
// rewrites many 4/8-byte fields while the surrounding bytes are unchanged.

const (
	binBundle     = 16
	binHeaderSize = 4096
)

type bundle struct {
	kind   byte // 0 opaque, 1 call, 2 abs
	target int
	op     [binBundle]byte
}

type zprog struct {
	funcs  [][]bundle
	rodata []byte
	data   []byte
	base   uint64
	stamp  uint64
}

func newFunc(r *RNG, nfuncs int) []bundle {
	n := 4 + r.Intn(60)
	f := make([]bundle, n)
	for i := range f {
		r.Fill(f[i].op[:])
		f[i].op[0] &= 0x7F
		switch k := r.Intn(10); {
		case k == 0:
			f[i].kind, f[i].target = 1, r.Intn(nfuncs)
		case k == 1:
			f[i].kind, f[i].target = 2, r.Intn(nfuncs)
		}
	}
	return f
}

func alignUp(n, a int) int { return (n + a - 1) / a * a }

// render lays the image out and returns it with the number of reference fields.
func (p *zprog) render() (img []byte, refs int) {
	textSize := 0
	faddr := make([]uint64, len(p.funcs))
	for i, f := range p.funcs {
		faddr[i] = uint64(binHeaderSize + textSize)
		textSize += len(f) * binBundle
	}
	type sec struct {
		kind      uint32
		off, size int
	}
	secs := []sec{{1, binHeaderSize, textSize}}
	next := func(kind uint32, size int) {
		last := secs[len(secs)-1]
		secs = append(secs, sec{kind, alignUp(last.off+last.size, 4096), size})
	}
	next(2, len(p.rodata))
	next(3, 8*len(p.funcs))
	next(4, len(p.data))
	end := secs[3].off + secs[3].size
	img = make([]byte, alignUp(end, 4096))
	le := binary.LittleEndian
	copy(img, "ZBIN")
	le.PutUint32(img[4:], 1)
	le.PutUint64(img[8:], p.base)
	le.PutUint64(img[16:], p.stamp)
	le.PutUint32(img[24:], uint32(len(secs)))
	for i, s := range secs {
		h := img[32+32*i:]
		le.PutUint32(h[0:], s.kind)
		le.PutUint64(h[8:], uint64(s.off))
		le.PutUint64(h[16:], uint64(s.size))
		le.PutUint64(h[24:], p.base+uint64(s.off))
	}
	pos := binHeaderSize
	for _, f := range p.funcs {
		for _, b := range f {
			out := img[pos : pos+binBundle]
			copy(out, b.op[:])
			switch b.kind {
			case 1:
				out[0] = 0xE8
				rel := int32(int64(faddr[b.target]) - int64(pos+5))
				le.PutUint32(out[1:], uint32(rel))
				refs++
			case 2:
				out[0] = 0xB8
				le.PutUint64(out[1:], p.base+faddr[b.target])
				refs++
			}
			pos += binBundle
		}
	}
	copy(img[secs[1].off:], p.rodata)
	for i := range p.funcs {
		le.PutUint64(img[secs[2].off+8*i:], p.base+faddr[i])
		refs++
	}
	copy(img[secs[3].off:], p.data)
	return img, refs
}

func init() {
	bin := func(name string, fn func(r *RNG, p *zprog, i int) string) {
		register(Family{Name: name, Format: "zbin", Workload: "binary-rebuild", Ext: ".zbin",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				p := &zprog{base: 0x400000, stamp: 1_700_000_000}
				nf := max(8, c.size/2/(34*binBundle))
				for i := 0; i < nf; i++ {
					p.funcs = append(p.funcs, newFunc(r, nf))
				}
				p.rodata = Text(r, c.size/6)
				p.data = r.Bytes(c.size / 6)
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i > 0 {
						desc = fn(r, p, i)
					}
					img, refs := p.render()
					return img, desc, map[string][]string{
						"reference_fields": {strconv.Itoa(refs)},
						"sections":         {"text", "rodata", "ptrtab", "data"},
					}
				})
			}})
	}
	bin("bin-rebase", func(r *RNG, p *zprog, i int) string {
		p.base += uint64(0x10000 * (1 + r.Intn(8)))
		return "move image base; call rel32 unchanged, abs64 and pointer table shift"
	})
	bin("bin-insert-func", func(r *RNG, p *zprog, i int) string {
		k := len(p.funcs) / 2
		for _, f := range p.funcs {
			for j := range f {
				if f[j].kind != 0 && f[j].target >= k {
					f[j].target++
				}
			}
		}
		nf := newFunc(r, len(p.funcs)+1)
		p.funcs = append(p.funcs[:k:k], append([][]bundle{nf}, p.funcs[k:]...)...)
		return "insert one function mid-text; later symbols shift"
	})
	bin("bin-meta", func(r *RNG, p *zprog, i int) string {
		p.stamp += 86400
		copy(p.rodata[:32], Text(r, 32))
		return "header timestamp and build-id string only"
	})
	bin("bin-semantic", func(r *RNG, p *zprog, i int) string {
		for k := 0; k < 3; k++ {
			f := p.funcs[r.Intn(len(p.funcs))]
			for e := 0; e < 4; e++ {
				b := &f[r.Intn(len(f))]
				if b.kind == 0 {
					r.Fill(b.op[:])
					b.op[0] &= 0x7F
				} else {
					b.target = r.Intn(len(p.funcs))
				}
			}
		}
		return "edit ~12 bundles in 3 functions"
	})
}
