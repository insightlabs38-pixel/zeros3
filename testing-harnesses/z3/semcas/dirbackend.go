package semcas

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/semcore"
)

// DirBackend is the research directory store: blobs/<payload sha>, comps/<cid>, recipes/<sha>.
type DirBackend struct{ Dir string }

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (d DirBackend) p(sub string, id semcore.ID) string {
	return filepath.Join(d.Dir, sub, id.String())
}

func (d DirBackend) HasBlob(id semcore.ID) bool {
	_, err := os.Stat(d.p("blobs", id))
	return err == nil
}
func (d DirBackend) PutBlob(id semcore.ID, b []byte) error { return writeAtomic(d.p("blobs", id), b) }
func (d DirBackend) DeleteBlob(id semcore.ID) error        { return os.Remove(d.p("blobs", id)) }

type fileBlob struct {
	f *os.File
	n int64
}

func (b fileBlob) ReadAt(p []byte, off int64) (int, error) { return b.f.ReadAt(p, off) }
func (b fileBlob) Size() int64                             { return b.n }
func (b fileBlob) Close() error                            { return b.f.Close() }

func (d DirBackend) OpenBlob(id semcore.ID) (semcore.BlobReader, error) {
	f, err := os.Open(d.p("blobs", id))
	if err != nil {
		return nil, semcore.ErrMissing
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return fileBlob{f, st.Size()}, nil
}

func (d DirBackend) PutRecord(cid semcore.ID, b []byte) error {
	return writeAtomic(d.p("comps", cid), b)
}
func (d DirBackend) PutRecipe(sha semcore.ID, b []byte) error {
	return writeAtomic(d.p("recipes", sha), b)
}
func (d DirBackend) GetRecipe(sha semcore.ID) ([]byte, error) {
	return os.ReadFile(d.p("recipes", sha))
}

func parseName(n string) (semcore.ID, error) {
	var id semcore.ID
	b, err := hex.DecodeString(n)
	if err != nil || len(b) != len(id) {
		return id, fmt.Errorf("semcas: bad component name %q", n)
	}
	copy(id[:], b)
	return id, nil
}

func (d DirBackend) ListRecords(fn func(semcore.ID, []byte) error) error {
	ents, err := os.ReadDir(filepath.Join(d.Dir, "comps"))
	if err != nil {
		return err
	}
	for _, e := range ents {
		id, err := parseName(e.Name())
		if err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(d.Dir, "comps", e.Name()))
		if err != nil {
			return err
		}
		if err := fn(id, b); err != nil {
			return err
		}
	}
	return nil
}

func (d DirBackend) ListRecipes() ([]semcore.ID, error) {
	ents, err := os.ReadDir(filepath.Join(d.Dir, "recipes"))
	if err != nil {
		return nil, err
	}
	var out []semcore.ID
	for _, e := range ents {
		id, err := parseName(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// Open creates dir if needed and returns a Store over it with the research format
// extensions (exact DEFLATE and Parquet replay) installed.
func Open(dir string) (*semcore.Store, error) {
	for _, sub := range []string{"blobs", "comps", "recipes"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	s, err := semcore.NewStore(DirBackend{dir})
	if err != nil {
		return nil, err
	}
	s.Ext = FormatExt{}
	return s, nil
}
