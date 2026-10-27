package snappyz3

// The deterministic reference encoder: a port of the Go snappy implementation's
// encode_other.go (golang/snappy, BSD-3-Clause; the copy previously lived in eraez3,
// which now calls this one). It is NOT trusted to reproduce any producer's blocks:
// sstz3 compares its output with each stored block and, on equality, records a single
// "predicted" flag instead of the command list. Any block it does not reproduce keeps
// its explicit command recipe, so a difference in encoder version or algorithm costs
// bytes, never exactness.

import "encoding/binary"

const (
	tagLiteral = 0x00
	tagCopy1   = 0x01
	tagCopy2   = 0x02
)

func load32(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

func load64(b []byte, i int) uint64 {
	return uint64(load32(b, i)) | uint64(load32(b, i+4))<<32
}

func hash(u, shift uint32) uint32 { return (u * 0x1e35a7bd) >> shift }

func emitLiteral(dst, lit []byte) int {
	i, n := 0, uint(len(lit)-1)
	switch {
	case n < 60:
		dst[0] = uint8(n)<<2 | tagLiteral
		i = 1
	case n < 1<<8:
		dst[0] = 60<<2 | tagLiteral
		dst[1] = uint8(n)
		i = 2
	default:
		dst[0] = 61<<2 | tagLiteral
		dst[1] = uint8(n)
		dst[2] = uint8(n >> 8)
		i = 3
	}
	return i + copy(dst[i:], lit)
}

func emitCopy(dst []byte, offset, length int) int {
	i := 0
	for length >= 68 {
		dst[i+0] = 63<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		i += 3
		length -= 64
	}
	if length > 64 {
		dst[i+0] = 59<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		i += 3
		length -= 60
	}
	if length >= 12 || offset >= 2048 {
		dst[i+0] = uint8(length-1)<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		return i + 3
	}
	dst[i+0] = uint8(offset>>8)<<5 | uint8(length-4)<<2 | tagCopy1
	dst[i+1] = uint8(offset)
	return i + 2
}

func encodeBlock(dst, src []byte) (d int) {
	const (
		inputMargin            = 16 - 1
		minNonLiteralBlockSize = 1 + 1 + inputMargin
		maxTableSize           = 1 << 14
		tableMask              = maxTableSize - 1
	)
	if len(src) < minNonLiteralBlockSize {
		return emitLiteral(dst, src)
	}
	shift := uint32(32 - 8)
	for tableSize := 1 << 8; tableSize < maxTableSize && tableSize < len(src); tableSize *= 2 {
		shift--
	}
	var table [maxTableSize]uint16
	sLimit := len(src) - inputMargin
	nextEmit := 0
	s := 1
	nextHash := hash(load32(src, s), shift)
	for {
		skip := 32
		nextS := s
		candidate := 0
		for {
			s = nextS
			bytesBetweenHashLookups := skip >> 5
			nextS = s + bytesBetweenHashLookups
			skip += bytesBetweenHashLookups
			if nextS > sLimit {
				goto emitRemainder
			}
			candidate = int(table[nextHash&tableMask])
			table[nextHash&tableMask] = uint16(s)
			nextHash = hash(load32(src, nextS), shift)
			if load32(src, s) == load32(src, candidate) {
				break
			}
		}
		d += emitLiteral(dst[d:], src[nextEmit:s])
		for {
			base := s
			s += 4
			for i := candidate + 4; s < len(src) && src[i] == src[s]; i, s = i+1, s+1 {
			}
			d += emitCopy(dst[d:], base-candidate, s-base)
			nextEmit = s
			if s >= sLimit {
				goto emitRemainder
			}
			x := load64(src, s-1)
			prevHash := hash(uint32(x>>0), shift)
			table[prevHash&tableMask] = uint16(s - 1)
			currHash := hash(uint32(x>>8), shift)
			candidate = int(table[currHash&tableMask])
			table[currHash&tableMask] = uint16(s)
			if uint32(x>>8) != load32(src, candidate) {
				nextHash = hash(uint32(x>>16), shift)
				s++
				break
			}
		}
	}
emitRemainder:
	if nextEmit < len(src) {
		d += emitLiteral(dst[d:], src[nextEmit:])
	}
	return d
}

// EncodeRef compresses src with the reference algorithm as one block (the callers'
// blocks are below the 64 KiB fragment size of the C++ encoder; larger inputs are not
// predicted).
func EncodeRef(src []byte) []byte {
	if len(src) == 0 {
		return []byte{0}
	}
	dst := make([]byte, binary.MaxVarintLen32+len(src)+len(src)/6+32)
	d := binary.PutUvarint(dst, uint64(len(src)))
	d += encodeBlock(dst[d:], src)
	return dst[:d]
}

// MaxPredict is the largest input EncodeRef is used for.
const MaxPredict = 1 << 16

// ---- C++ snappy 1.1.10 CompressFragment -------------------------------------------

func hashMask(u uint32, mask uint32, top uint) uint32 { return ((u * 0x1e35a7bd) >> (32 - top)) & mask }

// EncodeCPP compresses src as one block the way snappy 1.1.10's CompressFragment does
// (the library LevelDB links on current distributions). It differs from EncodeRef in the
// hash: the top 14 bits of the product masked to the table size rather than the top
// log2(table size) bits. Like EncodeRef it is only a predictor.
func EncodeCPP(src []byte) []byte { return encodeCPP(src, 14) }

// EncodeCPP15 is EncodeCPP with a 15-bit hash, which is what the snappy bundled with
// RocksDB 9.8.4 (rocksdict 0.3.29) uses: the same algorithm, hashing the top 15 bits of
// the product masked to the table size (found by comparing the 8 443 Snappy blocks of
// one RocksDB table set against hash widths 12-17).
func EncodeCPP15(src []byte) []byte { return encodeCPP(src, 15) }

func encodeCPP(src []byte, top uint) []byte {
	n := len(src)
	dst := make([]byte, binary.MaxVarintLen32+n+n/6+64)
	d := binary.PutUvarint(dst, uint64(n))
	if n == 0 {
		return dst[:d]
	}
	if n < 15 {
		d += emitLiteral(dst[d:], src)
		return dst[:d]
	}
	tableSize := 256
	for tableSize < 1<<top && tableSize < n {
		tableSize *= 2
	}
	mask := uint32(tableSize - 1)
	table := make([]uint16, tableSize)
	buf := append(append(make([]byte, 0, n+16), src...), make([]byte, 16)...) // loads may run 15 bytes past ip_limit
	ipLimit := n - 15
	ip, nextEmit := 0, 0
	preload := load32(buf, 1)
	for {
		nextEmit = ip
		ip++
		data := load64(buf, ip)
		skip := uint32(32)
		cand := 0
		found := false
		if ipLimit-ip >= 16 {
			delta := ip
		scan:
			for j := 0; j < 4; j++ {
				for k := 0; k < 4; k++ {
					i := 4*j + k
					dword := uint32(data)
					if i == 0 {
						dword = preload
					}
					h := hashMask(dword, mask, top)
					cand = int(table[h])
					table[h] = uint16(delta + i)
					if load32(buf, cand) == dword {
						d += emitLiteral(dst[d:], src[nextEmit:ip+i])
						ip += i
						found = true
						break scan
					}
					data >>= 8
				}
				data = load64(buf, ip+4*j+4)
			}
			if !found {
				ip += 16
				skip += 16
			}
		}
		if !found {
			for {
				data = load64(buf, ip)
				h := hashMask(uint32(data), mask, top)
				step := skip >> 5
				skip += step
				nextIP := ip + int(step)
				if nextIP > ipLimit {
					ip = nextEmit
					goto remainder
				}
				cand = int(table[h])
				table[h] = uint16(ip)
				if uint32(data) == load32(buf, cand) {
					break
				}
				ip = nextIP
			}
			d += emitLiteral(dst[d:], src[nextEmit:ip])
		}
		for {
			base := ip
			l := 4
			for ip+l < n && src[cand+l] == src[ip+l] {
				l++
			}
			ip += l
			d += emitCopy(dst[d:], base-cand, l)
			if ip >= ipLimit {
				goto remainder
			}
			data = load64(buf, ip)
			table[hashMask(load32(buf, ip-1), mask, top)] = uint16(ip - 1)
			h := hashMask(uint32(data), mask, top)
			cand = int(table[h])
			table[h] = uint16(ip)
			if uint32(data) != load32(buf, cand) {
				break
			}
		}
		preload = uint32(data >> 8)
	}
remainder:
	if ip < n {
		d += emitLiteral(dst[d:], src[ip:])
	}
	return dst[:d]
}
