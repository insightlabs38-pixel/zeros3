package prodshadow

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// Production invariants the shadow mapping must respect (zeros3.go: pack records
// are 1..maxPackedChunkBytes = cdcMaxChunkSize; compaction skips loose chunks
// outside that range, so they would stay loose forever).
const (
	MaxChunk = 256 << 10 // production cdcMaxChunkSize / maxPackedChunkBytes
	MaxRefs  = 1 << 16   // chunk refs in one virtual component
	MaxSize  = int64(MaxRefs) * MaxChunk
)

// ErrDescriptor reports a malformed or non-canonical shadow descriptor / root.
var ErrDescriptor = errors.New("prodshadow: bad descriptor")

// Ref is one production CAS chunk of a virtual component.
type Ref struct {
	SHA [32]byte
	Len uint32
}

// Descriptor maps a logical component (SHA-256 of its exact bytes, Size) onto an
// ordered sequence of ordinary production chunks. It is shadow/lab metadata, not a
// production manifest: flat, sequential, never recursive.
type Descriptor struct {
	Size int64
	Refs []Ref
}

// Validate checks the structural invariants (not the content).
func (d *Descriptor) Validate() error {
	if d.Size < 1 || d.Size > MaxSize {
		return fmt.Errorf("%w: size %d", ErrDescriptor, d.Size)
	}
	if n := len(d.Refs); n < 1 || n > MaxRefs {
		return fmt.Errorf("%w: %d refs", ErrDescriptor, n)
	}
	var sum int64
	for _, r := range d.Refs {
		if r.Len < 1 || r.Len > MaxChunk {
			return fmt.Errorf("%w: chunk length %d", ErrDescriptor, r.Len)
		}
		sum += int64(r.Len) // cannot overflow: <= MaxRefs * MaxChunk
	}
	if sum != d.Size {
		return fmt.Errorf("%w: refs sum to %d, size says %d", ErrDescriptor, sum, d.Size)
	}
	return nil
}

// Append appends the canonical body: uvarint size, uvarint count, then per ref
// 32 SHA bytes and a uvarint length. It is self-delimiting.
func (d *Descriptor) Append(b []byte) []byte {
	b = binary.AppendUvarint(b, uint64(d.Size))
	b = binary.AppendUvarint(b, uint64(len(d.Refs)))
	for _, r := range d.Refs {
		b = append(b, r.SHA[:]...)
		b = binary.AppendUvarint(b, uint64(r.Len))
	}
	return b
}

// uvarint reads a minimally encoded uvarint.
func uvarint(b []byte) (uint64, int, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 || n != len(binary.AppendUvarint(nil, v)) {
		return 0, 0, fmt.Errorf("%w: bad varint", ErrDescriptor)
	}
	return v, n, nil
}

// ParseDescriptor parses one descriptor body from the front of b and returns the
// bytes consumed. Allocation is bounded by len(b), never by a declared count.
func ParseDescriptor(b []byte) (*Descriptor, int, error) {
	size, n, err := uvarint(b)
	if err != nil {
		return nil, 0, err
	}
	pos := n
	count, n, err := uvarint(b[pos:])
	if err != nil {
		return nil, 0, err
	}
	pos += n
	if size > uint64(MaxSize) || count > MaxRefs || count > uint64(len(b)-pos)/33 {
		return nil, 0, fmt.Errorf("%w: size %d count %d", ErrDescriptor, size, count)
	}
	d := &Descriptor{Size: int64(size), Refs: make([]Ref, 0, count)}
	for i := uint64(0); i < count; i++ {
		if len(b)-pos < 33 {
			return nil, 0, fmt.Errorf("%w: truncated", ErrDescriptor)
		}
		var r Ref
		copy(r.SHA[:], b[pos:])
		pos += 32
		l, n, err := uvarint(b[pos:])
		if err != nil {
			return nil, 0, err
		}
		pos += n
		if l > MaxChunk {
			return nil, 0, fmt.Errorf("%w: chunk length %d", ErrDescriptor, l)
		}
		r.Len = uint32(l)
		d.Refs = append(d.Refs, r)
	}
	if err := d.Validate(); err != nil {
		return nil, 0, err
	}
	return d, pos, nil
}

// offsets returns the cumulative start offset of every ref (len(Refs)+1 entries).
func (d *Descriptor) offsets() []int64 {
	o := make([]int64, len(d.Refs)+1)
	for i, r := range d.Refs {
		o[i+1] = o[i] + int64(r.Len)
	}
	return o
}

// ---- shadow root --------------------------------------------------------------

