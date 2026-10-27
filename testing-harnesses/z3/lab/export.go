package lab

import "io"

// ChunkCDC streams r through the lab's copy of ZeroS3 CDC v1, calling fn with
// every chunk (a view valid only during the call) and its offset.
func ChunkCDC(r io.Reader, fn func(off int64, chunk []byte) error) error { return forEachChunk(r, fn) }

// PackedLen is the stored size under the production pack rule (DEFLATE, kept
// only if it saves at least 1/16).
func PackedLen(b []byte) int64 { return packedLen(b) }

// GearAt exposes the production gear table entry (laboratory sensitivity runs
// that scale the chunk-size parameters keep the same gear function).
func GearAt(b byte) uint64 { return gearTable[b] }

// ChunkScaled is the same gear CDC with scaled parameters (sensitivity runs; moved here from
// cmd/z3bin unchanged so later studies share it). target must be a power of two.
func ChunkScaled(b []byte, min, target, max int) [][]byte {
	bits := 0
	for 1<<bits < target {
		bits++
	}
	maskS, maskL := uint64(1)<<bits-1, uint64(1)<<(bits-1)-1
	var out [][]byte
	for len(b) > 0 {
		lim := len(b)
		if lim > max {
			lim = max
		}
		n := lim
		var h uint64
		for i := 0; i < lim; i++ {
			h = (h << 1) + gearTable[b[i]]
			sz := i + 1
			if sz < min {
				continue
			}
			m := maskL
			if sz < target {
				m = maskS
			}
			if h&m == 0 {
				n = sz
				break
			}
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

// IRRep returns the exact-DEFLATE reconstruction representation for spec (Z3-32 builds custom
// specs with an Inner transform).
func IRRep(spec IRSpec) Rep { return irRep(spec) }
