package lab

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"

	"zeros3-z3/tarz3"
)

// Z3-11: tar-aware plaintext segmentation. A tar stream is cut into
//
//	"tarhdr"  the exact structural bytes of a member: its header block(s) with
//	          any PAX / GNU long-name entries, plus (small members) the payload
//	"tarpay"  the member payload and its block padding, identified by content only
//
// so a payload is the same component whatever its path, mtime, mode, owner,
// neighbours or position. The pieces concatenate to the exact tar bytes (the
// padding is part of the payload piece: it is a function of the length).

// TarSpec selects the segmentation policy.
type TarSpec struct {
	Name     string
	SmallMax int // payloads up to this many bytes travel inside the member's header piece
	CDCMin   int // payloads of at least this many bytes are cut with CDC (0 = whole-member identity)
}

// TarSpecs lists the laboratory's tar segmentations (report order).
func TarSpecs() []TarSpec {
	return []TarSpec{
		{Name: "tarmem"},                  // whole-member payloads, every member header separate
		{Name: "tarmem-s", SmallMax: 512}, // + small members merged with their header
		{Name: "tarmem-cdc64k", SmallMax: 512, CDCMin: 64 << 10},
		{Name: "tarmem-cdc16k", SmallMax: 512, CDCMin: 16 << 10},
	}
}

func tarSpecByName(n string) (TarSpec, bool) {
	for _, s := range TarSpecs() {
		if s.Name == n {
			return s, true
		}
	}
	return TarSpec{}, false
}

// TarStats describe one segmentation.
type TarStats struct {
	Members, Prefixed         int
	HdrBytes, PayBytes, Trail int64
	SmallMerged, CDCMembers   int
}

// tarPieces appends the pieces of plain (an entire tar stream) to p. It returns
// the *tarz3.UnsupportedError of an unparseable stream and appends nothing then.
func tarPieces(p *Plan, plain []byte, ts TarSpec, inlineMax int64) (TarStats, error) {
	var st TarStats
	a, err := tarz3.Parse(plain, tarz3.Options{})
	if err != nil {
		return st, err
	}
	emit := func(b []byte, class string) {
		cp := append([]byte(nil), b...)
		if int64(len(cp)) < inlineMax {
			p.Pieces = append(p.Pieces, Piece{Inline: cp, Len: int64(len(cp))})
			return
		}
		c := bytesComp(sum(cp), cp)
		c.Class = class
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: int64(len(cp))})
	}
	for i := range a.Members {
		m := &a.Members[i]
		st.Members++
		if m.Prefix > 0 {
			st.Prefixed++
		}
		hdr := plain[m.Off:m.PayOff]
		st.HdrBytes += int64(len(hdr))
		if m.PayLen == 0 {
			emit(hdr, "tarhdr")
			continue
		}
		body := plain[m.PayOff:m.End()] // payload + padding
		if m.PayLen <= int64(ts.SmallMax) {
			st.SmallMerged++
			emit(plain[m.Off:m.End()], "tarhdr")
			st.PayBytes += m.PayLen
			continue
		}
		emit(hdr, "tarhdr")
		st.PayBytes += m.PayLen
		if ts.CDCMin > 0 && m.PayLen >= int64(ts.CDCMin) {
			st.CDCMembers++
			if err := forEachChunk(bytes.NewReader(body), func(_ int64, c []byte) error {
				emit(c, "tarpay")
				return nil
			}); err != nil {
				return st, err
			}
			continue
		}
		emit(body, "tarpay")
	}
	if a.TrailOff < a.Size {
		st.Trail = a.Size - a.TrailOff
		emit(plain[a.TrailOff:], "tarhdr")
	}
	return st, nil
}

// tarPlain returns the tar bytes of an input: the file itself ("tar") or, for the
// oracle variants, the inflated stream of a gzip file (a plan that does not
// reconstruct the compressed bytes).
func tarPlain(in *Input, oracle bool) ([]byte, error) {
	switch {
	case in.Format == "tar" && !oracle:
		return in.Src.Span(0, in.Src.Size)
	case in.Format == "gzip" && oracle:
		g, err := gzip.NewReader(io.NewSectionReader(in.Src.R, 0, in.Src.Size))
		if err != nil {
			return nil, ErrNotApplicable
		}
		return inflateLimited(g)
	}
	return nil, ErrNotApplicable
}

// tarRep is the plaintext tar segmentation as a representation. oracle=false
// applies to plain tar files and is exact; oracle=true ("oracle-"+name) applies
// to gzip files, segments their inflated tar and makes no claim to rebuild the
// compressed bytes (the exact composition is the irp-*+tar* family).
func tarRep(ts TarSpec, oracle bool) Rep {
	name := ts.Name
	if oracle {
		name = "oracle-" + name
	}
	return Rep{Name: name, Analyze: func(in *Input) (*Plan, error) {
		plain, err := tarPlain(in, oracle)
		if err != nil {
			return nil, err
		}
		p := &Plan{Oracle: oracle}
		st, err := tarPieces(p, plain, ts, in.Opts.InlineMax)
		var u *tarz3.UnsupportedError
		if errors.As(err, &u) { // not a tar we reconstruct exactly: plain CDC, no claim of tar semantics
			p.Pieces = p.Pieces[:0]
			if err := chunkBytesClass(p, plain, "plain"); err != nil {
				return nil, err
			}
			p.Detail = map[string]any{"tar_unsupported": u.Reason, "plain_bytes": len(plain)}
			if !oracle {
				p.Concat = true
				p.Rebuild = ConcatRebuild(p)
			}
			return p, nil
		}
		if err != nil {
			return nil, err
		}
		p.Detail = map[string]any{"plain_bytes": len(plain), "tar_members": st.Members, "tar_prefixed": st.Prefixed, "tar_header_bytes": st.HdrBytes,
			"tar_payload_bytes": st.PayBytes, "tar_trailer_bytes": st.Trail, "tar_small_merged": st.SmallMerged, "tar_cdc_members": st.CDCMembers}
		if !oracle {
			p.Concat = true
			p.Rebuild = ConcatRebuild(p)
		}
		return p, nil
	}}
}
