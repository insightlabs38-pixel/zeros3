package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Evidence classes (every corpus and result row carries exactly one).
const (
	ClsWorkload  = "PRODUCER_WORKLOAD"
	ClsReexport  = "REAL_REEXPORT"
	ClsReference = "REFERENCE_FIXTURE"
	ClsSynthetic = "SYNTHETIC_FIXTURE"
	ClsOracle    = "ORACLE"
)

func cacheRoot() string {
	if r := os.Getenv("Z3_STRUCT_CACHE"); r != "" {
		return r
	}
	return filepath.Join(os.Getenv("HOME"), ".cache", "z3-parquet")
}

// state is one Parquet file of a family.
type state struct {
	Fam, Rev, Class, Mutation, Base, Path string
}

type famMeta struct {
	Producer  map[string]any `json:"producer"`
	Revisions []struct {
		Rev, Mutation, Note, Base string
	} `json:"revisions"`
}

func classOf(note string) string {
	for _, f := range strings.Fields(note) {
		if strings.HasPrefix(f, "class=") {
			return strings.TrimPrefix(f, "class=")
		}
	}
	return ClsWorkload
}

// loadFamily lists the states of parquet/<fam> in revision order of its meta.json.
func loadFamily(fam string) ([]state, map[string]any, error) {
	dir := filepath.Join(cacheRoot(), "parquet", fam)
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, nil, err
	}
	var m famMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, err
	}
	var out []state
	for _, r := range m.Revisions {
		out = append(out, state{fam, r.Rev, classOf(r.Note), r.Mutation, r.Base, filepath.Join(dir, r.Rev, "db.parquet")})
	}
	return out, m.Producer, nil
}

// refStates lists the pinned Apache parquet-testing fixtures.
func refStates() []state {
	dir := filepath.Join(cacheRoot(), "ref")
	ents, _ := os.ReadDir(dir)
	var out []state
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".parquet") {
			out = append(out, state{"ref", strings.TrimSuffix(e.Name(), ".parquet"), ClsReference, "", "", filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rev < out[j].Rev })
	return out
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
