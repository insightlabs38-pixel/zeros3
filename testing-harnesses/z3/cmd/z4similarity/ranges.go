package main

import (
	"fmt"
	"os"
	"zeros3-z3/lab"
)

// Compare equal cold requests at beginning/middle/end. Bytes are compressed
// record payloads, matching the delta closure metric (no decoded-block cache).
func baselineRanges(f Family) {
	read, logical := map[string]int64{}, map[string]int64{}
	for _, path := range f.Paths {
		data, e := os.ReadFile(path)
		must(e)
		for mode, params := range map[string][3]int{"cdc": {16 << 10, 64 << 10, 256 << 10}, "fine": {2 << 10, 8 << 10, 32 << 10}} {
			chunks := lab.ChunkScaled(data, params[0], params[1], params[2])
			cost := make([]int64, len(chunks))
			for i, c := range chunks {
				cost[i] = lab.PackedLen(c)
			}
			for _, size := range []int{4096, 65536, 1048576} {
				key := fmt.Sprintf("%s_%d", mode, size)
				for _, position := range []int{0, len(data) / 2, max(0, len(data)-size)} {
					left, skip := size, position
					for i, c := range chunks {
						if skip >= len(c) {
							skip -= len(c)
							continue
						}
						if left <= 0 {
							break
						}
						n := min(left, len(c)-skip)
						logical[key] += int64(n)
						read[key] += cost[i]
						left -= n
						skip = 0
					}
				}
			}
		}
	}
	amp := map[string]float64{}
	for k, n := range logical {
		if n > 0 {
			amp[k] = float64(read[k]) / float64(n)
		}
	}
	emit(map[string]any{"family": f.Name, "range_payload_amplification": amp})
}
