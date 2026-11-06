package main

import (
	"bytes"
	"crypto/sha256"
	"os"

	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// specialCosts charges the existing bespoke representations (kinds 11/12) in the same physical
// model as every other scheme here: unique reachable objects at packed+96, plus 36 per root.
func specialCosts(kind string, files []string) []int64 {
	seen := map[[32]byte]bool{}
	var cum int64
	var out []int64
	for _, path := range files {
		d, err := os.ReadFile(path)
		must(err)
		var bundle *kinds.Bundle
		switch kind {
		case "dex":
			bundle, _, err = kinds.EncodeDEX(d, dexz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
		case "clr":
			bundle, _, err = kinds.EncodeCLR(d, clrz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
		}
		must(err)
		store := repz3.NewMemStore()
		root, _, err := bundle.Persist(store)
		must(err)
		env := kinds.NewEnvLimits(store, repz3.Limits{MaxDepth: 4, MaxWhole: 128 << 20, MaxObjects: 1 << 20, MaxRepBytes: 32 << 20})
		var buf bytes.Buffer
		n, sum, err := env.Reconstruct(root, &buf)
		must(err)
		if n != int64(len(d)) || sum != sha256.Sum256(d) {
			panic("special representation does not reproduce source")
		}
		reach, err := env.Reachable(root)
		must(err)
		for _, id := range reach.Order {
			if !seen[id] {
				seen[id] = true
				cum += lab.PackedLen(store.Bytes(id)) + 96
			}
		}
		cum += 36
		out = append(out, cum)
	}
	return out
}
