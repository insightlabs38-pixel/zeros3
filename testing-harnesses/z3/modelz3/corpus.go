// Package modelz3 is the Z3-29 laboratory glue for model artifacts: the pinned real corpus
// (realworld/models.json), a content-addressed accounting store, exact structural
// representations of GGUF and PyTorch ZIP files on the repz3 contract, and the measurement
// helpers the report tables use. It adds nothing to production.
package modelz3

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Entry is one pinned artifact (realworld/models.json).
type Entry struct {
	Group, Format, Name string
	URL, SHA256         string
	Size                int64
	Ref, License        string
	Relationship        string
}

// Corpus is the pinned set plus the local cache that holds the bytes (never committed).
type Corpus struct {
	Cache   string
	Entries []Entry
}

func LoadCorpus(manifest, cache string) (*Corpus, error) {
	b, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	var m struct {
		Sources []struct {
			Group        string `json:"group"`
			Format       string `json:"format"`
			Name         string `json:"name"`
			URL          string `json:"url"`
			SHA256       string `json:"sha256"`
			Size         int64  `json:"size"`
			Ref          string `json:"ref"`
			License      string `json:"license"`
			Relationship string `json:"relationship"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	c := &Corpus{Cache: cache}
	for _, s := range m.Sources {
		c.Entries = append(c.Entries, Entry{s.Group, s.Format, s.Name, s.URL, s.SHA256, s.Size, s.Ref, s.License, s.Relationship})
	}
	return c, nil
}

// Group returns the entries of a group in manifest order.
func (c *Corpus) Group(g string) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if e.Group == g {
			out = append(out, e)
		}
	}
	return out
}

// Path is the cache location of an entry's bytes.
func (c *Corpus) Path(e Entry) string { return filepath.Join(c.Cache, e.SHA256) }

// Open opens an entry and checks its size.
func (c *Corpus) Open(e Entry) (*os.File, error) {
	f, err := os.Open(c.Path(e))
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || st.Size() != e.Size {
		f.Close()
		return nil, fmt.Errorf("modelz3: %s: cached size mismatch", e.Name)
	}
	return f, nil
}
