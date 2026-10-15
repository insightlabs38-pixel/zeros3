// Package sqlitez3 is the Z3-18 exact representation of a SQLite main database
// file: a page tree with the page-number head of every non-b-tree page split off.
//
// Why. A SQLite file is an array of fixed-size pages. Sparse updates, appends and
// B-tree splits rewrite single pages, and VACUUM moves pages without changing
// most of them; production CDC (64 KiB average) cannot see page granularity. Two
// things keep page granularity cheap and effective:
//
//   - Tree. A flat list of 4 KiB page digests costs ~0.8 % of the file per
//     revision. Pages are therefore grouped under content-addressed *nodes*
//     (Fanout page entries each); the recipe lists node digests only, and an
//     untouched node is reused whole.
//   - Head split. A page that does not start with a b-tree type byte (overflow
//     chain, freelist, pointer-map and zero pages all start with a 0x00 byte, as
//     page numbers are < 2^24 in practice) begins with a 4-byte page number that
//     changes whenever the page moves. That head is stored in the node as a delta
//     against "the next page", and the 4-byte-shorter body is the component, so a
//     moved overflow chain still shares its bodies.
//
// The split is purely byte-level and reversible for any page content: no
// b-tree is walked, so a wrong guess about a page's role costs nothing but a few
// recipe bytes, never exactness.
//
// Shape. Stdlib only; no import of the laboratory. The source is an
// io.ReaderAt plus size read one page at a time; reconstruction writes
// progressively to an io.Writer and fetches components through a callback.
// Declared sizes never drive an allocation larger than the recipe itself.
//
// Recipe (all integers unsigned varints unless noted):
//
//	"SQZ1" | pageSize | fanout | size | tail (size % pageSize raw bytes) | node digests (32 B each)
//
// Node (one per Fanout consecutive pages; the last node may be shorter):
//
//	per page: flag (0 = whole page, 1 = head split) | [zigzag(head - (pageIndex+2))] | SHA-256 of the component (32 B)
//
// A split page's component is the page without its first 4 bytes; head is that
// big-endian uint32. A sequential overflow chain therefore has delta 0.
package sqlitez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	ErrUnsupported = errors.New("sqlitez3: not a supported SQLite database")
	ErrCorrupt     = errors.New("sqlitez3: corrupt")
	ErrLimit       = errors.New("sqlitez3: limit exceeded")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}

const (
	// DefaultFanout is the page entries per node.
	DefaultFanout = 64
	maxFanout     = 4096
	magic         = "SQZ1"
	sqliteMagic   = "SQLite format 3\x00"
)

// Limits bound what a parse or reconstruction will accept.
type Limits struct {
	MaxSize int64 // database bytes (default 1 TiB)
}

func (l Limits) norm() Limits {
	if l.MaxSize <= 0 {
		l.MaxSize = 1 << 40
	}
	return l
}

// Options configure Encode.
type Options struct {
	Fanout int // page entries per node (default DefaultFanout)
	Lim    Limits
}

// ID is a SHA-256 component identity.
type ID = [32]byte

// Sink receives every component Encode produces (page bodies and nodes), in
// production order. data is only valid during the call; the callee deduplicates.
type Sink func(id ID, data []byte) error

// Getter returns the exact bytes of a stored component.
type Getter func(id ID) ([]byte, error)

// Stats summarise one Encode.
type Stats struct {
	Pages, Split, Nodes int
	PageSize            int
	Size                int64
}

