// Package torchz3 is the Z3-29 strict, bounded raw ZIP/ZIP64 structure parser for `torch.save`
// checkpoints: stdlib only, ReaderAt plus size, no extraction, no pickle execution (data.pkl is an
// opaque byte range). It accepts exactly the shape torch writes - stored (method 0) members laid
// out back to back, each with a local header (alignment padding in the extra field), its data and
// an optional data descriptor, followed by the central directory and the ZIP64/regular end
// records - and tiles the file exactly so a representation can rebuild it byte for byte.
// Anything else (deflate, gaps, overlap, a central/local mismatch, multi-disk) returns
// ErrUnsupported: the file keeps its CDC representation.
package torchz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

var (
	ErrFormat      = errors.New("torchz3: not a valid ZIP file")
	ErrUnsupported = errors.New("torchz3: ZIP feature outside the torch.save shape")
	ErrLimit       = errors.New("torchz3: limit exceeded")
)

// Limits bound every parse.
type Limits struct {
	MaxEntries int64 // default 1<<20
	MaxCD      int64 // central directory bytes (default 256 MiB)
	MaxName    int   // default 4096
}

func (l Limits) norm() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = 1 << 20
	}
	if l.MaxCD <= 0 {
		l.MaxCD = 256 << 20
	}
	if l.MaxName <= 0 {
		l.MaxName = 4096
	}
	return l
}

// Member is one archive entry with the absolute spans of its parts.
type Member struct {
	Name         string
	Flags        uint16
	CRC          uint32
	HeaderOff    int64 // local header start
	DataBegin    int64 // first byte of the stored data
	DataEnd      int64
	DescEnd      int64 // end of the data descriptor (== DataEnd when there is none)
	Zip64Desc    bool
	LocalExtra   int
	CentralBegin int64
	CentralEnd   int64
}

// Size is the stored length of the member data.
func (m *Member) Size() int64 { return m.DataEnd - m.DataBegin }

// File is a parsed checkpoint archive.
type File struct {
	Size    int64
	Members []Member // file order
	CDBegin int64
	CDEnd   int64
	Zip64   bool // a ZIP64 end record is present
	Comment int
}

const (
	sigLocal   = 0x04034b50
	sigCentral = 0x02014b50
	sigEOCD    = 0x06054b50
	sigEOCD64  = 0x06064b50
	sigLoc64   = 0x07064b50
	sigDesc    = 0x08074b50
)

