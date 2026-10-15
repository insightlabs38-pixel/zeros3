package gitz3

import (
	"bytes"
	"fmt"
	"strings"
)

// BuildFixtures produces the tiny committed test fixtures (deterministic, from
// the installed git): per object format a pack with OFS_DELTA entries, a pack
// with REF_DELTA entries, a thin pack, their indexes and one loose object of
// each type. Unit tests parse the committed bytes and never need git; a test
// regenerates them when the recorded git version is installed.
func BuildFixtures(work string) (map[string][]byte, error) {
	out := map[string][]byte{"GIT_VERSION": []byte(GitVersion() + "\n")}
	for _, a := range []Algo{SHA1, SHA256} {
		h := &History{}
		rng := NewRng(3)
		t0 := Snap{"a.txt": TextFile(rng, 1500), "dir/c.txt": TextFile(rng, 300)}
		i0 := h.Add(t0)
		t1 := t0.Clone()
		t1["a.txt"] = EditText(rng, t0["a.txt"], 0.5, 2)
		i1 := h.Add(t1, i0)
		t2 := t1.Clone()
		t2["b.txt"] = TextFile(rng, 400)
		a1 := t1["a.txt"]
		t2["a.txt"] = append(append([]byte(nil), a1[:600]...), a1[800:]...) // shrinks: git deltas the smaller new blob against the larger edge blob
		h.Add(t2, i1)
		for _, v := range []struct {
			name string
			spec RepackSpec
		}{{"ofs", RepackSpec{Force: true}}, {"ref", RepackSpec{Force: true, REFDelta: true}}} {
			r, err := Build(work, "fx-"+a.String()+v.name, a, h, v.spec)
			if err != nil {
				return nil, err
			}
			pp, err := r.PackPairs()
			if err != nil || len(pp) != 1 {
				return nil, fmt.Errorf("fixture pack: %v (%d packs)", err, len(pp))
			}
			out[fmt.Sprintf("%s-%s.pack", a, v.name)] = pp[0].Pack
			out[fmt.Sprintf("%s-%s.idx", a, v.name)] = pp[0].Idx
		}
		// the same pack with a version-1 index (characterisation only)
		rv1, err := Build(work, "fx-v1-"+a.String(), a, h, RepackSpec{Force: true, IndexV1: true})
		if err != nil {
			return nil, err
		}
		pv1, err := rv1.PackPairs()
		if err != nil || len(pv1) != 1 {
			return nil, fmt.Errorf("v1 fixture: %v", err)
		}
		out[fmt.Sprintf("%s-v1.idx", a)] = pv1[0].Idx
		out[fmt.Sprintf("%s-v1.pack", a)] = pv1[0].Pack
		// loose objects of every type
		r, err := Build(work, "fx-loose-"+a.String(), a, h, SpecUnpack)
		if err != nil {
			return nil, err
		}
		lo, err := r.Loose()
		if err != nil {
			return nil, err
		}
		seen := map[ObjType]bool{}
		for _, l := range lo {
			p, err := ParseLoose(a, l.Data, Limits{})
			if err != nil {
				return nil, err
			}
			if !seen[p.Type] {
				seen[p.Type] = true
				out[fmt.Sprintf("%s-loose-%s", a, p.Type)] = l.Data
				out[fmt.Sprintf("%s-loose-%s.name", a, p.Type)] = []byte(l.Path + "\n")
			}
		}
		// annotated tag object so every type is present
		rt, err := Build(work, "fx-tag-"+a.String(), a, h, SpecUnpack)
		if err != nil {
			return nil, err
		}
		tip, err := rt.Git(nil, "rev-parse", "refs/heads/main")
		if err != nil {
			return nil, err
		}
		if _, err := rt.Git(nil, "tag", "-a", "-m", "fixture tag", "v1", strings.TrimSpace(string(tip))); err != nil {
			return nil, err
		}
		lo, err = rt.Loose()
		if err != nil {
			return nil, err
		}
		for _, l := range lo {
			if p, err := ParseLoose(a, l.Data, Limits{}); err == nil && p.Type == Tag {
				out[fmt.Sprintf("%s-loose-tag", a)] = l.Data
				out[fmt.Sprintf("%s-loose-tag.name", a)] = []byte(l.Path + "\n")
			}
		}
		// thin pack: objects of the last commit only, deltas against objects the receiver has
		rn, err := Build(work, "fx-thin-"+a.String(), a, h, SpecDefault)
		if err != nil {
			return nil, err
		}
		revs, err := rn.Git(nil, "rev-list", "--objects", "main~1..main")
		if err != nil {
			return nil, err
		}
		cfg := []string{"pack-objects", "--stdout", "--thin", "--revs", "--delta-base-offset"}
		thin, err := rn.Git(bytes.NewReader([]byte("main\n^main~1\n")), cfg...)
		_ = revs
		if err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s-thin.pack", a)] = thin
	}
	return out, nil
}
