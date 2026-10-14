package lab

import (
	"bytes"
	"io"
	"sort"

	"zeros3-z3/eraez3"
)

// Z3-15C: EraE representations. The file is an e2store container; headers,
// bodies and receipts are framed Snappy of RLP, total difficulty is 32 raw
// bytes, and a trailing component index holds offsets derivable from the entry
// positions.
//
//	era-entry   structural cut: every entry (8-byte header + payload) a component
//	era-entry-i64  ... entries under 64 B inline in the recipe
//	era-plain   exact plaintext representation: framed-Snappy entries proven
//	            canonical (the reference encoder reproduces them) are stored as
//	            per-type plaintext streams cut with production CDC; the layout,
//	            version/accumulator and component-index parameters are the
//	            recipe; the index is rebuilt from positions
func EraReps() []Rep {
	var out []Rep
	for _, r := range Reps() {
		switch r.Name {
		case "raw", "cdc", "fixed-64k":
			out = append(out, r)
		}
	}
	return append(out,
		Rep{"era-entry", func(in *Input) (*Plan, error) { return analyzeEraEntry(in, 0) }},
		Rep{"era-entry-i64", func(in *Input) (*Plan, error) { return analyzeEraEntry(in, 64) }},
		Rep{"era-plain", analyzeEraPlain},
	)
}

func eraNA(err error) error {
	if eraez3.IsFallback(err) {
		return ErrNotApplicable
	}
	return err
}

func analyzeEraEntry(in *Input, inlineMax int64) (*Plan, error) {
	ents, err := eraez3.Parse(in.Src.R, in.Src.Size, eraez3.DefaultLimits())
	if err != nil {
		return nil, eraNA(err)
	}
	p := &Plan{Concat: true}
	for _, e := range ents {
		n := e.End() - e.Off
		if n < inlineMax {
			b, err := in.Src.Span(e.Off, n)
			if err != nil {
				return nil, err
			}
			p.Pieces = append(p.Pieces, Piece{Inline: b, Len: n})
			continue
		}
		b, err := in.Src.Span(e.Off, n)
		if err != nil {
			return nil, err
		}
		c := spanComp(in.Src, e.Off, n, sum(b))
		c.Class = "entry"
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: n})
	}
	p.Rebuild = ConcatRebuild(p)
	return p, nil
}

func analyzeEraPlain(in *Input) (*Plan, error) {
	rep, st, err := eraez3.Build(in.Src.R, in.Src.Size, eraez3.DefaultLimits(), true)
	if err != nil {
		return nil, eraNA(err)
	}
	p := &Plan{}
	type chunkRef struct {
		id   ID
		size int
	}
	streamChunks := map[uint16][]ID{}
	var types []int
	for t := range rep.Streams {
		types = append(types, int(t))
	}
	sort.Ints(types)
	for _, ti := range types {
		t := uint16(ti)
		if err := forEachChunk(bytes.NewReader(rep.Streams[t]), func(_ int64, c []byte) error {
			cp := append([]byte(nil), c...)
			cc := bytesComp(sum(cp), cp)
			cc.Class = eraClass(t)
			p.Pieces = append(p.Pieces, Piece{Comp: cc, Len: int64(len(cp))})
			streamChunks[t] = append(streamChunks[t], cc.ID)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// large verbatim payloads (non-canonical framing, unknown entries) become
	// components; the recipe keeps a placeholder so only their reference is counted.
	slim := *rep
	slim.Verbatim = make([][]byte, len(rep.Verbatim))
	verbIDs := make([]ID, len(rep.Verbatim))
	for i, v := range rep.Verbatim {
		if len(v) >= 512 {
			vv := append([]byte(nil), v...)
			c := bytesComp(sum(vv), vv)
			c.Class = "era-verbatim"
			p.Pieces = append(p.Pieces, Piece{Comp: c, Len: int64(len(vv))})
			verbIDs[i] = c.ID
			slim.Verbatim[i] = nil
		} else {
			slim.Verbatim[i] = v
		}
	}
	blob, sz := slim.Marshal()
	p.MetaBytes = int64(len(blob))
	p.Detail = map[string]any{"recipe": sz, "stats": *st, "recipe_bytes": len(blob), "index_mode": int(rep.IndexMode)}
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		r2, err := eraez3.Unmarshal(blob)
		if err != nil {
			return err
		}
		for i, id := range verbIDs {
			if id != (ID{}) {
				b, err := get(id)
				if err != nil {
					return err
				}
				r2.Verbatim[i] = b
			}
		}
		streams := map[uint16][]byte{}
		for t, ids := range streamChunks {
			var s []byte
			for _, id := range ids {
				b, err := get(id)
				if err != nil {
					return err
				}
				s = append(s, b...)
			}
			streams[t] = s
		}
		return r2.Reconstruct(w, streams)
	}
	return p, nil
}

func eraClass(t uint16) string {
	switch t {
	case eraez3.TypeHeader:
		return "era-header"
	case eraez3.TypeBody:
		return "era-body"
	case eraez3.TypeSlimReceipts, eraez3.TypeReceipts:
		return "era-receipts"
	case eraez3.TypeTD:
		return "era-td"
	}
	return "era-other"
}
