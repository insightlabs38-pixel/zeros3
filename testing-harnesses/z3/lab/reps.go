package lab

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// Input is what a representation sees for one revision.
type Input struct {
	Family, Format string
	Revision       string
	Mutation       string // corpus mutation label of this revision (informational)
	Src            Source
	Prev           *Input // previous revision of the same family, or nil
	Opts           Options

	segs    []Seg
	segsErr error
	segsOK  bool
	dsteps  map[deltaMode]map[string]dstep

	// parsed caches native-parser results per stream offset, so several
	// DEFLATE representations of one revision parse it once.
	parsed map[int64]*parsedStream
}

// Options bound and tune laboratory experiments.
type Options struct {
	InlineMax   int64 // regions smaller than this live in the recipe
	MaxSegBytes int64 // largest segment a delta/oracle experiment will load
}

func DefaultOptions() Options { return Options{InlineMax: 512, MaxSegBytes: 256 << 20} }

// Segs returns the cached segmentation of the revision.
func (in *Input) Segs() ([]Seg, error) {
	if !in.segsOK {
		in.segs, in.segsErr = Segment(in.Format, in.Src)
		in.segsOK = true
	}
	return in.segs, in.segsErr
}

// ErrNotApplicable means the representation does not apply to this input
// (no segmenter, wrong format). It is not a failure.
var ErrNotApplicable = errors.New("lab: representation not applicable")

// Rep is a research representation: Analyze maps one revision to a Plan.
type Rep struct {
	Name    string
	Analyze func(in *Input) (*Plan, error)
}

// Reps returns the built-in representations in report order.
func Reps() []Rep {
	reps := []Rep{
		{"raw", analyzeRaw},
		{"cdc", func(in *Input) (*Plan, error) { return analyzeChunked(in, cdcSplit) }},
		{"fixed-64k", func(in *Input) (*Plan, error) { return analyzeChunked(in, fixedSplit(64<<10)) }},
		{"fixed-4k", func(in *Input) (*Plan, error) { return analyzeChunked(in, fixedSplit(4<<10)) }},
		{"struct-seg", func(in *Input) (*Plan, error) { return analyzeStruct(in, false) }},
		{"struct-cdc", func(in *Input) (*Plan, error) { return analyzeStruct(in, true) }},
		{"delta-replace", func(in *Input) (*Plan, error) { return analyzeDelta(in, deltaReplace) }},
		{"delta-xor", func(in *Input) (*Plan, error) { return analyzeDelta(in, deltaXOR) }},
		{"delta-xorz", func(in *Input) (*Plan, error) { return analyzeDelta(in, deltaXORZ) }},
		{"deflate-ir", analyzeDeflateIR},
		{"preflate", analyzePreflate},
		{"preflate-rcdc", analyzePreflateRCDC},
		{"oracle-logical", analyzeLogicalOracle},
		{"oracle-refmask", func(in *Input) (*Plan, error) { return analyzeRefMask(in, false) }},
		{"oracle-refmask+refs", func(in *Input) (*Plan, error) { return analyzeRefMask(in, true) }},
	}
	for _, sp := range IRSpecs() {
		reps = append(reps, irRep(sp))
	}
	return reps
}

// spanComp is a component whose payload is a span of a source.
func spanComp(s Source, off, n int64, id ID) *Comp {
	return &Comp{ID: id, Size: n, Data: func() ([]byte, error) { return s.Span(off, n) }}
}

func bytesComp(id ID, b []byte) *Comp {
	return &Comp{ID: id, Size: int64(len(b)), Data: func() ([]byte, error) { return b, nil }}
}

func rawID(name string, rev string, s Source) (ID, error) {
	h := sha256.New()
	h.Write([]byte("raw/" + name + "/" + rev + "/"))
	r, err := s.Reader(0, s.Size)
	if err != nil {
		return ID{}, err
	}
	if _, err := io.Copy(h, r); err != nil {
		return ID{}, err
	}
	var id ID
	copy(id[:], h.Sum(nil))
	return id, nil
}

