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
	"path/filepath"
	"testing"
	"time"
)

func TestRepONNXInlineTensorLayout(t *testing.T) {
	message := func(dst []byte, tag byte, body []byte) []byte {
		dst = append(dst, tag)
		dst = binary.AppendUvarint(dst, uint64(len(body)))
		return append(dst, body...)
	}
	weights := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	raw := message(nil, 0x4a, weights)
	tensor := []byte{0x08, 2, 0x08, 2, 0x10, 10}
	tensor = message(tensor, 0x42, []byte("layer.weight"))
	write := func(tensor []byte) []byte { return message([]byte{8, 8}, 0x3a, message(nil, 0x2a, tensor)) }
	model := write(append(bytes.Clone(tensor), raw...))
	kind, ok := repCandidate(bytes.NewReader(model), int64(len(model)))
	if !ok || kind != repKindModel {
		t.Fatal("inline model was not recognized")
	}
	spans, err := repModelLayout(bytes.NewReader(model), int64(len(model)))
	mustOK(t, err)
	if len(spans) != 3 {
		t.Fatalf("wanted three tiled layout spans, got %+v", spans)
	}
	m := spans[1]
	if m.name != "layer.weight" || m.width != 2 || m.dtype != "F16" || m.format != "onnx" ||
		!bytes.Equal(model[m.begin:m.end], weights) {
		t.Fatalf("wrong inline tensor span: %+v", m)
	}
	for _, extra := range [][]byte{raw, {0x6a, 1, 'x'}, {0x72, 1, 'x'}, {0x70, 1}} {
		invalid := write(append(append(bytes.Clone(tensor), raw...), extra...))
		if _, err := repModelLayout(bytes.NewReader(invalid), int64(len(invalid))); !errors.Is(err, errRepUnsupported) {
			t.Fatalf("ambiguous or external tensor was admitted: %x %v", extra, err)
		}
	}
	bad := model[:len(model)-1]
	if _, err := repONNXLayout(bytes.NewReader(bad), int64(len(bad))); err == nil {
		t.Fatal("truncated graph was admitted")
	}
}

func TestAuthorityONNXCrossFormatEconomics(t *testing.T) {
	root, checkpoint := os.Getenv("ZEROS3_C5_TENSOR_EXPORTS"), os.Getenv("ZEROS3_C5_TENSOR_SOURCE")
	if root == "" || checkpoint == "" {
		t.Skip("set ZEROS3_C5_TENSOR_EXPORTS and ZEROS3_C5_TENSOR_SOURCE for the pinned export corpus")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	mustOK(t, err)
	var manifest struct {
		SourceSHA string `json:"source_sha256"`
		Exports   []struct {
			File, SHA256 string
		}
	}
	mustOK(t, json.Unmarshal(manifestBytes, &manifest))
	if len(manifest.Exports) < 1 || len(manifest.Exports) > 8 {
		t.Fatalf("expected one to eight watched exports, got %d", len(manifest.Exports))
	}
	type asset struct {
		path, sha string
		size      int64
	}
	inputs := []asset{{path: checkpoint, sha: manifest.SourceSHA}}
	for _, a := range manifest.Exports {
		inputs = append(inputs, asset{path: filepath.Join(root, a.File), sha: a.SHA256})
	}
	var logical int64
	for i := range inputs {
		f, err := os.Stat(inputs[i].path)
		mustOK(t, err)
		if f.Size() < 4096 {
			t.Fatalf("fixture too short: %s", inputs[i].path)
		}
		inputs[i].size = f.Size()
		logical += f.Size()
	}
	report := map[string]any{"logical_bytes": logical}
	for _, mode := range []string{"cdc", "generic", "structured"} {
		l := newAuthLab(t)
		for _, asset := range inputs {
			f, err := os.Open(asset.path)
			mustOK(t, err)
			h := sha256.New()
			ing, err := l.s.ingestStream(io.TeeReader(f, h), true, ingestOptions{size: asset.size, overlapFlush: true})
			f.Close()
			mustOK(t, err)
			if fmt.Sprintf("%x", h.Sum(nil)) != asset.sha {
				t.Fatalf("%s does not match its independent fixture stamp", asset.path)
			}
			_, err = l.s.commitIngested("b", filepath.Base(asset.path), ing, "application/octet-stream", nil, putCondition{})
			mustOK(t, err)
		}
		start := time.Now()
		var migration AuthReport
		if mode != "cdc" {
			migration = l.migrate(AuthPolicy{Order: "oldest", Strategy: "forced", AllowRep: mode == "structured",
				AllowDelta: mode == "generic", NoZN7: true, MinSaving: 0.01, MinObject: 1, MinRepGroup: 1})
		}
		migrationSeconds := time.Since(start).Seconds()
		l.reclaim()
		l.verifyOK("model history after GC and repack")
		start = time.Now()
		for _, asset := range inputs {
			key := filepath.Base(asset.path)
			rd := l.s.openObjectReader(l.man(key), byteRange{0, asset.size - 1})
			h := sha256.New()
			for {
				chunk, err := rd.next()
				if err == io.EOF {
					break
				}
				mustOK(t, err)
				h.Write(chunk)
			}
			rd.close()
			if fmt.Sprintf("%x", h.Sum(nil)) != asset.sha {
				t.Fatalf("%s %s whole body changed", mode, key)
			}
		}
		whole := float64(logical) / (1 << 20) / time.Since(start).Seconds()
		start = time.Now()
		for _, asset := range inputs {
			key := filepath.Base(asset.path)
			off := asset.size/2 - 2048
			f, err := os.Open(asset.path)
			mustOK(t, err)
			var expected [4096]byte
			_, err = f.ReadAt(expected[:], off)
			f.Close()
			mustOK(t, err)
			if got := l.read(key, byteRange{off, off + 4095}); !bytes.Equal(got, expected[:]) {
				t.Fatalf("%s %s sampled range changed", mode, key)
			}
		}
		report[mode] = map[string]any{"physical_bytes": dirBytes(t, l.dir), "migration_s": migrationSeconds,
			"whole_mib_s": whole, "sample_range_s": time.Since(start).Seconds(), "forms": migration.Plans}
		l.close()
		mustOK(t, os.RemoveAll(l.dir))
	}
	out, err := json.MarshalIndent(report, "", "  ")
	mustOK(t, err)
	t.Log(string(out))
	if path := os.Getenv("ZEROS3_C5_TENSOR_OUT"); path != "" {
		mustOK(t, os.WriteFile(path, append(out, '\n'), 0o600))
	}
}
