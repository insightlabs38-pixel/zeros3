package debugsym

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

// Plan uses the same conservative 96-byte reference accounting as the other
// frontier studies; Proof separately serializes/replays actual Concat bytes.
// target=0 keeps rows/blocks whole; positive targets restart production CDC at
// structural boundaries, grouping tiny neighboring rows to avoid reference tax.
func Plan(data []byte, target int) (*lab.Plan, *Info, error) {
	in, e := Parse(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, nil, e
	}
	p := &lab.Plan{Concat: true, MetaBytes: 32}
	add := func(b []byte, name string) {
		payload := append([]byte(nil), b...)
		c := &lab.Comp{ID: sha256.Sum256(payload), Size: int64(len(payload)), Class: name, Data: func() ([]byte, error) { return payload, nil }}
		p.Pieces = append(p.Pieces, lab.Piece{Comp: c, Len: int64(len(payload))})
	}
	spans := in.Spans
	if target > 0 {
		var grouped []Span
		for _, s := range spans {
			n := len(grouped)
			if n > 0 && grouped[n-1].Name == s.Name && grouped[n-1].End == s.Off && grouped[n-1].End-grouped[n-1].Off < int64(target) {
				grouped[n-1].End = s.End
			} else {
				grouped = append(grouped, s)
			}
		}
		spans = grouped
	}
	for _, s := range spans {
		b := data[s.Off:s.End]
		if target > 0 && len(b) > target*2 {
			for _, c := range lab.ChunkScaled(b, target/4, target, target*4) {
				add(c, s.Name)
			}
		} else {
			add(b, s.Name)
		}
	}
	p.Rebuild = lab.ConcatRebuild(p)
	return p, in, nil
}

// Proof crosses a serialized, closed decoder boundary with all physical bytes
// addressed by SHA-256. No parse object or source-file closure reaches decode.
func Proof(p *lab.Plan, want []byte) error {
	st := repz3.NewMemStore()
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(want))}
	for _, piece := range p.Pieces {
		b, e := piece.Comp.Data()
		if e != nil {
			return e
		}
		id, _ := st.Put(b)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: id, Size: int64(len(b))})
	}
	raw, e := r.Marshal()
	if e != nil {
		return e
	}
	id, _ := st.Put(raw)
	env := &repz3.Env{Src: st, Limits: repz3.Limits{MaxMembers: 1 << 20, MaxRepBytes: 32 << 20, MaxWhole: MaxFile, MaxObjects: 1 << 20}}
	var out bytes.Buffer
	n, sum, e := env.Reconstruct(id, &out)
	if e != nil {
		return e
	}
	if n != int64(len(want)) || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		return fmt.Errorf("debugsym: exact replay differs")
	}
	_, e = env.Reachable(id)
	return e
}
