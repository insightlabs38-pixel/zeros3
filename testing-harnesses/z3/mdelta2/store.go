package mdelta2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// StoreDir holds containers named by the SHA-256 of the model file they reconstruct.
type StoreDir struct{ Dir string }

func (s StoreDir) Path(id SHA) string { return filepath.Join(s.Dir, hex.EncodeToString(id[:])+".zb5") }

func (s StoreDir) Put(c *Container, b []byte) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path(c.FileSHA), b, 0o644)
}

// StoreResolver decodes base tensors out of stored containers, bounding depth, rejecting cycles and
// caching decoded tensors under a byte budget. Not safe for concurrent use.
type StoreResolver struct {
	S        StoreDir
	MaxDepth int
	CacheCap int64
	conts    map[SHA]*Container
	cache    map[tkey][]byte
	cacheSz  int64
	stack    []SHA
	Reads    int64 // container bytes read from the store
	Loads    int
}

type tkey struct {
	b SHA
	i int
}

func NewStoreResolver(s StoreDir) *StoreResolver {
	return &StoreResolver{S: s, MaxDepth: MaxDepth, CacheCap: 256 << 20, conts: map[SHA]*Container{}, cache: map[tkey][]byte{}}
}

func (r *StoreResolver) load(id SHA) (*Container, error) {
	if c, ok := r.conts[id]; ok {
		return c, nil
	}
	b, err := os.ReadFile(r.S.Path(id))
	if err != nil {
		return nil, ErrMissing
	}
	r.Reads += int64(len(b))
	r.Loads++
	c, err := ParseContainer(b)
	if err != nil {
		return nil, err
	}
	if c.FileSHA != id {
		return nil, ErrIdentity
	}
	r.conts[id] = c
	return c, nil
}

func (r *StoreResolver) Tensor(base SHA, idx int) (DType, []byte, error) {
	for _, s := range r.stack {
		if s == base {
			return 0, nil, ErrCycle
		}
	}
	if len(r.stack) >= r.MaxDepth {
		return 0, nil, ErrDepth
	}
	c, err := r.load(base)
	if err != nil {
		return 0, nil, err
	}
	if idx < 0 || idx >= len(c.T) {
		return 0, nil, ErrFormat
	}
	k := tkey{base, idx}
	if d, ok := r.cache[k]; ok {
		return c.T[idx].DType, d, nil
	}
	r.stack = append(r.stack, base)
	d, err := c.DecodeTensor(idx, r)
	r.stack = r.stack[:len(r.stack)-1]
	if err != nil {
		return 0, nil, err
	}
	if r.cacheSz+int64(len(d)) <= r.CacheCap {
		r.cache[k] = d
		r.cacheSz += int64(len(d))
	}
	return c.T[idx].DType, d, nil
}

// Replay decodes the stored file id with a fresh resolver and returns its bytes.
func (s StoreDir) Replay(id SHA) ([]byte, *StoreResolver, error) {
	r := NewStoreResolver(s)
	c, err := r.load(id)
	if err != nil {
		return nil, r, err
	}
	var buf bytes.Buffer
	buf.Grow(int(c.FileLen))
	r.stack = []SHA{id}
	err = c.DecodeTo(&buf, r)
	return buf.Bytes(), r, err
}

func FileSHA(b []byte) SHA { return sha256.Sum256(b) }

func (r *StoreResolver) Gaps(base SHA) ([]byte, error) {
	for _, s := range r.stack {
		if s == base {
			return nil, ErrCycle
		}
	}
	if len(r.stack) >= r.MaxDepth {
		return nil, ErrDepth
	}
	c, err := r.load(base)
	if err != nil {
		return nil, err
	}
	r.stack = append(r.stack, base)
	err = c.resolveGaps(r)
	r.stack = r.stack[:len(r.stack)-1]
	return c.Gaps, err
}
