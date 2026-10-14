// Package tarz3 is the Z3-11 exact tar structure parser: it walks a tar byte
// stream block by block and separates the exact structural bytes (headers, PAX
// and GNU long-name entries with their data, the trailer) from the member
// payloads, so that structure + payloads rebuild the original bytes. It never
// regenerates a header: the stream is the source of truth, the parse only
// decides where bytes belong.
//
// Anything the walker cannot account for exactly (bad checksum, a size that
// does not fit, truncated payload, non-zero padding, GNU sparse headers, ...)
// returns *UnsupportedError and the caller falls back to raw bytes.
package tarz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const BlockSize = 512

// UnsupportedError means the stream is not a tar this package reconstructs exactly.
type UnsupportedError struct {
	Off    int64
	Reason string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("tarz3: unsupported at offset %d: %s", e.Off, e.Reason)
}

func unsupported(off int64, f string, a ...any) error {
	return &UnsupportedError{off, fmt.Sprintf(f, a...)}
}

// Options bound the work done on untrusted input.
type Options struct {
	MaxMembers int   // default 1<<22
	MaxPax     int64 // largest PAX/GNU metadata entry accepted (default 1 MiB)
}

func (o Options) norm() Options {
	if o.MaxMembers <= 0 {
		o.MaxMembers = 1 << 22
	}
	if o.MaxPax <= 0 {
		o.MaxPax = 1 << 20
	}
	return o
}

// Member is one logical archive member: optional PAX / GNU long-name prefix
// entries followed by the main entry.
type Member struct {
	Off    int64 // first byte of the member (its first header block)
	HdrLen int   // bytes from Off to the end of the main header block (prefix entries included)
	PayOff int64 // Off + HdrLen
	PayLen int64 // file content bytes of the main entry
	PadLen int   // zero bytes after the payload up to the block boundary
	Type   byte  // main entry type flag ('0' for regular files, NUL normalised)

	Path, Link     string // resolved names (PAX > GNU long > ustar prefix+name); analysis only
	Mode, UID, GID int64
	MTime          int64  // seconds; PAX fractional part ignored
	Format         string // "v7" | "ustar" | "gnu" for the main header
	Prefix         int    // number of prefix entries (PAX x / GNU L K)
}

// End is the first byte after the member including padding.
func (m *Member) End() int64 { return m.PayOff + m.PayLen + int64(m.PadLen) }

// Archive is the structural parse of one tar stream.
type Archive struct {
	Size     int64
	Members  []Member
	TrailOff int64 // first byte after the last member; the rest is the trailer
	Global   int   // PAX global header entries (kept in the structure)
	Formats  map[string]int
	Types    map[byte]int
}