// Root is the only thing a reader must find to use a shadow representation. It
// names the authoritative object it shadows, the representation root and the
// descriptors of the virtual components in the representation's closure.
type Root struct {
	Key     string
	ObjSHA  [32]byte // SHA-256 of the authoritative object's bytes
	ObjSize int64
	Rep     [32]byte // representation object (logical id)
	Descs   map[[32]byte]*Descriptor
}

const (
	rootMagic   = "Z3SR"
	rootVersion = 1
	MaxKey      = 1024
	MaxRootSize = 64 << 20
)

var crcTab = crc32.MakeTable(crc32.Castagnoli)

// Marshal is canonical: descriptors in ascending logical-SHA order, CRC32C trailer.
func (r *Root) Marshal() ([]byte, error) {
	if len(r.Key) > MaxKey || r.ObjSize < 0 {
		return nil, fmt.Errorf("%w: key/size", ErrDescriptor)
	}
	b := append([]byte(rootMagic), rootVersion)
	b = binary.AppendUvarint(b, uint64(len(r.Key)))
	b = append(b, r.Key...)
	b = append(b, r.ObjSHA[:]...)
	b = binary.AppendUvarint(b, uint64(r.ObjSize))
	b = append(b, r.Rep[:]...)
	ids := make([][32]byte, 0, len(r.Descs))
	for id := range r.Descs {
		ids = append(ids, id)
	}
	sortSHAFast(ids)
	b = binary.AppendUvarint(b, uint64(len(ids)))
	for _, id := range ids {
		d := r.Descs[id]
		if err := d.Validate(); err != nil {
			return nil, err
		}
		b = append(b, id[:]...)
		b = d.Append(b)
	}
	if len(b) > MaxRootSize {
		return nil, fmt.Errorf("%w: root of %d bytes", ErrDescriptor, len(b))
	}
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, crcTab)), nil
}

// UnmarshalRoot parses and fully validates a root; it never panics on hostile bytes.
func UnmarshalRoot(b []byte) (*Root, error) {
	if len(b) < len(rootMagic)+1+4 || len(b) > MaxRootSize {
		return nil, fmt.Errorf("%w: root length %d", ErrDescriptor, len(b))
	}
	body, tail := b[:len(b)-4], b[len(b)-4:]
	if crc32.Checksum(body, crcTab) != binary.LittleEndian.Uint32(tail) {
		return nil, fmt.Errorf("%w: root checksum", ErrDescriptor)
	}
	if string(body[:4]) != rootMagic || body[4] != rootVersion {
		return nil, fmt.Errorf("%w: magic/version", ErrDescriptor)
	}
	pos := 5
	klen, n, err := uvarint(body[pos:])
	if err != nil {
		return nil, err
	}
	pos += n
	if klen > MaxKey || uint64(len(body)-pos) < klen+32 {
		return nil, fmt.Errorf("%w: key", ErrDescriptor)
	}
	r := &Root{Key: string(body[pos : pos+int(klen)]), Descs: map[[32]byte]*Descriptor{}}
	pos += int(klen)
	copy(r.ObjSHA[:], body[pos:])
	pos += 32
	sz, n, err := uvarint(body[pos:])
	if err != nil || sz > 1<<62 {
		return nil, fmt.Errorf("%w: object size", ErrDescriptor)
	}
	r.ObjSize = int64(sz)
	pos += n
	if len(body)-pos < 32 {
		return nil, fmt.Errorf("%w: truncated", ErrDescriptor)
	}
	copy(r.Rep[:], body[pos:])
	pos += 32
	nd, n, err := uvarint(body[pos:])
	if err != nil {
		return nil, err
	}
	pos += n
	if nd > uint64(len(body)-pos)/36 {
		return nil, fmt.Errorf("%w: %d descriptors in %d bytes", ErrDescriptor, nd, len(body)-pos)
	}
	var prev [32]byte
	for i := uint64(0); i < nd; i++ {
		if len(body)-pos < 32 {
			return nil, fmt.Errorf("%w: truncated", ErrDescriptor)
		}
		var id [32]byte
		copy(id[:], body[pos:])
		pos += 32
		if i > 0 && string(id[:]) <= string(prev[:]) {
			return nil, fmt.Errorf("%w: descriptors not strictly ascending", ErrDescriptor)
		}
		prev = id
		d, used, err := ParseDescriptor(body[pos:])
		if err != nil {
			return nil, err
		}
		pos += used
		r.Descs[id] = d
	}
	if pos != len(body) {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrDescriptor, len(body)-pos)
	}
	return r, nil
}

// ID is the root's content address (its file name).
func (r *Root) ID() ([32]byte, error) {
	b, err := r.Marshal()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}
