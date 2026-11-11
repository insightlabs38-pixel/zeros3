package semcore

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

// MemBackend is an in-memory Backend (tests, differential vectors, ephemeral adapters).
type MemBackend struct {
	mu      sync.Mutex
	blobs   map[ID][]byte
	recs    map[ID][]byte
	recipes map[ID][]byte
}

func NewMemBackend() *MemBackend {
	return &MemBackend{blobs: map[ID][]byte{}, recs: map[ID][]byte{}, recipes: map[ID][]byte{}}
}

func (m *MemBackend) HasBlob(id ID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.blobs[id]
	return ok
}

func (m *MemBackend) PutBlob(id ID, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[id] = append([]byte(nil), b...)
	return nil
}

type memBlob struct{ r *bytes.Reader }

func (b memBlob) ReadAt(p []byte, off int64) (int, error) { return b.r.ReadAt(p, off) }
func (b memBlob) Size() int64                             { return b.r.Size() }
func (b memBlob) Close() error                            { return nil }

func (m *MemBackend) OpenBlob(id ID) (BlobReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok {
		return nil, ErrMissing
	}
	return memBlob{bytes.NewReader(b)}, nil
}

func (m *MemBackend) DeleteBlob(id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, id)
	return nil
}

func (m *MemBackend) PutRecord(cid ID, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[cid] = append([]byte(nil), b...)
	return nil
}

func (m *MemBackend) ListRecords(fn func(ID, []byte) error) error {
	m.mu.Lock()
	ids := make([]ID, 0, len(m.recs))
	for k := range m.recs {
		ids = append(ids, k)
	}
	m.mu.Unlock()
	for _, k := range ids {
		m.mu.Lock()
		b := m.recs[k]
		m.mu.Unlock()
		if err := fn(k, b); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemBackend) PutRecipe(sha ID, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recipes[sha] = append([]byte(nil), b...)
	return nil
}

func (m *MemBackend) GetRecipe(sha ID) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.recipes[sha]
	if !ok {
		return nil, errors.New("semcore: recipe not found")
	}
	return b, nil
}

func (m *MemBackend) ListRecipes() ([]ID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ID
	for k := range m.recipes {
		out = append(out, k)
	}
	return out, nil
}

// Corrupt flips one byte of a stored blob (test hook for fail-closed behavior).
func (m *MemBackend) Corrupt(id ID, at int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok || at >= len(b) {
		return false
	}
	b[at] ^= 0x40
	return true
}

var _ = io.EOF