// analyzeRaw stores every revision whole, never sharing anything.
func analyzeRaw(in *Input) (*Plan, error) {
	id, err := rawID(in.Family, in.Revision, in.Src)
	if err != nil {
		return nil, err
	}
	c := &Comp{ID: id, Size: in.Src.Size, Data: nil}
	c.Data = func() ([]byte, error) { return in.Src.Span(0, in.Src.Size) }
	p := &Plan{Pieces: []Piece{{Comp: c, Len: in.Src.Size}}, Concat: true, NoPack: true}
	p.Rebuild = ConcatRebuild(p)
	return p, nil
}

type splitFn func(r io.Reader, fn func(off int64, b []byte) error) error

func cdcSplit(r io.Reader, fn func(int64, []byte) error) error { return forEachChunk(r, fn) }

func fixedSplit(n int) splitFn {
	return func(r io.Reader, fn func(int64, []byte) error) error { return forEachFixed(r, n, fn) }
}

// appendChunked splits [off,off+n) of s and appends one piece per chunk.
func appendChunked(p *Plan, s Source, off, n int64, split splitFn) error {
	r, err := s.Reader(off, n)
	if err != nil {
		return err
	}
	return split(r, func(o int64, b []byte) error {
		p.Pieces = append(p.Pieces, Piece{Comp: spanComp(s, off+o, int64(len(b)), sum(b)), Len: int64(len(b))})
		return nil
	})
}

func analyzeChunked(in *Input, split splitFn) (*Plan, error) {
	p := &Plan{Concat: true}
	if err := appendChunked(p, in.Src, 0, in.Src.Size, split); err != nil {
		return nil, err
	}
	p.Rebuild = ConcatRebuild(p)
	return p, nil
}

// appendRegion adds a region either inline (small) or as one component.
func appendRegion(p *Plan, s Source, off, n int64, o Options) error {
	if n < o.InlineMax {
		b, err := s.Span(off, n)
		if err != nil {
			return err
		}
		p.Pieces = append(p.Pieces, Piece{Inline: b, Len: n})
		return nil
	}
	b, err := s.Span(off, n)
	if err != nil {
		return err
	}
	p.Pieces = append(p.Pieces, Piece{Comp: spanComp(s, off, n, sum(b)), Len: n})
	return nil
}

// analyzeStruct cuts the object at segment boundaries. Gaps are CDC-chunked;
// segments are stored whole (cdcInside=false) or CDC-chunked from their own
// first byte (cdcInside=true).
func analyzeStruct(in *Input, cdcInside bool) (*Plan, error) {
	segs, err := in.Segs()
	if err != nil {
		return nil, notApplicable(err)
	}
	p := &Plan{Concat: true}
	nseg := 0
	for _, rg := range Regions(in.Src.Size, segs) {
		switch {
		case rg.Len < in.Opts.InlineMax:
			err = appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts)
		case rg.Seg == nil || cdcInside:
			err = appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit)
		default:
			err = appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts)
		}
		if err != nil {
			return nil, err
		}
		if rg.Seg != nil {
			nseg++
		}
	}
	p.Rebuild = ConcatRebuild(p)
	p.Detail = map[string]any{"segments": nseg}
	return p, nil
}

func notApplicable(err error) error {
	if errors.Is(err, ErrNoSegmenter) {
		return ErrNotApplicable
	}
	return err // malformed container: an optimisation failure, reported as such
}

// ---- delta experiments ------------------------------------------------------

type deltaMode int

