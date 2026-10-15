package ociz3

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Store is a digest-addressed blob store (an OCI layout's blobs/sha256 directory,
// a registry cache, or memory). Blob returns the stored bytes without verifying
// them; callers verify descriptors. Lab-only shape: whole blobs as []byte.
type Store interface {
	Blob(digest string) ([]byte, error)
}

// MemStore is an in-memory store keyed by digest.
type MemStore map[string][]byte

func (m MemStore) Blob(d string) ([]byte, error) {
	if b, ok := m[d]; ok {
		return b, nil
	}
	return nil, ErrMissing
}

// Put stores b under its own sha256 digest and returns it.
func (m MemStore) Put(b []byte) string {
	s := sha256.Sum256(b)
	d := "sha256:" + hex.EncodeToString(s[:])
	m[d] = b
	return d
}

// DirStore reads an OCI image layout directory. Digests are validated to be
// 64 lower-case hex characters before they become part of a path, so a hostile
// descriptor cannot escape the blobs directory.
type DirStore struct {
	Root    string
	MaxBlob int64
}

func (d DirStore) Blob(digest string) ([]byte, error) {
	hexs, err := ParseDigest(digest)
	if err != nil {
		return nil, err
	}
	max := d.MaxBlob
	if max <= 0 {
		max = DefaultLimits().MaxBlob
	}
	f, err := os.Open(filepath.Join(d.Root, "blobs", "sha256", hexs))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissing
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%w: blob %d bytes", ErrLimit, st.Size())
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

// ReadLayout returns the exact bytes of oci-layout and index.json of a layout dir.
func ReadLayout(root string) (layout, index []byte, err error) {
	if layout, err = os.ReadFile(filepath.Join(root, "oci-layout")); err != nil {
		return nil, nil, err
	}
	index, err = os.ReadFile(filepath.Join(root, "index.json"))
	return
}

// Sum256 returns the sha256 digest string of b.
func Sum256(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// WriteLayout writes an OCI image layout directory whose index.json lists the
// registry root document (an image index stored as a blob). All blobs of st are
// written under blobs/sha256.
func WriteLayout(dir string, rootDigestDoc []byte, st MemStore) error {
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		return err
	}
	for d, b := range st {
		hexs, err := ParseDigest(d)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "blobs", "sha256", hexs), b, 0o644); err != nil {
			return err
		}
	}
	idx, err := LayoutRoot(rootDigestDoc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), LayoutMarker, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o644)
}

// LayoutRoot builds the index.json of a layout that holds one registry document
// (an index or a manifest) as its only entry.
func LayoutRoot(doc []byte) ([]byte, error) {
	_, mt, err := ClassifyRoot(doc, Limits{})
	if err != nil {
		return nil, err
	}
	return RootIndex([]Descriptor{{MediaType: mt, Digest: Sum256(doc), Size: int64(len(doc))}}, nil, MTOCIIndex)
}
