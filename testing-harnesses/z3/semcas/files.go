package semcas

import (
	"bytes"
	"errors"
	"fmt"

	"zeros3-z3/deflate"
	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
)

// FilePlanner stores container formats (tar, gzip layers, Git packs and loose objects) as
// recipes over format-neutral content components: file/blob bytes carry no framing, so a
// tar member, a Git blob and an OCI layer entry with the same bytes share one component.
type FilePlanner struct {
	S                 *Store
	ChunkMin, ChunkTg int
	ChunkMax          int
	// Exact reports stream/entry counts by outcome.
	Exact map[string]int
}

func NewFilePlanner(s *Store) *FilePlanner {
	return &FilePlanner{S: s, ChunkMin: 2 << 10, ChunkTg: 8 << 10, ChunkMax: 32 << 10, Exact: map[string]int{}}
}

func bytesDesc(kind uint8, n int) Desc {
	return Desc{Kind: kind, Version: 1, DType: DU8, Shape: []uint64{uint64(n)}, Ordered: true}
}

// content stores whole-file bytes as a KindFile component. Large files are physically
// chunked by CDC (a recipe over KindBytes chunks) so revisions keep sharing chunks, while
// the semantic identity stays that of the complete file.
func (f *FilePlanner) content(kind uint8, b []byte) (ID, error) {
	d := bytesDesc(kind, len(b))
	if len(b) < LitChunkMin {
		r, err := f.S.PutRaw(d, b)
		if err != nil {
			return ID{}, err
		}
		return r.CID, nil
	}
	cid := CID(&d, int64(len(b)), PayloadSHA(b))
	if f.S.Has(cid) {
		return cid, nil
	}
	var bd Builder
	for _, ch := range lab.ChunkScaled(b, f.ChunkMin, f.ChunkTg, f.ChunkMax) {
		cd := bytesDesc(KindBytes, len(ch))
		r, err := f.S.PutRaw(cd, ch)
		if err != nil {
			return ID{}, err
		}
		bd.Ref(r.CID, "")
	}
	bd.R.Size, bd.R.SHA = int64(len(b)), PayloadSHA(b)
	r, err := f.S.PutDerived(d, nil, []Op{{Kind: OpRecipe, Aux: bd.R.Marshal()}}, nil, b)
	if err != nil {
		return ID{}, err
	}
	return r.CID, nil
}

// recipeComp stores bytes that are exactly the replay of recipe rc as a derived component.
func (f *FilePlanner) recipeComp(kind uint8, rc *Recipe, data []byte) (ID, error) {
	d := bytesDesc(kind, len(data))
	r, err := f.S.PutDerived(d, nil, []Op{{Kind: OpRecipe, Aux: rc.Marshal()}}, nil, data)
	if err != nil {
		return ID{}, err
	}
	return r.CID, nil
}

// deflated stores a raw DEFLATE stream as a derivation from its plaintext component;
// without exact replay the compressed bytes become an opaque component instead.
func (f *FilePlanner) deflated(plainID ID, plain, raw []byte, st *deflate.Stream) (ID, string, error) {
	if st != nil {
		if _, res, ok := encodeStream(st, raw); ok {
			d := bytesDesc(KindBytes, len(raw))
			r, err := f.S.PutDerived(d, []ID{plainID}, []Op{{Kind: OpDeflate, Aux: res}}, nil, raw)
			if err != nil {
				return ID{}, "", err
			}
			return r.CID, "replay", nil
		}
	}
	id, err := f.content(KindBytes, raw)
	return id, "opaque", err
}

// IngestTar handles an uncompressed tar or a single-member gzip of one.
func (f *FilePlanner) IngestTar(name string, data []byte) (*Recipe, error) {
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, rest, err := deflate.ParseGzip(data, deflate.Options{MaxOutput: 1 << 30}, false)
		if err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("semcas: %s: gzip: %v", name, err)
		}
		plain := gz.Stream.Plain
		tarID, err := f.tarComp(plain)
		if err != nil {
			return nil, err
		}
		raw := data[len(gz.Header) : len(data)-len(gz.Trailer)]
		dID, how, err := f.deflated(tarID, plain, raw, gz.Stream)
		if err != nil {
			return nil, err
		}
		f.Exact["gzip-"+how]++
		b.Lit(gz.Header)
		b.Ref(dID, "deflate")
		b.Lit(gz.Trailer)
		rc := b.R
		return &rc, nil
	}
	tarID, err := f.tarComp(data)
	if err != nil {
		return nil, err
	}
	b.Ref(tarID, "tar")
	rc := b.R
	return &rc, nil
}

