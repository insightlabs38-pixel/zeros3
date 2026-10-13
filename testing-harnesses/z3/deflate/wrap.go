package deflate

import (
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"hash/crc32"
)

// Gzip is one RFC1952 member. Header and Trailer keep the original bytes, so
// replay never regenerates a canonical header.
type Gzip struct {
	Header  []byte // ID1 ID2 CM FLG MTIME XFL OS [extra][name][comment][hcrc]
	Trailer []byte // CRC32, ISIZE (8 bytes)
	Stream  *Stream

	// Parsed header fields (informational; Header is authoritative).
	Flags          byte
	MTime          uint32
	XFL, OS        byte
	Extra          []byte
	Name, Comment  []byte
	HeaderCRC      uint16
	CRC32, ISize   uint32
	ChecksumsMatch bool
}

// ParseGzip parses one gzip member at the start of data and returns the bytes
// that follow it. Checksums (CRC32, ISIZE, header CRC16) are verified unless
// ignoreChecksums is set, in which case Gzip.ChecksumsMatch reports them.
func ParseGzip(data []byte, opt Options, ignoreChecksums bool) (*Gzip, []byte, error) {
	if len(data) < 10 {
		return nil, nil, ErrTruncated
	}
	if data[0] != 0x1f || data[1] != 0x8b || data[2] != 8 {
		return nil, nil, fmt.Errorf("%w: not a gzip deflate member", ErrCorrupt)
	}
	g := &Gzip{Flags: data[3], MTime: binary.LittleEndian.Uint32(data[4:]), XFL: data[8], OS: data[9]}
	if g.Flags&0xe0 != 0 {
		return nil, nil, fmt.Errorf("%w: reserved gzip flags %#x", ErrCorrupt, g.Flags)
	}
	n := 10
	if g.Flags&4 != 0 {
		if len(data) < n+2 {
			return nil, nil, ErrTruncated
		}
		xl := int(binary.LittleEndian.Uint16(data[n:]))
		if len(data) < n+2+xl {
			return nil, nil, ErrTruncated
		}
		g.Extra = data[n+2 : n+2+xl]
		n += 2 + xl
	}
	for _, f := range []struct {
		bit byte
		dst *[]byte
	}{{8, &g.Name}, {16, &g.Comment}} {
		if g.Flags&f.bit == 0 {
			continue
		}
		z := indexZero(data[n:])
		if z < 0 {
			return nil, nil, ErrTruncated
		}
		*f.dst = data[n : n+z]
		n += z + 1
	}
	g.ChecksumsMatch = true
	if g.Flags&2 != 0 {
		if len(data) < n+2 {
			return nil, nil, ErrTruncated
		}
		g.HeaderCRC = binary.LittleEndian.Uint16(data[n:])
		if uint16(crc32.ChecksumIEEE(data[:n])) != g.HeaderCRC {
			g.ChecksumsMatch = false
		}
		n += 2
	}
	g.Header = data[:n]
	o := opt
	o.AllowTrailing = true
	s, err := Parse(data[n:], o)
	if err != nil {
		return nil, nil, err
	}
	g.Stream = s
	end := n + s.Consumed
	if len(data) < end+8 {
		return nil, nil, ErrTruncated
	}
	g.Trailer = data[end : end+8]
	g.CRC32, g.ISize = binary.LittleEndian.Uint32(g.Trailer), binary.LittleEndian.Uint32(g.Trailer[4:])
	if crc32.ChecksumIEEE(s.Plain) != g.CRC32 || uint32(len(s.Plain)) != g.ISize {
		g.ChecksumsMatch = false
	}
	if !g.ChecksumsMatch && !ignoreChecksums {
		return nil, nil, fmt.Errorf("%w: gzip CRC32/ISIZE/header CRC", ErrChecksum)
	}
	return g, data[end+8:], nil
}

func indexZero(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

// Bytes replays the member exactly.
func (g *Gzip) Bytes() ([]byte, error) {
	body, err := g.Stream.Bytes()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(g.Header)+len(body)+8)
	out = append(out, g.Header...)
	out = append(out, body...)
	return append(out, g.Trailer...), nil
}

// ParseGzipAll parses back-to-back gzip members that exactly cover data.
func ParseGzipAll(data []byte, opt Options, ignoreChecksums bool) ([]*Gzip, error) {
	var out []*Gzip
	for len(data) > 0 {
		g, rest, err := ParseGzip(data, opt, ignoreChecksums)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
		data = rest
	}
	if len(out) == 0 {
		return nil, ErrTruncated
	}
	return out, nil
}

// Zlib is an RFC1950 stream without a preset dictionary.
type Zlib struct {
	Header  []byte // CMF FLG
	Trailer []byte // Adler-32
	Stream  *Stream
}

// ParseZlib parses a zlib wrapper that exactly covers data.
func ParseZlib(data []byte, opt Options) (*Zlib, error) {
	if len(data) < 2+4 {
		return nil, ErrTruncated
	}
	cmf, flg := data[0], data[1]
	if cmf&15 != 8 || cmf>>4 > 7 || (uint(cmf)<<8|uint(flg))%31 != 0 {
		return nil, fmt.Errorf("%w: zlib header", ErrCorrupt)
	}
	if flg&0x20 != 0 {
		return nil, fmt.Errorf("%w: zlib preset dictionary", ErrUnsupported)
	}
	body := data[2 : len(data)-4]
	s, err := Parse(body, opt)
	if err != nil {
		return nil, err
	}
	z := &Zlib{Header: data[:2], Trailer: data[len(data)-4:], Stream: s}
	if adler32.Checksum(s.Plain) != binary.BigEndian.Uint32(z.Trailer) {
		return nil, fmt.Errorf("%w: adler32", ErrChecksum)
	}
	return z, nil
}

// Bytes replays the stream exactly.
func (z *Zlib) Bytes() ([]byte, error) {
	body, err := z.Stream.Bytes()
	if err != nil {
		return nil, err
	}
	out := append(append(append([]byte(nil), z.Header...), body...), z.Trailer...)
	return out, nil
}
