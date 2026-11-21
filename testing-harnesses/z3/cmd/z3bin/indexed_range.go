package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"zeros3-z3/binref"
	"zeros3-z3/dcore"
)

type executableSpan struct {
	Kind   byte
	Region int
	Offset int64
	Size   int64
}

func executableStreamSpans(wire []byte) ([]executableSpan, error) {
	if !bytes.HasPrefix(wire, []byte("ZEB1")) {
		return nil, fmt.Errorf("invalid executable stream magic")
	}
	left := wire[4:]
	count, step := binary.Uvarint(left)
	if step < 1 || count > 65536 {
		return nil, fmt.Errorf("invalid executable stream count")
	}
	left = left[step:]
	spans := make([]executableSpan, 0, count)
	for range count {
		if len(left) == 0 || int(left[0]) >= len(executableStreamKinds) {
			return nil, fmt.Errorf("invalid executable stream kind")
		}
		kind := left[0]
		region, step := binary.Varint(left[1:])
		if step < 1 || region < 0 || region > 1<<20 {
			return nil, fmt.Errorf("invalid executable stream region")
		}
		left = left[1+step:]
		size, step := binary.Uvarint(left)
		if step < 1 || size > uint64(len(left)-step) {
			return nil, fmt.Errorf("invalid executable stream length")
		}
		left = left[step:]
		spans = append(spans, executableSpan{kind, int(region), int64(len(wire) - len(left)), int64(size)})
		left = left[size:]
	}
	if len(left) != 0 {
		return nil, fmt.Errorf("trailing executable stream bytes")
	}
	return spans, nil
}

type indexedRange struct {
	Offset         int64 `json:"offset"`
	Length         int64 `json:"length"`
	ReplayUS       int64 `json:"replay_us"`
	BaseReadBytes  int64 `json:"base_read_bytes"`
	DeltaReadBytes int64 `json:"delta_read_bytes"`
	DecodedWindows int   `json:"decoded_windows"`
}

func measureExecutableIndex(path string, target, want []byte, record *dcore.WRecord, base io.ReaderAt, params dcore.WParams) (int, []indexedRange, error) {
	spans, err := executableStreamSpans(target)
	if err != nil {
		return 0, nil, err
	}
	type catalog struct {
		TargetSHA [32]byte
		Spans     []executableSpan
	}
	raw, err := json.Marshal(catalog{record.SHA, spans})
	if err != nil {
		return 0, nil, err
	}
	stamp := sha256.Sum256(raw)
	if err := os.WriteFile(path, append(raw, stamp[:]...), 0600); err != nil {
		return 0, nil, err
	}
	loaded, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	payload := loaded[:len(loaded)-sha256.Size]
	if sha256.Sum256(payload) != stamp || !bytes.Equal(loaded[len(payload):], stamp[:]) {
		return 0, nil, fmt.Errorf("executable index digest mismatch")
	}
	var opened catalog
	if err := json.Unmarshal(payload, &opened); err != nil || opened.TargetSHA != record.SHA {
		return 0, nil, fmt.Errorf("executable index identity mismatch: %v", err)
	}
	type key struct {
		kind   string
		region int
	}
	lookup := make(map[key]executableSpan, len(opened.Spans))
	for _, span := range opened.Spans {
		if span.Offset < 0 || span.Offset > record.Size || span.Size < 0 || span.Size > record.Size-span.Offset ||
			int(span.Kind) >= len(executableStreamKinds) {
			return 0, nil, fmt.Errorf("executable index span outside target")
		}
		k := key{executableStreamKinds[span.Kind], span.Region}
		if _, exists := lookup[k]; exists {
			return 0, nil, fmt.Errorf("duplicate executable stream in index")
		}
		lookup[k] = span
	}
	size := int64(len(want))
	var results []indexedRange
	for _, pos := range []int64{0, size / 7, size / 3, size / 2, size * 4 / 5, max(0, size-4096)} {
		n := min(int64(4096), size-pos)
		sample := indexedRange{Offset: pos, Length: n}
		start := time.Now()
		read := func(kind string, region int, off, n int64) ([]byte, error) {
			span, ok := lookup[key{kind, region}]
			if !ok && kind == "refs" {
				return nil, nil
			}
			if !ok || off < 0 || off > span.Size {
				return nil, fmt.Errorf("missing or invalid %s/%d stream", kind, region)
			}
			if n == -1 {
				n = span.Size - off
			}
			if n < 0 || n > span.Size-off {
				return nil, fmt.Errorf("invalid %s/%d stream range", kind, region)
			}
			data, stats, err := record.DecodeRange(base, params, span.Offset+off, n)
			sample.BaseReadBytes += stats.BaseReadBytes
			sample.DeltaReadBytes += stats.PayloadBytes
			sample.DecodedWindows += stats.Frames
			return data, err
		}
		got, err := binref.ReconstructRange(read, pos, n)
		sample.ReplayUS = time.Since(start).Microseconds()
		if err != nil || !bytes.Equal(got, want[pos:pos+n]) {
			return 0, nil, fmt.Errorf("executable range [%d,%d) differs: %v", pos, pos+n, err)
		}
		results = append(results, sample)
	}
	return len(loaded), results, nil
}