const (
	deltaReplace deltaMode = iota
	deltaXOR
	deltaXORZ
)

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func deflateRaw(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// analyzeDelta compares aligned equal-length segments (matched by key)
// against the previous revision. Unchanged segments are references; a changed
// segment with a same-key, same-length predecessor is a delta against that
// predecessor (base bytes are read from the previous revision; chain
// resolution cost is not modelled); everything else is stored whole.
func analyzeDelta(in *Input, mode deltaMode) (*Plan, error) {
	segs, err := in.Segs()
	if err != nil {
		return nil, notApplicable(err)
	}
	var prevSegs map[string]Seg
	var prevSteps map[string]dstep
	if in.Prev != nil {
		if ps, perr := in.Prev.Segs(); perr == nil {
			prevSegs = map[string]Seg{}
			for _, g := range ps {
				prevSegs[g.Key] = g
			}
			prevSteps = in.Prev.dsteps[mode]
		}
	}
	p := &Plan{}
	var steps []dstep
	segSteps := map[string]dstep{}
	var deltaSegs, deltaBytes, refSegs int
	for _, rg := range Regions(in.Src.Size, segs) {
		if rg.Len < in.Opts.InlineMax {
			b, err := in.Src.Span(rg.Off, rg.Len)
			if err != nil {
				return nil, err
			}
			p.Pieces = append(p.Pieces, Piece{Inline: b, Len: rg.Len})
			steps = append(steps, dstep{inline: b})
			continue
		}
		if rg.Seg == nil { // gaps: CDC chunks, stored as in every other representation
			sub := &Plan{}
			if err := appendChunked(sub, in.Src, rg.Off, rg.Len, cdcSplit); err != nil {
				return nil, err
			}
			for _, pc := range sub.Pieces {
				p.Pieces = append(p.Pieces, pc)
				steps = append(steps, dstep{comp: pc.Comp, regionLen: pc.Len})
			}
			continue
		}
		if rg.Len > in.Opts.MaxSegBytes {
			return nil, fmt.Errorf("segment %q (%d bytes) exceeds delta limit %d", rg.Seg.Key, rg.Len, in.Opts.MaxSegBytes)
		}
		cur, err := in.Src.Span(rg.Off, rg.Len)
		if err != nil {
			return nil, err
		}
		curID := sum(cur)
		st := dstep{regionLen: rg.Len, comp: spanComp(in.Src, rg.Off, rg.Len, curID)}
		if pg, ok := prevSegs[rg.Seg.Key]; ok && pg.Len == rg.Len && mode != deltaReplace {
			base, err := in.Prev.Src.Span(pg.Off, pg.Len)
			if err != nil {
				return nil, err
			}
			if sum(base) == curID {
				// Unchanged: keep whatever representation the previous
				// revision used for this segment (whole blob or delta).
				if ps, ok := prevSteps[rg.Seg.Key]; ok {
					st = ps
				}
				refSegs++
			} else {
				x := xorBytes(base, cur)
				payload := x
				if mode == deltaXORZ {
					payload = deflateRaw(x)
				}
				if mode == deltaXOR || int64(len(payload)) < rg.Len {
					h := sha256.New()
					h.Write([]byte{'d', byte(mode)})
					bid := sum(base)
					h.Write(bid[:])
					h.Write(payload)
					var id ID
					copy(id[:], h.Sum(nil))
					st.comp = bytesComp(id, payload)
					st.delta, st.z = true, mode == deltaXORZ
					st.baseSrc, st.baseOff = in.Prev.Src, pg.Off
					deltaSegs++
					deltaBytes += len(payload)
				}
			}
		}
		segSteps[rg.Seg.Key] = st
		p.Pieces = append(p.Pieces, Piece{Comp: st.comp, Len: rg.Len})
		steps = append(steps, st)
	}
	if in.dsteps == nil {
		in.dsteps = map[deltaMode]map[string]dstep{}
	}
	in.dsteps[mode] = segSteps
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		for _, st := range steps {
			if st.comp == nil {
				if _, err := w.Write(st.inline); err != nil {
					return err
				}
				continue
			}
			blob, err := get(st.comp.ID)
			if err != nil {
				return err
			}
			if st.delta {
				if st.z {
					if blob, err = io.ReadAll(flate.NewReader(bytes.NewReader(blob))); err != nil {
						return err
					}
				}
				base, err := st.baseSrc.Span(st.baseOff, st.regionLen)
				if err != nil {
					return err
				}
				if len(blob) != len(base) {
					return fmt.Errorf("delta length %d, base %d", len(blob), len(base))
				}
				blob = xorBytes(base, blob)
			}
			if _, err := w.Write(blob); err != nil {
				return err
			}
		}
		return nil
	}
	p.Detail = map[string]any{"delta_segments": deltaSegs, "delta_payload_bytes": deltaBytes, "unchanged_aligned_segments": refSegs,
		"note": "base read from the revision the delta was made against; chain walking cost not modelled"}
	return p, nil
}

