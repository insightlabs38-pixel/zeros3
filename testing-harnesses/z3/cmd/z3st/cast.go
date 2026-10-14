package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"strings"

	"zeros3-z3/tdelta"
)

// f16to32 is the exact binary16 -> binary32 widening (no rounding involved).
func f16to32(h uint16) uint32 {
	s := uint32(h>>15) << 31
	e := uint32(h >> 10 & 31)
	m := uint32(h & 1023)
	switch {
	case e == 0 && m == 0:
		return s
	case e == 0: // subnormal: normalise
		sh := uint32(0)
		for m&1024 == 0 {
			m <<= 1
			sh++
		}
		m &= 1023
		return s | (113-sh)<<23 | m<<13
	case e == 31:
		return s | 0xff<<23 | m<<13
	}
	return s | (e+112)<<23 | m<<13
}

// f32toBF16 rounds binary32 to bfloat16, nearest even (NaN stays NaN).
func f32toBF16(b uint32) uint16 {
	if b&0x7fffffff > 0x7f800000 {
		return uint16(b>>16) | 0x40
	}
	return uint16((b + 0x7fff + (b >> 16 & 1)) >> 16)
}

// castCmd: does a dtype-converted copy of the base explain a fine-tune stored in
// another dtype? F16 -> F32 is exact; F16 -> BF16 goes through one rounding. The
// cast is done on bit patterns with integer arithmetic only.
func castCmd(args []string) {
	fs := flag.NewFlagSet("cast", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "rw-pythia14m-siblings,rw-pythia14m-sft-koshirowada,rw-pythia14m-logs-honicky", "family globs")
	baseFam := fs.String("base-family", "rw-pythia14m-train", "family holding the base checkpoint")
	baseLabel := fs.String("base-label", "step143000", "base checkpoint label")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	bf, _ := load(*dir, []string{*baseFam})
	var base *ckpt
	for _, c := range bf[*baseFam] {
		if c.label == *baseLabel {
			base = c
		}
	}
	if base == nil {
		fatal(fmt.Errorf("base %s not found", *baseLabel))
	}
	fmt.Println("| family | target | target dtype | base dtype | elements | bit-identical to the cast base % | bytes-equal % | median log2(1+ULP distance) |")
	fmt.Println("|---|---|---|---|---:|---:|---:|---:|")
	for _, fam := range order {
		for _, c := range cks[fam] {
			var tot tdelta.Stats
			td := ""
			for i := range c.f.Tensors {
				tb := &c.f.Tensors[i]
				ta, ok := base.f.Lookup(tb.Name)
				if !ok || !equalInts(ta.Shape, tb.Shape) || ta.DType != "F16" || (tb.DType != "F32" && tb.DType != "BF16" && tb.DType != "F16") {
					continue
				}
				pa, pb := base.payload(ta), c.payload(tb)
				td = tb.DType
				var cast []byte
				switch tb.DType {
				case "F16":
					cast = pa
				case "F32":
					cast = make([]byte, 0, 2*len(pa))
					for j := 0; j < len(pa); j += 2 {
						cast = binary.LittleEndian.AppendUint32(cast, f16to32(binary.LittleEndian.Uint16(pa[j:])))
					}
				case "BF16":
					cast = make([]byte, 0, len(pa))
					for j := 0; j < len(pa); j += 2 {
						cast = binary.LittleEndian.AppendUint16(cast, f32toBF16(f16to32(binary.LittleEndian.Uint16(pa[j:]))))
					}
				}
				if len(cast) != len(pb) {
					continue
				}
				s := tdelta.Compare(tb.DType, cast, pb, tb.Bits/8, tb.Shape)
				addStats(&tot, &s)
			}
			if tot.Elems == 0 {
				continue
			}
			fmt.Printf("| %s | %s | %s | F16 | %d | %s | %s | %d |\n", strings.TrimPrefix(fam, "rw-"), c.label, td, tot.Elems, pct2(tot.EqualElems, tot.Elems), pct2(tot.EqualBytes, tot.Bytes), medianLog2(tot.DistLog2))
		}
	}
}

var _ = math.Pi
