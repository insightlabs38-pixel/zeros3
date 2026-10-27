package sstz3

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// Family is the footer family; it selects how a footer is read and nothing else.
type Family uint8

const (
	FamilyLegacy       Family = 1 // 48-byte footer, magic 0xdb4775248b80fb57: LevelDB, RocksDB format_version 0
	FamilyRocksDB      Family = 2 // 53-byte footer, BlockBasedTable magic: RocksDB format_version 1-6
	FamilyPebble       Family = 3 // 53-byte footer, Pebble magic: Pebble-native table formats
	legacyMagic               = 0xdb4775248b80fb57
	blockBasedMagic           = 0x88e241b785f4cff7
	pebbleMagic               = 0xb3aa9ff0b3aa9ff0 // bytes f0 9f aa b3 f0 9f aa b3, little endian
	trailerLen                = 5
	legacyFooterLen           = 48
	versionedFooterLen        = 53
	maxHandleLen              = 20
)

func (f Family) String() string {
	switch f {
	case FamilyLegacy:
		return "legacy"
	case FamilyRocksDB:
		return "rocksdb"
	case FamilyPebble:
		return "pebble"
	}
	return "?"
}

// Checksum types as written in versioned footers (RocksDB numbering; Pebble uses 1 for
// CRC32C as well).
const (
	CksumNone  = 0
	CksumCRC32 = 1
	CksumXXH32 = 2
	CksumXXH64 = 3
	CksumXXH3  = 4
)

// Block compression types (the trailer's first byte).
const (
	CompNone   = 0
	CompSnappy = 1
	CompZlib   = 2
	CompBZip2  = 3
	CompLZ4    = 4
	CompLZ4HC  = 5
	CompXpress = 6
	CompZstd   = 7
)

// Handle locates a block: Size excludes the 5-byte trailer.
type Handle struct{ Off, Size uint64 }

// Footer is the decoded footer.
type Footer struct {
	Family    Family
	Version   uint32 // format version (0 for the legacy footer)
	Checksum  uint8
	Metaindex Handle
	Index     Handle // zero when IndexInMeta
	Len       int
	// BaseContext is RocksDB format 6's per-file context checksum seed.
	BaseContext uint32
	IndexInMeta bool // RocksDB format >= 6: the index handle lives in the metaindex
}

func getHandle(b []byte) (Handle, int, bool) {
	off, n1 := binary.Uvarint(b)
	if n1 <= 0 {
		return Handle{}, 0, false
	}
	sz, n2 := binary.Uvarint(b[n1:])
	if n2 <= 0 {
		return Handle{}, 0, false
	}
	return Handle{off, sz}, n1 + n2, true
}

// ReadFooter reads and decodes the footer of a table of the given size.
func ReadFooter(r io.ReaderAt, size int64) (Footer, error) {
	if size < legacyFooterLen {
		return Footer{}, corrupt("file of %d bytes has no footer", size)
	}
	var m [8]byte
	if err := readFull(r, m[:], size-8); err != nil {
		return Footer{}, err
	}
	magic := binary.LittleEndian.Uint64(m[:])
	var f Footer
	switch magic {
	case legacyMagic:
		var b [legacyFooterLen]byte
		if err := readFull(r, b[:], size-legacyFooterLen); err != nil {
			return f, err
		}
		f.Family, f.Len = FamilyLegacy, legacyFooterLen
		f.Checksum = CksumCRC32
		mh, n, ok := getHandle(b[:])
		if !ok {
			return f, corrupt("metaindex handle")
		}
		ih, _, ok := getHandle(b[n:40])
		if !ok {
			return f, corrupt("index handle")
		}
		f.Metaindex, f.Index = mh, ih
	case blockBasedMagic, pebbleMagic:
		if size < versionedFooterLen {
			return f, corrupt("file of %d bytes has no versioned footer", size)
		}
		var b [versionedFooterLen]byte
		if err := readFull(r, b[:], size-versionedFooterLen); err != nil {
			return f, err
		}
		f.Family, f.Len = FamilyRocksDB, versionedFooterLen
		if magic == pebbleMagic {
			f.Family = FamilyPebble
		}
		f.Version = binary.LittleEndian.Uint32(b[41:])
		f.Checksum = b[0]
		if f.Family == FamilyRocksDB && f.Version > 6 {
			return f, nil // a format this package does not know: Open classifies it Unsupported
		}
		if f.Family == FamilyRocksDB && f.Version >= 6 {
			if b[1] != 0x3e || b[2] != 0x00 || b[3] != 0x7a || b[4] != 0x00 {
				return f, corrupt("format 6 extended magic")
			}
			f.BaseContext = binary.LittleEndian.Uint32(b[9:])
			msz := uint64(binary.LittleEndian.Uint32(b[13:]))
			// the metaindex block is the last block before the footer
			end := uint64(size) - versionedFooterLen
			if msz+trailerLen > end {
				return f, corrupt("metaindex size %d", msz)
			}
			f.Metaindex = Handle{end - msz - trailerLen, msz}
			f.IndexInMeta = true
		} else {
			mh, n, ok := getHandle(b[1:41])
			if !ok {
				return f, corrupt("metaindex handle")
			}
			ih, _, ok := getHandle(b[1+n : 41])
			if !ok {
				return f, corrupt("index handle")
			}
			f.Metaindex, f.Index = mh, ih
		}
	default:
		return f, unsupported("magic %#x", magic)
	}
	return f, nil
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// RawBlock is a block as stored.
type RawBlock struct {
	Payload []byte // stored (possibly compressed) bytes, without the trailer
	Trailer [trailerLen]byte
}

// Comp is the compression type byte.
func (b *RawBlock) Comp() uint8 { return b.Trailer[0] }

// ReadBlock reads the block at h, checking the handle against the file size and the
// limits before allocating. The trailer's checksum is verified when verify is set and
// the checksum type is CRC32C (the only one portable without further code).
func ReadBlock(r io.ReaderAt, size int64, h Handle, cksum uint8, verify bool, lim Limits) (*RawBlock, error) {
	lim = lim.norm()
	if h.Size > uint64(lim.MaxBlockBytes) || h.Off > uint64(size) || h.Size+trailerLen > uint64(size)-h.Off {
		return nil, corrupt("block handle %d+%d in a file of %d", h.Off, h.Size, size)
	}
	buf := make([]byte, h.Size+trailerLen)
	if err := readFull(r, buf, int64(h.Off)); err != nil {
		return nil, err
	}
	b := &RawBlock{Payload: buf[:h.Size]}
	copy(b.Trailer[:], buf[h.Size:])
	if verify && cksum == CksumCRC32 {
		c := crc32.Update(crc32.Checksum(b.Payload, castagnoli), castagnoli, b.Trailer[:1])
		c = (c>>15 | c<<17) + 0xa282ead8
		if c != binary.LittleEndian.Uint32(b.Trailer[1:]) {
			return nil, corrupt("block at %d: checksum mismatch", h.Off)
		}
	}
	return b, nil
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)
