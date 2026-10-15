package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/fstate"
)

type RevMeta struct {
	Rev          string `json:"rev"`
	Mutation     string `json:"mutation"`
	LogicalBytes int64  `json:"logical_bytes"`
	Note         string `json:"note"`
	Base         string `json:"base"`
}

type FamMeta struct {
	Lane      string         `json:"lane"`
	Family    string         `json:"family"`
	Seed      int            `json:"seed"`
	Producer  map[string]any `json:"producer"`
	Revisions []RevMeta      `json:"revisions"`
}

func cacheRoot() string {
	if p := os.Getenv("Z3_STRUCT_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "structured")
}

func loadMeta(dir string) (*FamMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var m FamMeta
	return &m, json.Unmarshal(b, &m)
}

// loadState reads every file of a revision directory (the oracle layout sidecars
// are not part of the state; see loadSidecar).
func loadState(dir, rev, kind string) (*fstate.State, error) {
	root := filepath.Join(dir, rev)
	st := &fstate.State{Label: rev}
	var files []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasSuffix(p, ".layout.json") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(root, p)
		st.Files = append(st.Files, fstate.File{Name: rel, Kind: kind, Data: b})
	}
	return st, nil
}
