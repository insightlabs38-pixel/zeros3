package composez3

import (
	"bytes"
	"io"

	"zeros3-z3/carz3"
	"zeros3-z3/lab"
)

// The CAR inner transform is the Z3-14 block-set recipe: blocks are SHA-256 components,
// CIDs derive from the digest, and the marshalled carz3 recipe is the inner recipe.
func encodeCAR(payload []byte) (*Inner, error) {
	o := carz3.Options{Limits: carz3.DefaultLimits()}
	rep, st, err := carz3.Build(bytes.NewReader(payload), int64(len(payload)), o)
	if err != nil {
		return nil, err
	}
	blob, _ := rep.Marshal()
	in := &Inner{Kind: KindCAR, Recipe: blob, Detail: map[string]any{"sections": st.Sections}}
	seen := map[[32]byte]bool{}
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if r.Inline != nil || seen[r.Sum] {
			continue
		}
		seen[r.Sum] = true
		b := append([]byte(nil), payload[r.SrcOff:r.SrcOff+r.Len]...)
		in.Comps = append(in.Comps, &lab.Comp{ID: lab.ID(r.Sum), Size: r.Len, Class: ClassPayload, Data: bytesData(b)})
	}
	return in, nil
}

func rebuildCAR(w io.Writer, recipe []byte, get Getter) error {
	rep, err := carz3.Unmarshal(recipe)
	if err != nil {
		return err
	}
	return rep.Reconstruct(w, func(s [32]byte) ([]byte, error) { return get(lab.ID(s)) })
}
