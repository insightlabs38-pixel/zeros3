package sstz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"zeros3-z3/snappyz3"
)

// Class is the support classification of a table feature.
type Class uint8

const (
	Supported   Class = 1 // understood: parsed and re-expressed
	Opaque      Class = 2 // preserved as exact bytes without being understood
	Unsupported Class = 3 // the table is rejected (ordinary CDC fallback)
)

func (c Class) String() string {
	switch c {
	case Supported:
		return "SUPPORTED"
	case Opaque:
		return "OPAQUE_PRESERVED"
	case Unsupported:
		return "UNSUPPORTED_FALLBACK"
	}
	return "?"
}

// Feature is one classified property of a table.
type Feature struct {
	Name   string
	Class  Class
	Detail string
}

// Table is the parsed skeleton of one SST: footer, metadata names, properties and the
// ordered list of data block handles. Data blocks are not read by Open.
type Table struct {
	Size     int64
	Footer   Footer
	Meta     map[string]Handle // metaindex entries
	Props    map[string][]byte // properties block
	Blocks   []Handle          // data blocks in file order, contiguous from offset 0
	DataEnd  int64             // end of the last data block (its trailer included)
	Features []Feature

	twoLevel bool
	keep     bool

	// IndexKeys (OpenKeys only) are the separator keys of the data-block index, one per
	// data block; IndexUserKeys says they are user keys (RocksDB) rather than internal keys.
	IndexKeys     [][]byte
	IndexUserKeys bool
}

// Reject returns the first Unsupported feature as an error, or nil.
func (t *Table) Reject() error {
	for _, f := range t.Features {
		if f.Class == Unsupported {
			return unsupported("%s: %s", f.Name, f.Detail)
		}
	}
	return nil
}

// PropUint returns an integer property (varint-encoded).
func (t *Table) PropUint(name string) (uint64, bool) {
	b, ok := t.Props[name]
	if !ok {
		return 0, false
	}
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, false
	}
	return v, true
}

// Prop returns a string property.
func (t *Table) Prop(name string) string { return string(t.Props[name]) }

// DecodeBlock decompresses a raw block. Snappy returns the exact command recipe. Any
// other compression type is Unsupported.
func DecodeBlock(b *RawBlock, lim Limits) (decoded []byte, cmds []snappyz3.Cmd, err error) {
	lim = lim.norm()
	switch b.Comp() {
	case CompNone:
		return b.Payload, nil, nil
	case CompSnappy:
		d, c, err := snappyz3.Parse(b.Payload, snappyz3.Limits{MaxDecoded: lim.MaxDecoded})
		if err != nil {
			return nil, nil, fmt.Errorf("%w: snappy block: %v", ErrUnsupported, err)
		}
		return d, c, nil
	}
	return nil, nil, unsupported("compression type %d", b.Comp())
}

// walkMeta decodes a metadata block (uncompressed, or Snappy for the index) and walks
// its key/value entries.
func walkMeta(r io.ReaderAt, size int64, h Handle, cksum uint8, lim Limits, fn func(key, val []byte, restart bool) error) error {
	if h.Size > uint64(lim.MaxMeta) {
		return ErrLimit
	}
	rb, err := ReadBlock(r, size, h, cksum, true, lim)
	if err != nil {
		return err
	}
	dec, _, err := DecodeBlock(rb, lim)
	if err != nil {
		return err
	}
	var lay *Layout
	lay, err = WalkBlock(dec, lim, nil)
	if err != nil {
		return err
	}
	isRestart := map[int]bool{}
	for _, i := range lay.RestartIdx {
		isRestart[i] = true
	}
	_, err = WalkBlock(dec, lim, func(i int, key, val []byte) error { return fn(key, val, isRestart[i]) })
	return err
}

// Open parses the skeleton of the table: footer, metaindex, properties and the index
// down to the list of data blocks. It reads O(metadata) bytes. It classifies every
// feature it meets; a table with an Unsupported feature is returned with the feature
// recorded (Reject reports it) so a characterisation can still describe it.
// OpenKeys is Open that also keeps the index separator keys (for point lookups).
func OpenKeys(r io.ReaderAt, size int64, lim Limits) (*Table, error) { return open(r, size, lim, true) }

func Open(r io.ReaderAt, size int64, lim Limits) (*Table, error) { return open(r, size, lim, false) }

