package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"zeros3-z3/binref"
	"zeros3-z3/dcore"
)

var executableStreamKinds = []string{"regions", "units", "payload", "refs"}

func packExecutableStreams(streams []binref.Stream) ([]byte, error) {
	result := binary.AppendUvarint([]byte("ZEB1"), uint64(len(streams)))
	for _, stream := range streams {
		kind := slices.Index(executableStreamKinds, stream.Kind)
		if kind < 0 {
			return nil, fmt.Errorf("unsupported executable stream %q", stream.Kind)
		}
		result = append(result, byte(kind))
		result = binary.AppendVarint(result, int64(stream.Region))
		result = binary.AppendUvarint(result, uint64(len(stream.Data)))
		result = append(result, stream.Data...)
	}
	return result, nil
}

func replayExecutableStreams(wire []byte) ([]byte, error) {
	if !bytes.HasPrefix(wire, []byte("ZEB1")) {
		return nil, fmt.Errorf("invalid executable stream header")
	}
	r := wire[4:]
	count, n := binary.Uvarint(r)
	if n < 1 || count > 65536 {
		return nil, fmt.Errorf("invalid executable stream count")
	}
	r = r[n:]
	streams := make([]binref.Stream, 0, count)
	for range count {
		if len(r) == 0 || int(r[0]) >= len(executableStreamKinds) {
			return nil, fmt.Errorf("invalid executable stream kind")
		}
		kind := executableStreamKinds[r[0]]
		region, n := binary.Varint(r[1:])
		if n < 1 || int64(int(region)) != region {
			return nil, fmt.Errorf("invalid executable stream region")
		}
		r = r[1+n:]
		size, n := binary.Uvarint(r)
		if n < 1 || size > uint64(len(r)-n) {
			return nil, fmt.Errorf("invalid executable stream size")
		}
		r = r[n:]
		streams = append(streams, binref.Stream{Kind: kind, Region: int(region), Data: r[:size]})
		r = r[size:]
	}
	if len(r) != 0 {
		return nil, fmt.Errorf("trailing executable stream data")
	}
	return binref.Reconstruct(streams)
}

type exactExecutableDelta struct {
	Mode              string         `json:"mode"`
	EncodedBytes      int64          `json:"encoded_bytes"`
	BaseStoredBytes   int64          `json:"base_stored_bytes"`
	BaseOpenMicros    int64          `json:"base_open_micros"`
	EncodeMS          int64          `json:"encode_ms"`
	WholeReplayMicros int64          `json:"whole_replay_micros"`
	RangeReplayMicros int64          `json:"range_4k_replay_micros"`
	RangeBaseRead     int64          `json:"range_base_read_bytes"`
	DecodedSHA        string         `json:"decoded_sha256"`
	IndexedTableBytes int            `json:"indexed_table_bytes,omitempty"`
	IndexedRanges     []indexedRange `json:"indexed_4k_ranges,omitempty"`
}

// Persist a comparable base for each experiment, including the decoded size
// and content digest alongside its raw or deflated bytes.
func saveExecutableBase(path string, base []byte) error {
	sum := sha256.Sum256(base)
	header := append([]byte("ZEBP1"), sum[:]...)
	header = binary.AppendUvarint(header, uint64(len(base)))
	var encoded bytes.Buffer
	w, err := flate.NewWriter(&encoded, flate.DefaultCompression)
	if err != nil {
		return err
	}
	if _, err := w.Write(base); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	body := base
	var codec byte
	if encoded.Len() <= len(base)-max(1, len(base)/16) {
		codec, body = 1, encoded.Bytes()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(header, codec)); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	return f.Sync()
}

func openExecutableBase(path string, expected [32]byte) ([]byte, int64, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	stored := int64(len(body))
	const header = 5 + sha256.Size
	if len(body) <= header || string(body[:5]) != "ZEBP1" || !bytes.Equal(body[5:header], expected[:]) {
		return nil, 0, fmt.Errorf("invalid executable base header")
	}
	length, n := binary.Uvarint(body[header:])
	if n < 1 || header+n >= len(body) || length > 256<<20 {
		return nil, 0, fmt.Errorf("invalid executable base length")
	}
	codec := body[header+n]
	result := body[header+n+1:]
	switch codec {
	case 0:
	case 1:
		r := flate.NewReader(bytes.NewReader(result))
		result, err = io.ReadAll(io.LimitReader(r, int64(length)+1))
		r.Close()
		if err != nil {
			return nil, 0, err
		}
	default:
		return nil, 0, fmt.Errorf("unknown executable base codec")
	}
	if uint64(len(result)) != length || sha256.Sum256(result) != expected {
		return nil, 0, fmt.Errorf("stored executable base differs from its expected digest")
	}
	return result, stored, nil
}