// PageSize validates the 100-byte header and returns the page size.
func PageSize(hdr []byte) (int, error) {
	if len(hdr) < 100 || !bytes.HasPrefix(hdr, []byte(sqliteMagic)) {
		return 0, ErrUnsupported
	}
	ps := int(binary.BigEndian.Uint16(hdr[16:]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return 0, ErrUnsupported
	}
	return ps, nil
}

func readFullAt(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || err == io.EOF {
		return corrupt("source ended at %d, wanted %d bytes at %d", off+int64(n), len(b), off)
	}
	return err
}

func zig(v int64) uint64   { return uint64(v<<1) ^ uint64(v>>63) }
func unzig(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

func nodeCount(npages int64, fanout int) int64 { return (npages + int64(fanout) - 1) / int64(fanout) }

// Encode reads the size bytes of r, emits every component to put, and returns the
// serialised recipe. A source that is not a SQLite database (bad magic, odd page
// size, shorter than one page) is ErrUnsupported; a source shorter than size is
// ErrCorrupt. It reads sequentially, one page at a time, never past size.
func Encode(r io.ReaderAt, size int64, opt Options, put Sink) ([]byte, Stats, error) {
	lim := opt.Lim.norm()
	fan := opt.Fanout
	if fan == 0 {
		fan = DefaultFanout
	}
	var st Stats
	if fan < 2 || fan > maxFanout {
		return nil, st, fmt.Errorf("%w: fanout %d", ErrUnsupported, fan)
	}
	if size < 100 {
		return nil, st, ErrUnsupported
	}
	if size > lim.MaxSize {
		return nil, st, fmt.Errorf("%w: %d bytes", ErrLimit, size)
	}
	var hdr [100]byte
	if err := readFullAt(r, hdr[:], 0); err != nil {
		return nil, st, err
	}
	ps, err := PageSize(hdr[:])
	if err != nil {
		return nil, st, err
	}
	if size < int64(ps) {
		return nil, st, ErrUnsupported
	}
	npages := size / int64(ps)
	tail := size % int64(ps)
	st.PageSize, st.Size, st.Pages = ps, size, int(npages)

	rec := append([]byte(magic), binary.AppendUvarint(nil, uint64(ps))...)
	rec = binary.AppendUvarint(rec, uint64(fan))
	rec = binary.AppendUvarint(rec, uint64(size))
	tailOff := npages * int64(ps)
	if tail > 0 {
		tb := make([]byte, tail)
		if err := readFullAt(r, tb, tailOff); err != nil {
			return nil, st, err
		}
		rec = append(rec, tb...)
	}
	page := make([]byte, ps)
	var node []byte
	for i := int64(0); i < npages; i++ {
		if err := readFullAt(r, page, i*int64(ps)); err != nil {
			return nil, st, err
		}
		body, flag := page, byte(0)
		var head uint32
		if i > 0 && page[0] == 0 {
			flag, head, body = 1, binary.BigEndian.Uint32(page), page[4:]
			st.Split++
		}
		id := sha256.Sum256(body)
		if err := put(id, body); err != nil {
			return nil, st, err
		}
		node = append(node, flag)
		if flag == 1 {
			node = binary.AppendUvarint(node, zig(int64(head)-(i+2)))
		}
		node = append(node, id[:]...)
		if (i+1)%int64(fan) == 0 || i == npages-1 {
			nid := sha256.Sum256(node)
			if err := put(nid, node); err != nil {
				return nil, st, err
			}
			rec = append(rec, nid[:]...)
			st.Nodes++
			node = node[:0]
		}
	}
	return rec, st, nil
}

// Recipe is a parsed, bounds-checked recipe. Its Nodes slice aliases the input.
type Recipe struct {
	PageSize, Fanout int
	Size, Pages      int64
	Tail             []byte
	nodes            []byte // 32-byte digests
}

// NodeCount is the number of nodes.
func (rc *Recipe) NodeCount() int { return len(rc.nodes) / 32 }

// ParseRecipe validates a serialised recipe. Nothing is allocated from a declared
// count: the node-digest section must have exactly the length the declared size
// implies, so every count is bounded by len(b).
func ParseRecipe(b []byte, lim Limits) (*Recipe, error) {
	lim = lim.norm()
	if len(b) < len(magic) || string(b[:4]) != magic {
		return nil, corrupt("recipe magic")
	}
	pos := 4
	next := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, corrupt("recipe varint")
		}
		pos += n
		return v, nil
	}
	ps, err := next()
	if err != nil {
		return nil, err
	}
	fan, err := next()
	if err != nil {
		return nil, err
	}
	size, err := next()
	if err != nil {
		return nil, err
	}
	if ps < 512 || ps > 65536 || ps&(ps-1) != 0 || fan < 2 || fan > maxFanout {
		return nil, corrupt("recipe geometry %d/%d", ps, fan)
	}
	if size > uint64(lim.MaxSize) {
		return nil, fmt.Errorf("%w: %d bytes", ErrLimit, size)
	}
	if size < ps {
		return nil, corrupt("size %d below one page", size)
	}
	rc := &Recipe{PageSize: int(ps), Fanout: int(fan), Size: int64(size), Pages: int64(size / ps)}
	tail := int64(size % ps)
	nn := nodeCount(rc.Pages, rc.Fanout)
	if int64(len(b)-pos) != tail+nn*32 {
		return nil, corrupt("recipe length %d, geometry implies %d", len(b)-pos, tail+nn*32)
	}
	rc.Tail = b[pos : pos+int(tail)]
	rc.nodes = b[pos+int(tail):]
	return rc, nil
}

