package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"
)

func TestC15SimilarityRunGeometry(t *testing.T) {
	const hot = uint64(0x9f276a377093d126)
	for _, tc := range []struct{ count, bits int }{{43, 0}, {1041, 8}, {65537, 12}, {260000, 16}} {
		t.Run(fmt.Sprint(tc.bits), func(t *testing.T) {
			entries := make([]authSimilarityEntry, tc.count)
			for i := range entries {
				feature := uint64(i) * 0x9e3779b97f4a7c15
				if i%3 == 0 {
					feature = hot
				}
				entries[i] = authSimilarityEntry{feature: feature, id: uint32(i + 1)}
			}
			// File order is independent of the writer's input order.
			slices.Reverse(entries)
			var buffer bytes.Buffer
			mustOK(t, writeAuthSimilarityRun(&buffer, 47, entries))
			raw := buffer.Bytes()
			run, err := openAuthSimilarityRun(bytes.NewReader(raw), int64(len(raw)))
			mustOK(t, err)
			if run.prefix != tc.bits || run.count != uint32(tc.count) || run.digest != sha256.Sum256(raw) {
				t.Fatalf("wrong geometry or digest for %d records", tc.count)
			}
			var want []uint32
			for i := 0; i < tc.count; i += 3 {
				want = append(want, uint32(i+1))
			}
			want = want[max(0, len(want)-64):]
			for _, retain := range []bool{true, false} {
				if !retain {
					run.directory = nil
				}
				got, err := run.lookup(hot, 64)
				mustOK(t, err)
				if !slices.Equal(got, want) {
					t.Fatalf("wrong most-recent postings: %v", got)
				}
				got, err = run.lookup(0xffffffffffffeeee, 64)
				mustOK(t, err)
				if len(got) != 0 {
					t.Fatal("missing key returned a posting")
				}
			}

			corrupt := bytes.Clone(raw)
			if tc.bits == 12 {
				// A 52-bit suffix occupies seven bytes with four zero padding bits.
				corrupt[run.base+6] |= 0x80
			} else if tc.bits != 16 {
				corrupt[21] = 1
			} else {
				corrupt[9] ^= 1
			}
			if tc.bits != 16 {
				sealSimilarityFixture(corrupt)
			}
			if _, err = openAuthSimilarityRun(bytes.NewReader(corrupt), int64(len(corrupt))); err == nil {
				t.Fatal("malformed compact posting run was accepted")
			}
		})
	}
}
