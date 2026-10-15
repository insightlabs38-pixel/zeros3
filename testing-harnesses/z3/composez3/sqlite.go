package composez3

import (
	"bytes"
	"io"

	"zeros3-z3/lab"
	"zeros3-z3/sqlitez3"
)

func encodeSQLite(payload []byte) (*Inner, error) {
	in := &Inner{Kind: KindSQLite}
	seen := map[lab.ID]bool{}
	rec, st, err := sqlitez3.Encode(bytes.NewReader(payload), int64(len(payload)), sqlitez3.Options{}, func(id sqlitez3.ID, b []byte) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		cp := append([]byte(nil), b...)
		in.Comps = append(in.Comps, &lab.Comp{ID: id, Size: int64(len(cp)), Data: bytesData(cp)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// page bodies are payload; nodes (any other size) are inner metadata
	pr, err := sqlitez3.ParseRecipe(rec, sqlitez3.Limits{})
	if err != nil {
		return nil, err
	}
	for _, c := range in.Comps {
		c.Class = ClassMeta
		if c.Size == int64(pr.PageSize) || c.Size == int64(pr.PageSize-4) {
			c.Class = ClassPayload
		}
	}
	in.Recipe, in.Detail = rec, map[string]any{"pages": st.Pages, "nodes": st.Nodes, "split": st.Split}
	return in, nil
}

func rebuildSQLite(w io.Writer, recipe []byte, get Getter) error {
	return sqlitez3.Decode(w, recipe, func(id sqlitez3.ID) ([]byte, error) { return get(lab.ID(id)) }, sqlitez3.Limits{})
}
