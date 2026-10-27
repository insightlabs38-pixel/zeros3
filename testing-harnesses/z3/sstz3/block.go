package sstz3

import (
	"encoding/binary"
	"math"
)

// Layout describes how a decoded block frames its entries. A block that matches the
// table's derived layout (restarts at every RestartInterval-th entry, every
// non-restart entry sharing the maximal prefix with its predecessor) needs no
// stored layout at all.
type Layout struct {
	Entries    int
	RestartIdx []int // entry index of every restart
	Shared     []int // shared-prefix length of every entry
	HashIndex  bool  // RocksDB data block hash index flag (unsupported)
}

// WalkBlock validates a decoded block (entries, restart array, restart count) and
// calls fn for every entry in order with its full key (valid only during the call)
// and value (aliases b). It rejects bad varints, arithmetic overflow, entries or
// restarts outside the payload, a shared length above the previous key's length,
// restarts that are not strictly increasing entry boundaries (the first at 0), and a
// truncated restart array. fn may be nil.
func WalkBlock(b []byte, lim Limits, fn func(i int, key, val []byte) error) (*Layout, error) {
	lim = lim.norm()
	if len(b) < 4 {
		return nil, corrupt("block of %d bytes", len(b))
	}
	nr := binary.LittleEndian.Uint32(b[len(b)-4:])
	l := &Layout{}
	if nr&(1<<31) != 0 { // RocksDB: data-block hash index appended after the restarts
		l.HashIndex = true
		return l, unsupported("data block hash index")
	}
	if nr == 0 || uint64(nr) > uint64(len(b)-4)/4 || int64(nr) > int64(lim.MaxEntries) {
		return nil, corrupt("restart count %d in a block of %d bytes", nr, len(b))
	}
	payload := len(b) - 4 - 4*int(nr)
	if payload < 0 {
		return nil, corrupt("restart array exceeds the block")
	}
	restarts := make([]uint32, nr)
	for i := range restarts {
		restarts[i] = binary.LittleEndian.Uint32(b[payload+4*i:])
		if int64(restarts[i]) > int64(payload) || (i > 0 && restarts[i] <= restarts[i-1]) || (i == 0 && restarts[i] != 0) {
			return nil, corrupt("restart %d offset %d (payload %d)", i, restarts[i], payload)
		}
	}
	var key []byte
	pos, ri := 0, 0
	for pos < payload {
		isRestart := ri < len(restarts) && int(restarts[ri]) == pos
		if ri < len(restarts) && int(restarts[ri]) < pos {
			return nil, corrupt("restart %d is inside an entry", ri)
		}
		shared, n1 := binary.Uvarint(b[pos:payload])
		if n1 <= 0 {
			return nil, corrupt("shared length varint at %d", pos)
		}
		nonShared, n2 := binary.Uvarint(b[pos+n1 : payload])
		if n2 <= 0 {
			return nil, corrupt("non-shared length varint at %d", pos)
		}
		vlen, n3 := binary.Uvarint(b[pos+n1+n2 : payload])
		if n3 <= 0 {
			return nil, corrupt("value length varint at %d", pos)
		}
		hdr := pos + n1 + n2 + n3
		if shared > uint64(len(key)) || (isRestart && shared != 0) {
			return nil, corrupt("shared length %d with previous key of %d at %d", shared, len(key), pos)
		}
		if nonShared > uint64(lim.MaxRecordBytes) || vlen > uint64(lim.MaxRecordBytes) ||
			nonShared > uint64(payload-hdr) || vlen > uint64(payload-hdr)-nonShared {
			return nil, corrupt("entry at %d exceeds the block", pos)
		}
		if l.Entries >= lim.MaxEntries {
			return nil, ErrLimit
		}
		if isRestart {
			l.RestartIdx = append(l.RestartIdx, l.Entries)
			ri++
		}
		key = append(key[:shared], b[hdr:hdr+int(nonShared)]...)
		val := b[hdr+int(nonShared) : hdr+int(nonShared)+int(vlen)]
		l.Shared = append(l.Shared, int(shared))
		if fn != nil {
			if err := fn(l.Entries, key, val); err != nil {
				return nil, err
			}
		}
		l.Entries++
		pos = hdr + int(nonShared) + int(vlen)
	}
	if pos != payload {
		return nil, corrupt("entries end at %d, payload ends at %d", pos, payload)
	}
	if ri != len(restarts) && !(payload == 0 && nr == 1) {
		return nil, corrupt("%d restarts do not sit on entry boundaries", len(restarts)-ri)
	}
	if l.Entries == 0 && (nr != 1 || payload != 0) {
		return nil, corrupt("empty block with %d restarts", nr)
	}
	return l, nil
}

// derives reports whether the block's framing equals the one derived from the
// table's restart interval r and maximal prefix sharing, given the entries' keys.
// keys is called with every entry index in order.
func (l *Layout) derivesFrom(r int, keyAt func(i int) []byte) bool {
	if r <= 0 {
		return false
	}
	for k, idx := range l.RestartIdx {
		if idx != k*r {
			return false
		}
	}
	if len(l.RestartIdx) != (l.Entries+r-1)/r && l.Entries > 0 {
		return false
	}
	var prev []byte
	for i := 0; i < l.Entries; i++ {
		k := keyAt(i)
		want := 0
		if i%r != 0 {
			want = commonPrefix(prev, k)
		}
		if l.Shared[i] != want {
			return false
		}
		prev = k
	}
	return true
}

func commonPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// BlockWriter rebuilds the exact bytes of a decoded block from full keys and values.
type BlockWriter struct {
	buf      []byte
	restarts []uint32
	prev     []byte
	n        int
}

// Reset starts a new block, reusing buffers.
func (w *BlockWriter) Reset() {
	w.buf, w.restarts, w.prev, w.n = w.buf[:0], w.restarts[:0], w.prev[:0], 0
}

// Add appends one entry. restart forces a restart point; shared < 0 means the maximal
// common prefix with the previous key (0 at a restart); otherwise the stored value is
// used and must not exceed the previous key's length.
func (w *BlockWriter) Add(key, val []byte, restart bool, shared int) error {
	if restart {
		w.restarts = append(w.restarts, uint32(len(w.buf)))
		shared = 0
	} else if shared < 0 {
		shared = commonPrefix(w.prev, key)
	}
	if shared > len(w.prev) || shared > len(key) {
		return corrupt("shared length %d above key lengths %d/%d", shared, len(w.prev), len(key))
	}
	if int64(len(w.buf))+int64(len(key))+int64(len(val))+30 > math.MaxUint32 {
		return ErrLimit
	}
	w.buf = binary.AppendUvarint(w.buf, uint64(shared))
	w.buf = binary.AppendUvarint(w.buf, uint64(len(key)-shared))
	w.buf = binary.AppendUvarint(w.buf, uint64(len(val)))
	w.buf = append(w.buf, key[shared:]...)
	w.buf = append(w.buf, val...)
	w.prev = append(w.prev[:0], key...)
	w.n++
	return nil
}

// Finish appends the restart array and count and returns the block bytes (valid until
// the next Reset).
func (w *BlockWriter) Finish() []byte {
	if len(w.restarts) == 0 { // an empty block still has one restart at 0
		w.restarts = append(w.restarts, 0)
	}
	for _, r := range w.restarts {
		w.buf = binary.LittleEndian.AppendUint32(w.buf, r)
	}
	w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(len(w.restarts)))
	return w.buf
}
