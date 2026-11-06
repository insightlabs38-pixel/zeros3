package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"zeros3-z3/simdelta"
)

// Aggregate is a physical experiment over exact exported deltas. Records remain
// independently addressable; an alternate whole-pack DEFLATE model explicitly
// charges decompressing/fetching the entire aggregate for a cold range.
func aggregate(dir string) {
	b, e := os.ReadFile(filepath.Join(dir, "roots.json"))
	must(e)
	var m manifest
	must(json.Unmarshal(b, &m))
	wires := map[[32]byte][]byte{}
	var order [][32]byte
	active := map[[32]byte]bool{}
	var visit func([32]byte)
	visit = func(h [32]byte) {
		if _, ok := wires[h]; ok {
			return
		}
		if active[h] {
			panic("aggregate cycle")
		}
		active[h] = true
		w, e := os.ReadFile(filepath.Join(dir, hex.EncodeToString(h[:])+".delta"))
		must(e)
		hd, e := simdelta.Inspect(w)
		must(e)
		if hd.Hash != h {
			panic("aggregate identity")
		}
		for _, b := range hd.Bases {
			visit(b)
		}
		delete(active, h)
		wires[h] = w
		order = append(order, h)
	}
	for _, root := range m.Roots {
		for _, h := range root {
			visit(h)
		}
	}
	for _, cap := range []int{1 << 20, 16 << 20, 64 << 20} {
		var packs [][]byte
		locations := map[[32]byte]int{}
		offsets := map[[32]byte]int{}
		var current []byte
		for _, h := range order {
			w := wires[h]
			if len(current) > 0 && len(current)+len(w)+44 > cap {
				packs = append(packs, current)
				current = nil
			}
			locations[h] = len(packs)
			offsets[h] = len(current)
			header := make([]byte, 44)
			copy(header, h[:])
			binary.LittleEndian.PutUint32(header[32:], uint32(len(w)))
			current = append(current, header...)
			current = append(current, w...)
		}
		if len(current) > 0 {
			packs = append(packs, current)
		}
		var independent, outer, raw int64
		encode := time.Now()
		for _, p := range packs {
			var z bytes.Buffer
			f, _ := flate.NewWriter(&z, 6)
			_, _ = f.Write(p)
			must(f.Close())
			reader := flate.NewReader(bytes.NewReader(z.Bytes()))
			round, e := io.ReadAll(reader)
			must(e)
			must(reader.Close())
			if !bytes.Equal(round, p) {
				panic("outer aggregate reconstruction")
			}
			outer += int64(z.Len() + 80)
			raw += int64(len(p) + 80)
		}
		encodeMS := float64(time.Since(encode).Microseconds()) / 1000
		independent = raw + int64(len(wires)*52)
		outer += int64(len(wires) * 52)
		// Verify every indexed record's exact payload hash and every native root
		// through the same authenticated graph resolver used by ordinary replay.
		get := func(h [32]byte) ([]byte, error) {
			p, ok := locations[h]
			if !ok {
				return nil, fmt.Errorf("missing aggregate member")
			}
			off := offsets[h]
			b := packs[p]
			if !bytes.Equal(b[off:off+32], h[:]) {
				return nil, fmt.Errorf("aggregate index mismatch")
			}
			n := int(binary.LittleEndian.Uint32(b[off+32:]))
			if n != len(wires[h]) {
				return nil, fmt.Errorf("aggregate length mismatch")
			}
			return b[off+44 : off+44+n], nil
		}
		for _, root := range m.Roots {
			for _, h := range root {
				v, e := simdelta.Resolve(h, get, 4, int64(simdelta.MaxBytes)*64)
				must(e)
				if sha256.Sum256(v) != h {
					panic("aggregate replay")
				}
			}
		}
		latest := map[[32]byte]bool{}
		var mark func([32]byte)
		mark = func(h [32]byte) {
			if latest[h] {
				return
			}
			latest[h] = true
			hd, e := simdelta.Inspect(wires[h])
			must(e)
			for _, b := range hd.Bases {
				mark(b)
			}
		}
		if len(m.Roots) > 0 {
			for _, h := range m.Roots[len(m.Roots)-1] {
				mark(h)
			}
		}
		livePacks := map[int]bool{}
		var liveBytes int64
		for h := range latest {
			livePacks[locations[h]] = true
			liveBytes += int64(len(wires[h]) + 96)
		}
		var pinned int64
		for p := range livePacks {
			pinned += int64(len(packs[p]) + 80)
		}
		type rangeRow struct {
			Logical                                                      int
			Requested, RecordReads, PackReads, WireBytes, WholePackBytes int64
		}
		var ranges []rangeRow
		for _, size := range []int{4096, 65536, 1048576, 0} {
			r := rangeRow{Logical: size}
			for rev, root := range m.Roots {
				left := size
				if size == 0 {
					left = m.Sizes[rev]
				}
				seenObjects := map[[32]byte]bool{}
				seenPacks := map[int]bool{}
				var touch func([32]byte)
				touch = func(h [32]byte) {
					if seenObjects[h] {
						return
					}
					seenObjects[h] = true
					r.RecordReads++
					r.WireBytes += int64(len(wires[h]))
					p := locations[h]
					if !seenPacks[p] {
						seenPacks[p] = true
						r.PackReads++
						r.WholePackBytes += int64(len(packs[p]))
					}
					hd, e := simdelta.Inspect(wires[h])
					must(e)
					for _, b := range hd.Bases {
						touch(b)
					}
				}
				for _, h := range root {
					if left <= 0 {
						break
					}
					hd, e := simdelta.Inspect(wires[h])
					must(e)
					n := hd.Size
					if n > left {
						n = left
					}
					left -= n
					r.Requested += int64(n)
					touch(h)
				}
			}
			ranges = append(ranges, r)
		}
		emit(map[string]any{"store": dir, "cap_bytes": cap, "objects": len(wires), "aggregates": len(packs), "independent_record_bytes": independent, "whole_pack_compressed_bytes": outer, "outer_encode_proof_ms": encodeMS, "latest_live_record_bytes": liveBytes, "latest_pinned_pack_bytes": pinned, "ranges": ranges})
	}
	fmt.Fprintln(os.Stderr, "aggregation done", dir)
}
