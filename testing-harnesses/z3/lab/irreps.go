package lab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
	"zeros3-z3/tarz3"
)

// Z3-06/07 representations built on the exact reconstruction IR (package ir).
//
// Each raw DEFLATE stream (raw file, gzip payload, ZIP member) is parsed by the
// native parser and stored as
//
//	semantic plaintext   CDC-chunked into the family store (or, "-pu", cut at the
//	                     unit boundaries), the same CAS the preflate rep uses
//	reconstruction units content-addressed unit blobs (ir.Encode)
//	skeleton             block table, unit index, CRC: recipe metadata, charged whole
//
// A stream the parser rejects falls back to CDC of its compressed bytes. Every
// row is rebuilt from the store only (skeleton bytes + unit blobs + plaintext
// chunks), replayed, and compared by length, SHA-256 and bytes.

type parsedStream struct {
	s   *deflate.Stream
	dur time.Duration
}

func (in *Input) parseStream(off, n int64) (*parsedStream, error) {
	if ps := in.parsed[off]; ps != nil {
		return ps, nil
	}
	raw, err := in.Src.Span(off, n)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	s, err := deflate.Parse(raw, deflate.Options{MaxOutput: int(min(in.Opts.MaxSegBytes, 1<<30))})
	if err != nil {
		return nil, err
	}
	ps := &parsedStream{s: s, dur: time.Since(t0)}
	if in.parsed == nil {
		in.parsed = map[int64]*parsedStream{}
	}
	in.parsed[off] = ps
	return ps, nil
}

// StreamRegion is a raw DEFLATE stream (or wrapper/other bytes) of an object.
type StreamRegion struct {
	Off, Len int64
	Stream   bool
}

// StreamRegions partitions the object like the preflate/IR representations do.
func (in *Input) StreamRegions() ([]StreamRegion, error) {
	rs, err := deflateRegions(in)
	if err != nil {
		return nil, err
	}
	out := make([]StreamRegion, len(rs))
	for i, r := range rs {
		out[i] = StreamRegion{r.Off, r.Len, r.Stream}
	}
	return out, nil
}

// ParseStream parses the raw DEFLATE stream at [off, off+n) (cached per Input).
func (in *Input) ParseStream(off, n int64) (*deflate.Stream, error) {
	ps, err := in.parseStream(off, n)
	if err != nil {
		return nil, err
	}
	return ps.s, nil
}

// Inner replaces the CDC chunking of a member's plaintext (Z3-32). Encode returns the pieces that
// represent plain (Inline pieces carry recipe bytes, component pieces carry stored bytes) or
// ok=false to leave the plaintext to CDC. Decode rebuilds the plaintext from those pieces only.
type Inner interface {
	Encode(plain []byte) (pieces []Piece, ok bool, err error)
	Decode(pieces []Piece, get func(ID) ([]byte, error)) ([]byte, error)
}

// IRSpec names one reconstruction-IR representation.
type IRSpec struct {
	Name   string
	Codec  ir.Codec
	Policy ir.Policy
	// UnitPlain cuts the plaintext at the unit boundaries instead of CDC.
	UnitPlain bool
	Profile   uint8
	Mode      string // ratio | balanced | fast (AutoProfile only)
	NoHdrPred bool   // keep every block header exact (Z3-06/07 skeleton)
	// TarPlain cuts the plaintext with the named tar segmentation (Z3-11) instead of
	// CDC; a plaintext that is not a tar this package reconstructs exactly stays CDC.
	TarPlain string
	// Inner, when set, represents the plaintext of DEFLATE members and the data of stored ZIP
	// members it accepts (Z3-32); everything else stays CDC.
	Inner Inner
}

