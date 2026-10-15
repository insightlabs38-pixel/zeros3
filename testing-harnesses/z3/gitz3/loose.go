package gitz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"io"

	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

// LoosePlan represents one loose object file. Mode ModeZ stores the file's
// zlib stream; ModeP stores the canonical object bytes (so for a SHA-256
// repository the component's SHA-256 *is* the Git object id) plus the exact
// DEFLATE reconstruction. The file name is derived from the object id when it
// is byte-identical to the real name; otherwise it is carried in the recipe.
func LoosePlan(a Algo, lf LooseFile, o RepOpts) (*lab.Plan, error) {
	lo, err := ParseLoose(a, lf.Data, Limits{})
	if err != nil {
		return nil, err
	}
	hex := HexID(lo.ID)
	derived := lf.Path == hex[:2]+"/"+hex[2:]
	var meta []byte
	meta = append(meta, metaMagic, byte(a))
	zc, explicit := zhdrCode(lf.Data[:2])
	var skel []byte
	var units [][]byte
	irOK := false
	pol := ir.Policy{Kind: ir.PStream}
	if len(lo.Canon) > defaultUnit {
		pol = ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}
	}
	if pk, err := ir.Encode(lo.Z.Stream, ir.Config{Codec: ir.Pred, Policy: pol, Profile: ir.AutoProfile}); err == nil {
		if out, err := replayDeflate(pk.Skeleton, pk.Units, lo.Canon); err == nil && bytes.Equal(out, lf.Data[2:len(lf.Data)-4]) {
			skel, units, irOK = pk.Skeleton, pk.Units, true
		}
	}
	ep := &EntryPrep{Z: lf.Data, Comp: lo.Canon, Skel: skel, Units: units, IROK: irOK, Kind: byte(lo.Type)}
	mode := o.chooseMode(ep)
	flags := byte(mode) | zc<<1
	if !derived {
		flags |= 1 << 4
	}
	meta = append(meta, flags)
	if explicit {
		meta = append(meta, lf.Data[:2]...)
	}
	if !derived {
		meta = appendUv(meta, uint64(len(lf.Path)))
		meta = append(meta, lf.Path...)
	}
	pcs, nPlain, nUnits := entryPieces(ep, mode, o)
	if mode == ModeZ {
		meta = appendUv(meta, uint64(len(pcs)))
	} else {
		meta = appendUv(meta, uint64(nPlain))
		meta = appendUv(meta, uint64(len(skel)))
		meta = append(meta, skel...)
		meta = appendUv(meta, uint64(nUnits))
	}
	pl := &lab.Plan{Pieces: pcs, MetaBytes: int64(len(meta)), Detail: map[string]any{"meta": meta}}
	pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		_, err := RebuildLoose(meta, pl.Pieces, get, w)
		return err
	}
	return pl, nil
}

// RebuildLoose writes the loose file and returns its path.
func RebuildLoose(meta []byte, pieces []lab.Piece, get func(lab.ID) ([]byte, error), w io.Writer) (string, error) {
	m := &mreader{b: meta}
	if m.byte() != metaMagic {
		return "", corrupt("recipe magic")
	}
	a := Algo(m.byte())
	if a != SHA1 && a != SHA256 {
		return "", corrupt("recipe algo")
	}
	fl := m.byte()
	mode, zc := int(fl&1), (fl>>1)&7
	var zh [2]byte
	switch {
	case zc < 4:
		zh = knownZHdr[zc]
	case zc == 7:
		copy(zh[:], m.take(2))
	default:
		return "", corrupt("zlib header code")
	}
	path := ""
	if fl&(1<<4) != 0 {
		path = string(m.take(int(m.uv(1 << 12))))
	}
	var z []byte
	if mode == ModeZ {
		n := int(m.uv(1 << 20))
		if m.err != nil || n > len(pieces) {
			return "", corrupt("recipe pieces")
		}
		for _, pc := range pieces[:n] {
			b, err := pieceBytes(pc, get)
			if err != nil {
				return "", err
			}
			z = append(z, b...)
		}
		if path == "" {
			lo, err := ParseLoose(a, z, Limits{})
			if err != nil {
				return "", err
			}
			h := HexID(lo.ID)
			path = h[:2] + "/" + h[2:]
		}
	} else {
		nPlain := int(m.uv(1 << 20))
		skel := m.take(int(m.uv(1 << 30)))
		nUnits := int(m.uv(1 << 24))
		if m.err != nil || nPlain+nUnits > len(pieces) {
			return "", corrupt("recipe pieces")
		}
		var canon []byte
		for _, pc := range pieces[:nPlain] {
			b, err := pieceBytes(pc, get)
			if err != nil {
				return "", err
			}
			canon = append(canon, b...)
		}
		ups := pieces[nPlain : nPlain+nUnits]
		ubs := make([][]byte, len(ups))
		for k := range ups {
			var err error
			if ubs[k], err = pieceBytes(ups[k], get); err != nil {
				return "", err
			}
		}
		def, err := replayDeflate(skel, ubs, canon)
		if err != nil {
			return "", err
		}
		z = append(append(zh[:0:0], zh[:]...), def...)
		z = binary.BigEndian.AppendUint32(z, adler32.Checksum(canon))
		if path == "" {
			h := HexID(a.Sum(canon))
			path = h[:2] + "/" + h[2:]
		}
	}
	if m.err != nil {
		return "", m.err
	}
	_, err := w.Write(z)
	return path, err
}

var _ = fmt.Sprint
