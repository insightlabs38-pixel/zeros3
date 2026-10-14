package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
)

// A small copy/literal delta, enough to ask "how many bytes of cur are not a
// byte-for-byte substring of base" independent of chunk boundaries. Cost is
// literal bytes DEFLATEd plus ~6 bytes per copy op. It is a measuring
// instrument, not a candidate codec.

const dB = 24 // anchor length

type DeltaStat struct {
	Literal int // literal bytes before compression
	Copies  int
	Cost    int // DEFLATE(literals) + op overhead
	Runs    [][2]int
}

const dMul = 0x9E3779B97F4A7C15

func deltaCost(base, cur []byte) DeltaStat { return deltaRuns(base, cur, false) }

func deltaRuns(base, cur []byte, keep bool) DeltaStat {
	var st DeltaStat
	if len(cur) < dB || len(base) < dB {
		st.Literal = len(cur)
		st.Cost = len(cur)
		return st
	}
	// pow = dMul^(dB-1)
	pow := uint64(1)
	for i := 0; i < dB-1; i++ {
		pow *= dMul
	}
	idx := make(map[uint64]int32, len(base)/8)
	hash := func(b []byte) uint64 {
		var x uint64
		for _, c := range b {
			x = x*dMul + uint64(c) + 1
		}
		return x
	}
	roll := func(x uint64, out, in byte) uint64 {
		return (x-(uint64(out)+1)*pow)*dMul + uint64(in) + 1
	}
	x := hash(base[:dB])
	for i := 0; ; i++ {
		if i%8 == 0 {
			if _, ok := idx[x]; !ok {
				idx[x] = int32(i)
			}
		}
		if i+dB >= len(base) {
			break
		}
		x = roll(x, base[i], base[i+dB])
	}
	var lits []byte
	litStart := 0
	x = hash(cur[:dB])
	i := 0
	for i+dB <= len(cur) {
		if p, ok := idx[x]; ok && bytes.Equal(base[p:int(p)+dB], cur[i:i+dB]) {
			// extend forward and backward
			f := dB
			for int(p)+f < len(base) && i+f < len(cur) && base[int(p)+f] == cur[i+f] {
				f++
			}
			b := 0
			for int(p)-b > 0 && i-b > litStart && base[int(p)-b-1] == cur[i-b-1] {
				b++
			}
			lits = append(lits, cur[litStart:i-b]...)
			if keep && i-b > litStart {
				st.Runs = append(st.Runs, [2]int{litStart, i - b})
			}
			st.Copies++
			i += f
			litStart = i
			if i+dB <= len(cur) {
				x = hash(cur[i : i+dB])
			}
			continue
		}
		if i+dB < len(cur) {
			x = roll(x, cur[i], cur[i+dB])
		}
		i++
	}
	lits = append(lits, cur[litStart:]...)
	if keep && len(cur) > litStart {
		st.Runs = append(st.Runs, [2]int{litStart, len(cur)})
	}
	st.Literal = len(lits)
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(lits)
	w.Close()
	st.Cost = buf.Len() + st.Copies*6 + len(binary.AppendUvarint(nil, uint64(len(cur))))
	return st
}