// IRSpecs lists the laboratory's IR representations (report order).
func IRSpecs() []IRSpec {
	pol := func(k ir.PolicyKind, t int) ir.Policy { return ir.Policy{Kind: k, Target: t} }
	return []IRSpec{
		{Name: "ir-mono-e2", Codec: ir.E2, Policy: pol(ir.PStream, 0)},
		{Name: "ir-block-e2", Codec: ir.E2, Policy: pol(ir.PBlock, 0)},
		{Name: "ir-block-e2-pu", Codec: ir.E2, Policy: pol(ir.PBlock, 0), UnitPlain: true},
		{Name: "ir-group256k-e2", Codec: ir.E2, Policy: pol(ir.PGroup, 256<<10)},
		{Name: "ir-cblk64k-e2", Codec: ir.E2, Policy: pol(ir.PContentBlocks, 64<<10)},
		{Name: "ir-tok1k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 1<<10)},
		{Name: "ir-tok2k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 2<<10)},
		{Name: "ir-tok4k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 4<<10)},
		{Name: "ir-tok8k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 8<<10)},
		{Name: "ir-tok16k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 16<<10)},
		{Name: "ir-tok16k-e2-pu", Codec: ir.E2, Policy: pol(ir.PTokRun, 16<<10), UnitPlain: true},
		{Name: "ir-tok64k-e2", Codec: ir.E2, Policy: pol(ir.PTokRun, 64<<10)},
		{Name: "ir-tok16k-e1", Codec: ir.E1, Policy: pol(ir.PTokRun, 16<<10)},
		{Name: "ir-tok16k-e1f", Codec: ir.E1Flate, Policy: pol(ir.PTokRun, 16<<10)},
		{Name: "irp-mono", Codec: ir.Pred, Policy: pol(ir.PStream, 0), Profile: ir.AutoProfile},
		{Name: "irp-tok1k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 1<<10), Profile: ir.AutoProfile},
		{Name: "irp-tok2k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 2<<10), Profile: ir.AutoProfile},
		{Name: "irp-tok4k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 4<<10), Profile: ir.AutoProfile},
		{Name: "irp-tok16k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile},
		{Name: "irp-tok64k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 64<<10), Profile: ir.AutoProfile},
		{Name: "irp-tok16k-bal", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, Mode: "balanced"},
		{Name: "irp-tok16k-fast", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, Mode: "fast"},
		{Name: "irp-tok4k-bal", Codec: ir.Pred, Policy: pol(ir.PTokRun, 4<<10), Profile: ir.AutoProfile, Mode: "balanced"},
		{Name: "irp-tok16k-xh", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, NoHdrPred: true},
		{Name: "irp-tok16k+tarmem", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, TarPlain: "tarmem"},
		{Name: "irp-tok16k+tarmem-s", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, TarPlain: "tarmem-s"},
		{Name: "irp-tok16k+tarmem-cdc64k", Codec: ir.Pred, Policy: pol(ir.PTokRun, 16<<10), Profile: ir.AutoProfile, TarPlain: "tarmem-cdc64k"},
	}
}

func irRep(spec IRSpec) Rep {
	return Rep{Name: spec.Name, Analyze: func(in *Input) (*Plan, error) { return analyzeIR(in, spec) }}
}

type irStream struct {
	first, mid, last int // pieces [first,mid) plaintext, [mid,last) units
	region           pfRegion
	skel             []byte
	plainLen         int
	plainSplit       []int // unit-aligned plaintext boundaries (UnitPlain)
	inner            bool  // pieces [first,mid) are Inner pieces, not plaintext chunks
}

func irUnitID(b []byte) ID { return sum(append([]byte("z3/ir/unit\x00"), b...)) }

func analyzeIR(in *Input, spec IRSpec) (*Plan, error) {
	regions, err := deflateRegions(in)
	if err != nil {
		return nil, err
	}
	p := &Plan{}
	var streams []*irStream
	var stored []*irStored
	var stats irStats
	t0 := time.Now()
	for _, rg := range regions {
		switch {
		case rg.Inline:
			stats.wrapper += rg.Len
			if err := appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts); err != nil {
				return nil, err
			}
			continue
		case !rg.Stream:
			var e error
			if rg.Stored && spec.Inner != nil && rg.Len > 0 {
				data, err := in.Src.Span(rg.Off, rg.Len)
				if err != nil {
					return nil, err
				}
				if pcs, ok, err := spec.Inner.Encode(data); err != nil {
					return nil, err
				} else if ok {
					first := len(p.Pieces)
					p.Pieces = append(p.Pieces, pcs...)
					stored = append(stored, &irStored{first: first, last: len(p.Pieces), region: rg})
					stats.innerStored++
					continue
				}
			}
			if rg.Len < in.Opts.InlineMax {
				e = appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts)
			} else {
				e = appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit)
			}
			if e != nil {
				return nil, e
			}
			continue
		}
		stats.streams++
		stats.comp += rg.Len
		ps, err := in.parseStream(rg.Off, rg.Len)
		if err != nil {
			stats.fallback += rg.Len
			stats.fails++
			if e := appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit); e != nil {
				return nil, e
			}
			continue
		}
		s := ps.s
		pk, err := ir.Encode(s, ir.Config{Codec: spec.Codec, Policy: spec.Policy, Profile: spec.Profile, Mode: spec.Mode, NoHeaderPred: spec.NoHdrPred})
		if err != nil {
			stats.fallback += rg.Len
			stats.fails++
			if e := appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit); e != nil {
				return nil, e
			}
			continue
		}
		if stats.profiles == nil {
			stats.profiles = map[uint8]int{}
		}
		stats.profiles[pk.Skel.Profile]++
		st := &irStream{first: len(p.Pieces), region: rg, skel: pk.Skeleton, plainLen: len(s.Plain)}
		if spec.UnitPlain {
			st.plainSplit = unitPlainBoundaries(s, pk)
			prev := 0
			for _, b := range append(st.plainSplit, len(s.Plain)) {
				if b > prev {
					cp := append([]byte(nil), s.Plain[prev:b]...)
					p.Pieces = append(p.Pieces, Piece{Comp: classed(bytesComp(sum(cp), cp), "plain"), Len: int64(len(cp))})
				}
				prev = b
			}
		} else if ts, ok := tarSpecByName(spec.TarPlain); ok {
			n0 := len(p.Pieces)
			st, terr := tarPieces(p, s.Plain, ts, in.Opts.InlineMax)
			var u *tarz3.UnsupportedError
			switch {
			case errors.As(terr, &u): // fail safe: CDC plaintext
				p.Pieces = p.Pieces[:n0]
				stats.tarFallback++
				if err := chunkBytesClass(p, s.Plain, "plain"); err != nil {
					return nil, err
				}
			case terr != nil:
				return nil, terr
			default:
				stats.tarMembers += st.Members
				stats.tarHdr += st.HdrBytes
			}
		} else if pcs, ok, err := innerPieces(spec.Inner, s.Plain); err != nil {
			return nil, err
		} else if ok {
			p.Pieces = append(p.Pieces, pcs...)
			st.inner = true
			stats.innerStreams++
		} else if err := chunkBytesClass(p, s.Plain, "plain"); err != nil {
			return nil, err
		}
		st.mid = len(p.Pieces)
		for _, u := range pk.Units {
			ub := u
			if int64(len(ub)) < unitInlineMax { // a reference would cost more than the unit itself
				p.Pieces = append(p.Pieces, Piece{Inline: ub, Len: int64(len(ub))})
				stats.inlineUnits++
				continue
			}
			p.Pieces = append(p.Pieces, Piece{Comp: classed(bytesComp(irUnitID(ub), ub), "unit"), Len: int64(len(ub))})
		}
		if len(p.Pieces) == st.first { // empty stream (e.g. an empty ZIP member): keep a placeholder so first < last
			p.Pieces = append(p.Pieces, Piece{Inline: []byte{}})
		}
		st.last = len(p.Pieces)
		p.MetaBytes += int64(len(pk.Skeleton))
		streams = append(streams, st)
		stats.plain += int64(len(s.Plain))
		stats.skel += int64(len(pk.Skeleton))
		stats.unitBytes += int64(pk.TotalUnitBytes())
		stats.units += len(pk.Units)
		stats.tokens += pk.Tokens
		stats.matches += pk.Matches
		stats.ok++
		stats.parse += ps.dur
	}
	stats.analyze = time.Since(t0)
	p.Detail = map[string]any{
		"streams": stats.streams, "streams_ok": stats.ok, "compressed_bytes": stats.comp, "plain_bytes": stats.plain,
		"ir_skeleton_bytes": stats.skel, "ir_unit_bytes": stats.unitBytes, "ir_units": stats.units, "ir_inline_units": stats.inlineUnits, "tokens": stats.tokens,
		"matches": stats.matches, "wrapper_bytes": stats.wrapper, "fallback_bytes": stats.fallback,
		"analyze_ms": ms(stats.analyze), "parse_ms": ms(stats.parse),
		"inner_streams": stats.innerStreams, "inner_stored": stats.innerStored, "tar_fallback_streams": stats.tarFallback, "tar_members": stats.tarMembers, "tar_header_bytes": stats.tarHdr,
		"codec": spec.Codec.String(), "policy": spec.Policy.String(), "profiles": profileSummary(stats.profiles),
	}
	byFirst := map[int]*irStream{}
	for _, s := range streams {
		byFirst[s.first] = s
	}
	byStored := map[int]*irStored{}
	for _, s := range stored {
		byStored[s.first] = s
	}
	var replayDur time.Duration
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		for i := 0; i < len(p.Pieces); i++ {
			if s := byStored[i]; s != nil {
				data, err := spec.Inner.Decode(p.Pieces[s.first:s.last], get)
				if err != nil {
					return err
				}
				orig, err := in.Src.Span(s.region.Off, s.region.Len)
				if err != nil {
					return err
				}
				if !bytes.Equal(data, orig) {
					return fmt.Errorf("inner reconstruction of a stored member differs (%d vs %d bytes)", len(data), len(orig))
				}
				if _, err := w.Write(data); err != nil {
					return err
				}
				i = s.last - 1
				continue
			}
			if s := byFirst[i]; s != nil {
				plain := make([]byte, 0, s.plainLen)
				if s.inner {
					d, err := spec.Inner.Decode(p.Pieces[s.first:s.mid], get)
					if err != nil {
						return err
					}
					plain = append(plain, d...)
				}
				for _, pc := range p.Pieces[s.first:s.mid] {
					if s.inner {
						break
					}
					if pc.Comp == nil { // inline piece (tar structure)
						plain = append(plain, pc.Inline...)
						continue
					}
					b, err := get(pc.Comp.ID)
					if err != nil {
						return err
					}
					plain = append(plain, b...)
				}
				units := p.Pieces[s.mid:s.last]
				var out bytes.Buffer
				t := time.Now()
				err := ir.Reconstruct(&out, s.skel, func(k int) ([]byte, error) {
					if k >= len(units) {
						return nil, fmt.Errorf("unit %d missing", k)
					}
					if units[k].Comp == nil {
						return units[k].Inline, nil
					}
					return get(units[k].Comp.ID)
				}, plain, ir.DecodeOptions{})
				replayDur += time.Since(t)
				p.Detail["replay_ms"] = ms(replayDur)
				if err != nil {
					return err
				}
				orig, err := in.Src.Span(s.region.Off, s.region.Len)
				if err != nil {
					return err
				}
				if !bytes.Equal(out.Bytes(), orig) {
					return fmt.Errorf("ir reconstruction differs from original stream (%d vs %d bytes)", out.Len(), len(orig))
				}
				if _, err := w.Write(out.Bytes()); err != nil {
					return err
				}
				i = s.last - 1
				continue
			}
			pc := p.Pieces[i]
			if pc.Comp == nil {
				if _, err := w.Write(pc.Inline); err != nil {
					return err
				}
				continue
			}
			b, err := get(pc.Comp.ID)
			if err != nil {
				return err
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return nil
	}
	return p, nil
}

// unitInlineMax: units smaller than this live in the recipe instead of the store.
const unitInlineMax = 64

type irStored struct {
	first, last int
	region      pfRegion
}

func innerPieces(in Inner, plain []byte) ([]Piece, bool, error) {
	if in == nil {
		return nil, false, nil
	}
	return in.Encode(plain)
}

type irStats struct {
	innerStored, innerStreams                  int
	profiles                                   map[uint8]int
	inlineUnits                                int
	streams, ok, fails, units, tokens, matches int
	comp, plain, skel, unitBytes, wrapper      int64
	fallback                                   int64
	tarFallback, tarMembers                    int
	tarHdr                                     int64
	analyze, parse                             time.Duration
}

// unitPlainBoundaries returns the plaintext offsets at which units begin
// (and stored-block spans begin/end), sorted, without 0.
func unitPlainBoundaries(s *deflate.Stream, pk *ir.Package) []int {
	set := map[int]bool{}
	for _, st := range pk.UnitPlainStart {
		if st > 0 {
			set[st] = true
		}
	}
	for i := range s.Blocks {
		b := &s.Blocks[i]
		if b.Kind == deflate.Stored {
			if b.PlainOff > 0 {
				set[b.PlainOff] = true
			}
			if e := b.PlainOff + b.PlainLen; e < len(s.Plain) {
				set[e] = true
			}
		}
	}
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func classed(c *Comp, class string) *Comp { c.Class = class; return c }

func chunkBytesClass(p *Plan, b []byte, class string) error {
	return forEachChunk(bytes.NewReader(b), func(off int64, c []byte) error {
		cp := append([]byte(nil), c...)
		p.Pieces = append(p.Pieces, Piece{Comp: classed(bytesComp(sum(cp), cp), class), Len: int64(len(cp))})
		return nil
	})
}

// FamilyInputs opens every revision of every selected family of a corpus.
// The returned closer releases the files.
func FamilyInputs(dir string, families []string, opts Options) (map[string][]*Input, []string, io.Closer, error) {
	m, err := corpusManifest(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	var closers multiCloser
	out := map[string][]*Input{}
	var order []string
	names, by := m.Group()
	for _, fam := range names {
		if !matchAny(families, fam) {
			continue
		}
		var prev *Input
		for _, mem := range by[fam] {
			p, err := corpusMemberPath(dir, mem)
			if err != nil {
				return nil, nil, nil, err
			}
			src, cl, err := OpenSource(p)
			if err != nil {
				return nil, nil, nil, err
			}
			closers = append(closers, cl)
			in := &Input{Family: mem.Family, Format: mem.Format, Revision: mem.Revision, Src: src, Opts: opts}
			in.Mutation = mem.Mutation
			_ = prev
			out[fam] = append(out[fam], in)
			prev = in
		}
		order = append(order, fam)
	}
	return out, order, closers, nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	for _, c := range m {
		c.Close()
	}
	return nil
}

// DropParsed releases cached parse results of this revision.
func (in *Input) DropParsed() { in.parsed = nil }

func profileSummary(m map[uint8]int) string {
	var ids []int
	for id := range m {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	var parts []string
	for _, id := range ids {
		name := fmt.Sprint(id)
		if id < len(ir.Profiles) {
			name = ir.Profiles[id].Name
		}
		parts = append(parts, fmt.Sprintf("%s x%d", name, m[uint8(id)]))
	}
	return strings.Join(parts, ", ")
}
