// Package admission replays durable physical candidate frontiers. It is research
// state only: no persistent planner primitive or mutable global identity space.
package admission

import (
	"encoding/hex"
	"fmt"
	"sort"
)

type Object struct {
	SHA         string
	Raw, Packed int64
	Role        string
}
type Candidate struct {
	Name, Root, ProofSHA  string
	Objects               []int
	Sample                []string
	AnalysisNS, ReadBytes int64
}
type Stage struct {
	Label, InputSHA string
	Size            int64
	Candidates      []Candidate
}
type Case struct {
	Name, Class, Evidence, KeyHint string
	Stages                         []Stage
}
type Corpus struct {
	Version        int
	ObjectOverhead int64
	Objects        []Object
	Cases          []Case
}

func sha(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }
func (c *Corpus) Validate() error {
	if c.Version != 1 || len(c.Objects) > 2_000_000 || len(c.Cases) > 256 || c.ObjectOverhead < 0 || c.ObjectOverhead > 4096 {
		return fmt.Errorf("admission: schema bound")
	}
	seen := map[string]bool{}
	objectIndex := map[string]int{}
	for index, o := range c.Objects {
		if !sha(o.SHA) || seen[o.SHA] || o.Raw < 0 || o.Raw > 128<<20 || o.Packed < 0 || o.Packed > o.Raw {
			return fmt.Errorf("admission: invalid physical object")
		}
		seen[o.SHA] = true
		objectIndex[o.SHA] = index
	}
	for _, f := range c.Cases {
		if len(f.Stages) < 1 || len(f.Stages) > 64 || f.Class == "" || f.Evidence == "" {
			return fmt.Errorf("admission: case bound/provenance")
		}
		for _, s := range f.Stages {
			if !sha(s.InputSHA) || s.Size < 0 || s.Size > 128<<20 || len(s.Candidates) < 1 || len(s.Candidates) > 8 {
				return fmt.Errorf("admission: stage bound")
			}
			names := map[string]bool{}
			cdc := false
			for _, p := range s.Candidates {
				if names[p.Name] || p.ProofSHA != s.InputSHA || !sha(p.Root) || len(p.Objects) > 1<<20 || len(p.Sample) > 64 || p.AnalysisNS < 0 || p.ReadBytes < 0 {
					return fmt.Errorf("admission: candidate proof/limit")
				}
				names[p.Name] = true
				cdc = cdc || p.Name == "cdc"
				last := -1
				for _, id := range p.Objects {
					if id <= last || id >= len(c.Objects) {
						return fmt.Errorf("admission: candidate IDs unordered/outside")
					}
					last = id
				}
				root, ok := objectIndex[p.Root]
				if !ok {
					return fmt.Errorf("admission: missing root object")
				}
				foundRoot := false
				members := map[int]bool{}
				for _, id := range p.Objects {
					members[id] = true
					foundRoot = foundRoot || id == root
				}
				if !foundRoot {
					return fmt.Errorf("admission: hidden root cost")
				}
				for _, id := range p.Sample {
					if !sha(id) || !seen[id] || !members[objectIndex[id]] {
						return fmt.Errorf("admission: sample identity")
					}
				}
			}
			if !cdc {
				return fmt.Errorf("admission: missing authoritative fallback")
			}
		}
	}
	return nil
}
func Sample(ids []string) []string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	if len(ids) > 64 {
		ids = ids[:64]
	}
	return ids
}
