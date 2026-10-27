// Package repz3 is the Z3-20/21 candidate serialized representation contract. It is a
// laboratory object, not a ZeroS3 production format: nothing here is a
// compatibility promise and the byte layout may change after Z3-20.
//
// A representation (Rep) is one small, versioned, deterministic binary object that
// describes how to rebuild an exact byte string from SHA-256-addressed components
// and from child representations. The package knows components, member roles,
// framing, child links, opaque decoder-specific recipe bytes, native lookup
// sections, bounded reads and transitive reachability. It does not know CAR, SQLite,
// Git or Merkle semantics: those live behind a finite in-code kind table (see
// Table) and stay inside the opaque Recipe. The package imports only the standard
// library and none of the laboratory packages.
//
// Wire format (all integers are unsigned varints unless a width is given; the
// object is consumed exactly, trailing bytes are corruption):
//
//	"Z3RP" | version (1 B) | kind (1 B) | size
//	n members, each
//	    STORED  0x01 | SHA-256 (32 B) | length
//	    DERIVED 0x03 | SHA-256 of the framed output (32 B) | length of the framed output |
//	            child representation object SHA-256 (32 B) |
//	            prefix length, prefix bytes | zero-suffix length
//	recipe length | recipe bytes (opaque to this package)
//	n record sections, each: flags (1 B) | key width | record count | component SHA-256 (32 B)
//
// Roles. STORED: the bytes are one component fetched by SHA-256. DERIVED: the bytes
// are never stored; the child representation produces the middle of them and the
// framing (prefix bytes, zero suffix) supplies the rest, and the whole is checked
// against the member's SHA-256 and length. VIA: the component exists and is part
// of the representation but is named only inside another stored component (a
// node, a digest table), so the representation carries no reference to it and the
// wire has no VIA entry; the kind's Expand discovers it (see Reachable).
//
// Integrity model. A representation is itself a SHA-256-addressed object: it is
// loaded by its digest and parsed only after the digest matches, so a corrupt
// recipe never reaches a decoder. Every component is length-checked before it is
// read and hashed while it is read; every derived output is length- and
// SHA-256-checked; the top-level output length is checked and its SHA-256 is
// returned for the caller to compare with the object's identity. There is no
// separate checksum field. Native identities are lookup hints and never replace
// SHA-256.
package repz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// SHA is the SHA-256 identity of exact bytes.
type SHA = [32]byte

// Version is the only contract version this package decodes (Z3-21: 2).
const Version = 2

const magic = "Z3RP"

var (
	ErrCorrupt     = errors.New("repz3: corrupt representation")
	ErrUnsupported = errors.New("repz3: unsupported version or kind")
	ErrLimit       = errors.New("repz3: limit exceeded")
	ErrMissing     = errors.New("repz3: object not in source")
	ErrMismatch    = errors.New("repz3: object does not match its recipe")
	ErrShort       = errors.New("repz3: short read from source")
	ErrCycle       = fmt.Errorf("%w: representation cycle", ErrCorrupt)
	ErrDepth       = fmt.Errorf("%w: nesting depth", ErrLimit)
	ErrAmbiguous   = errors.New("repz3: native identity maps to different components")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}

// Kind selects the registered-in-code decoder.
type Kind uint8

const (
	KindConcat  Kind = 1 // members in order are the output (generic; the only built-in kind)
	KindCAR     Kind = 2
	KindSQLite  Kind = 3
	KindGit     Kind = 4
	KindMerkle  Kind = 5
	KindGzip    Kind = 6
	KindZstd    Kind = 7  // Z3-25 lab kind: exact Zstandard replay over a semantic plaintext child
	KindModel   Kind = 8  // Z3-29 lab kind: model artifact as an op list over components, optionally XOR-plane residuals against a base member
	KindSST     Kind = 9  // Z3-30 lab kind: LevelDB / RocksDB / Pebble table as logical record groups plus per-block framing and Snappy replay
	KindParquet Kind = 10 // Z3-31 lab kind: Parquet file as per-column logical value/level streams plus page-layout and compression replay recipes
	KindDEX     Kind = 11 // Z3-32 lab kind: DEX as a masked payload plus semantic-identity reference streams, in index nodes of stored groups
)

// Known reports whether this contract version defines the kind.
func (k Kind) Known() bool { return k >= KindConcat && k <= KindDEX }

// Role is how a component takes part in a representation.
type Role uint8

const (
	Stored  Role = 1
	Via     Role = 2 // never on the wire: discovered by the kind's Expand
	Derived Role = 3
)

// Frame is the exact bytes around a child's output inside a derived member.
type Frame struct {
	Prefix []byte // literal bytes before the child output (e.g. an object header)
	Zeros  int    // zero bytes after it (e.g. block padding)
}

// Member is one wire entry. For Stored, SHA/Size name the component. For Derived,
// SHA/Size name the framed output (Prefix || child output || Zeros) and Child is
// the child representation object.
type Member struct {
	Role  Role
	SHA   SHA
	Size  int64
	Child SHA
	Frame Frame
}

