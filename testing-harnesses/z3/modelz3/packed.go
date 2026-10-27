package modelz3

import (
	"bytes"
	"compress/flate"
	"io"
	"sync"

	"zeros3-z3/repz3"
)

// PackedSource is a repz3.Source over a MemStore whose objects are held the way the production pack
// rule holds them: DEFLATE when that saves at least 1/16, raw otherwise. A read of a packed object
// inflates the whole record (one record is cached, so sequential chunk reads inflate it once). It
// models the store-side cost of reading residual components, which the raw MemStore hides.
type PackedSource struct {
	mu      sync.Mutex
	objs    map[SHA]packedObj
	cacheID SHA
	cache   []byte
	Inflate int64 // bytes inflated
}

type packedObj struct {
	data []byte
	raw  int64
	z    bool
}

func NewPackedSource(st *repz3.MemStore) *PackedSource {
	p := &PackedSource{objs: map[SHA]packedObj{}}
	for _, id := range st.IDs() {
		b := st.Bytes(id)
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		w.Write(b)
		w.Close()
		if len(b) > 0 && int64(buf.Len()) <= int64(len(b)-max(1, len(b)/16)) {
			p.objs[id] = packedObj{buf.Bytes(), int64(len(b)), true}
		} else {
			p.objs[id] = packedObj{b, int64(len(b)), false}
		}
	}
	return p
}

func (p *PackedSource) Size(id SHA) (int64, error) {
	o, ok := p.objs[id]
	if !ok {
		return 0, repz3.ErrMissing
	}
	return o.raw, nil
}

func (p *PackedSource) ReadAt(id SHA, b []byte, off int64) (int, error) {
	o, ok := p.objs[id]
	if !ok {
		return 0, repz3.ErrMissing
	}
	if off < 0 || off > o.raw {
		return 0, io.EOF
	}
	raw := o.data
	if o.z {
		p.mu.Lock()
		if p.cache == nil || p.cacheID != id {
			out, err := io.ReadAll(flate.NewReader(bytes.NewReader(o.data)))
			if err != nil {
				p.mu.Unlock()
				return 0, err
			}
			p.cache, p.cacheID = out, id
			p.Inflate += int64(len(out))
		}
		raw = p.cache
		p.mu.Unlock()
	}
	n := copy(b, raw[off:])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}
