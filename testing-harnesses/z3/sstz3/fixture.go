package sstz3

// Synthetic fixtures (SYNTHETIC_FIXTURE): an independent minimal table writer used by the
// parser unit tests and by the representation tests of other packages. It exists only to
// build tiny tables with chosen footers, compression and block layouts; it is never used to
// produce evidence about a producer.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand"

	"zeros3-z3/snappyz3"
)

// FixtureKV is one internal-key/value record of a fixture.
type FixtureKV struct {
	uk   []byte
	seq  uint64
	kind byte
	val  []byte
}

// NewFixtureKV builds a record.
func NewFixtureKV(uk []byte, seq uint64, kind byte, val []byte) FixtureKV {
	return FixtureKV{uk, seq, kind, val}
}

func ikey(k FixtureKV) []byte {
	return binary.LittleEndian.AppendUint64(append([]byte(nil), k.uk...), k.seq<<8|uint64(k.kind))
}

func uv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// block builds a block with its own prefix compression (not BlockWriter).
func FixtureBlock(keys, vals [][]byte, interval int) []byte {
	var b []byte
	var rs []uint32
	var prev []byte
	for i := range keys {
		sh := 0
		if i%interval == 0 {
			rs = append(rs, uint32(len(b)))
		} else {
			for sh < len(prev) && sh < len(keys[i]) && prev[sh] == keys[i][sh] {
				sh++
			}
		}
		b = uv(b, uint64(sh))
		b = uv(b, uint64(len(keys[i])-sh))
		b = uv(b, uint64(len(vals[i])))
		b = append(b, keys[i][sh:]...)
		b = append(b, vals[i]...)
		prev = keys[i]
	}
	if len(keys) == 0 {
		rs = []uint32{0}
	}
	for _, r := range rs {
		b = binary.LittleEndian.AppendUint32(b, r)
	}
	return binary.LittleEndian.AppendUint32(b, uint32(len(rs)))
}

func maskedCRC(payload []byte, typ byte) uint32 {
	c := crc32.Update(crc32.Checksum(payload, castagnoli), castagnoli, []byte{typ})
	return (c>>15 | c<<17) + 0xa282ead8
}

type FixtureCfg struct {
	Family    string // legacy, rocksdb, pebble
	Snappy    int    // 0 none, 1 all snappy where it helps, 2 alternate
	BlockSize int
	Interval  int
	Filter    bool
}

func handle(off, size int) []byte { return uv(uv(nil, uint64(off)), uint64(size)) }

// BuildFixture writes kvs (already in internal-key order) as a table.
func BuildFixture(kvs []FixtureKV, c FixtureCfg) []byte {
	if c.Interval == 0 {
		c.Interval = 16
	}
	var out []byte
	type idx struct {
		key []byte
		h   []byte
	}
	var index []idx
	emit := func(payload []byte, typ byte) (off, size int) {
		off, size = len(out), len(payload)
		out = append(out, payload...)
		out = append(out, typ)
		return off, size
	}
	put := func(raw []byte, allowSnappy bool, n int) (int, int) {
		typ, pl := byte(CompNone), raw
		if allowSnappy && (c.Snappy == 1 || c.Snappy == 2 && n%2 == 0) {
			if s := snappyz3.EncodeRef(raw); len(s) < len(raw)-len(raw)/8 {
				typ, pl = CompSnappy, s
			}
		}
		off, size := emit(pl, typ)
		out = binary.LittleEndian.AppendUint32(out, maskedCRC(pl, typ))
		return off, size
	}
	var keys, vals [][]byte
	size, nblk := 0, 0
	flush := func() {
		if len(keys) == 0 {
			return
		}
		off, sz := put(FixtureBlock(keys, vals, c.Interval), true, nblk)
		index = append(index, idx{keys[len(keys)-1], handle(off, sz)})
		keys, vals, size = nil, nil, 0
		nblk++
	}
	for _, k := range kvs {
		keys = append(keys, ikey(k))
		vals = append(vals, k.val)
		size += len(k.uk) + len(k.val) + 12
		if size >= c.BlockSize {
			flush()
		}
	}
	flush()
	var metaK, metaV [][]byte
	if c.Filter {
		fo, fs := put(bytes.Repeat([]byte{0xAB}, 40), false, 0)
		metaK, metaV = append(metaK, []byte("filter.test")), append(metaV, handle(fo, fs))
	}
	if c.Family != "legacy" {
		var pk, pv [][]byte
		pk = append(pk, []byte("rocksdb.num.data.blocks"), []byte("rocksdb.num.entries"))
		pv = append(pv, uv(nil, uint64(len(index))), uv(nil, uint64(len(kvs))))
		po, ps := put(FixtureBlock(pk, pv, 1), false, 0)
		metaK, metaV = append(metaK, []byte("rocksdb.properties")), append(metaV, handle(po, ps))
	}
	ik, iv := make([][]byte, len(index)), make([][]byte, len(index))
	for i, x := range index {
		ik[i], iv[i] = x.key, x.h
	}
	io_, is := put(FixtureBlock(ik, iv, 1), false, 0)
	mo, ms := put(FixtureBlock(metaK, metaV, 1), false, 0)
	switch c.Family {
	case "legacy":
		f := append(handle(mo, ms), handle(io_, is)...)
		f = append(f, make([]byte, 40-len(f))...)
		f = binary.LittleEndian.AppendUint64(f, legacyMagic)
		out = append(out, f...)
	default:
		f := append([]byte{CksumCRC32}, handle(mo, ms)...)
		f = append(f, handle(io_, is)...)
		f = append(f, make([]byte, 41-len(f))...)
		if c.Family == "pebble" {
			f = binary.LittleEndian.AppendUint32(f, 4)
			f = binary.LittleEndian.AppendUint64(f, pebbleMagic)
		} else {
			f = binary.LittleEndian.AppendUint32(f, 5)
			f = binary.LittleEndian.AppendUint64(f, blockBasedMagic)
		}
		out = append(out, f...)
	}
	return out
}

// GenFixture generates records in internal-key order; big adds values larger than a block.
func GenFixture(n int, seed int64, big bool) []FixtureKV {
	r := rand.New(rand.NewSource(seed))
	var out []FixtureKV
	for i := 0; i < n; i++ {
		uk := []byte(fmt.Sprintf("user/%06d/%s", i/3, []string{"a", "bb", "ccc"}[i%3]))
		if i == 0 {
			uk = nil // the empty user key
		}
		var v []byte
		c := r.Intn(5)
		if c == 3 && !big {
			c = 2
		}
		switch c {
		case 0:
		case 1:
			v = bytes.Repeat([]byte("value-"), 1+r.Intn(60))
		case 2:
			v = make([]byte, 100+r.Intn(300))
			r.Read(v)
		case 3:
			v = make([]byte, 9000+r.Intn(9000)) // bigger than a block
			r.Read(v)
		default:
			v = []byte(fmt.Sprintf(`{"id":%d,"name":"n%d"}`, i, i%7))
		}
		out = append(out, FixtureKV{uk: uk, seq: uint64(1000 + r.Intn(1<<20)), kind: byte(r.Intn(2)), val: v})
	}
	return out
}
