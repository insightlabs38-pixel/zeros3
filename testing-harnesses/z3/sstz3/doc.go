// Package sstz3 is the Z3-30 exact SST (sorted string table) substrate for the
// LevelDB / RocksDB BlockBasedTable / Pebble family. It is a laboratory object, not a
// ZeroS3 production format.
//
// What the three producers share, and the package exploits, is the block core: a
// table is a run of data blocks (each: prefix-compressed entries, a uint32 restart
// array and its count, optionally compressed, followed by a 5-byte trailer of
// compression type and checksum) and then metadata blocks (filter, range deletions,
// properties, metaindex, index) and a footer. The producers differ in the footer
// (48-byte legacy; 53-byte versioned; RocksDB format 6 extended), the checksum
// (masked CRC32C, xxHash variants, context-keyed xxh3), the metadata block names and
// the compression set. Open understands those differences and classifies every
// feature as supported, opaque-preserved or unsupported.
//
// Reconstruction does not. Encode keeps everything after the last data block as one
// opaque exact span (so the footer, index, filters and properties never need to be
// understood to be rebuilt) and re-expresses the data blocks as
//
//	canonical logical records (user key | value | internal-key trailer) held in
//	  content-addressed groups,
//	+ per-block physical framing: entry count, restart layout exceptions, the 5-byte
//	  trailer, and (Snappy) the exact command recipe (snappyz3).
//
// Decode rebuilds the original bytes from the serialized recipe and those components
// only. It never calls a Snappy encoder and needs no database library.
//
// The package imports only the standard library and snappyz3. Every count, size and
// read is bounded; Open and Encode work on io.ReaderAt plus a size and never load
// the file.
package sstz3

import (
	"errors"
	"fmt"
)

var (
	// ErrCorrupt reports bytes that violate the table or block format.
	ErrCorrupt = errors.New("sstz3: corrupt table")
	// ErrUnsupported reports a well-formed table (or feature) the exact representation
	// does not express; the caller falls back to ordinary CDC.
	ErrUnsupported = errors.New("sstz3: unsupported table")
	// ErrLimit reports that a configured bound would be exceeded.
	ErrLimit = errors.New("sstz3: limit exceeded")
	// ErrRecipe reports a malformed serialized recipe.
	ErrRecipe = errors.New("sstz3: malformed recipe")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}
func unsupported(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrUnsupported}, a...)...)
}
func recipeErr(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrRecipe}, a...)...)
}

// Limits bound every parse. The zero value selects the defaults.
type Limits struct {
	MaxBlocks      int   // data blocks per table (default 1<<20)
	MaxBlockBytes  int64 // one stored block payload (default 16 MiB)
	MaxDecoded     int64 // one decompressed block (default 64 MiB)
	MaxEntries     int   // entries per block (default 1<<22)
	MaxMeta        int64 // one metadata block read by Open (default 64 MiB)
	MaxTail        int64 // opaque tail span (default 256 MiB)
	MaxRecordBytes int64 // one key or value (default 64 MiB)
	MaxRecipe      int64 // recipe head bytes (default 256 MiB)
}

func (l Limits) norm() Limits {
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = 1 << 20
	}
	if l.MaxBlockBytes <= 0 {
		l.MaxBlockBytes = 16 << 20
	}
	if l.MaxDecoded <= 0 {
		l.MaxDecoded = 64 << 20
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = 1 << 22
	}
	if l.MaxMeta <= 0 {
		l.MaxMeta = 64 << 20
	}
	if l.MaxTail <= 0 {
		l.MaxTail = 256 << 20
	}
	if l.MaxRecordBytes <= 0 {
		l.MaxRecordBytes = 64 << 20
	}
	if l.MaxRecipe <= 0 {
		l.MaxRecipe = 256 << 20
	}
	return l
}
