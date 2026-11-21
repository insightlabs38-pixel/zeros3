package parity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSingleLossAndDamagedParity(t *testing.T) {
	chunks := [][]byte{[]byte("a small base"), []byte("short"), []byte("a substantially longer component")}
	wire, err := Encode(chunks)
	if err != nil {
		t.Fatal(err)
	}
	have := make(map[ID][]byte)
	for _, data := range chunks {
		have[sha256.Sum256(data)] = data
	}
	get := func(sum ID) ([]byte, error) {
		if data := have[sum]; data != nil {
			return data, nil
		}
		return nil, fs.ErrNotExist
	}
	if _, _, err := Repair(wire, get); !errors.Is(err, ErrHealthy) {
		t.Fatalf("intact group: %v", err)
	}
	for _, original := range chunks {
		sum := sha256.Sum256(original)
		delete(have, sum)
		id, got, err := Repair(wire, get)
		if err != nil || id != sum || !bytes.Equal(got, original) {
			t.Fatalf("missing member: %x, %q, %v", id, got, err)
		}
		have[sum] = bytes.Clone(original)
		have[sum][0] ^= 1
		id, got, err = Repair(wire, get)
		if err != nil || id != sum || !bytes.Equal(got, original) {
			t.Fatalf("corrupt member: %x, %q, %v", id, got, err)
		}
		have[sum] = original
	}
	for _, lost := range chunks[:2] {
		delete(have, sha256.Sum256(lost))
	}
	if _, _, err := Repair(wire, get); !errors.Is(err, ErrMultiple) {
		t.Fatalf("two missing members: %v", err)
	}
	bad := bytes.Clone(wire)
	bad[len(bad)-sha256.Size-1] ^= 1
	if _, _, err := Repair(bad, get); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged parity: %v", err)
	}
}

func TestActualArchiveSliceRepair(t *testing.T) {
	input := os.Getenv("ZEROS3_PARITY_ARCHIVE")
	out := os.Getenv("ZEROS3_PARITY_OUT")
	if input == "" || out == "" {
		t.Skip("set ZEROS3_PARITY_ARCHIVE and ZEROS3_PARITY_OUT for the real archive control")
	}
	f, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = 1 << 20
	const members = 8
	parts := make([][]byte, members)
	have := make(map[ID][]byte, members)
	for i := range parts {
		parts[i] = make([]byte, size)
		// Separate the slices so the sample spans the real release.
		if _, err := io.ReadFull(io.NewSectionReader(f, int64(i*3*size), size), parts[i]); err != nil {
			t.Fatal(err)
		}
		have[sha256.Sum256(parts[i])] = parts[i]
	}
	start := time.Now()
	wire, err := Encode(parts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "archive.parity"), wire, 0o600); err != nil {
		t.Fatal(err)
	}
	encode := time.Since(start)
	wire, err = os.ReadFile(filepath.Join(out, "archive.parity"))
	if err != nil {
		t.Fatal(err)
	}
	var durations []float64
	for _, part := range parts {
		want := sha256.Sum256(part)
		delete(have, want)
		start := time.Now()
		id, data, err := Repair(wire, func(sum ID) ([]byte, error) {
			if b := have[sum]; b != nil {
				return b, nil
			}
			return nil, fs.ErrNotExist
		})
		durations = append(durations, float64(time.Since(start).Microseconds())/1000)
		if err != nil || id != want || !bytes.Equal(part, data) {
			t.Fatalf("unable to restore %x independently: %v", want, err)
		}
		have[want] = part
	}
	fp := sha256.New()
	if _, err := io.Copy(fp, io.NewSectionReader(f, 0, 1<<62)); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"members": members, "part_bytes": size, "sidecar_bytes": len(wire), "encode_ms": encode.Milliseconds(),
		"repairs_ms": durations, "archive_sha256": fp.Sum(nil), "verified_repairs": len(durations)}
	target, err := os.OpenFile(filepath.Join(out, "measurements.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := json.NewEncoder(target).Encode(record); err != nil {
		t.Fatal(err)
	}
	t.Logf("one verified restoration for each of eight archive members, sidecar %d bytes", len(wire))
}