func checkExecutableDelta(path, mode string, base, target, want []byte, normalized, reuseRangeWindows bool) (exactExecutableDelta, error) {
	stats := exactExecutableDelta{Mode: mode}
	params := dcore.DefaultWParams()
	params.Window, params.Workers = 1<<20, 2
	started := time.Now()
	baseInput := bytes.NewReader(base)
	index, err := dcore.BuildWIndex(baseInput, int64(len(base)), params.Anchor, params.Workers)
	if err != nil {
		return stats, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	if _, err := dcore.WEncode(f, bytes.NewReader(target), int64(len(target)), baseInput, int64(len(base)), index, params); err != nil {
		return stats, err
	}
	if err := f.Sync(); err != nil {
		return stats, err
	}
	st, err := f.Stat()
	if err != nil {
		return stats, err
	}
	stats.EncodedBytes = st.Size()
	stats.EncodeMS = time.Since(started).Milliseconds()
	if err := saveExecutableBase(path+".base", base); err != nil {
		return stats, err
	}
	started = time.Now()
	diskBase, stored, err := openExecutableBase(path+".base", sha256.Sum256(base))
	stats.BaseStoredBytes = stored
	stats.BaseOpenMicros = time.Since(started).Microseconds()
	if err != nil {
		return stats, err
	}
	baseInput = bytes.NewReader(diskBase)
	record, err := dcore.OpenWRecord(f, st.Size())
	if err != nil {
		return stats, err
	}
	replay := func() ([]byte, int64, error) {
		var out bytes.Buffer
		res, err := record.Decode(&out, baseInput, params)
		if err != nil {
			return nil, 0, err
		}
		if normalized {
			data, err := replayExecutableStreams(out.Bytes())
			return data, res.BaseReadBytes, err
		}
		return out.Bytes(), res.BaseReadBytes, nil
	}
	started = time.Now()
	got, _, err := replay()
	stats.WholeReplayMicros = time.Since(started).Microseconds()
	if err != nil || !bytes.Equal(got, want) {
		return stats, fmt.Errorf("%s whole replay mismatch: %v", mode, err)
	}
	stats.DecodedSHA = fmt.Sprintf("%x", sha256.Sum256(got))
	offset := int64(len(want) / 2)
	end := min(offset+4096, int64(len(want)))
	started = time.Now()
	var mid []byte
	if normalized {
		got, stats.RangeBaseRead, err = replay()
		if err == nil {
			mid = got[offset:end]
		}
	} else {
		var read dcore.WDecodeStats
		mid, read, err = record.DecodeRange(baseInput, params, offset, end-offset)
		stats.RangeBaseRead = read.BaseReadBytes
	}
	stats.RangeReplayMicros = time.Since(started).Microseconds()
	if err != nil || !bytes.Equal(mid, want[offset:end]) {
		return stats, fmt.Errorf("%s range replay mismatch: %v", mode, err)
	}
	if normalized {
		stats.IndexedTableBytes, stats.IndexedRanges, err = measureExecutableIndex(path+".index", target, want, record, baseInput, params, reuseRangeWindows)
		if err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func runExactExecutableDelta(args []string) {
	fs := flag.NewFlagSet("exact-delta", flag.ExitOnError)
	baseline := fs.String("base", "", "base binary")
	current := fs.String("cur", "", "current binary")
	out := fs.String("out", "", "new directory for the encoded records")
	independentRanges := fs.Bool("independent-range-streams", false, "measure each stream slice with a fresh decoder")
	fs.Parse(args)
	if *baseline == "" || *current == "" || *out == "" {
		fatal(fmt.Errorf("exact-delta requires -base, -cur and -out"))
	}
	base, target := mustLoad(*baseline), mustLoad(*current)
	if err := os.Mkdir(*out, 0o700); err != nil {
		fatal(err)
	}
	plain, err := checkExecutableDelta(filepath.Join(*out, "raw.zdw"), "raw", base.Data, target.Data, target.Data, false, false)
	if err != nil {
		fatal(err)
	}
	normalizeOne := func(b *bin) []byte {
		n, _, err := normalize(b, cfg{Set: setFull, Ident: binref.IdentSymbol, Units: "auto"})
		if err != nil {
			fatal(err)
		}
		wire, err := packExecutableStreams(n.Streams)
		if err != nil {
			fatal(err)
		}
		back, err := replayExecutableStreams(wire)
		if err != nil || !bytes.Equal(back, b.Data) {
			fatal(fmt.Errorf("normalization mismatch for %s: %v", b.Name, err))
		}
		return wire
	}
	baseWire, targetWire := normalizeOne(base), normalizeOne(target)
	norm, err := checkExecutableDelta(filepath.Join(*out, "normalized.zdw"), "normalized", baseWire, targetWire, target.Data, true, !*independentRanges)
	if err != nil {
		fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode([]exactExecutableDelta{plain, norm}); err != nil {
		fatal(err)
	}
}
