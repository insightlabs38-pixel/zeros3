package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/st"
)

type scheme struct {
	name string
	cut  func(t tensorView, fn func(b []byte))
}

type tensorView struct {
	data  []byte
	shape []int64
	width int
}

func fixed(n int) func(t tensorView, fn func(b []byte)) {
	return func(t tensorView, fn func(b []byte)) {
		for i := 0; i < len(t.data); i += n {
			fn(t.data[i:min(i+n, len(t.data))])
		}
	}
}

var schemes = []scheme{
	{"whole tensor", func(t tensorView, fn func(b []byte)) { fn(t.data) }},
	{"fixed 64 KiB", fixed(64 << 10)},
	{"fixed 16 KiB", fixed(16 << 10)},
	{"fixed 4 KiB", fixed(4 << 10)},
	{"CDC inside tensor", func(t tensorView, fn func(b []byte)) {
		lab.ChunkCDC(bytes.NewReader(t.data), func(off int64, c []byte) error { fn(c); return nil })
	}},
	{"row blocks (2-D tensors)", func(t tensorView, fn func(b []byte)) {
		if len(t.shape) != 2 || t.shape[1] <= 0 || t.width == 0 {
			fn(t.data)
			return
		}
		row := int(t.shape[1]) * t.width
		for i := 0; i < len(t.data); i += row {
			fn(t.data[i:min(i+row, len(t.data))])
		}
	}},
}

// blocksCmd (Z3-09G): content-addressed block reuse of the target against the
// base for each way of cutting tensors.
func blocksCmd(args []string) {
	fs := flag.NewFlagSet("blocks", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	vsFirst := fs.Bool("vs-first", false, "compare with r0")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	hdr := "| family | base -> target | whole-file CDC |"
	sep := "|---|---|---:|"
	for _, s := range schemes {
		hdr += " " + s.name + " |"
		sep += "---:|"
	}
	fmt.Println("Reused bytes of the target, % of its tensor bytes (content-addressed against every block of the base).")
	fmt.Println(hdr)
	fmt.Println(sep)
	for _, fam := range order {
		c := cks[fam]
		for j := 1; j < len(c); j++ {
			i := j - 1
			if *vsFirst {
				i = 0
			}
			a, b := c[i], c[j]
			var tot int64
			for k := range b.f.Tensors {
				tot += b.f.Tensors[k].Size()
			}
			row := fmt.Sprintf("| %s | %s -> %s | %s |", strings.TrimPrefix(fam, "rw-"), a.label, b.label, pct(int64(len(b.data))-newChunkBytes(a, b), int64(len(b.data))))
			for _, s := range schemes {
				set := map[[32]byte]bool{}
				for k := range a.f.Tensors {
					t := &a.f.Tensors[k]
					s.cut(viewOf(a, t), func(blk []byte) { set[sha256.Sum256(blk)] = true })
				}
				var reused int64
				for k := range b.f.Tensors {
					t := &b.f.Tensors[k]
					s.cut(viewOf(b, t), func(blk []byte) {
						if set[sha256.Sum256(blk)] {
							reused += int64(len(blk))
						}
					})
				}
				row += " " + pct(reused, tot) + " |"
			}
			fmt.Println(row)
		}
	}
}

func viewOf(c *ckpt, t *st.Tensor) tensorView {
	w := 0
	if t.Bits%8 == 0 {
		w = t.Bits / 8
	}
	return tensorView{data: c.payload(t), shape: t.Shape, width: w}
}
