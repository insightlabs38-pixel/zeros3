// Package tensorgrid is an isolated layout experiment for exact two-dimensional tensor replay.
package tensorgrid

import (
	"crypto/sha256"
	"errors"
	"io"
)

const (
	tileSide = 32
	maxTiles = 1 << 20
)

type Index struct {
	Rows, Cols, Elem int
	Tiles            [][sha256.Size]byte
}

func (idx Index) geometry() (rows, cols int, err error) {
	switch idx.Elem {
	case 1, 2, 4, 8:
	default:
		return 0, 0, errors.New("unsupported element width")
	}
	if idx.Rows <= 0 || idx.Rows > 1<<20 || idx.Cols <= 0 || idx.Cols > 1<<20 {
		return 0, 0, errors.New("matrix dimensions out of bounds")
	}
	rows, cols = (idx.Rows+tileSide-1)/tileSide, (idx.Cols+tileSide-1)/tileSide
	if int64(rows)*int64(cols) > maxTiles {
		return 0, 0, errors.New("too many tiles")
	}
	return rows, cols, nil
}

func (idx Index) Size() int64 { return int64(idx.Rows) * int64(idx.Cols) * int64(idx.Elem) }

func (idx Index) tile(row, col int) (off int64, height, width int) {
	top, left := row*tileSide, col*tileSide
	height = min(tileSide, idx.Rows-top)
	width = min(tileSide, idx.Cols-left)
	off = (int64(top)*int64(idx.Cols) + int64(height)*int64(left)) * int64(idx.Elem)
	return off, height, width
}

// Build writes the same canonical tile bytes for a row-major input and its transpose.
func Build(dst io.Writer, src io.ReaderAt, rows, cols, elem int, transposed bool) (Index, error) {
	idx := Index{Rows: rows, Cols: cols, Elem: elem}
	nRows, nCols, err := idx.geometry()
	if err != nil {
		return Index{}, err
	}
	idx.Tiles = make([][sha256.Size]byte, 0, nRows*nCols)
	var tile [tileSide * tileSide * 8]byte
	var span [tileSide * 8]byte
	for tr := range nRows {
		for tc := range nCols {
			_, h, w := idx.tile(tr, tc)
			p := tile[:h*w*elem]
			top, left := tr*tileSide, tc*tileSide
			if transposed {
				for c := range w {
					slice := span[:h*elem]
					offset := (int64(left+c)*int64(rows) + int64(top)) * int64(elem)
					if _, err := src.ReadAt(slice, offset); err != nil {
						return Index{}, err
					}
					for r := range h {
						at := (r*w + c) * elem
						copy(p[at:at+elem], slice[r*elem:(r+1)*elem])
					}
				}
			} else {
				for r := range h {
					offset := (int64(top+r)*int64(cols) + int64(left)) * int64(elem)
					if _, err := src.ReadAt(p[r*w*elem:(r+1)*w*elem], offset); err != nil {
						return Index{}, err
					}
				}
			}
			n, err := dst.Write(p)
			if err != nil {
				return Index{}, err
			}
			if n != len(p) {
				return Index{}, io.ErrShortWrite
			}
			idx.Tiles = append(idx.Tiles, sha256.Sum256(p))
		}
	}
	return idx, nil
}

type Grid struct {
	src        io.ReaderAt
	idx        Index
	cols       int
	tileCols   int
	transposed bool
}

// Open binds a view to an exact-sized tile file and a trusted tile digest table.
func Open(src io.ReaderAt, size int64, index Index, transposed bool) (*Grid, error) {
	r, c, err := index.geometry()
	if err != nil {
		return nil, err
	}
	if size != index.Size() || len(index.Tiles) != r*c {
		return nil, errors.New("tile file or digest table has the wrong size")
	}
	cols := index.Cols
	if transposed {
		cols = index.Rows
	}
	return &Grid{src: src, idx: index, cols: cols, tileCols: c, transposed: transposed}, nil
}

type ReadStats struct {
	TilesRead int64 `json:"tiles_read"`
	BytesRead int64 `json:"bytes_read"`
}

func (g *Grid) ReadAt(p []byte, off int64) (int, error) {
	n, _, err := g.ReadAtMeasured(p, off)
	return n, err
}