// Index is a reference to a record section: Count records of key[KeyWidth] ||
// SHA-256, strictly ascending by key, stored as one component. It maps a
// format-native identity to the component that holds the native object. The
// records are real bytes in a component, not part of the representation object,
// so a point lookup reads O(log Count) records through the bounded source API.
// Owns marks a section whose record targets are live while the section is
// referenced (the section is then the VIA list of the representation).
type Index struct {
	KeyWidth int
	Count    int64
	Ref      SHA
	Owns     bool
}

// Bytes is the size of the index component.
func (x Index) Bytes() int64 { return x.Count * int64(x.KeyWidth+32) }

const flagOwns = 1

// Rep is a decoded representation object.
type Rep struct {
	Kind    Kind
	Size    int64 // exact logical output length
	Members []Member
	Recipe  []byte // opaque decoder-specific bytes
	Indexes []Index
}

// Limits bound every parse and walk. The zero value selects the defaults.
type Limits struct {
	MaxDepth    int   // nesting depth, outermost = 1 (default 3: gzip -> tar -> database; hard maximum HardMaxDepth)
	MaxMembers  int   // members per representation (default 1<<22)
	MaxRepBytes int64 // serialized representation object (default 256 MiB)
	MaxWhole    int64 // one whole-component fetch (default 64 MiB)
	MaxObjects  int   // distinct objects a reachability walk may list (default 1<<24)
}

// HardMaxDepth is the deepest nesting any Limits can request.
const HardMaxDepth = 4

const (
	maxPrefix     = 64
	maxZeros      = 1 << 16
	maxKeyWidth   = 64
	maxIndexCount = 1 << 28
	maxIndexes    = 16
	minMemberWire = 1 + 32 + 1
)

func (l Limits) norm() Limits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = 3
	}
	l.MaxDepth = min(l.MaxDepth, HardMaxDepth)
	if l.MaxMembers <= 0 {
		l.MaxMembers = 1 << 22
	}
	if l.MaxRepBytes <= 0 {
		l.MaxRepBytes = 256 << 20
	}
	if l.MaxWhole <= 0 {
		l.MaxWhole = 64 << 20
	}
	if l.MaxObjects <= 0 {
		l.MaxObjects = 1 << 24
	}
	return l
}

// Validate checks the structural invariants that Marshal and Unmarshal share.
func (r *Rep) Validate(lim Limits) error {
	lim = lim.norm()
	if !r.Kind.Known() {
		return fmt.Errorf("%w: kind %d", ErrUnsupported, r.Kind)
	}
	if r.Size < 0 {
		return corrupt("negative size")
	}
	if len(r.Members) > lim.MaxMembers {
		return fmt.Errorf("%w: %d members", ErrLimit, len(r.Members))
	}
	sizeOf := map[SHA]int64{}
	var sum int64
	for i := range r.Members {
		m := &r.Members[i]
		if m.Size < 0 {
			return corrupt("member %d: negative length", i)
		}
		switch m.Role {
		case Stored:
			if prev, ok := sizeOf[m.SHA]; ok && prev != m.Size {
				return corrupt("member %d: component listed with lengths %d and %d", i, prev, m.Size)
			}
			sizeOf[m.SHA] = m.Size
		case Derived:
			f := m.Frame
			if len(f.Prefix) > maxPrefix || f.Zeros < 0 || f.Zeros > maxZeros {
				return corrupt("member %d: frame bounds", i)
			}
			if int64(len(f.Prefix))+int64(f.Zeros) > m.Size {
				return corrupt("member %d: frame exceeds the framed length", i)
			}
		case Via:
			return corrupt("member %d: VIA has no wire form", i)
		default:
			return corrupt("member %d: role %d", i, m.Role)
		}
		if sum > math.MaxInt64-m.Size {
			return corrupt("member lengths overflow")
		}
		sum += m.Size
	}
	if r.Kind == KindConcat {
		if sum != r.Size {
			return corrupt("concat members total %d, size %d", sum, r.Size)
		}
		if len(r.Recipe) != 0 || len(r.Indexes) != 0 {
			return corrupt("concat carries a recipe or index")
		}
	}
	if len(r.Indexes) > maxIndexes {
		return corrupt("%d index sections", len(r.Indexes))
	}
	for i, x := range r.Indexes {
		if i > 0 && bytes.Compare(r.Indexes[i-1].Ref[:], x.Ref[:]) >= 0 {
			return corrupt("index %d: sections must be strictly ascending by component digest", i)
		}
		if x.KeyWidth < 1 || x.KeyWidth > maxKeyWidth || x.Count < 1 || x.Count > maxIndexCount {
			return corrupt("index %d: geometry %d x %d", i, x.KeyWidth, x.Count)
		}
	}
	return nil
}