func open(r io.ReaderAt, size int64, lim Limits, keep bool) (*Table, error) {
	lim = lim.norm()
	f, err := ReadFooter(r, size)
	if err != nil {
		return nil, err
	}
	t := &Table{Size: size, Footer: f, Meta: map[string]Handle{}, Props: map[string][]byte{}, keep: keep}
	add := func(n string, c Class, d string) { t.Features = append(t.Features, Feature{n, c, d}) }
	switch f.Family {
	case FamilyLegacy:
		add("footer", Supported, "legacy 48-byte, masked CRC32C")
	case FamilyRocksDB:
		c := Supported
		if f.Version > 6 {
			c = Unsupported
		}
		add("footer", c, fmt.Sprintf("rocksdb format_version %d, checksum type %d", f.Version, f.Checksum))
	case FamilyPebble:
		c := Supported
		if f.Version < 1 || f.Version > 4 {
			c = Unsupported
		}
		add("footer", c, fmt.Sprintf("pebble table format %d, checksum type %d", f.Version, f.Checksum))
	}
	if f.Checksum != CksumCRC32 {
		add("checksum", Opaque, fmt.Sprintf("type %d: trailers preserved verbatim, not verified", f.Checksum))
	}
	if err := t.Reject(); err != nil {
		return t, nil
	}
	if f.Metaindex.Off > uint64(size) {
		return nil, corrupt("metaindex handle")
	}
	var propsH Handle
	var haveProps bool
	err = walkMeta(r, size, f.Metaindex, f.Checksum, lim, func(key, val []byte, _ bool) error {
		h, n, ok := getHandle(val)
		if !ok || n != len(val) {
			return corrupt("metaindex handle for %q", key)
		}
		name := string(key)
		if _, dup := t.Meta[name]; dup {
			return corrupt("duplicate metaindex name %q", name)
		}
		t.Meta[name] = h
		if name == "rocksdb.properties" {
			propsH, haveProps = h, true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if haveProps {
		err = walkMeta(r, size, propsH, f.Checksum, lim, func(key, val []byte, _ bool) error {
			t.Props[string(key)] = append([]byte(nil), val...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// metadata blocks by name
	names := make([]string, 0, len(t.Meta))
	for n := range t.Meta {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch {
		case n == "rocksdb.properties":
			add("properties", Opaque, "block preserved")
		case n == "rocksdb.index":
			// RocksDB format 6: the index handle
		case strings.HasPrefix(n, "fullfilter.") || strings.HasPrefix(n, "filter."):
			add("filter", Opaque, n)
		case strings.HasPrefix(n, "partitionedfilter."):
			add("filter", Unsupported, "partitioned filter: "+n)
		case n == "rocksdb.range_del" || n == "rocksdb.range_del2":
			add("range-deletion", Opaque, n)
		case n == "pebble.range_key":
			add("range-keys", Opaque, n)
		case n == "pebble.value_index":
			add("value-blocks", Opaque, "separated value region preserved in the tail; data-block value handles are values")
		case n == "rocksdb.compression_dict":
			add("compression-dictionary", Unsupported, n)
		case strings.HasPrefix(n, "pebble.") || strings.HasPrefix(n, "rocksdb."):
			add("metablock", Opaque, n)
		default:
			add("metablock", Opaque, n)
		}
	}
	if it, ok := t.PropUint("rocksdb.block.based.table.index.type"); ok && it != 0 {
		if it == 2 {
			t.twoLevel = true
			add("index-type", Opaque, "two-level index: partitions traversed, every index block preserved in the tail")
		} else {
			add("index-type", Unsupported, fmt.Sprintf("index type %d (hash or first-key index)", it))
		}
	}
	if d, ok := t.PropUint("rocksdb.data.block.index.type"); ok && d != 0 {
		add("data-block-index", Unsupported, "hash index")
	}
	if t.Prop("rocksdb.compression_options") != "" && strings.Contains(t.Prop("rocksdb.compression_options"), "max_dict_bytes=") &&
		!strings.Contains(t.Prop("rocksdb.compression_options"), "max_dict_bytes=0") {
		add("compression-dictionary", Unsupported, "dictionary compression enabled")
	}
	if err := t.Reject(); err != nil {
		return t, nil
	}
	// index (one level, or a two-level index whose top level lists index partitions)
	ih := f.Index
	if f.IndexInMeta {
		h, ok := t.Meta["rocksdb.index"]
		if !ok {
			return nil, corrupt("format 6 table without an index handle")
		}
		ih = h
	}
	delta := false
	if v, ok := t.PropUint("rocksdb.index.value.is.delta.encoded"); ok && v != 0 {
		delta = true
	}
	if v, ok := t.PropUint("rocksdb.index.key.is.user.key"); ok && v != 0 {
		t.IndexUserKeys = true
	}
	var pos uint64
	collect := func(h Handle) error {
		return walkIndex(r, size, h, f.Checksum, delta, lim, func(key []byte, dh Handle) error {
			if t.keep {
				t.IndexKeys = append(t.IndexKeys, append([]byte(nil), key...))
			}
			if len(t.Blocks) >= lim.MaxBlocks {
				return ErrLimit
			}
			if dh.Off != pos {
				return unsupported("data blocks are not contiguous: block %d at %d, expected %d", len(t.Blocks), dh.Off, pos)
			}
			if dh.Size > uint64(lim.MaxBlockBytes) || dh.Size+trailerLen > uint64(size)-pos {
				return corrupt("data block %d handle", len(t.Blocks))
			}
			pos = dh.Off + dh.Size + trailerLen
			t.Blocks = append(t.Blocks, dh)
			return nil
		})
	}
	if t.twoLevel {
		var parts []Handle
		err = walkIndex(r, size, ih, f.Checksum, delta, lim, func(_ []byte, h Handle) error {
			if len(parts) >= lim.MaxBlocks {
				return ErrLimit
			}
			parts = append(parts, h)
			return nil
		})
		for i := 0; err == nil && i < len(parts); i++ {
			err = collect(parts[i])
		}
	} else {
		err = collect(ih)
	}
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			add("data-layout", Unsupported, err.Error())
			return t, nil
		}
		return nil, err
	}
	t.DataEnd = int64(pos)
	if int64(pos) > size-int64(f.Len) {
		return nil, corrupt("data blocks end at %d, footer starts at %d", pos, size-int64(f.Len))
	}
	if n, ok := t.PropUint("rocksdb.num.data.blocks"); ok && n != uint64(len(t.Blocks)) {
		return nil, corrupt("index lists %d data blocks, properties say %d", len(t.Blocks), n)
	}
	add("data-blocks", Supported, fmt.Sprintf("%d contiguous blocks", len(t.Blocks)))
	return t, nil
}

// walkIndex walks an index block (plain LevelDB entries, or RocksDB's value-delta
// encoding in which entries carry no value length and non-restart handles are signed
// size deltas) and reports every block handle.
func walkIndex(r io.ReaderAt, size int64, h Handle, cksum uint8, delta bool, lim Limits, fn func([]byte, Handle) error) error {
	if !delta {
		return walkMeta(r, size, h, cksum, lim, func(key, val []byte, _ bool) error {
			dh, n, ok := getHandle(val)
			if !ok {
				return corrupt("index handle")
			}
			if n != len(val) {
				return unsupported("index value with trailing bytes")
			}
			return fn(key, dh)
		})
	}
	if h.Size > uint64(lim.MaxMeta) {
		return ErrLimit
	}
	rb, err := ReadBlock(r, size, h, cksum, true, lim)
	if err != nil {
		return err
	}
	b, _, err := DecodeBlock(rb, lim)
	if err != nil {
		return err
	}
	if len(b) < 8 {
		return corrupt("index block of %d bytes", len(b))
	}
	nr := binary.LittleEndian.Uint32(b[len(b)-4:])
	if nr == 0 || uint64(nr) > uint64(len(b)-4)/4 {
		return corrupt("index restart count")
	}
	payload := len(b) - 4 - 4*int(nr)
	restart := map[int]bool{}
	for i := 0; i < int(nr); i++ {
		o := int(binary.LittleEndian.Uint32(b[payload+4*i:]))
		if o > payload {
			return corrupt("index restart offset")
		}
		restart[o] = true
	}
	var prev Handle
	var key []byte
	first := true
	for pos := 0; pos < payload; {
		start := pos
		shared, n1 := binary.Uvarint(b[pos:payload])
		if n1 <= 0 {
			return corrupt("index entry")
		}
		pos += n1
		ns, n2 := binary.Uvarint(b[pos:payload])
		if n2 <= 0 || ns > uint64(payload-pos-n2) || shared > uint64(len(key)) {
			return corrupt("index entry key")
		}
		key = append(key[:shared], b[pos+n2:pos+n2+int(ns)]...)
		pos += n2 + int(ns)
		var dh Handle
		if restart[start] || first {
			hh, n, ok := getHandle(b[pos:payload])
			if !ok {
				return corrupt("index handle")
			}
			dh, pos = hh, pos+n
		} else {
			d, n := binary.Varint(b[pos:payload])
			if n <= 0 {
				return corrupt("delta-encoded index value")
			}
			sz := int64(prev.Size) + d
			if sz < 0 {
				return corrupt("delta-encoded index size")
			}
			dh, pos = Handle{prev.Off + prev.Size + trailerLen, uint64(sz)}, pos+n
		}
		first = false
		prev = dh
		if err := fn(key, dh); err != nil {
			return err
		}
	}
	return nil
}
