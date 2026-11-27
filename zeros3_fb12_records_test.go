package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type fb12Input struct {
	Corpus string `json:"corpus"`
	Label  string `json:"label"`
	Path   string `json:"path"`
}

type fb12CorpusWriter struct {
	file *os.File
	out  *bufio.Writer
	seen map[[32]byte]struct{}
	rows int
	raw  int64
}

// This emits the actual native CDC records and both production DEFLATE
// forms. The independent research executable can then reuse these exact
// records without duplicating the chunker or rebuilding the large engine.
func TestFB12ExportRecordCorpora(t *testing.T) {
	manifest, dest := os.Getenv("ZEROS3_FB12_INPUTS"), os.Getenv("ZEROS3_FB12_RECORDS")
	if manifest == "" || dest == "" {
		t.Skip("set ZEROS3_FB12_INPUTS and ZEROS3_FB12_RECORDS to export native record corpora")
	}
	data, err := os.ReadFile(manifest)
	mustOK(t, err)
	var inputs []fb12Input
	mustOK(t, json.Unmarshal(data, &inputs))
	mustOK(t, os.MkdirAll(dest, 0700))
	writers := map[string]*fb12CorpusWriter{}
	flate, blocks := newCompressor(true), newBlockCompressor(true, 12)
	for _, input := range inputs {
		if input.Corpus == "." || input.Corpus == "" || filepath.Base(input.Corpus) != input.Corpus {
			t.Fatalf("invalid corpus name %q", input.Corpus)
		}
		w := writers[input.Corpus]
		if w == nil {
			f, err := os.Create(filepath.Join(dest, input.Corpus+".zrc"))
			mustOK(t, err)
			t.Cleanup(func() { f.Close() })
			w = &fb12CorpusWriter{file: f, out: bufio.NewWriterSize(f, 256<<10), seen: map[[32]byte]struct{}{}}
			writers[input.Corpus] = w
			var header []byte
			header = append(header, "ZRC1"...)
			for _, n := range []uint32{cdcMaxChunkSize, packHeaderSize, packFooterSize,
				packRecordHeaderSize, packIndexEntrySize, defaultPackTargetBytes} {
				header = binary.LittleEndian.AppendUint32(header, n)
			}
			_, err = w.out.Write(header)
			mustOK(t, err)
		}
		f, err := os.Open(input.Path)
		mustOK(t, err)
		h := sha256.New()
		chunker := newCDCChunker(io.TeeReader(f, h))
		var inputBytes int64
		for {
			raw, err := chunker.nextView()
			if errors.Is(err, io.EOF) {
				break
			}
			mustOK(t, err)
			inputBytes += int64(len(raw))
			sum := sha256.Sum256(raw)
			if _, ok := w.seen[sum]; ok {
				continue
			}
			w.seen[sum] = struct{}{}
			d, dKind := flate.encode(raw)
			b, bKind := blocks.encode(raw)
			for _, field := range []struct{ body []byte; kind byte }{{d, dKind}, {b, bKind}} {
				_, err := decodePackPayload(field.kind, field.body, uint32(len(raw)), sum, nil)
				mustOK(t, err)
			}
			var frame [14]byte
			binary.LittleEndian.PutUint32(frame[0:4], uint32(len(raw)))
			binary.LittleEndian.PutUint32(frame[4:8], uint32(len(d)))
			binary.LittleEndian.PutUint32(frame[8:12], uint32(len(b)))
			frame[12], frame[13] = dKind, bKind
			for _, part := range [][]byte{frame[:], raw, d, b} {
				_, err := w.out.Write(part)
				mustOK(t, err)
			}
			w.rows++
			w.raw += int64(len(raw))
		}
		chunker.release()
		mustOK(t, f.Close())
		t.Logf("input %s/%s bytes=%d sha256=%x", input.Corpus, input.Label, inputBytes, h.Sum(nil))
	}
	names := make([]string, 0, len(writers))
	for name := range writers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := writers[name]
		mustOK(t, w.out.Flush())
		mustOK(t, w.file.Close())
		t.Logf("corpus %s distinct_cdc_records=%d logical_bytes=%d", name, w.rows, w.raw)
	}
}