func appendUv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// Marshal serialises the representation. The same Rep always yields the same bytes.
func (r *Rep) Marshal() ([]byte, error) {
	if err := r.Validate(Limits{MaxMembers: math.MaxInt32}); err != nil {
		return nil, err
	}
	b := make([]byte, 0, 16+len(r.Members)*(minMemberWire+1)+len(r.Recipe)+len(r.Indexes)*40)
	b = append(b, magic...)
	b = append(b, Version, byte(r.Kind))
	b = appendUv(b, uint64(r.Size))
	b = appendUv(b, uint64(len(r.Members)))
	for i := range r.Members {
		m := &r.Members[i]
		b = append(b, byte(m.Role))
		b = append(b, m.SHA[:]...)
		b = appendUv(b, uint64(m.Size))
		if m.Role == Derived {
			b = append(b, m.Child[:]...)
			b = appendUv(b, uint64(len(m.Frame.Prefix)))
			b = append(b, m.Frame.Prefix...)
			b = appendUv(b, uint64(m.Frame.Zeros))
		}
	}
	b = appendUv(b, uint64(len(r.Recipe)))
	b = append(b, r.Recipe...)
	b = appendUv(b, uint64(len(r.Indexes)))
	for _, x := range r.Indexes {
		var fl byte
		if x.Owns {
			fl |= flagOwns
		}
		b = append(b, fl)
		b = appendUv(b, uint64(x.KeyWidth))
		b = appendUv(b, uint64(x.Count))
		b = append(b, x.Ref[:]...)
	}
	return b, nil
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) fail(f string, a ...any) {
	if r.err == nil {
		r.err = corrupt(f, a...)
	}
}

func (r *reader) uv(max uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 || n != binary.PutUvarint(make([]byte, binary.MaxVarintLen64), v) { // reject non-minimal encodings: one object, one byte string
		r.fail("varint at %d", r.pos)
		return 0
	}
	if v > max {
		r.fail("value %d above %d at %d", v, max, r.pos)
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.b)-r.pos {
		r.fail("%d bytes at %d exceed the object", n, r.pos)
		return nil
	}
	s := r.b[r.pos : r.pos+n]
	r.pos += n
	return s
}

func (r *reader) sha() (s SHA) {
	copy(s[:], r.take(32))
	return s
}

// Unmarshal parses and validates one representation object. No allocation is
// sized by a declared count beyond what the remaining bytes could hold.
func Unmarshal(b []byte, lim Limits) (*Rep, error) {
	lim = lim.norm()
	if len(b) < len(magic)+2 || string(b[:len(magic)]) != magic {
		return nil, corrupt("magic")
	}
	if b[4] != Version {
		return nil, fmt.Errorf("%w: contract version %d", ErrUnsupported, b[4])
	}
	if !Kind(b[5]).Known() {
		return nil, fmt.Errorf("%w: kind %d", ErrUnsupported, b[5])
	}
	r := &reader{b: b, pos: 6}
	rep := &Rep{Kind: Kind(b[5])}
	rep.Size = int64(r.uv(math.MaxInt64))
	n := r.uv(math.MaxInt32)
	if r.err == nil && n > uint64(lim.MaxMembers) {
		return nil, fmt.Errorf("%w: %d members", ErrLimit, n)
	}
	if r.err == nil && n > uint64(len(b)-r.pos)/minMemberWire {
		r.fail("%d members cannot fit in %d bytes", n, len(b)-r.pos)
	}
	if r.err == nil {
		rep.Members = make([]Member, 0, n)
	}
	for i := uint64(0); i < n && r.err == nil; i++ {
		var m Member
		role := r.take(1)
		if r.err != nil {
			break
		}
		m.Role = Role(role[0])
		switch m.Role {
		case Stored, Derived:
		default:
			r.fail("member %d: role %d", i, role[0])
			continue
		}
		m.SHA = r.sha()
		m.Size = int64(r.uv(math.MaxInt64))
		if m.Role == Derived {
			m.Child = r.sha()
			pl := r.uv(maxPrefix)
			m.Frame.Prefix = append([]byte(nil), r.take(int(pl))...)
			m.Frame.Zeros = int(r.uv(maxZeros))
		}
		rep.Members = append(rep.Members, m)
	}
	rl := r.uv(math.MaxInt32)
	if r.err == nil && rl > uint64(lim.MaxRepBytes) {
		return nil, fmt.Errorf("%w: recipe of %d bytes", ErrLimit, rl)
	}
	rep.Recipe = append([]byte(nil), r.take(int(rl))...)
	ni := r.uv(maxIndexes)
	for i := uint64(0); i < ni && r.err == nil; i++ {
		t := r.take(1)
		if r.err != nil {
			break
		}
		if t[0]&^flagOwns != 0 {
			r.fail("index %d: flags %#x", i, t[0])
			break
		}
		x := Index{Owns: t[0]&flagOwns != 0}
		x.KeyWidth = int(r.uv(maxKeyWidth))
		x.Count = int64(r.uv(maxIndexCount))
		x.Ref = r.sha()
		rep.Indexes = append(rep.Indexes, x)
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.pos != len(b) {
		return nil, corrupt("%d trailing bytes", len(b)-r.pos)
	}
	if err := rep.Validate(lim); err != nil {
		return nil, err
	}
	return rep, nil
}