// dstep is one recipe element of a delta plan: inline bytes, a stored blob,
// or a stored XOR delta to apply to bytes of an earlier revision.
type dstep struct {
	regionLen int64
	comp      *Comp
	inline    []byte
	delta, z  bool
	baseSrc   Source
	baseOff   int64
}

// ---- oracles (no reconstruction claim) --------------------------------------

// analyzeLogicalOracle chunks decoded content: ZIP member contents (plus raw
// gaps) or the plaintext of a gzip stream. It bounds what a perfect
// recompression-reconstruction transform could save, before the cost of the
// reconstruction information itself.
func analyzeLogicalOracle(in *Input) (*Plan, error) {
	p := &Plan{Oracle: true}
	switch in.Format {
	case "gzip", "deflate":
		var zr io.Reader = flate.NewReader(io.NewSectionReader(in.Src.R, 0, in.Src.Size))
		if in.Format == "gzip" {
			g, err := gzip.NewReader(io.NewSectionReader(in.Src.R, 0, in.Src.Size))
			if err != nil {
				return nil, err
			}
			zr = g
		}
		plain, err := inflateLimited(zr)
		if err != nil {
			return nil, err
		}
		if err := chunkBytes(p, plain); err != nil {
			return nil, err
		}
		p.Detail = map[string]any{"plain_bytes": len(plain)}
		return p, nil
	case "zip", "pytorch-zip", "ndarray":
		segs, err := in.Segs()
		if err != nil {
			return nil, notApplicable(err)
		}
		for _, rg := range Regions(in.Src.Size, segs) {
			if rg.Seg == nil || rg.Seg.Logical == nil {
				if err := appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit); err != nil {
					return nil, err
				}
				continue
			}
			b, err := rg.Seg.Logical()
			if err != nil {
				return nil, err
			}
			if err := chunkBytes(p, b); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	return nil, ErrNotApplicable
}

func chunkBytes(p *Plan, b []byte) error {
	return forEachChunk(bytes.NewReader(b), func(off int64, c []byte) error {
		cp := append([]byte(nil), c...)
		p.Pieces = append(p.Pieces, Piece{Comp: bytesComp(sum(cp), cp), Len: int64(len(cp))})
		return nil
	})
}

// analyzeRefMask zeroes the reference fields of a ZBIN image (rel32 calls,
// abs64 loads, the pointer table) and CDC-chunks the result. With chargeRefs
// the verbatim reference field bytes are added to the recipe, a pessimistic
// stand-in for a real symbol-relative reference encoding.
func analyzeRefMask(in *Input, chargeRefs bool) (*Plan, error) {
	if in.Format != "zbin" {
		return nil, ErrNotApplicable
	}
	segs, err := in.Segs()
	if err != nil {
		return nil, notApplicable(err)
	}
	if in.Src.Size > in.Opts.MaxSegBytes {
		return nil, fmt.Errorf("image of %d bytes exceeds mask limit", in.Src.Size)
	}
	img, err := in.Src.Span(0, in.Src.Size)
	if err != nil {
		return nil, err
	}
	var refs, refBytes int64
	for _, g := range segs {
		switch g.Key {
		case "text":
			for o := g.Off; o+binBundle <= g.Off+g.Len; o += binBundle {
				switch img[o] {
				case 0xE8:
					clear(img[o+1 : o+5])
					refs, refBytes = refs+1, refBytes+4
				case 0xB8:
					clear(img[o+1 : o+9])
					refs, refBytes = refs+1, refBytes+8
				}
			}
		case "ptrtab":
			clear(img[g.Off : g.Off+g.Len])
			refs, refBytes = refs+g.Len/8, refBytes+g.Len
		}
	}
	p := &Plan{Oracle: true}
	if err := chunkBytes(p, img); err != nil {
		return nil, err
	}
	if chargeRefs {
		p.MetaBytes = refBytes
	}
	p.Detail = map[string]any{"reference_fields": refs, "reference_field_bytes": refBytes}
	return p, nil
}

const binBundle = 16
