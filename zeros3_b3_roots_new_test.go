package main

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func rootsLen(r *authRoots) int { return r.len() }

// A content-digest table of a million entries against the Go map it replaces.
func BenchmarkB3DigestTable(b *testing.B) {
	keys := make([][32]byte, 1000000)
	for i := range keys {
		keys[i] = sha256.Sum256(fmt.Appendf(nil, "key %d", i))
	}
	b.Run("map", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			m := make(map[[32]byte]int64)
			for i, k := range keys {
				m[k] = int64(i)
			}
		}
	})
	b.Run("table", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			t := newAuthDigestTable(len(keys))
			for i, k := range keys {
				t.put(k, int64(i))
			}
		}
	})
}