func (rc *Recipe) node(k int64) ID { var id ID; copy(id[:], rc.nodes[32*k:]); return id }

// pagesIn is the number of pages node k describes.
func (rc *Recipe) pagesIn(k int64) int64 { return min(int64(rc.Fanout), rc.Pages-k*int64(rc.Fanout)) }

type entry struct {
	split bool
	delta int64
	id    ID
}

func parseNode(nb []byte, want int64) ([]entry, error) {
	out := make([]entry, 0, want)
	pos := 0
	for int64(len(out)) < want {
		if pos >= len(nb) {
			return nil, corrupt("node ends after %d of %d entries", len(out), want)
		}
		var e entry
		switch nb[pos] {
		case 0:
			pos++
		case 1:
			v, n := binary.Uvarint(nb[pos+1:])
			if n <= 0 {
				return nil, corrupt("node delta")
			}
			e.split, e.delta = true, unzig(v)
			pos += 1 + n
		default:
			return nil, corrupt("node flag %d", nb[pos])
		}
		if len(nb)-pos < 32 {
			return nil, corrupt("node digest")
		}
		copy(e.id[:], nb[pos:])
		pos += 32
		out = append(out, e)
	}
	if pos != len(nb) {
		return nil, corrupt("%d trailing bytes in node", len(nb)-pos)
	}
	return out, nil
}

func fetch(get Getter, id ID) ([]byte, error) {
	b, err := get(id)
	if err != nil {
		return nil, err
	}
	if sha256.Sum256(b) != id {
		return nil, corrupt("component %x does not match its digest", id[:6])
	}
	return b, nil
}

func (rc *Recipe) page(e entry, idx int64, get Getter) ([]byte, error) {
	body, err := fetch(get, e.id)
	if err != nil {
		return nil, err
	}
	if !e.split {
		if len(body) != rc.PageSize {
			return nil, corrupt("page component %d bytes", len(body))
		}
		return body, nil
	}
	if idx == 0 || len(body) != rc.PageSize-4 {
		return nil, corrupt("split page component %d bytes", len(body))
	}
	head := idx + 2 + e.delta
	if head < 0 || head > 1<<32-1 {
		return nil, corrupt("page %d head %d out of range", idx, head)
	}
	out := make([]byte, 0, rc.PageSize)
	out = binary.BigEndian.AppendUint32(out, uint32(head))
	return append(out, body...), nil
}

// Decode reconstructs the database from the serialised recipe and the stored
// components only, writing progressively to w. Component bytes are re-hashed.
func Decode(w io.Writer, recipe []byte, get Getter, lim Limits) error {
	rc, err := ParseRecipe(recipe, lim)
	if err != nil {
		return err
	}
	nn := int64(rc.NodeCount())
	for k := int64(0); k < nn; k++ {
		ents, err := rc.loadNode(k, get)
		if err != nil {
			return err
		}
		for j, e := range ents {
			pg, err := rc.page(e, k*int64(rc.Fanout)+int64(j), get)
			if err != nil {
				return err
			}
			if _, err := w.Write(pg); err != nil {
				return err
			}
		}
	}
	_, err = w.Write(rc.Tail)
	return err
}

func (rc *Recipe) loadNode(k int64, get Getter) ([]entry, error) {
	nb, err := fetch(get, rc.node(k))
	if err != nil {
		return nil, err
	}
	return parseNode(nb, rc.pagesIn(k))
}

// ReadPage returns page n (0-based): one node and one page component, nothing else.
func ReadPage(recipe []byte, n int64, get Getter, lim Limits) ([]byte, error) {
	rc, err := ParseRecipe(recipe, lim)
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= rc.Pages {
		return nil, fmt.Errorf("%w: page %d of %d", ErrCorrupt, n, rc.Pages)
	}
	k := n / int64(rc.Fanout)
	ents, err := rc.loadNode(k, get)
	if err != nil {
		return nil, err
	}
	return rc.page(ents[n-k*int64(rc.Fanout)], n, get)
}
