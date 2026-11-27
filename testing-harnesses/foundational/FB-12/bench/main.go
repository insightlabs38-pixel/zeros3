package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"zeros3-testing/foundational/FB-12/lzl"
)

type record struct {
	raw, deflate, blocks   []byte
	deflateKind, blockKind byte
	sum                    [32]byte
}

type framing struct {
	header, footer, record, index, target int
}

func readNative(path string) (framing, []record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return framing{}, nil, err
	}
	if len(data) < 28 || string(data[:4]) != "ZRC1" {
		return framing{}, nil, errors.New("invalid native corpus header")
	}
	n := func(at int) int { return int(binary.LittleEndian.Uint32(data[at : at+4])) }
	if n(4) != lzl.MaxRecordBytes {
		return framing{}, nil, errors.New("native and candidate record bounds differ")
	}
	f := framing{n(8), n(12), n(16), n(20), n(24)}
	if f.target <= 0 || f.header <= 0 || f.footer <= 0 || f.record <= 0 || f.index <= 0 {
		return framing{}, nil, errors.New("invalid native pack geometry")
	}
	var rows []record
	for data = data[28:]; len(data) != 0; {
		if len(data) < 14 {
			return f, nil, io.ErrUnexpectedEOF
		}
		a, b, c := n(0), n(4), n(8)
		if a < 1 || a > lzl.MaxRecordBytes || b < 1 || b > a || c < 1 || c > a || a+b+c > len(data)-14 {
			return f, nil, errors.New("invalid native corpus row")
		}
		r := record{raw: data[14 : 14+a], deflate: data[14+a : 14+a+b], blocks: data[14+a+b : 14+a+b+c],
			deflateKind: data[12], blockKind: data[13]}
		r.sum = sha256.Sum256(r.raw)
		rows = append(rows, r)
		data = data[14+a+b+c:]
	}
	return f, rows, nil
}

type payload struct {
	body []byte
	form byte
}

type result struct {
	Corpus               string  `json:"corpus"`
	Lane                 string  `json:"lane"`
	Records              int     `json:"records"`
	NativeLogicalBytes   int64   `json:"native_logical_bytes"`
	SelectedRangeRecords int     `json:"selected_range_records"`
	StoredPayloadBytes   int64   `json:"stored_payload_bytes"`
	PackBytes            int64   `json:"pack_bytes"`
	AllocatedBytes       int64   `json:"allocated_bytes"`
	Packs                int     `json:"packs"`
	EncodeWallNS         int64   `json:"encode_wall_ns,omitempty"`
	EncodeCPUNS          int64   `json:"encode_cpu_ns,omitempty"`
	DecodeCPUNS          []int64 `json:"decode_cpu_ns,omitempty"`
	DecodeWallNS         []int64 `json:"decode_wall_ns,omitempty"`
}

func cpuTime() time.Duration {
	var use syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &use); err != nil {
		log.Fatal(err)
	}
	return time.Duration(use.Utime.Nano() + use.Stime.Nano())
}

// measurePacks writes the selected payloads to real files with the framing
// sizes exported by the engine. Headers and indexes are fixed-width here.
// Their semantics remain the production writer's responsibility.
func measurePacks(f framing, stored []payload) (n, allocated int64, packs int, err error) {
	root, err := os.MkdirTemp("", "fb12-packs-")
	if err != nil {
		return 0, 0, 0, err
	}
	defer os.RemoveAll(root)
	for len(stored) != 0 {
		i, size := 0, f.header+f.footer
		for i < len(stored) && (size+len(stored[i].body)+f.record+f.index <= f.target || i == 0) {
			size += len(stored[i].body) + f.record + f.index
			i++
		}
		pack := make([]byte, 0, size)
		pack = append(pack, make([]byte, f.header)...)
		recordHeader := make([]byte, f.record)
		for _, p := range stored[:i] {
			pack = append(pack, recordHeader...)
			pack = append(pack, p.body...)
		}
		pack = append(pack, make([]byte, i*f.index+f.footer)...)
		path := filepath.Join(root, fmt.Sprintf("%d.pack", packs))
		if err := os.WriteFile(path, pack, 0600); err != nil {
			return 0, 0, 0, err
		}
		fi, err := os.Stat(path)
		if err != nil {
			return 0, 0, 0, err
		}
		n, allocated = n+fi.Size(), allocated+fi.Sys().(*syscall.Stat_t).Blocks*512
		packs++
		stored = stored[i:]
	}
	return
}

