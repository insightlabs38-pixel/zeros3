package frontier

import (
	"encoding/json"
	"fmt"
	"sort"
)

// TOCEntry covers the eStargz TOC and the zstd:chunked manifest (same CRFS
// lineage): the fields the characterisation needs.
type TOCEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	Offset      int64  `json:"offset"`
	EndOffset   int64  `json:"endOffset"`
	ChunkOffset int64  `json:"chunkOffset"`
	ChunkSize   int64  `json:"chunkSize"`
	ChunkDigest string `json:"chunkDigest"`
	Digest      string `json:"digest"`
	Mode        int64  `json:"mode"`
}

type TOC struct {
	Version int        `json:"version"`
	Entries []TOCEntry `json:"entries"`
}

func ParseTOC(b []byte, maxEntries int) (*TOC, error) {
	var t TOC
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	if len(t.Entries) > maxEntries {
		return nil, fmt.Errorf("toc: %d entries exceeds %d", len(t.Entries), maxEntries)
	}
	return &t, nil
}

// NativeChunk is one independently compressed unit as the TOC describes it.
type NativeChunk struct {
	Name     string
	Off, Len int64 // compressed span in the blob
	ULen     int64 // uncompressed content bytes (ChunkSize or file size)
	Digest   string
	FileOff  int64
	FileSize int64
}

// Chunks returns data chunks with compressed spans derived from consecutive
// distinct offsets (limit = end of data area). A regular file's chunkSize of 0
// means a single chunk covering the whole file.
func (t *TOC) Chunks(limit int64) []NativeChunk {
	var offs []int64
	seen := map[int64]bool{}
	for _, e := range t.Entries {
		if (e.Type == "reg" || e.Type == "chunk") && !seen[e.Offset] {
			seen[e.Offset] = true
			offs = append(offs, e.Offset)
		}
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	next := map[int64]int64{}
	for i, o := range offs {
		if i+1 < len(offs) {
			next[o] = offs[i+1]
		} else {
			next[o] = limit
		}
	}
	var out []NativeChunk
	sizes := map[string]int64{}
	for _, e := range t.Entries {
		if e.Type == "reg" {
			sizes[e.Name] = e.Size
		}
	}
	for _, e := range t.Entries {
		d := e.ChunkDigest
		if d == "" && e.Type == "reg" && e.Size > 0 {
			d = e.Digest
		}
		if (e.Type != "reg" && e.Type != "chunk") || d == "" {
			continue
		}
		u := e.ChunkSize
		if u == 0 {
			u = sizes[e.Name] - e.ChunkOffset
		}
		out = append(out, NativeChunk{Name: e.Name, Off: e.Offset, Len: next[e.Offset] - e.Offset, ULen: u, Digest: d, FileOff: e.ChunkOffset, FileSize: sizes[e.Name]})
	}
	return out
}