// tarComp splits a tar into header/padding literals and file-content components and returns
// the component whose payload is the whole tar.
func (f *FilePlanner) tarComp(tar []byte) (ID, error) {
	a, err := tarz3.Parse(tar, tarz3.Options{})
	if err != nil {
		var ue *tarz3.UnsupportedError
		if errors.As(err, &ue) {
			f.Exact["tar-unsupported"]++
		}
		id, e := f.content(KindBytes, tar)
		return id, e
	}
	var b Builder
	b.R.Size, b.R.SHA = int64(len(tar)), PayloadSHA(tar)
	pos := int64(0)
	for i := range a.Members {
		m := &a.Members[i]
		if m.Type != '0' || m.PayLen == 0 {
			continue
		}
		b.Lit(tar[pos:m.PayOff])
		cid, err := f.content(KindFile, tar[m.PayOff:m.PayOff+m.PayLen])
		if err != nil {
			return ID{}, err
		}
		b.Ref(cid, m.Path)
		pos = m.PayOff + m.PayLen
	}
	b.Lit(tar[pos:])
	f.Exact["tar"]++
	return f.recipeComp(KindBytes, &b.R, tar)
}

// IngestGitPack represents a Git v2 pack: entry headers and the trailer are literals; every
// non-delta entry's zlib stream is an exact replay from its object content (blobs become
// KindFile components); delta entries keep their compressed program bytes.
func (f *FilePlanner) IngestGitPack(name string, data []byte) (*Recipe, error) {
	p, err := gitz3.ParsePack(data, gitz3.SHA1, gitz3.Limits{})
	if err != nil {
		return nil, err
	}
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	pos := int64(0)
	for i := range p.Entries {
		e := &p.Entries[i]
		b.Lit(data[pos:e.ZOff])
		pos = e.ZEnd
		z := data[e.ZOff:e.ZEnd]
		if e.IsDelta() {
			f.Exact["git-delta-entry"]++
			id, err := f.content(KindBytes, z)
			if err != nil {
				return nil, err
			}
			b.Ref(id, "delta")
			continue
		}
		zl, err := deflate.ParseZlib(z, deflate.Options{MaxOutput: 1 << 30})
		if err != nil {
			id, e2 := f.content(KindBytes, z)
			if e2 != nil {
				return nil, e2
			}
			f.Exact["git-entry-opaque"]++
			b.Ref(id, "opaque")
			continue
		}
		kind := uint8(KindBytes)
		if e.Kind == 3 {
			kind = KindFile
		}
		plainID, err := f.content(kind, zl.Stream.Plain)
		if err != nil {
			return nil, err
		}
		raw := z[len(zl.Header) : len(z)-len(zl.Trailer)]
		dID, how, err := f.deflated(plainID, zl.Stream.Plain, raw, zl.Stream)
		if err != nil {
			return nil, err
		}
		f.Exact["git-entry-"+how]++
		b.Lit(zl.Header)
		b.Ref(dID, "zlib")
		b.Lit(zl.Trailer)
	}
	b.Lit(data[pos:])
	rc := b.R
	return &rc, nil
}

// IngestGitLoose represents one loose object file (zlib of "type size\0 payload").
func (f *FilePlanner) IngestGitLoose(name string, data []byte) (*Recipe, error) {
	zl, err := deflate.ParseZlib(data, deflate.Options{MaxOutput: 1 << 30})
	if err != nil {
		return nil, err
	}
	canon := zl.Stream.Plain
	i := bytes.IndexByte(canon, 0)
	if i < 0 {
		return nil, errors.New("semcas: loose object has no header terminator")
	}
	hdr, payload := canon[:i+1], canon[i+1:]
	kind := uint8(KindBytes)
	if bytes.HasPrefix(hdr, []byte("blob ")) {
		kind = KindFile
	}
	cID, err := f.content(kind, payload)
	if err != nil {
		return nil, err
	}
	od := bytesDesc(KindBytes, len(canon))
	obj, err := f.S.PutDerived(od, []ID{cID}, []Op{{Kind: OpPrepend, Aux: append([]byte(nil), hdr...)}}, nil, canon)
	if err != nil {
		return nil, err
	}
	raw := data[len(zl.Header) : len(data)-len(zl.Trailer)]
	dID, how, err := f.deflated(obj.CID, canon, raw, zl.Stream)
	if err != nil {
		return nil, err
	}
	f.Exact["git-loose-"+how]++
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	b.Lit(zl.Header)
	b.Ref(dID, "zlib")
	b.Lit(zl.Trailer)
	rc := b.R
	return &rc, nil
}

// IngestRaw stores an unrecognized file as chunked opaque bytes.
func (f *FilePlanner) IngestRaw(name string, data []byte) (*Recipe, error) {
	id, err := f.content(KindBytes, data)
	if err != nil {
		return nil, err
	}
	var b Builder
	b.R.Size, b.R.SHA = int64(len(data)), PayloadSHA(data)
	b.Ref(id, "raw")
	rc := b.R
	return &rc, nil
}