func encodeLane(name string, rows []record) ([]payload, int, error) {
	mode, depth, nice := lzl.ModeFast, 48, 64
	switch name {
	case "deflate6", "blocked4k":
	case "fast":
	case "fast-deep":
		depth, nice = 96, 128
	case "optimal":
		mode, nice = lzl.ModeOpt, 128
	case "optimal-deep":
		mode, depth, nice = lzl.ModeOpt, 256, 273
	default:
		return nil, 0, fmt.Errorf("unknown lane %q", name)
	}
	e := lzl.NewEncoder(lzl.Params{Mode: mode, Depth: depth, Nice: nice, Window: lzl.MaxRecordBytes})
	out := make([]payload, 0, len(rows))
	selected := 0
	var scratch []byte
	for _, r := range rows {
		best := payload{r.deflate, r.deflateKind}
		if name == "blocked4k" {
			best = payload{r.blocks, r.blockKind}
		} else if name != "deflate6" {
			encoded, err := e.EncodeRecord(scratch, r.raw)
			if err != nil && !errors.Is(err, lzl.ErrEncodedRecordTooLarge) {
				return nil, 0, err
			}
			if err == nil && len(encoded) < len(best.body) && len(encoded) <= len(r.raw)-max(1, len(r.raw)/16) {
				best = payload{bytes.Clone(encoded), 6}
				selected++
			}
			scratch = encoded
		}
		out = append(out, best)
	}
	return out, selected, nil
}

func decodeAll(rows []record, stored []payload) error {
	var decoder lzl.Decoder
	var flater io.ReadCloser
	defer func() {
		if flater != nil {
			flater.Close()
		}
	}()
	dst := make([]byte, lzl.MaxRecordBytes)
	reader := bytes.NewReader(nil)
	for i, r := range rows {
		p := stored[i]
		switch p.form {
		case 0:
			if sha256.Sum256(p.body) != r.sum {
				return errors.New("raw record mismatch")
			}
		case 1:
			reader.Reset(p.body)
			if flater == nil {
				flater = flate.NewReader(reader)
			} else if err := flater.(flate.Resetter).Reset(reader, nil); err != nil {
				return err
			}
			out := dst[:len(r.raw)]
			if _, err := io.ReadFull(flater, out); err != nil || sha256.Sum256(out) != r.sum {
				return errors.New("DEFLATE record mismatch")
			}
		case 6:
			out, err := decoder.DecodeRecord(dst[:0], p.body, len(r.raw), r.sum)
			if err != nil || !bytes.Equal(out, r.raw) {
				return errors.New("range-coded record mismatch")
			}
		default:
			return fmt.Errorf("unsupported benchmark decode form %d", p.form)
		}
	}
	return nil
}

func main() {
	input := flag.String("input", "", "directory of native .zrc fixtures")
	selected := flag.String("lanes", "deflate6,blocked4k,fast,fast-deep,optimal,optimal-deep", "comma-separated lanes")
	rounds := flag.Int("decode-rounds", 5, "reopen decode rounds")
	flag.Parse()
	if *input == "" || *rounds < 1 {
		log.Fatal("a fixture directory and positive decode round count are required")
	}
	files, err := filepath.Glob(filepath.Join(*input, "*.zrc"))
	if err != nil || len(files) == 0 {
		log.Fatal("no native fixtures")
	}
	slices.Sort(files)
	jsonOut := json.NewEncoder(os.Stdout)
	for _, path := range files {
		f, rows, err := readNative(path)
		if err != nil {
			log.Fatal(err)
		}
		for _, lane := range strings.Split(*selected, ",") {
			start, cpu := time.Now(), cpuTime()
			stored, n, err := encodeLane(lane, rows)
			if err != nil {
				log.Fatal(err)
			}
			res := result{Corpus: strings.TrimSuffix(filepath.Base(path), ".zrc"), Lane: lane,
				Records: len(rows), SelectedRangeRecords: n}
			if lane != "deflate6" && lane != "blocked4k" {
				res.EncodeWallNS, res.EncodeCPUNS = time.Since(start).Nanoseconds(), (cpuTime() - cpu).Nanoseconds()
			}
			for i, r := range rows {
				res.NativeLogicalBytes += int64(len(r.raw))
				res.StoredPayloadBytes += int64(len(stored[i].body))
			}
			res.PackBytes, res.AllocatedBytes, res.Packs, err = measurePacks(f, stored)
			if err != nil {
				log.Fatal(err)
			}
			if lane != "blocked4k" {
				for range *rounds {
					start, cpu := time.Now(), cpuTime()
					if err := decodeAll(rows, stored); err != nil {
						log.Fatal(err)
					}
					res.DecodeWallNS = append(res.DecodeWallNS, time.Since(start).Nanoseconds())
					res.DecodeCPUNS = append(res.DecodeCPUNS, (cpuTime() - cpu).Nanoseconds())
				}
			}
			if err := jsonOut.Encode(res); err != nil {
				log.Fatal(err)
			}
		}
	}
}