func le16(b []byte) uint64 { return uint64(binary.LittleEndian.Uint16(b)) }
func le32(b []byte) uint64 { return uint64(binary.LittleEndian.Uint32(b)) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

func readAt(r io.ReaderAt, off, n int64) ([]byte, error) {
	if n < 0 || off < 0 {
		return nil, fmt.Errorf("%w: bad range", ErrFormat)
	}
	b := make([]byte, n)
	got, err := r.ReadAt(b, off)
	if int64(got) < n {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	return b, nil
}

// Parse reads the end records and the central directory, then the local header of every member.
// It reads no member data.
func Parse(r io.ReaderAt, size int64, lim Limits) (*File, error) {
	lim = lim.norm()
	if size < 22 {
		return nil, fmt.Errorf("%w: %d bytes", ErrFormat, size)
	}
	tailN := min(size, 22+65535)
	tail, err := readAt(r, size-tailN, tailN)
	if err != nil {
		return nil, err
	}
	// the last EOCD whose comment length reaches the end of the file
	eo := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if le32(tail[i:]) == sigEOCD && int(le16(tail[i+20:])) == len(tail)-i-22 {
			eo = i
			break
		}
	}
	if eo < 0 {
		return nil, fmt.Errorf("%w: no end of central directory", ErrFormat)
	}
	eoff := size - tailN + int64(eo)
	e := tail[eo:]
	f := &File{Size: size, Comment: int(le16(e[20:]))}
	disk, cdDisk := le16(e[4:]), le16(e[6:])
	nEntries, total := le64(e[10:])&0xffff, le64(e[8:])&0xffff
	cdSize, cdOff := le32(e[12:]), le32(e[16:])
	if disk != 0 || cdDisk != 0 {
		return nil, fmt.Errorf("%w: multi-disk archive", ErrUnsupported)
	}
	end := eoff // end of what precedes the regular EOCD
	if nEntries == 0xffff || cdSize == 0xffffffff || cdOff == 0xffffffff || total == 0xffff || eoff >= 20 {
		// a ZIP64 locator directly before the EOCD
		if eoff >= 20 {
			lb, err := readAt(r, eoff-20, 20)
			if err != nil {
				return nil, err
			}
			if le32(lb) == sigLoc64 {
				f.Zip64 = true
				if le32(lb[4:]) != 0 || le32(lb[16:]) != 1 {
					return nil, fmt.Errorf("%w: ZIP64 multi-disk", ErrUnsupported)
				}
				o64 := le64(lb[8:])
				if o64 > uint64(eoff-20) || int64(o64)+56 != eoff-20 {
					return nil, fmt.Errorf("%w: ZIP64 end record is not directly before its locator", ErrUnsupported)
				}
				zb, err := readAt(r, int64(o64), 56)
				if err != nil {
					return nil, err
				}
				if le32(zb) != sigEOCD64 || le64(zb[4:]) != 44 {
					return nil, fmt.Errorf("%w: ZIP64 end record", ErrUnsupported)
				}
				if le32(zb[16:]) != 0 || le32(zb[20:]) != 0 {
					return nil, fmt.Errorf("%w: ZIP64 multi-disk", ErrUnsupported)
				}
				z1, z2, z3, z4 := le64(zb[24:]), le64(zb[32:]), le64(zb[40:]), le64(zb[48:])
				// the regular record may saturate a field but may not contradict the ZIP64 one
				if (nEntries != 0xffff && nEntries != z1) || (total != 0xffff && total != z2) ||
					(cdSize != 0xffffffff && cdSize != z3) || (cdOff != 0xffffffff && cdOff != z4) {
					return nil, fmt.Errorf("%w: regular and ZIP64 end records disagree", ErrFormat)
				}
				nEntries, total, cdSize, cdOff = z1, z2, z3, z4
				end = int64(o64)
			}
		}
	}
	if nEntries != total || nEntries > uint64(lim.MaxEntries) {
		return nil, fmt.Errorf("%w: %d entries", ErrLimit, nEntries)
	}
	if cdSize > uint64(lim.MaxCD) || cdOff > uint64(end) || cdSize > uint64(end)-cdOff || uint64(end)-cdOff != cdSize {
		return nil, fmt.Errorf("%w: central directory [%d,+%d) does not end at %d", ErrFormat, cdOff, cdSize, end)
	}
	if nEntries > cdSize/46 {
		return nil, fmt.Errorf("%w: %d entries cannot fit %d directory bytes", ErrFormat, nEntries, cdSize)
	}
	f.CDBegin, f.CDEnd = int64(cdOff), int64(cdOff+cdSize)
	cd, err := readAt(r, f.CDBegin, int64(cdSize))
	if err != nil {
		return nil, err
	}
	f.Members = make([]Member, 0, nEntries)
	names := map[string]bool{}
	pos := 0
	for i := uint64(0); i < nEntries; i++ {
		if pos+46 > len(cd) || le32(cd[pos:]) != sigCentral {
			return nil, fmt.Errorf("%w: central entry %d", ErrFormat, i)
		}
		h := cd[pos:]
		flags, method := uint16(le16(h[8:])), le16(h[10:])
		nlen, xlen, clen := int(le16(h[28:])), int(le16(h[30:])), int(le16(h[32:]))
		if nlen > lim.MaxName || pos+46+nlen+xlen+clen > len(cd) {
			return nil, fmt.Errorf("%w: central entry %d lengths", ErrFormat, i)
		}
		if method != 0 {
			return nil, fmt.Errorf("%w: compression method %d", ErrUnsupported, method)
		}
		if le16(h[34:]) != 0 {
			return nil, fmt.Errorf("%w: multi-disk member", ErrUnsupported)
		}
		m := Member{Name: string(h[46 : 46+nlen]), Flags: flags, CRC: uint32(le32(h[16:])),
			CentralBegin: f.CDBegin + int64(pos)}
		csz, usz, lho := le32(h[20:]), le32(h[24:]), le32(h[42:])
		ex := h[46+nlen : 46+nlen+xlen]
		for len(ex) >= 4 {
			id, l := le16(ex), int(le16(ex[2:]))
			if 4+l > len(ex) {
				return nil, fmt.Errorf("%w: extra field", ErrFormat)
			}
			if id == 1 { // ZIP64: present values in order usize, csize, offset
				z := ex[4 : 4+l]
				take := func(v *uint64) error {
					if *v != 0xffffffff {
						return nil
					}
					if len(z) < 8 {
						return fmt.Errorf("%w: short ZIP64 extra", ErrFormat)
					}
					*v, z = le64(z), z[8:]
					m.Zip64Desc = true
					return nil
				}
				if err := take(&usz); err != nil {
					return nil, err
				}
				if err := take(&csz); err != nil {
					return nil, err
				}
				if err := take(&lho); err != nil {
					return nil, err
				}
			}
			ex = ex[4+l:]
		}
		if csz != usz {
			return nil, fmt.Errorf("%w: stored member with sizes %d/%d", ErrFormat, csz, usz)
		}
		if lho > uint64(f.CDBegin) || csz > uint64(f.CDBegin)-lho {
			return nil, fmt.Errorf("%w: member %q outside the file", ErrFormat, m.Name)
		}
		if names[m.Name] {
			return nil, fmt.Errorf("%w: duplicate member %q", ErrFormat, m.Name)
		}
		names[m.Name] = true
		m.HeaderOff = int64(lho)
		m.DataEnd = int64(csz) // size for now
		m.CentralEnd = f.CDBegin + int64(pos+46+nlen+xlen+clen)
		// local header
		lb, err := readAt(r, m.HeaderOff, 30)
		if err != nil {
			return nil, err
		}
		if le32(lb) != sigLocal || uint16(le16(lb[6:])) != flags || le16(lb[8:]) != 0 {
			return nil, fmt.Errorf("%w: local header of %q", ErrFormat, m.Name)
		}
		ln, lx := int(le16(lb[26:])), int(le16(lb[28:]))
		nb, err := readAt(r, m.HeaderOff+30, int64(ln))
		if err != nil {
			return nil, err
		}
		if string(nb) != m.Name {
			return nil, fmt.Errorf("%w: local name %q differs from central %q", ErrFormat, nb, m.Name)
		}
		m.LocalExtra = lx
		m.DataBegin = m.HeaderOff + 30 + int64(ln) + int64(lx)
		m.DataEnd = m.DataBegin + int64(csz)
		m.DescEnd = m.DataEnd
		if m.DataEnd > f.CDBegin {
			return nil, fmt.Errorf("%w: member %q data past the central directory", ErrFormat, m.Name)
		}
		f.Members = append(f.Members, m)
		pos += 46 + nlen + xlen + clen
	}
	if pos != len(cd) {
		return nil, fmt.Errorf("%w: %d stray bytes in the central directory", ErrFormat, len(cd)-pos)
	}
	// file order must tile [0, CDBegin): sort by header offset
	order := make([]int, len(f.Members))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return f.Members[order[a]].HeaderOff < f.Members[order[b]].HeaderOff })
	next := int64(0)
	sorted := make([]Member, len(f.Members))
	for k, i := range order {
		m := f.Members[i]
		if m.HeaderOff != next {
			return nil, fmt.Errorf("%w: gap or overlap before %q at %d (expected %d)", ErrUnsupported, m.Name, m.HeaderOff, next)
		}
		if m.Flags&8 != 0 {
			// data descriptor: optional signature, crc, then 4- or 8-byte sizes
			d, err := readAt(r, m.DataEnd, min(24, f.CDBegin-m.DataEnd))
			if err != nil {
				return nil, err
			}
			off := 0
			if len(d) >= 4 && le32(d) == sigDesc {
				off = 4
			}
			w := 4
			if m.Zip64Desc {
				w = 8
			}
			if len(d) < off+4+2*w {
				return nil, fmt.Errorf("%w: data descriptor of %q", ErrFormat, m.Name)
			}
			if uint32(le32(d[off:])) != m.CRC {
				return nil, fmt.Errorf("%w: descriptor CRC of %q", ErrFormat, m.Name)
			}
			var cs, us uint64
			if w == 4 {
				cs, us = le32(d[off+4:]), le32(d[off+8:])
			} else {
				cs, us = le64(d[off+4:]), le64(d[off+12:])
			}
			if cs != uint64(m.Size()) || us != uint64(m.Size()) {
				return nil, fmt.Errorf("%w: descriptor sizes of %q", ErrFormat, m.Name)
			}
			m.DescEnd = m.DataEnd + int64(off+4+2*w)
		}
		next = m.DescEnd
		sorted[k] = m
	}
	if next != f.CDBegin {
		return nil, fmt.Errorf("%w: %d bytes between the last member and the central directory", ErrUnsupported, f.CDBegin-next)
	}
	f.Members = sorted
	return f, nil
}

