package brotliz3

import "math"

// Entropy coding helpers of the reference encoder: entropy estimates, length-limited Huffman
// trees, the RLE-friendly count smoothing and the code-length code.

var log2Table = func() (t [256]float64) {
	for i := 2; i < 256; i++ { // the C table holds float-precision literals
		t[i] = float64(float32(math.Log2(float64(i))))
	}
	return
}()

func fastLog2(v uint64) float64 {
	if v < 256 {
		return log2Table[v]
	}
	return log2Int(v)
}

// bitsEntropy is BrotliBitsEntropy. Products are rounded before they are accumulated (no FMA).
func bitsEntropy(pop []uint32) float64 {
	var sum uint64
	var r float64
	for _, p := range pop {
		sum += uint64(p)
		r -= float64(float64(p) * fastLog2(uint64(p)))
	}
	if sum != 0 {
		r += float64(float64(sum) * fastLog2(sum))
	}
	if r < float64(sum) {
		r = float64(sum)
	}
	return r
}

// estimateEntropy is encode.c's EstimateEntropy (Shannon entropy times the total).
func estimateEntropy(pop []uint32) float64 {
	var total uint64
	var r float64
	for _, p := range pop {
		total += uint64(p)
		r += float64(float64(p) * fastLog2(uint64(p)))
	}
	return float64(float64(total)*fastLog2(total)) - r
}

type huffNode struct {
	count       uint32
	left, right int16 // right doubles as the symbol of a leaf
}

func setDepth(p0 int, pool []huffNode, depth []uint8, maxDepth int) bool {
	var stack [16]int
	level, p := 0, p0
	stack[0] = -1
	for {
		if pool[p].left >= 0 {
			level++
			if level > maxDepth {
				return false
			}
			stack[level] = int(pool[p].right)
			p = int(pool[p].left)
			continue
		}
		depth[pool[p].right] = uint8(level)
		for level >= 0 && stack[level] == -1 {
			level--
		}
		if level < 0 {
			return true
		}
		p = stack[level]
		stack[level] = -1
	}
}

var shellGaps = [6]int{132, 57, 23, 10, 4, 1}

func huffLess(a, b *huffNode) bool {
	if a.count != b.count {
		return a.count < b.count
	}
	return a.right > b.right
}

func sortHuff(items []huffNode) {
	n := len(items)
	if n < 13 {
		for i := 1; i < n; i++ {
			tmp := items[i]
			k := i
			for j := i - 1; j >= 0 && huffLess(&tmp, &items[j]); j-- {
				items[k] = items[j]
				k = j
			}
			items[k] = tmp
		}
		return
	}
	g := 0
	if n < 57 {
		g = 2
	}
	for ; g < 6; g++ {
		gap := shellGaps[g]
		for i := gap; i < n; i++ {
			j := i
			tmp := items[i]
			for ; j >= gap && huffLess(&tmp, &items[j-gap]); j -= gap {
				items[j] = items[j-gap]
			}
			items[j] = tmp
		}
	}
}

// createHuffmanTree is BrotliCreateHuffmanTree: depth[] for data[] limited to treeLimit bits.
func createHuffmanTree(data []uint32, treeLimit int, tree []huffNode, depth []uint8) {
	sentinel := huffNode{count: math.MaxUint32, left: -1, right: -1}
	for limit := uint32(1); ; limit *= 2 {
		n := 0
		for i := len(data) - 1; i >= 0; i-- {
			if data[i] != 0 {
				tree[n] = huffNode{count: max(data[i], limit), left: -1, right: int16(i)}
				n++
			}
		}
		if n == 1 {
			depth[tree[0].right] = 1
			return
		}
		sortHuff(tree[:n])
		tree[n], tree[n+1] = sentinel, sentinel
		i, j := 0, n+1
		for k := n - 1; k != 0; k-- {
			var left, right int
			if tree[i].count <= tree[j].count {
				left = i
				i++
			} else {
				left = j
				j++
			}
			if tree[i].count <= tree[j].count {
				right = i
				i++
			} else {
				right = j
				j++
			}
			je := 2*n - k
			tree[je] = huffNode{count: tree[left].count + tree[right].count, left: int16(left), right: int16(right)}
			tree[je+1] = sentinel
		}
		if setDepth(2*n-1, tree, depth, treeLimit) {
			return
		}
	}
}

func reverseBits(n uint, b uint16) uint16 {
	var r uint16
	for i := uint(0); i < n; i++ {
		r = r<<1 | b&1
		b >>= 1
	}
	return r
}

// depthsToSymbols is BrotliConvertBitDepthsToSymbols (bit-reversed canonical codes).
func depthsToSymbols(depth []uint8, out []uint16) {
	var blCount, next [16]uint16
	for _, d := range depth {
		blCount[d]++
	}
	blCount[0] = 0
	code := 0
	for i := 1; i < 16; i++ {
		code = (code + int(blCount[i-1])) << 1
		next[i] = uint16(code)
	}
	for i, d := range depth {
		if d != 0 {
			out[i] = reverseBits(uint(d), next[d])
			next[d]++
		}
	}
}

