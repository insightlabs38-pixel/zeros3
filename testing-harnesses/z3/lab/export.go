package lab

import "io"

// ChunkCDC streams r through the lab's copy of ZeroS3 CDC v1, calling fn with
// every chunk (a view valid only during the call) and its offset.
func ChunkCDC(r io.Reader, fn func(off int64, chunk []byte) error) error { return forEachChunk(r, fn) }

// PackedLen is the stored size under the production pack rule (DEFLATE, kept
// only if it saves at least 1/16).
func PackedLen(b []byte) int64 { return packedLen(b) }
