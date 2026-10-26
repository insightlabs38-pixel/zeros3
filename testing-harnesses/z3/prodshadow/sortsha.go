package prodshadow

import (
	"bytes"
	"hash/crc32"
	"slices"
)

func sortSHAFast(ids [][32]byte) {
	slices.SortFunc(ids, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
}

func sortStrings(s []string) { slices.Sort(s) }

func crcOf(b []byte) uint32 { return crc32.Checksum(b, crcTab) }
