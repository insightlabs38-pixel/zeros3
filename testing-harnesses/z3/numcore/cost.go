package numcore

import (
	"bytes"
	"compress/flate"
	"math"
	"math/bits"
)

// Planes splits w-byte words into w byte planes, most significant byte first.
func Planes(res []byte, w int) [][]byte {
	n := len(res) / w
	pl := make([][]byte, w)
	for k := 0; k < w; k++ {
		pl[k] = make([]byte, n)
		for i := 0; i < n; i++ {
			pl[k][i] = res[i*w+(w-1-k)]
		}
	}
	return pl
}

func Unplanes(pl [][]byte, w int) []byte {
	n := 0
	if len(pl) > 0 {
		n = len(pl[0])
	}
	out := make([]byte, n*w)
	for k := 0; k < w; k++ {
		for i := 0; i < n; i++ {
			out[i*w+(w-1-k)] = pl[k][i]
		}
	}
	return out
}

// Deflate returns the raw DEFLATE size at the default level, the production pack coder family.
func Deflate(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// DeflateLen is the size of DEFLATE(b) or len(b)+1 when it does not help.
func DeflateLen(b []byte) int {
	z := len(Deflate(b))
	if z < len(b) {
		return z
	}
	return len(b)
}

// H0 is the order-0 empirical entropy of b in bytes (ideal static coder, no table).
func H0(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var c [256]int
	for _, x := range b {
		c[x]++
	}
	n := float64(len(b))
	h := 0.0
	for _, v := range c {
		if v > 0 {
			p := float64(v) / n
			h -= float64(v) * math.Log2(p)
		}
	}
	return h / 8
}

// HCtx is the entropy of plane k conditioned on a magnitude bucket of the byte above it (ideal
// context coder bound, no model cost).
func HCtx(pl [][]byte) float64 {
	tot := 0.0
	for k := range pl {
		if k == 0 {
			tot += H0(pl[0])
			continue
		}
		var cnt [8][256]int
		var cn [8]int
		for i, x := range pl[k] {
			c := bits.Len8(pl[k-1][i])
			if c > 7 {
				c = 7
			}
			cnt[c][x]++
			cn[c]++
		}
		for c := 0; c < 8; c++ {
			for _, v := range cnt[c] {
				if v > 0 {
					tot -= float64(v) * math.Log2(float64(v)/float64(cn[c])) / 8
				}
			}
		}
	}
	return tot
}

// PlaneDeflate is the production-style cost of the plane-split residual: DEFLATE of the
// concatenated planes (kept raw when DEFLATE does not help).
func PlaneDeflate(res []byte, w int) int {
	pl := Planes(res, w)
	return DeflateLen(bytes.Join(pl, nil))
}
