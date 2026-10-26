package zstdz3

import (
	"bufio"
	"io"
)

// Seq is one sequence with its offset resolved against the recent-offset history.
type Seq struct {
	LL, ML uint32
	Offset uint32 // bytes back from the match position
	OfVal  uint32 // offset value as coded: 1..3 repeat codes, larger values are Offset+3
}

// Sequences walks the transformed blocks of a described stream and calls fn with each
// block's sequences resolved to real offsets; plainOff is the stream offset of the
// block's first plaintext byte. It tracks the three recent offsets per frame exactly as
// the decoder does, so it needs only the skeleton and the unit stream.
func Sequences(s *Skeleton, units io.Reader, fn func(frame, block int, plainOff int64, seqs []Seq) error) error {
	br := bufio.NewReaderSize(units, 1<<16)
	var off int64
	for fi := range s.Frames {
		f := &s.Frames[fi]
		if f.Skippable {
			continue
		}
		rep := [3]uint32{1, 4, 8}
		for bi := range f.Blocks {
			k := &f.Blocks[bi]
			switch k.Type {
			case BlockFallback:
				if _, err := io.CopyN(io.Discard, br, int64(k.CompLen)); err != nil {
					return badIR("truncated unit stream")
				}
			case BlockCompressed:
				if k.NSeq > 0 {
					if k.Model {
						return unsupported("model-coded sequences need the plaintext; parse with NoSeqModel")
					}
					trip, err := readTriples(br, int(k.NSeq))
					if err != nil {
						return err
					}
					out := make([]Seq, len(trip))
					for i, t := range trip {
						o, err := resolveOffset(&rep, t)
						if err != nil {
							return err
						}
						out[i] = Seq{LL: t.ll, ML: t.ml, Offset: o, OfVal: t.of}
					}
					if err := fn(fi, bi, off, out); err != nil {
						return err
					}
				}
			}
			off += int64(k.DecLen)
		}
	}
	return nil
}

// resolveOffset applies the repeat-offset rules (RFC 8878 section 3.1.1.5) and returns
// the real offset, updating rep.
func resolveOffset(rep *[3]uint32, s seqRec) (uint32, error) {
	if s.of > 3 {
		off := s.of - 3
		rep[2], rep[1], rep[0] = rep[1], rep[0], off
		return off, nil
	}
	idx := int(s.of) - 1
	if s.ll == 0 {
		idx++
	}
	var off uint32
	switch idx {
	case 0:
		off = rep[0]
	case 1:
		off = rep[1]
		rep[1], rep[0] = rep[0], off
	case 2:
		off = rep[2]
		rep[2], rep[1], rep[0] = rep[1], rep[0], off
	default:
		off = rep[0] - 1
		if off == 0 {
			return 0, corrupt("repeat offset of zero")
		}
		rep[2], rep[1], rep[0] = rep[1], rep[0], off
	}
	return off, nil
}