// Trailer returns the bytes after the last member (zero blocks, record padding).
func (a *Archive) TrailerLen() int64 { return a.Size - a.TrailOff }

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// parseNum reads an octal field (NUL / space terminated) or a non-negative GNU
// base-256 field. Negative or oversized values report !ok.
func parseNum(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	if b[0]&0x80 != 0 {
		if b[0]&0x40 != 0 {
			return 0, false
		}
		v := uint64(b[0] & 0x3f)
		for _, c := range b[1:] {
			if v>>55 != 0 {
				return 0, false
			}
			v = v<<8 | uint64(c)
		}
		return int64(v), true
	}
	s := strings.Trim(string(b), " \x00")
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(s, 8, 64)
	return v, err == nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// checksumOK verifies the header checksum (unsigned sum with the field as spaces).
func checksumOK(h []byte) bool {
	want, ok := parseNum(h[148:156])
	if !ok {
		return false
	}
	var sum int64
	for i, c := range h {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int64(c)
	}
	if sum == want {
		return true
	}
	var ssum int64 // some old writers used signed chars
	for i, c := range h {
		if i >= 148 && i < 156 {
			c = ' '
		}
		ssum += int64(int8(c))
	}
	return ssum == want
}

// kind classifies a main/prefix type flag.
type kind uint8

const (
	kMain   kind = iota // file-like entry: a header and (maybe) payload
	kPax                // PAX extended header x (data = records, applies to the next entry)
	kGlobal             // PAX global header g
	kLongN              // GNU long name L
	kLongL              // GNU long link name K
)

func classify(t byte) (kind, bool) {
	switch t {
	case 'x':
		return kPax, true
	case 'g':
		return kGlobal, true
	case 'L':
		return kLongN, true
	case 'K':
		return kLongL, true
	case 'S':
		return 0, false // GNU sparse: extra header blocks and a map; not reconstructed here
	}
	return kMain, true
}

// headerOnly types carry no payload whatever their size field says (as in archive/tar).
func headerOnly(t byte) bool { return t >= '1' && t <= '6' }

func ceil512(n int64) int64 { return (n + BlockSize - 1) &^ (BlockSize - 1) }

// parsePax reads "len key=value\n" records; only the keys used here are kept.
func parsePax(b []byte) (map[string]string, error) {
	m := map[string]string{}
	for len(b) > 0 {
		sp := bytes.IndexByte(b, ' ')
		if sp <= 0 {
			return nil, errors.New("bad pax record length")
		}
		n, err := strconv.Atoi(string(b[:sp]))
		if err != nil || n <= sp+1 || n > len(b) || b[n-1] != '\n' {
			return nil, errors.New("bad pax record")
		}
		kv := b[sp+1 : n-1]
		eq := bytes.IndexByte(kv, '=')
		if eq <= 0 {
			return nil, errors.New("bad pax key")
		}
		m[string(kv[:eq])] = string(kv[eq+1:])
		b = b[n:]
	}
	return m, nil
}

// Parse walks b. It returns the exact member layout or an *UnsupportedError.
func Parse(b []byte, opt Options) (*Archive, error) {
	opt = opt.norm()
	a := &Archive{Size: int64(len(b)), Formats: map[string]int{}, Types: map[byte]int{}}
	pos := int64(0)
	size := int64(len(b))
	for {
		if pos == size {
			a.TrailOff = pos
			return a, nil
		}
		if size-pos < BlockSize {
			return nil, unsupported(pos, "%d trailing bytes do not form a block", size-pos)
		}
		if isZero(b[pos : pos+BlockSize]) { // end marker: everything from here is trailer
			a.TrailOff = pos
			return a, nil
		}
		if len(a.Members) >= opt.MaxMembers {
			return nil, unsupported(pos, "more than %d members", opt.MaxMembers)
		}
		m := Member{Off: pos}
		var pax map[string]string
		var longName, longLink string
		for {
			if size-pos < BlockSize {
				return nil, unsupported(pos, "truncated header")
			}
			h := b[pos : pos+BlockSize]
			if isZero(h) {
				return nil, unsupported(pos, "prefix entry without a following header")
			}
			if !checksumOK(h) {
				return nil, unsupported(pos, "header checksum mismatch")
			}
			t := h[156]
			if t == 0 {
				t = '0'
			}
			kd, ok := classify(t)
			if !ok {
				return nil, unsupported(pos, "type %q", t)
			}
			sz, ok := parseNum(h[124:136])
			if !ok || sz < 0 {
				return nil, unsupported(pos, "bad size field")
			}
			if kd == kMain {
				if v, ok := pax["size"]; ok { // PAX overrides the header size
					n, err := strconv.ParseInt(v, 10, 64)
					if err != nil || n < 0 {
						return nil, unsupported(pos, "bad pax size %q", v)
					}
					sz = n
				}
				if headerOnly(t) {
					sz = 0
				}
				pay := pos + BlockSize
				if sz > size-pay {
					return nil, unsupported(pos, "payload of %d bytes runs past the end", sz)
				}
				pad := ceil512(sz) - sz
				if pay+sz+pad > size {
					return nil, unsupported(pos, "padding runs past the end")
				}
				if !isZero(b[pay+sz : pay+sz+pad]) {
					return nil, unsupported(pos, "non-zero padding")
				}
				m.HdrLen = int(pay - m.Off)
				m.PayOff, m.PayLen, m.PadLen, m.Type = pay, sz, int(pad), t
				m.Mode, _ = parseNum(h[100:108])
				m.UID, _ = parseNum(h[108:116])
				m.GID, _ = parseNum(h[116:124])
				m.MTime, _ = parseNum(h[136:148])
				m.Path, m.Link = cstr(h[0:100]), cstr(h[157:257])
				switch {
				case bytes.Equal(h[257:263], []byte("ustar\x00")) && bytes.Equal(h[263:265], []byte("00")):
					m.Format = "ustar"
					if p := cstr(h[345:500]); p != "" {
						m.Path = p + "/" + m.Path
					}
				case bytes.Equal(h[257:265], []byte("ustar  \x00")):
					m.Format = "gnu"
				default:
					m.Format = "v7"
				}
				if longName != "" {
					m.Path = longName
				}
				if longLink != "" {
					m.Link = longLink
				}
				if v, ok := pax["path"]; ok {
					m.Path = v
				}
				if v, ok := pax["linkpath"]; ok {
					m.Link = v
				}
				pos = pay + sz + pad
				break
			}
			// prefix entry: header + data + padding stay in the structure
			if sz > opt.MaxPax {
				return nil, unsupported(pos, "metadata entry of %d bytes", sz)
			}
			dend := pos + BlockSize + sz
			if dend > size || pos+BlockSize+ceil512(sz) > size {
				return nil, unsupported(pos, "metadata entry runs past the end")
			}
			data := b[pos+BlockSize : dend]
			if !isZero(b[dend : pos+BlockSize+ceil512(sz)]) {
				return nil, unsupported(pos, "non-zero padding after metadata")
			}
			switch kd {
			case kPax:
				p, err := parsePax(data)
				if err != nil {
					return nil, unsupported(pos, "%v", err)
				}
				pax = p
				m.Prefix++
			case kGlobal:
				a.Global++
				if _, err := parsePax(data); err != nil {
					return nil, unsupported(pos, "%v", err)
				}
				m.Prefix++
			case kLongN:
				longName = cstr(data)
				m.Prefix++
			case kLongL:
				longLink = cstr(data)
				m.Prefix++
			}
			pos += BlockSize + ceil512(sz)
		}
		a.Formats[m.Format]++
		a.Types[m.Type]++
		a.Members = append(a.Members, m)
	}
}

// Digest is the identity of a payload: SHA-256 of its bytes (path, mode, times excluded).
func Digest(b []byte) [32]byte { return sha256.Sum256(b) }

// Split separates the exact structural stream from the payloads. structure
// holds, in order, every byte that is not file content and not implied block
// padding; payloads are sub-slices of b (no copy), one per member with data.
func Split(b []byte, a *Archive) (structure []byte, payloads [][]byte) {
	structure = make([]byte, 0, int(a.Size)/16)
	pos := int64(0)
	for i := range a.Members {
		m := &a.Members[i]
		structure = append(structure, b[pos:m.PayOff]...)
		if m.PayLen > 0 {
			payloads = append(payloads, b[m.PayOff:m.PayOff+m.PayLen])
		}
		pos = m.End()
	}
	structure = append(structure, b[pos:]...)
	return
}

// Join rebuilds the tar from the structural stream and its payloads, walking
// the headers inside structure itself (it needs nothing else to know where each
// payload goes). payload(i) must return the i-th payload (members with data, in order).
func Join(w io.Writer, structure []byte, payload func(i int) ([]byte, error), opt Options) error {
	opt = opt.norm()
	pos, idx := 0, 0
	var pax map[string]string
	for {
		if len(structure)-pos < BlockSize || isZero(structure[pos:pos+BlockSize]) {
			_, err := w.Write(structure[pos:])
			return err
		}
		h := structure[pos : pos+BlockSize]
		if !checksumOK(h) {
			return unsupported(int64(pos), "structure: header checksum mismatch")
		}
		t := h[156]
		if t == 0 {
			t = '0'
		}
		kd, ok := classify(t)
		if !ok {
			return unsupported(int64(pos), "structure: type %q", t)
		}
		sz, ok := parseNum(h[124:136])
		if !ok || sz < 0 {
			return unsupported(int64(pos), "structure: bad size")
		}
		if kd != kMain {
			if sz > opt.MaxPax || int64(pos)+BlockSize+ceil512(sz) > int64(len(structure)) {
				return unsupported(int64(pos), "structure: metadata entry out of range")
			}
			end := pos + BlockSize + int(ceil512(sz))
			if kd == kPax {
				p, err := parsePax(structure[pos+BlockSize : pos+BlockSize+int(sz)])
				if err != nil {
					return unsupported(int64(pos), "structure: %v", err)
				}
				pax = p
			}
			if _, err := w.Write(structure[pos:end]); err != nil {
				return err
			}
			pos = end
			continue
		}
		if v, ok := pax["size"]; ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return unsupported(int64(pos), "structure: bad pax size")
			}
			sz = n
		}
		pax = nil
		if headerOnly(t) {
			sz = 0
		}
		if _, err := w.Write(h); err != nil {
			return err
		}
		pos += BlockSize
		if sz > 0 {
			p, err := payload(idx)
			if err != nil {
				return err
			}
			idx++
			if int64(len(p)) != sz {
				return unsupported(int64(pos), "structure: payload %d has %d bytes, header says %d", idx-1, len(p), sz)
			}
			if _, err := w.Write(p); err != nil {
				return err
			}
			if pad := ceil512(sz) - sz; pad > 0 {
				if _, err := w.Write(make([]byte, pad)); err != nil {
					return err
				}
			}
		}
	}
}
