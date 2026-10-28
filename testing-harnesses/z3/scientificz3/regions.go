// Package scientificz3 contains bounded research-only container framing adapters.
// Reconstruction uses existing serialized primitives, not these parsers.
package scientificz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sort"
)

var ErrUnsupported = errors.New("scientific: unsupported profile")
var ErrCorrupt = errors.New("scientific: corrupt framing")
var ErrLimit = errors.New("scientific: limit exceeded")

const MaxSource = 128 << 20
const MaxRegion = 16 << 20
const MaxRegions = 65536

type Region struct {
	Off, Len int64
	Codec    string
	Width    int
	Decoded  int64
}
type Plan struct {
	Format  string
	Regions []Region
}

func bad(why string) error { return fmt.Errorf("%w: %s", ErrCorrupt, why) }
func span(b []byte, o, n uint64) ([]byte, error) {
	if o > uint64(len(b)) || n > uint64(len(b))-o {
		return nil, bad("span")
	}
	return b[o : o+n], nil
}
func u(b []byte, n int) (uint64, error) {
	if n < 1 || n > 8 || len(b) < n {
		return 0, bad("integer")
	}
	var v uint64
	for i := 0; i < n; i++ {
		v |= uint64(b[i]) << uint(8*i)
	}
	return v, nil
}
func (p *Plan) validate(size int) error {
	if len(p.Regions) > MaxRegions {
		return ErrLimit
	}
	sort.Slice(p.Regions, func(i, j int) bool { return p.Regions[i].Off < p.Regions[j].Off })
	end := int64(0)
	for _, r := range p.Regions {
		if r.Off < end || r.Len <= 0 || r.Len > MaxRegion || r.Off > int64(size) || r.Len > int64(size)-r.Off {
			return bad("region order/bounds")
		}
		end = r.Off + r.Len
	}
	return nil
}

// HDF5 metadata checksum: Bob Jenkins's public-domain lookup3/hashlittle,
// independently checked against checksums written by native HDF5 producers.
func lookup3(b []byte) uint32 {
	a := uint32(0xdeadbeef) + uint32(len(b))
	c := a
	d := a
	for len(b) > 12 {
		a += binary.LittleEndian.Uint32(b)
		d += binary.LittleEndian.Uint32(b[4:])
		c += binary.LittleEndian.Uint32(b[8:])
		a -= c
		a ^= bits.RotateLeft32(c, 4)
		c += d
		d -= a
		d ^= bits.RotateLeft32(a, 6)
		a += c
		c -= d
		c ^= bits.RotateLeft32(d, 8)
		d += a
		a -= c
		a ^= bits.RotateLeft32(c, 16)
		c += d
		d -= a
		d ^= bits.RotateLeft32(a, 19)
		a += c
		c -= d
		c ^= bits.RotateLeft32(d, 4)
		d += a
		b = b[12:]
	}
	if len(b) == 0 {
		return c
	}
	for i, v := range b {
		switch {
		case i < 4:
			a += uint32(v) << uint(8*i)
		case i < 8:
			d += uint32(v) << uint(8*(i-4))
		default:
			c += uint32(v) << uint(8*(i-8))
		}
	}
	c ^= d
	c -= bits.RotateLeft32(d, 14)
	a ^= c
	a -= bits.RotateLeft32(c, 11)
	d ^= a
	d -= bits.RotateLeft32(a, 25)
	c ^= d
	c -= bits.RotateLeft32(d, 16)
	a ^= c
	a -= bits.RotateLeft32(c, 4)
	d ^= a
	d -= bits.RotateLeft32(a, 14)
	c ^= d
	c -= bits.RotateLeft32(d, 24)
	return c
}
func checksum(b []byte) bool {
	return len(b) >= 4 && lookup3(b[:len(b)-4]) == binary.LittleEndian.Uint32(b[len(b)-4:])
}