// optimizeHuffmanCountsForRle is BrotliOptimizeHuffmanCountsForRle (size_t arithmetic kept).
func optimizeHuffmanCountsForRle(counts []uint32, good []uint8) {
	length := len(counts)
	nz := 0
	for _, c := range counts {
		if c != 0 {
			nz++
		}
	}
	if nz < 16 {
		return
	}
	for length != 0 && counts[length-1] == 0 {
		length--
	}
	if length == 0 {
		return
	}
	nonzeros, smallest := 0, uint32(1<<30)
	for i := 0; i < length; i++ {
		if counts[i] != 0 {
			nonzeros++
			if smallest > counts[i] {
				smallest = counts[i]
			}
		}
	}
	if nonzeros < 5 {
		return
	}
	if smallest < 4 && length-nonzeros < 6 {
		for i := 1; i < length-1; i++ {
			if counts[i-1] != 0 && counts[i] == 0 && counts[i+1] != 0 {
				counts[i] = 1
			}
		}
	}
	if nonzeros < 28 {
		return
	}
	for i := 0; i < length; i++ {
		good[i] = 0
	}
	symbol, step := counts[0], 0
	for i := 0; i <= length; i++ {
		if i == length || counts[i] != symbol {
			if (symbol == 0 && step >= 5) || (symbol != 0 && step >= 7) {
				for k := 0; k < step; k++ {
					good[i-k-1] = 1
				}
			}
			step = 1
			if i != length {
				symbol = counts[i]
			}
		} else {
			step++
		}
	}
	const streak = 1240
	stride := uint64(0)
	limit := uint64(256*(counts[0]+counts[1]+counts[2])/3 + 420) // uint32 arithmetic as in C
	sum := uint64(0)
	for i := 0; i <= length; i++ {
		if i == length || good[i] != 0 || (i != 0 && good[i-1] != 0) ||
			uint64(256*counts[i])-limit+streak >= 2*streak {
			if stride >= 4 || (stride >= 3 && sum == 0) {
				count := (sum + stride/2) / stride
				if count == 0 {
					count = 1
				}
				if sum == 0 {
					count = 0
				}
				for k := uint64(0); k < stride; k++ {
					counts[i-int(k)-1] = uint32(count)
				}
			}
			stride, sum = 0, 0
			if i < length-2 {
				limit = uint64(256*(counts[i]+counts[i+1]+counts[i+2])/3 + 420)
			} else if i < length {
				limit = uint64(256 * counts[i])
			} else {
				limit = 0
			}
		}
		stride++
		if i != length {
			sum += uint64(counts[i])
			if stride >= 4 {
				limit = (256*sum + stride/2) / stride
			}
			if stride == 4 {
				limit += 120
			}
		}
	}
}

func reverseU8(v []uint8) {
	for i, j := 0, len(v)-1; i < j; i, j = i+1, j-1 {
		v[i], v[j] = v[j], v[i]
	}
}

// writeHuffmanTree is BrotliWriteHuffmanTree: the code-length symbol sequence with RLE codes.
func writeHuffmanTree(depth []uint8, tree, extra []uint8) ([]uint8, []uint8) {
	length := len(depth)
	newLen := length
	for i := 0; i < length; i++ {
		if depth[length-i-1] != 0 {
			break
		}
		newLen--
	}
	rleNZ, rleZ := false, false
	if length > 50 {
		var totZ, totNZ, cntZ, cntNZ = 0, 0, 1, 1
		for i := 0; i < newLen; {
			v, reps := depth[i], 1
			for k := i + 1; k < newLen && depth[k] == v; k++ {
				reps++
			}
			if reps >= 3 && v == 0 {
				totZ += reps
				cntZ++
			}
			if reps >= 4 && v != 0 {
				totNZ += reps
				cntNZ++
			}
			i += reps
		}
		rleNZ, rleZ = totNZ > cntNZ*2, totZ > cntZ*2
	}
	prev := uint8(8)
	put := func(v, x uint8) {
		tree = append(tree, v)
		extra = append(extra, x)
	}
	for i := 0; i < newLen; {
		v, reps := depth[i], 1
		if (v != 0 && rleNZ) || (v == 0 && rleZ) {
			for k := i + 1; k < newLen && depth[k] == v; k++ {
				reps++
			}
		}
		i += reps
		if v == 0 {
			if reps == 11 {
				put(0, 0)
				reps--
			}
			if reps < 3 {
				for k := 0; k < reps; k++ {
					put(0, 0)
				}
			} else {
				start := len(tree)
				reps -= 3
				for {
					put(17, uint8(reps&7))
					reps >>= 3
					if reps == 0 {
						break
					}
					reps--
				}
				reverseU8(tree[start:])
				reverseU8(extra[start:])
			}
			continue
		}
		if prev != v {
			put(v, 0)
			reps--
		}
		if reps == 7 {
			put(v, 0)
			reps--
		}
		if reps < 3 {
			for k := 0; k < reps; k++ {
				put(v, 0)
			}
		} else {
			start := len(tree)
			reps -= 3
			for {
				put(16, uint8(reps&3))
				reps >>= 2
				if reps == 0 {
					break
				}
				reps--
			}
			reverseU8(tree[start:])
			reverseU8(extra[start:])
		}
		prev = v
	}
	return tree, extra
}
