package mdelta2

import (
	"crypto/sha256"
	"sort"
)

type SHA = [32]byte

func sum(b []byte) SHA { return sha256.Sum256(b) }

func sortBy(a []int, less func(x, y int) bool) {
	sort.Slice(a, func(i, j int) bool { return less(a[i], a[j]) })
}
