package scientificz3

import (
	"crypto/sha256"
	"fmt"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/zstdz3"
)

type Stats struct {
	Regions, Planes, Zstd, Opaque int
	FramingBytes, DecodedBytes    int64
	Fallbacks                     map[string]int
}

// CDC4K uses the existing exact Concat contract and laboratory scaled cutter.
func CDC4K(data []byte) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objs [][]byte
	for _, b := range lab.ChunkScaled(data, 1<<10, 4<<10, 16<<10) {
		v := append([]byte(nil), b...)
		objs = append(objs, v)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(v), Size: int64(len(v))})
	}
	return kinds.NewBundle(r, objs)
}

// planes emits existing PLANES ops over bounded STORED residual blocks.
// Fixed row groups keep the decoder's residual-role contract unchanged.
func planes(data []byte, width int) (*kinds.Bundle, error) {
	if width <= 1 || width > 4096 || len(data)%width != 0 || len(data) > MaxRegion {
		return nil, ErrUnsupported
	}
	elements := max(1, 8192/width)
	step := elements * width
	r := &repz3.Rep{Kind: repz3.KindModel, Size: int64(len(data))}
	var objects [][]byte
	var ops []kinds.ModelOp
	for off := 0; off < len(data); off += step {
		raw := data[off:min(off+step, len(data))]
		residual := kinds.PlaneChunks(raw, width, elements)
		i := len(r.Members)
		objects = append(objects, residual)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(residual), Size: int64(len(residual))})
		ops = append(ops, kinds.ModelOp{Tag: kinds.ModelPlanes, Res: i, W: width, E: elements})
	}
	r.Recipe = kinds.BuildModelRecipe(ops)
	return kinds.NewBundle(r, objects)
}

func EncodeHDF5(data []byte, deep bool) (*kinds.Bundle, Stats, error) {
	p, e := ParseHDF5(data)
	if e != nil {
		return nil, Stats{}, e
	}
	return Encode(data, p, deep)
}
func EncodeArrow(data []byte, deep bool) (*kinds.Bundle, Stats, error) {
	p, e := ParseArrow(data)
	if e != nil {
		return nil, Stats{}, e
	}
	return Encode(data, p, deep)
}

// Encode detaches validated regions. All unknown framing stays byte-exact. Deep
// mode composes existing plane and Zstd primitives; filter/LZ4 bytes stay opaque.
func Encode(data []byte, p *Plan, deep bool) (*kinds.Bundle, Stats, error) {
	st := Stats{Fallbacks: map[string]int{}}
	if len(data) > MaxSource || p == nil {
		return nil, st, ErrLimit
	}
	if e := p.validate(len(data)); e != nil {
		return nil, st, e
	}
	// Admission reserves cumulative declared embedded work before decoding any
	// region. Many tiny compressed buffers must not multiply the per-buffer cap.
	var decodedWork int64
	for _, x := range p.Regions {
		if x.Width < 0 || x.Width > 4096 || x.Decoded < 0 || x.Decoded > MaxRegion {
			return nil, st, ErrLimit
		}
		if deep && x.Codec == "zstd" {
			if x.Decoded > (1<<30)-decodedWork {
				return nil, st, ErrLimit
			}
			decodedWork += x.Decoded
		}
	}
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objs [][]byte
	var kids []*kinds.Bundle
	stored := func(b []byte) {
		if len(b) == 0 {
			return
		}
		v := append([]byte(nil), b...)
		objs = append(objs, v)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(v), Size: int64(len(v))})
	}
	fine := func(b []byte) {
		for _, v := range lab.ChunkScaled(b, 1<<10, 4<<10, 16<<10) {
			stored(v)
		}
	}
	off := int64(0)
	for _, x := range p.Regions {
		st.Regions++
		st.FramingBytes += x.Off - off
		fine(data[off:x.Off])
		raw := data[x.Off : x.Off+x.Len]
		var child *kinds.Bundle
		var e error
		switch {
		case deep && x.Codec == "raw" && x.Width > 1 && len(raw)%x.Width == 0:
			child, e = planes(raw, x.Width)
			if e == nil {
				st.Planes++
			}
		case deep && x.Codec == "zstd":
			n := max(x.Decoded, 1)
			limits := zstdz3.Limits{MaxFrames: 1, MaxBlocks: 65536, MaxWindow: 16 << 20, MaxFrameDecoded: n, MaxTotalDecoded: n, MaxLiterals: n, MaxSequences: n, MaxIRBytes: 16 << 20, MaxSkippable: 1 << 20}
			child, _, e = kinds.EncodeZstdLimited(raw, func(plain []byte) (*kinds.Bundle, error) {
				if int64(len(plain)) != x.Decoded {
					return nil, bad("Arrow decoded length")
				}
				st.DecodedBytes += int64(len(plain))
				if x.Width > 1 && len(plain)%x.Width == 0 {
					return planes(plain, x.Width)
				}
				return CDC4K(plain)
			}, limits)
			if e == nil {
				st.Zstd++
			}
		}
		if e != nil {
			st.Fallbacks[e.Error()]++
			child = nil
		}
		if child != nil {
			r.Members = append(r.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(raw), Size: int64(len(raw)), Child: child.ID})
			kids = append(kids, child)
		} else if x.Codec == "raw" {
			fine(raw)
		} else {
			st.Opaque++
			stored(raw)
		}
		off = x.Off + x.Len
	}
	st.FramingBytes += int64(len(data)) - off
	fine(data[off:])
	if len(r.Members) > 1<<20 {
		return nil, st, fmt.Errorf("%w: member count", ErrLimit)
	}
	b, e := kinds.NewBundle(r, objs, kids...)
	return b, st, e
}
