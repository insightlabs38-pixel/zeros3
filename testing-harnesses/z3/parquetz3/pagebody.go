package parquetz3

// PageBody returns the uncompressed body of a page (v1: the whole body; v2: the level
// bytes followed by the decompressed value section) and the codec recipe that rebuilds
// the stored payload from it. The recipe is verified. It is the "decompressed page"
// view used by the structural baselines and by the logical representation's analyzer.
func (a *Analyzer) PageBody(c *Chunk, p *Page) ([]byte, CompRec, error) {
	payload, err := ReadPayload(a.R, p)
	if err != nil {
		return nil, CompRec{}, err
	}
	if p.Type != PageDataV2 {
		return a.comp(c.Codec).Analyze(payload, int64(p.Uncomp))
	}
	lv := int64(p.DefLen) + int64(p.RepLen)
	if !p.IsComp {
		return payload, CompRec{ID: CompNone}, nil
	}
	vals, rec, err := a.comp(c.Codec).Analyze(payload[lv:], int64(p.Uncomp)-lv)
	if err != nil {
		return nil, rec, err
	}
	return append(append(make([]byte, 0, int64(len(vals))+lv), payload[:lv]...), vals...), rec, nil
}

// PagePayload rebuilds the stored payload of page p from its body and codec recipe.
func (a *Analyzer) PagePayload(p *Page, body []byte, rec CompRec) ([]byte, error) {
	cp := a.comp(CodecNone)
	if p.Type != PageDataV2 {
		return cp.Replay(rec, body)
	}
	lv := int64(p.DefLen) + int64(p.RepLen)
	if lv > int64(len(body)) {
		return nil, corrupt("v2 level bytes %d beyond the body", lv)
	}
	v, err := cp.Replay(rec, body[lv:])
	if err != nil {
		return nil, err
	}
	return append(append(make([]byte, 0, int64(len(v))+lv), body[:lv]...), v...), nil
}
