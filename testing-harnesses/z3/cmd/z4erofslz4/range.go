package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"zeros3-z3/lz4z3"
)

type rangeEntry struct {
	Start   int64
	Length  int
	Payload int
	Stored  int
	Offset  int64
	Plain   int
	Padding int
	Digest  [32]byte
}

func makeRangeIndex(raw []byte, image io.ReaderAt, size int64) ([]rangeEntry, error) {
	const head = len(format) + 8 + 2*sha256.Size
	if len(raw) < head || string(raw[:len(format)]) != format || binary.LittleEndian.Uint64(raw[len(format):]) != uint64(size) {
		return nil, errors.New("wrong EROFS range recipe")
	}
	var row []rangeEntry
	cursor := head
	var out int64
	loadU64 := func() (uint64, error) {
		n, count := binary.Uvarint(raw[cursor:])
		if count < 1 {
			return 0, errors.New("invalid cluster recipe length")
		}
		cursor += count
		return n, nil
	}
	var b [4096]byte
	add := func(e rangeEntry) error {
		orig := b[:e.Length]
		if _, err := image.ReadAt(orig, out); err != nil {
			return err
		}
		e.Start, e.Digest = out, sha256.Sum256(orig)
		row = append(row, e)
		out += int64(e.Length)
		return nil
	}
	for cursor < len(raw) && out < size {
		tag := raw[cursor]
		cursor++
		switch tag {
		case 0:
			n, err := loadU64()
			if err != nil || n == 0 || n > uint64(len(raw)-cursor) || n > uint64(size-out) {
				return nil, errors.New("invalid EROFS literal")
			}
			for n != 0 {
				take := int(min(n, uint64(len(b))))
				if err := add(rangeEntry{Length: take, Payload: cursor, Stored: take}); err != nil {
					return nil, err
				}
				cursor += take
				n -= uint64(take)
			}
		case 1:
			var fields [4]uint64
			for i := range fields {
				v, err := loadU64()
				if err != nil {
					return nil, err
				}
				fields[i] = v
			}
			offset, plain, padding, stored := fields[0], fields[1], fields[2], fields[3]
			if offset > 1<<40 || plain == 0 || plain > 1<<20 || padding >= 4096 || stored > uint64(len(raw)-cursor) || size-out < 4096 {
				return nil, errors.New("invalid EROFS indexed frame")
			}
			e := rangeEntry{Length: 4096, Payload: cursor, Stored: int(stored), Offset: int64(offset), Plain: int(plain), Padding: int(padding)}
			if err := add(e); err != nil {
				return nil, err
			}
			cursor += int(stored)
		default:
			return nil, errors.New("unrecognized EROFS frame type")
		}
	}
	if cursor != len(raw) || out != size {
		return nil, errors.New("EROFS index does not cover image exactly")
	}
	return row, nil
}

func indexedErofsRange(recipe []byte, basis io.ReaderAt, index []rangeEntry, off, n int64) ([]byte, int64, int, error) {
	output := make([]byte, 0, n)
	var sourceBytes int64
	frames := 0
	at := sort.Search(len(index), func(i int) bool { return index[i].Start+int64(index[i].Length) > off })
	for _, e := range index[at:] {
		if e.Start >= off+n {
			break
		}
		b := recipe[e.Payload : e.Payload+e.Stored]
		if e.Plain > 0 {
			data := make([]byte, e.Plain)
			if _, err := basis.ReadAt(data, e.Offset); err != nil {
				return nil, 0, 0, err
			}
			sourceBytes += int64(len(data))
			var lim lz4z3.Limits
			record, consumed, err := lz4z3.Unmarshal(b, lim)
			if err != nil || consumed != len(b) {
				return nil, 0, 0, errors.New("invalid indexed cluster recipe")
			}
			c, err := record.Replay(nil, data, lim)
			if err != nil || len(c)+e.Padding != e.Length {
				return nil, 0, 0, errors.New("wrong indexed compressed size")
			}
			b = append(make([]byte, e.Padding, e.Length), c...)
		}
		if sha256.Sum256(b) != e.Digest {
			return nil, 0, 0, errors.New("indexed EROFS extent digest mismatch")
		}
		a, z := max(off-e.Start, 0), min(off+n-e.Start, int64(e.Length))
		output = append(output, b[a:z]...)
		frames++
	}
	if int64(len(output)) != n {
		return nil, 0, 0, errors.New("indexed EROFS range truncated")
	}
	return output, sourceBytes, frames, nil
}

func runIndexedRange(imagePath, sourcePath, recipePath, prefix string) error {
	image, err := os.Open(imagePath)
	if err != nil {
		return err
	}
	defer image.Close()
	orig, size, err := digestFile(image)
	if err != nil {
		return err
	}
	basis, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer basis.Close()
	stamp, _, err := digestFile(basis)
	if err != nil {
		return err
	}
	re, err := os.Stat(recipePath)
	if err != nil || re.Size() > 64<<20 {
		return errors.New("range probe requires a bounded recipe")
	}
	raw, err := os.ReadFile(recipePath)
	if err != nil {
		return err
	}
	index, err := makeRangeIndex(raw, image, size)
	if err != nil {
		return err
	}
	i := len(format) + 8
	if !bytes.Equal(raw[i:i+32], stamp[:]) || !bytes.Equal(raw[i+32:i+64], orig[:]) {
		return errors.New("EROFS recipe source or image digest mismatch")
	}
	type result struct {
		Offset    int64 `json:"offset"`
		BaseBytes int64 `json:"basis_bytes"`
		Micros    int64 `json:"us"`
		Frames    int   `json:"frames"`
	}
	var samples []result
	bin := []byte("Z3EIX1")
	bin = append(bin, orig[:]...)
	bin = append(bin, stamp[:]...)
	for _, e := range index {
		bin = binary.AppendUvarint(bin, uint64(e.Start))
		bin = binary.AppendUvarint(bin, uint64(e.Length))
		bin = binary.AppendUvarint(bin, uint64(e.Payload))
		bin = binary.AppendUvarint(bin, uint64(e.Stored))
		bin = binary.AppendUvarint(bin, uint64(e.Offset))
		bin = binary.AppendUvarint(bin, uint64(e.Plain))
		bin = binary.AppendUvarint(bin, uint64(e.Padding))
		bin = append(bin, e.Digest[:]...)
	}
	sum := sha256.Sum256(bin)
	bin = append(bin, sum[:]...)
	if err := os.WriteFile(prefix+".index", bin, 0600); err != nil {
		return err
	}
	var expected [4096]byte
	for i := int64(0); i < 32; i++ {
		off := i*(size-4096)/31 + min(32, (size-4096)%31)
		off = min(off, size-4096)
		if _, err := image.ReadAt(expected[:], off); err != nil {
			return err
		}
		t0 := time.Now()
		got, reads, frames, err := indexedErofsRange(raw, basis, index, off, 4096)
		elapsed := time.Since(t0)
		if err != nil || !bytes.Equal(got, expected[:]) {
			return fmt.Errorf("EROFS range offset %d changed: %v", off, err)
		}
		samples = append(samples, result{Offset: off, BaseBytes: reads, Micros: elapsed.Microseconds(), Frames: frames})
	}
	report := struct {
		IndexBytes int      `json:"serialized_index_bytes"`
		Entries    int      `json:"indexed_frames"`
		Samples    []result `json:"exact_4k_ranges"`
	}{len(bin), len(index), samples}
	return json.NewEncoder(os.Stdout).Encode(report)
}
