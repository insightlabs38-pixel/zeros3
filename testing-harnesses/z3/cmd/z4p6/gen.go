package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// cmdGen writes a synthetic related-model family for scale tests, streaming (no file is ever
// resident): a (BF16, N MiB), b (exact F32 widening of a), c (exact F16 conversion of b's
// values), d (a's tensors in reversed order = shard/layout reorder), e (unrelated random BF16).
// Optionally one oversize tensor file. Values are Gaussian-like so bit patterns resemble weights.
func cmdGen(args []string) {
	fl := flag.NewFlagSet("gen", flag.ExitOnError)
	dir := fl.String("dir", "", "output directory")
	mib := fl.Int("mib", 256, "BF16 family size N (MiB)")
	tmib := fl.Int("tensor-mib", 4, "tensor size in the BF16 file (MiB)")
	big := fl.Int("big-mib", 0, "also write big.safetensors with one tensor of this many MiB (BF16)")
	fl.Parse(args)
	must(os.MkdirAll(*dir, 0o755))
	n := *mib / *tmib
	if n < 1 {
		n = 1
	}
	elems := *tmib << 19 // BF16 elements per tensor
	cols := 1024
	rows := elems / cols
	val := func(seed uint64, i int, r *rand.Rand) float32 {
		_ = i
		// sum of uniforms: bell-shaped, small magnitude
		x := r.Float64() + r.Float64() + r.Float64() + r.Float64() - 2
		return float32(x * 0.05)
	}
	bf := func(f float32) uint16 {
		u := math.Float32bits(f)
		u += 0x7fff + (u>>16)&1
		return uint16(u >> 16)
	}
	type spec struct {
		name  string
		dtype string
		w     int
		order []int
		gen   func(t int, emit func([]byte))
	}
	tensorBF := func(t int, seed uint64, emit func([]byte)) {
		r := rand.New(rand.NewPCG(seed+uint64(t), 99))
		buf := make([]byte, 2*cols)
		for row := 0; row < rows; row++ {
			for c := 0; c < cols; c++ {
				binary.LittleEndian.PutUint16(buf[2*c:], bf(val(seed, 0, r)))
			}
			emit(buf)
		}
	}
	tensorF32 := func(t int, seed uint64, emit func([]byte)) {
		tensorBF(t, seed, func(b []byte) {
			o := make([]byte, 4*cols)
			for c := 0; c < cols; c++ {
				binary.LittleEndian.PutUint32(o[4*c:], uint32(binary.LittleEndian.Uint16(b[2*c:]))<<16)
			}
			emit(o)
		})
	}
	tensorF16 := func(t int, seed uint64, emit func([]byte)) {
		tensorF32(t, seed, func(b []byte) {
			o := make([]byte, 2*cols)
			for c := 0; c < cols; c++ {
				binary.LittleEndian.PutUint16(o[2*c:], f32tof16(binary.LittleEndian.Uint32(b[4*c:])))
			}
			emit(o)
		})
	}
	fwd, rev := make([]int, n), make([]int, n)
	for i := range fwd {
		fwd[i], rev[i] = i, n-1-i
	}
	specs := []spec{
		{"a.safetensors", "BF16", 2, fwd, func(t int, e func([]byte)) { tensorBF(t, 1, e) }},
		{"b.safetensors", "F32", 4, fwd, func(t int, e func([]byte)) { tensorF32(t, 1, e) }},
		{"c.safetensors", "F16", 2, fwd, func(t int, e func([]byte)) { tensorF16(t, 1, e) }},
		{"d.safetensors", "BF16", 2, rev, func(t int, e func([]byte)) { tensorBF(t, 1, e) }},
		{"e.safetensors", "BF16", 2, fwd, func(t int, e func([]byte)) { tensorBF(t, 7777, e) }},
	}
	for _, sp := range specs {
		writeSafetensors(filepath.Join(*dir, sp.name), sp.dtype, sp.w, rows, cols, sp.order, sp.gen)
	}
	if *big > 0 {
		brows := (*big << 19) / cols
		writeBig(filepath.Join(*dir, "big.safetensors"), brows, cols, func(emit func([]byte)) {
			r := rand.New(rand.NewPCG(555, 1))
			buf := make([]byte, 2*cols)
			for row := 0; row < brows; row++ {
				for c := 0; c < cols; c++ {
					binary.LittleEndian.PutUint16(buf[2*c:], bf(val(0, 0, r)))
				}
				emit(buf)
			}
		})
	}
	fmt.Println(`{"gen":"ok"}`)
}

func f32tof16(f uint32) uint16 {
	// IEEE binary16, round-to-nearest-even; matches semcore's converter for finite values
	s := uint16(f>>16) & 0x8000
	e := int32(f>>23) & 0xff
	m := f & 0x7fffff
	switch {
	case e == 0xff:
		if m != 0 {
			return s | 0x7e00
		}
		return s | 0x7c00
	case e > 142:
		return s | 0x7c00
	case e < 113:
		if e < 102 {
			return s
		}
		m |= 0x800000
		sh := uint(126 - e)
		h := uint16(m >> sh)
		rem := m & (1<<sh - 1)
		half := uint32(1) << (sh - 1)
		if rem > half || (rem == half && h&1 == 1) {
			h++
		}
		return s | h
	}
	h := uint16(e-112)<<10 | uint16(m>>13)
	rem := m & 0x1fff
	if rem > 0x1000 || (rem == 0x1000 && h&1 == 1) {
		h++
	}
	return s | h
}

func writeSafetensors(path, dtype string, w, rows, cols int, order []int, gen func(int, func([]byte))) {
	type ent struct {
		DType   string   `json:"dtype"`
		Shape   []int    `json:"shape"`
		Offsets [2]int64 `json:"data_offsets"`
	}
	var names []string
	hdr := map[string]ent{}
	off := int64(0)
	size := int64(rows) * int64(cols) * int64(w)
	for _, t := range order {
		nm := fmt.Sprintf("layers.%d.weight", t)
		names = append(names, nm)
		hdr[nm] = ent{dtype, []int{rows, cols}, [2]int64{off, off + size}}
		off += size
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, nm := range names {
		if i > 0 {
			sb.WriteByte(',')
		}
		b, _ := json.Marshal(hdr[nm])
		fmt.Fprintf(&sb, "%q:%s", nm, b)
	}
	sb.WriteByte('}')
	for sb.Len()%8 != 0 {
		sb.WriteByte(' ')
	}
	f, err := os.Create(path)
	must(err)
	bw := bufio.NewWriterSize(f, 4<<20)
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(sb.Len()))
	bw.Write(l[:])
	bw.WriteString(sb.String())
	for _, t := range order {
		gen(t, func(b []byte) { bw.Write(b) })
	}
	must(bw.Flush())
	must(f.Close())
}

func writeBig(path string, rows, cols int, gen func(func([]byte))) {
	h := fmt.Sprintf(`{"big":{"dtype":"BF16","shape":[%d,%d],"data_offsets":[0,%d]}}`, rows, cols, int64(rows)*int64(cols)*2)
	for len(h)%8 != 0 {
		h += " "
	}
	f, err := os.Create(path)
	must(err)
	bw := bufio.NewWriterSize(f, 4<<20)
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(len(h)))
	bw.Write(l[:])
	bw.WriteString(h)
	gen(func(b []byte) { bw.Write(b) })
	must(bw.Flush())
	must(f.Close())
}
