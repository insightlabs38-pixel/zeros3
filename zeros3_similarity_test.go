package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type similarityFixturePosting struct {
	feature uint64
	id      uint32
}

// Encode the documented research layout independently of the production reader.
func similarityFixtureRun(entries []similarityFixturePosting) []byte {
	const header, directory, entry = 20, 65537 * 4, 10
	run := make([]byte, header+directory+entry*len(entries)+4)
	copy(run, "ZSR1")
	binary.LittleEndian.PutUint64(run[4:], 11)
	binary.LittleEndian.PutUint64(run[12:], uint64(len(entries)))
	i := 0
	for bucket := 0; bucket <= 65536; bucket++ {
		binary.LittleEndian.PutUint32(run[header+bucket*4:], uint32(i))
		for i < len(entries) && int(entries[i].feature>>48) == bucket {
			off := header + directory + entry*i
			for b := 0; b < 6; b++ {
				run[off+b] = byte(entries[i].feature >> (8 * b))
			}
			binary.LittleEndian.PutUint32(run[off+6:], entries[i].id)
			i++
		}
	}
	sealSimilarityFixture(run)
	return run
}

func sealSimilarityFixture(run []byte) {
	crc := crc32.Checksum(run[:len(run)-4], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(run[len(run)-4:], crc)
}

func TestAuthoritySimilarityRunLookup(t *testing.T) {
	const hot = 0x1234000000000004
	entries := []similarityFixturePosting{{2, 1}, {hot - 1, 201}}
	for i := uint32(1); i <= 1000; i++ {
		entries = append(entries, similarityFixturePosting{hot, i})
	}
	entries = append(entries, similarityFixturePosting{hot + 1, 202}, similarityFixturePosting{^uint64(0), 999})
	body := similarityFixtureRun(entries)
	r, err := openAuthSimilarityRun(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if r.generation != 11 || r.count != uint32(len(entries)) || r.digest != sha256.Sum256(body) {
		t.Fatalf("wrong metadata: %d %d %x", r.generation, r.count, r.digest)
	}
	for _, tc := range []struct {
		feature uint64
		count   int
		want    []uint32
	}{
		{0, 64, nil}, {2, 1, []uint32{1}}, {hot, 3, []uint32{998, 999, 1000}},
		{hot + 1, 64, []uint32{202}}, {hot + 2, 64, nil}, {^uint64(0), 64, []uint32{999}},
	} {
		got, err := r.lookup(tc.feature, tc.count)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("lookup %x: got %v, want %v, err=%v", tc.feature, got, tc.want, err)
		}
	}
	if got, err := r.lookup(hot, 5000); err != nil || len(got) != 64 || got[0] != 937 {
		t.Fatalf("hot feature was not bounded: %v %v", got, err)
	}
	f := filepath.Join(t.TempDir(), "research.zsr")
	if err := os.WriteFile(f, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"-run", f, "-sha256", fmt.Sprintf("%x", r.digest), "-feature", fmt.Sprint(hot)}
	if err := inspectAuthSimilarityRun(args, &out); err != nil {
		t.Fatal(err)
	}
	var report struct{ IDs []uint32 }
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.IDs) != 64 {
		t.Fatalf("report did not include bounded IDs: %v %v", report.IDs, err)
	}
	args[3] = fmt.Sprintf("%064x", 5)
	if err := inspectAuthSimilarityRun(args, &out); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}

func TestAuthoritySimilarityRunCorruption(t *testing.T) {
	fresh := similarityFixtureRun([]similarityFixturePosting{{2, 1}, {2, 3}, {3, 1}})
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"magic", func(b []byte) []byte { b[3] = '2'; sealSimilarityFixture(b); return b }},
		{"zero generation", func(b []byte) []byte { clear(b[4:12]); sealSimilarityFixture(b); return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
		{"first directory", func(b []byte) []byte { b[20] = 1; sealSimilarityFixture(b); return b }},
		{"duplicate posting", func(b []byte) []byte { b[authSimilarityRunBase+16] = 1; sealSimilarityFixture(b); return b }},
		{"zero ID", func(b []byte) []byte { b[authSimilarityRunBase+6] = 0; sealSimilarityFixture(b); return b }},
		{"checksum", func(b []byte) []byte { b[len(b)-1] ^= 1; return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.edit(slices.Clone(fresh))
			if _, err := openAuthSimilarityRun(bytes.NewReader(b), int64(len(b))); err == nil {
				t.Fatal("invalid run accepted")
			}
		})
	}
}
