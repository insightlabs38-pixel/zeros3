package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"sort"

	"zeros3-z3/eraez3"
)

// seg is a half-open byte range of a file.
type seg struct {
	off, n int64
	class  string
}

// ---- SQLite: fixed pages from the 100-byte header (characterisation scanner) ----

func sqlitePages(d []byte) ([]seg, error) {
	if len(d) < 100 || !bytes.HasPrefix(d, []byte("SQLite format 3\x00")) {
		return nil, errors.New("not sqlite")
	}
	ps := int64(binary.BigEndian.Uint16(d[16:]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 || int64(len(d))%ps != 0 {
		return nil, errors.New("bad page size / length")
	}
	var out []seg
	for off := int64(0); off < int64(len(d)); off += ps {
		hdr := off
		if off == 0 {
			hdr = 100
		}
		c := "other"
		switch d[hdr] {
		case 2:
			c = "idx-int"
		case 5:
			c = "tbl-int"
		case 10:
			c = "idx-leaf"
		case 13:
			c = "tbl-leaf"
		}
		out = append(out, seg{off, ps, c})
	}
	return out, nil
}

// sqlitePagesNorm splits every non-first page that starts with a zero byte (an
// overflow-chain, freelist or pointer-map page: its first four bytes are a page
// number, not content) into a 4-byte head and its body.
func sqlitePagesNorm(d []byte) ([]seg, error) {
	pg, err := sqlitePages(d)
	if err != nil {
		return nil, err
	}
	var out []seg
	for _, s := range pg {
		if s.off > 0 && d[s.off] == 0 {
			out = append(out, seg{s.off, 4, "head"}, seg{s.off + 4, s.n - 4, "body"})
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// ---- LevelDB table: footer -> index block -> data blocks (characterisation scanner) ----

const ldbMagic = 0xdb4775248b80fb57

func uv(b []byte) (uint64, int) { v, n := binary.Uvarint(b); return v, n }

func ldbBlocks(d []byte) ([]seg, error) {
	if len(d) < 48 || binary.LittleEndian.Uint64(d[len(d)-8:]) != ldbMagic {
		return nil, errors.New("not a leveldb table")
	}
	f := d[len(d)-48:]
	_, n1 := uv(f)
	_, n2 := uv(f[n1:])
	ioff, n3 := uv(f[n1+n2:])
	isz, _ := uv(f[n1+n2+n3:])
	if ioff+isz+5 > uint64(len(d)) {
		return nil, errors.New("index handle out of range")
	}
	blk := d[ioff : ioff+isz]
	if d[ioff+isz] == 1 {
		var err error
		if blk, err = eraez3.BlockDecode(blk, 16<<20); err != nil {
			return nil, err
		}
	}
	if len(blk) < 4 {
		return nil, errors.New("short index block")
	}
	nr := int(binary.LittleEndian.Uint32(blk[len(blk)-4:]))
	end := len(blk) - 4 - 4*nr
	if end < 0 {
		return nil, errors.New("bad restarts")
	}
	var out []seg
	var prevEnd int64
	for p := 0; p < end; {
		_, a := uv(blk[p:])
		_, b := uv(blk[p+a:])
		vl, c := uv(blk[p+a+b:])
		sh, _ := uv(blk[p:])
		ns, _ := uv(blk[p+a:])
		_ = sh
		p += a + b + c + int(ns)
		v := blk[p : p+int(vl)]
		p += int(vl)
		off, k := uv(v)
		sz, _ := uv(v[k:])
		if int64(off) != prevEnd {
			return nil, errors.New("data blocks not contiguous")
		}
		prevEnd = int64(off) + int64(sz) + 5
		out = append(out, seg{int64(off), int64(sz) + 5, "data"})
	}
	if prevEnd > int64(len(d)) {
		return nil, errors.New("blocks beyond file")
	}
	out = append(out, seg{prevEnd, int64(len(d)) - prevEnd, "tail"})
	return out, nil
}

// ---- Parquet: column chunks from pyarrow's footer (EXTERNAL ORACLE sidecar) ----

type pqLayout struct {
	FooterLen int `json:"footer_len"`
	RowGroups [][]struct {
		Name       string `json:"name"`
		Start, Len int64
	} `json:"row_groups"`
}

var pqSidecar = map[[32]byte]*pqLayout{}

func loadSidecar(path string, data []byte) {
	b, err := os.ReadFile(path + ".layout.json")
	if err != nil {
		return
	}
	var l pqLayout
	if json.Unmarshal(b, &l) == nil {
		pqSidecar[sha256.Sum256(data)] = &l
	}
}

func pqChunks(d []byte) ([]seg, error) {
	l := pqSidecar[sha256.Sum256(d)]
	if l == nil {
		return nil, errors.New("no oracle layout")
	}
	var out []seg
	pos := int64(0)
	for _, rg := range l.RowGroups {
		for _, c := range rg {
			if c.Start < pos || c.Start+c.Len > int64(len(d)) {
				return nil, errors.New("overlapping or out-of-range chunk")
			}
			if c.Start > pos {
				out = append(out, seg{pos, c.Start - pos, "gap"})
			}
			out = append(out, seg{c.Start, c.Len, "chunk:" + c.Name})
			pos = c.Start + c.Len
		}
	}
	if pos < int64(len(d)) {
		out = append(out, seg{pos, int64(len(d)) - pos, "footer"})
	}
	return out, nil
}

// ---- Zarr v3 shard: inner chunks from the trailing shard index (characterisation scanner;
// 16 inner chunks per shard is the corpus configuration, index at the end, crc32c not verified here) ----

func zarrShardChunks(d []byte) ([]seg, error) {
	const n = 16
	if len(d) < n*16+4 {
		return nil, errors.New("not a shard")
	}
	idx := d[len(d)-n*16-4 : len(d)-4]
	end := int64(len(d) - n*16 - 4)
	var out []seg
	for i := 0; i < n; i++ {
		off := int64(binary.LittleEndian.Uint64(idx[16*i:]))
		sz := int64(binary.LittleEndian.Uint64(idx[16*i+8:]))
		if off < 0 || sz < 0 || off+sz > end {
			return nil, errors.New("shard index entry out of range")
		}
		out = append(out, seg{off, sz, "chunk"})
	}
	// the index lists chunks in coordinate order; the file holds them in write order
	sort.Slice(out, func(a, b int) bool { return out[a].off < out[b].off })
	pos := int64(0)
	for _, s := range out {
		if s.off != pos {
			return nil, errors.New("shard index not a contiguous tiling")
		}
		pos += s.n
	}
	if pos != end {
		return nil, errors.New("shard chunks do not reach the index")
	}
	return append(out, seg{end, int64(len(d)) - end, "index"}), nil
}