// ReadAtMeasured validates each distinct tile before emitting its bytes. A bounded
// hash-indexed cache is local to one call so concurrent views share no mutable state.
func (g *Grid) ReadAtMeasured(p []byte, off int64) (int, ReadStats, error) {
	var stats ReadStats
	if off < 0 {
		return 0, stats, errors.New("negative tensor offset")
	}
	requested := len(p)
	if requested == 0 {
		return 0, stats, nil
	}
	remaining := g.idx.Size() - off
	if remaining <= 0 {
		return 0, stats, io.EOF
	}
	p = p[:min(int64(requested), remaining)]
	type cachedTile struct {
		id   int
		data []byte
	}
	var cache [256]cachedTile
	var n int
	for n < len(p) {
		elem := int64(g.idx.Elem)
		logical := off + int64(n)
		row, col := int(logical/elem/int64(g.cols)), int(logical/elem%int64(g.cols))
		inside := int(logical % elem)
		r, c := row, col
		if g.transposed {
			r, c = col, row
		}
		tr, tc := r/tileSide, c/tileSide
		base, h, w := g.idx.tile(tr, tc)
		id := tr*g.tileCols + tc
		slot := &cache[id%len(cache)]
		if slot.data == nil || slot.id != id {
			block := make([]byte, h*w*int(elem))
			if _, err := g.src.ReadAt(block, base); err != nil {
				return n, stats, err
			}
			stats.TilesRead++
			stats.BytesRead += int64(len(block))
			want := g.idx.Tiles[id]
			if sha256.Sum256(block) != want {
				return n, stats, errors.New("tensor tile digest mismatch")
			}
			*slot = cachedTile{id, block}
		}
		at := (r%tileSide*w+c%tileSide)*int(elem) + inside
		if !g.transposed {
			end := (r%tileSide + 1) * w * int(elem)
			n += copy(p[n:], slot.data[at:end])
			continue
		}
		n += copy(p[n:], slot.data[at:at+int(elem)-inside])
	}
	if n < requested {
		return n, stats, io.EOF
	}
	return n, stats, nil
}

// WriteTo replays complete row bands. For normal tensor shapes each authenticated tile is read once.
func (g *Grid) WriteTo(dst io.Writer) (int64, error) {
	index := g.idx
	elem := index.Elem
	viewRows, viewCols := index.Rows, index.Cols
	outer, inner := (index.Rows+tileSide-1)/tileSide, g.tileCols
	if g.transposed {
		viewRows, viewCols = viewCols, viewRows
		outer, inner = inner, outer
	}
	maxBand := min(tileSide, viewRows) * viewCols * elem
	if maxBand > 16<<20 {
		buffer := make([]byte, 64<<10)
		return io.CopyBuffer(dst, io.NewSectionReader(g, 0, index.Size()), buffer)
	}
	band := make([]byte, maxBand)
	var scratch [tileSide * tileSide * 8]byte
	var written int64
	for strip := range outer {
		rows := min(tileSide, viewRows-strip*tileSide)
		out := band[:rows*viewCols*elem]
		for block := range inner {
			tr, tc := strip, block
			if g.transposed {
				tr, tc = block, strip
			}
			offset, height, width := index.tile(tr, tc)
			tile := scratch[:height*width*elem]
			if _, err := g.src.ReadAt(tile, offset); err != nil {
				return written, err
			}
			if sha256.Sum256(tile) != index.Tiles[tr*g.tileCols+tc] {
				return written, errors.New("tensor tile digest mismatch")
			}
			left := block * tileSide
			if g.transposed {
				for r := range height {
					for c := range width {
						src := (r*width + c) * elem
						dst := (c*viewCols + left + r) * elem
						copy(out[dst:dst+elem], tile[src:src+elem])
					}
				}
				continue
			}
			for r := range height {
				src := r * width * elem
				dst := (r*viewCols + left) * elem
				copy(out[dst:dst+width*elem], tile[src:src+width*elem])
			}
		}
		n, err := dst.Write(out)
		written += int64(n)
		if err != nil {
			return written, err
		}
		if n != len(out) {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
