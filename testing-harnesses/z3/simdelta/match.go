package simdelta

import "encoding/binary"

// Fixed-width anchor hashes are filters only: match extension compares bytes.
// Four locations per hash bound adversarial collision/repetition work. Striding
// the base bounds index size; target probes every displacement.
func anchorHash(b []byte) uint64 {
	var h uint64 = 1469598103934665603
	for _, v := range b {
		h = (h ^ uint64(v)) * 1099511628211
	}
	return h
}

type loc struct{ base, off int }

func match(bases [][]byte, t []byte, anchor, stride, slots int) []op {

	// A compact open-addressed index avoids one heap allocation per anchor.
	// Bound retained anchors, adapting base stride for large multi-base inputs.
	total := 0
	for _, b := range bases {
		total += len(b)
	}
	if minStride := (total + (1 << 18) - 1) / (1 << 18); stride < minStride {
		stride = minStride
	}
	capacity := 1
	for capacity < 2*(total/stride+len(bases)+1) {
		capacity *= 2
	}
	type bucket struct {
		hash      uint64
		positions [4]uint32
		count     uint8
	}
	table := make([]bucket, capacity)
	mask := uint64(capacity - 1)
	for b, s := range bases {
		for i := 0; i+anchor <= len(s); i += stride {
			h := anchorHash(s[i : i+anchor])
			slot := h & mask
			for probe := 0; probe < 8; probe++ {
				v := &table[slot]
				if v.count == 0 {
					v.hash = h
				}
				if v.hash == h {
					if int(v.count) < slots {
						v.positions[v.count] = uint32(b)<<24 | uint32(i)
						v.count++
					}
					break
				}
				slot = (slot + 1) & mask
			}
		}
	}

	var ops []op
	start := 0
	for i := 0; i+anchor <= len(t); {
		best := loc{}
		n := 0
		h := anchorHash(t[i : i+anchor])

		slot := h & mask
		for probe := 0; probe < 8; probe++ {
			v := &table[slot]
			if v.count == 0 {
				break
			}
			if v.hash != h {
				slot = (slot + 1) & mask
				continue
			}
			for _, pos := range v.positions[:v.count] {
				p := loc{int(pos >> 24), int(pos & 0xffffff)}
				b := bases[p.base]
				k := 0
				for p.off+k < len(b) && i+k < len(t) && b[p.off+k] == t[i+k] {
					k++
				}
				if k > n {
					n = k
					best = p
				}
			}
			break
		}

		if n < anchor {
			i++
			continue
		}
		if i > start {
			ops = append(ops, op{lit: t[start:i]})
		}
		ops = append(ops, op{base: best.base, off: best.off, n: n})
		i += n
		start = i
	}
	if start < len(t) {
		ops = append(ops, op{lit: t[start:]})
	}
	return ops
}

// Features retains distinct bottom-k content windows. Repeated byte strings do
// not fill the sketch. Windows are sampled by their content, never position.
func Features(b []byte, k int) []uint64 {
	if k <= 0 || k > 64 {
		return nil
	}
	var out []uint64
	const mul uint64 = 0x9e3779b97f4a7c15
	pow := uint64(1)
	for i := 0; i < 15; i++ {
		pow *= mul
	}
	var rolling uint64
	if len(b) >= 16 {
		for _, v := range b[:16] {
			rolling = rolling*mul + uint64(v) + 1
		}
	}
	for i := 0; i+16 <= len(b); i++ {
		if i > 0 {
			rolling = (rolling-(uint64(b[i-1])+1)*pow)*mul + uint64(b[i+15]) + 1
		}
		h := mix(rolling)
		if len(out) == k && h >= out[len(out)-1] {
			continue
		}
		j := 0
		for j < len(out) && out[j] < h {
			j++
		}
		if j < len(out) && out[j] == h {
			continue
		}
		out = append(out, 0)
		copy(out[j+1:], out[j:])
		out[j] = h
		if len(out) > k {
			out = out[:k]
		}
	}
	return out
}

// Superfeatures combine nearby order statistics into four independent bands.
func Superfeatures(f []uint64) []uint64 {
	var out []uint64
	for i := 0; i+3 < len(f); i += 4 {
		var b [32]byte
		for j := 0; j < 4; j++ {
			binary.LittleEndian.PutUint64(b[j*8:], f[i+j])
		}
		out = append(out, anchorHash(b[:]))
	}
	return out
}
func Edges(b []byte) []uint64 {
	if len(b) < 32 {
		return Features(b, 4)
	}
	return []uint64{anchorHash(b[:32]), anchorHash(b[len(b)-32:])}
}

func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// BlockFeatures sample local content, retaining recall when a large component
// contains a similar subregion that loses the global bottom-k competition.
func BlockFeatures(b []byte) []uint64 {
	var out []uint64
	for i := 0; i < len(b) && len(out) < 64; i += 4096 {
		end := i + 4096
		if end > len(b) {
			end = len(b)
		}
		out = append(out, Features(b[i:end], 1)...)
	}
	return out
}
