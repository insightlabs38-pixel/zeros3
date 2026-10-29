package idxref

import (
	"fmt"
	"io"
	"sort"
)

// Span is an exact file interval. Patch stores a width-preserving reference mask.
type Span struct{ Off, Len int64 }
type Patch struct {
	Off   int64
	Bytes [5]byte
	Len   uint8
}
type viewSpan struct{ file, payload, n int64 }

// WindowView exposes masked bytes with header spans excised. It owns compact
// structural descriptors, never a file-sized byte copy. The source stays immutable.
type WindowView struct {
	r       io.ReaderAt
	spans   []viewSpan
	patches []Patch
	size    int64
}

func NewWindowView(r io.ReaderAt, size int64, excised []Span, patches []Patch) (*WindowView, error) {
	if size < 0 || len(patches) > 4<<20 || len(excised) > 1<<16 {
		return nil, bad("masked view limits")
	}
	v := &WindowView{r: r, patches: patches}
	pos := int64(0)
	for _, x := range excised {
		if x.Off < pos || x.Off > size || x.Len < 0 || x.Len > size-x.Off {
			return nil, bad("excised geometry")
		}
		if pos < x.Off {
			v.spans = append(v.spans, viewSpan{pos, v.size, x.Off - pos})
			v.size += x.Off - pos
		}
		pos = x.Off + x.Len
	}
	if pos < size {
		v.spans = append(v.spans, viewSpan{pos, v.size, size - pos})
		v.size += size - pos
	}
	sort.SliceStable(v.patches, func(i, j int) bool { return v.patches[i].Off < v.patches[j].Off })
	end := int64(0)
	for _, p := range v.patches {
		if p.Len == 0 || p.Len > 5 || p.Off < end || p.Off > size || int64(p.Len) > size-p.Off {
			return nil, bad("mask geometry")
		}
		end = p.Off + int64(p.Len)
	}
	return v, nil
}
func (v *WindowView) Size() int64 { return v.size }
func (v *WindowView) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative payload offset")
	}
	if len(b) == 0 {
		return 0, nil
	}
	if off >= v.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(b) && off < v.size {
		i := sort.Search(len(v.spans), func(i int) bool { return v.spans[i].payload+v.spans[i].n > off })
		s := v.spans[i]
		file := s.file + off - s.payload
		take := int64(len(b) - n)
		if take > s.n-(off-s.payload) {
			take = s.n - (off - s.payload)
		}
		dst := b[n : n+int(take)]
		got, err := v.r.ReadAt(dst, file)
		if got != len(dst) {
			return n + got, io.ErrUnexpectedEOF
		}
		if err != nil && err != io.EOF {
			return n + got, err
		}
		j := sort.Search(len(v.patches), func(j int) bool { return v.patches[j].Off+int64(v.patches[j].Len) > file })
		for ; j < len(v.patches) && v.patches[j].Off < file+take; j++ {
			p := &v.patches[j]
			a, z := p.Off, p.Off+int64(p.Len)
			if a < file {
				a = file
			}
			if z > file+take {
				z = file + take
			}
			copy(dst[a-file:z-file], p.Bytes[a-p.Off:z-p.Off])
		}
		n += int(take)
		off += take
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

// FNVRange hashes a range in bounded windows, identical to FNV64 over its bytes.
func FNVRange(r io.ReaderAt, start, end int64, scratch []byte) (uint64, error) {
	if start < 0 || end < start || len(scratch) == 0 {
		return 0, bad("hash range")
	}
	h := uint64(FNVInit)
	for start < end {
		n := int64(len(scratch))
		if n > end-start {
			n = end - start
		}
		b := scratch[:n]
		got, err := r.ReadAt(b, start)
		if got != len(b) {
			return 0, io.ErrUnexpectedEOF
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		h = FNV64(h, b)
		start += n
	}
	return h, nil
}