// Segment is one piece of the exact file layout.
type Segment struct {
	Begin, End int64
	Kind       string // "frame" (local header + preceding descriptor) | "pickle" | "storage" | "other" | "central"
	Name       string
}

// Segments tiles [0,Size): per member the framing before its data (the previous member's data
// descriptor plus this local header), the data, and finally the central directory and end
// records as one "central" segment. Member data of data.pkl is "pickle", of .../data/<id> is
// "storage", any other member "other" (byteorder, version, serialization_id, ...).
func (f *File) Segments() []Segment {
	var segs []Segment
	pos := int64(0)
	for i := range f.Members {
		m := &f.Members[i]
		if m.DataBegin > pos {
			segs = append(segs, Segment{pos, m.DataBegin, "frame", m.Name})
		}
		kind := "other"
		switch {
		case strings.HasSuffix(m.Name, "/data.pkl") || m.Name == "data.pkl":
			kind = "pickle"
		case strings.Contains(m.Name, "/data/"):
			kind = "storage"
		}
		if m.DataEnd > m.DataBegin {
			segs = append(segs, Segment{m.DataBegin, m.DataEnd, kind, m.Name})
		} else {
			segs = append(segs, Segment{m.DataBegin, m.DataBegin, kind, m.Name})
		}
		pos = m.DataEnd
	}
	out := segs[:0:0]
	for _, s := range segs {
		if s.End > s.Begin {
			out = append(out, s)
		}
	}
	// the last member's descriptor and the central part are one trailing segment
	last := int64(0)
	if len(out) > 0 {
		last = out[len(out)-1].End
	}
	out = append(out, Segment{last, f.Size, "central", ""})
	return out
}
