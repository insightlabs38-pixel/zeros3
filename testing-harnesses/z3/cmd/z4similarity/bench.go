package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"time"
	"zeros3-z3/simdelta"
)

// benchCodec isolates actual codec encoding from candidate discovery. These are
// sampled aligned pairs, not an assertion that discovery always finds them.
func benchCodec(f Family, algorithm string) {
	if len(f.Paths) < 2 {
		return
	}
	a, e := os.Open(f.Paths[0])
	must(e)
	defer a.Close()
	b, e := os.Open(f.Paths[len(f.Paths)-1])
	must(e)
	defer b.Close()
	as, e := a.Stat()
	must(e)
	bs, e := b.Stat()
	must(e)
	size := as.Size()
	if bs.Size() < size {
		size = bs.Size()
	}
	if size < 65536 {
		return
	}
	var bases, targets, wires [][]byte
	var logical, wire int64
	for i := 0; i < 20; i++ {
		off := int64(i) * (size - 65536) / 19
		off -= off % 4
		base, target := make([]byte, 65536), make([]byte, 65536)
		_, e = a.ReadAt(base, off)
		must(e)
		_, e = b.ReadAt(target, off)
		must(e)
		bases = append(bases, base)
		targets = append(targets, target)
		logical += 65536
	}
	t := time.Now()
	for i, target := range targets {
		w, e := simdelta.Encode(algorithm, [][]byte{bases[i]}, target)
		must(e)
		wires = append(wires, w)
		wire += int64(len(w) + 96)
	}
	encoding := time.Since(t)
	t = time.Now()
	for i, w := range wires {
		out, e := simdelta.Decode(w, func(h [32]byte) ([]byte, error) {
			if h != sha256.Sum256(bases[i]) {
				return nil, fmt.Errorf("base hash")
			}
			return bases[i], nil
		})
		must(e)
		if !bytes.Equal(out, targets[i]) {
			panic("codec benchmark exact")
		}
	}
	decoding := time.Since(t)
	emit(map[string]any{"family": f.Name, "algorithm": algorithm, "pairing": "aligned-sampled-64KiB", "samples": 20, "logical_bytes": logical, "wire_bytes_plus_records": wire, "encode_ms": float64(encoding.Microseconds()) / 1000, "decode_ms": float64(decoding.Microseconds()) / 1000})
}
