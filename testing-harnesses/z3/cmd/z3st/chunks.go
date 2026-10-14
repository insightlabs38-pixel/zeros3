package main

import (
	"bytes"
	"crypto/sha256"

	"zeros3-z3/lab"
)

func forChunks(c *ckpt, fn func(h [32]byte, size int)) {
	lab.ChunkCDC(bytes.NewReader(c.data), func(off int64, ch []byte) error {
		fn(sha256.Sum256(ch), len(ch))
		return nil
	})
}

func chunkSet(c *ckpt) map[[32]byte]bool {
	m := map[[32]byte]bool{}
	forChunks(c, func(h [32]byte, _ int) { m[h] = true })
	return m
}
