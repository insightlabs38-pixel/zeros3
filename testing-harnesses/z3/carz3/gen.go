package carz3

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"sort"
)

// Deterministic CAR writers and block generators for the laboratory corpora.
// Nothing here is a general CAR encoder: it produces exactly the fixture
// shapes the experiments need.

// MhFakeBlake is a multihash code the lab cannot verify (blake2b-256). Blocks
// that use it carry a SHA-256 digest purely so that identity is stable; the
// parser must report them as not-verified rather than interpret them.
const MhFakeBlake = 0xb220

// Block is one CAR section: an exact CID and its payload.
type Block struct {
	CID  []byte
	Data []byte
}

// CIDv1 builds a CIDv1 for data under the given codec and multihash code.
func CIDv1(codec, mh uint64, data []byte) []byte {
	var d []byte
	switch mh {
	case MhSHA2_256, MhFakeBlake:
		s := sha256.Sum256(data)
		d = s[:]
	case MhSHA2_512:
		s := sha512.Sum512(data)
		d = s[:]
	case MhSHA3_256:
		s := sha3.Sum256(data)
		d = s[:]
	case MhIdentity:
		d = data
	default:
		panic("carz3: unsupported generator multihash")
	}
	b := AppendUvarint(nil, 1)
	b = AppendUvarint(b, codec)
	b = AppendUvarint(b, mh)
	b = AppendUvarint(b, uint64(len(d)))
	return append(b, d...)
}

// CIDv0 builds a CIDv0 (dag-pb, sha2-256).
func CIDv0(data []byte) []byte {
	s := sha256.Sum256(data)
	return append([]byte{0x12, 0x20}, s[:]...)
}

// NewBlock builds a block with a CIDv1/sha2-256 CID.
func NewBlock(codec uint64, data []byte) Block {
	return Block{CID: CIDv1(codec, MhSHA2_256, data), Data: data}
}

func cborUint(major byte, v uint64) []byte {
	m := major << 5
	switch {
	case v < 24:
		return []byte{m | byte(v)}
	case v < 1<<8:
		return []byte{m | 24, byte(v)}
	case v < 1<<16:
		return binary.BigEndian.AppendUint16([]byte{m | 25}, uint16(v))
	case v < 1<<32:
		return binary.BigEndian.AppendUint32([]byte{m | 26}, uint32(v))
	}
	return binary.BigEndian.AppendUint64([]byte{m | 27}, v)
}

func cborLink(cid []byte) []byte {
	b := []byte{0xd8, 0x2a}
	b = append(b, cborUint(2, uint64(len(cid)+1))...)
	b = append(b, 0)
	return append(b, cid...)
}

// HeaderBody is the canonical DAG-CBOR CARv1 header body for roots (roots
// key first, as sorted DAG-CBOR requires); rootsFirst=false yields the
// non-canonical order some writers use.
func HeaderBody(roots [][]byte, rootsFirst bool) []byte {
	arr := cborUint(4, uint64(len(roots)))
	for _, r := range roots {
		arr = append(arr, cborLink(r)...)
	}
	rk := append([]byte{0x65}, "roots"...)
	vk := append([]byte{0x67}, "version"...)
	b := []byte{0xa2}
	if rootsFirst {
		b = append(append(append(b, rk...), arr...), append(vk, 0x01)...)
	} else {
		b = append(append(append(b, vk...), 0x01), append(rk, arr...)...)
	}
	return b
}

// WriteV1 streams a CARv1: header body then sections from next until it
// reports false.
func WriteV1(w io.Writer, hdr []byte, next func() (Block, bool)) error {
	if _, err := w.Write(append(AppendUvarint(nil, uint64(len(hdr))), hdr...)); err != nil {
		return err
	}
	var buf []byte
	for {
		b, ok := next()
		if !ok {
			return nil
		}
		buf = AppendUvarint(buf[:0], uint64(len(b.CID)+len(b.Data)))
		buf = append(buf, b.CID...)
		buf = append(buf, b.Data...)
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
}

// V1 is a convenience that materialises a CARv1 from a block list.
func V1(roots [][]byte, blocks []Block) []byte {
	return V1With(HeaderBody(roots, true), blocks)
}

func V1With(hdr []byte, blocks []Block) []byte {
	var out bytes.Buffer
	i := 0
	WriteV1(&out, hdr, func() (Block, bool) {
		if i == len(blocks) {
			return Block{}, false
		}
		i++
		return blocks[i-1], true
	})
	return out.Bytes()
}

// V2Opts shapes a CARv2 wrapper. IndexCodec 0 means no index.
type V2Opts struct {
	Char       [16]byte
	PadBefore  int
	PadAfter   int
	PadByte    byte
	IndexCodec uint64
}

// V2From wraps the CARv1 bytes v1 (whose sections are blocks, in order) as a
// CARv2, building the sorted index the way the lab's BuildIndex does.
func V2From(v1 []byte, o V2Opts) []byte {
	var out []byte
	hasIdx := o.IndexCodec != 0
	hdr := DeriveHeader(o.Char[:], o.PadBefore, int64(len(v1)), o.PadAfter, hasIdx)
	out = append(out, V2Pragma...)
	out = append(out, hdr[:]...)
	out = append(out, bytes.Repeat([]byte{o.PadByte}, o.PadBefore)...)
	out = append(out, v1...)
	out = append(out, bytes.Repeat([]byte{o.PadByte}, o.PadAfter)...)
	if hasIdx {
		rd, err := NewReader(bytes.NewReader(v1), DefaultLimits())
		if err != nil {
			panic(err)
		}
		var recs []IndexRec
		for {
			e, err := rd.Next()
			if err != nil {
				break
			}
			r := IndexRec{Digest: append([]byte(nil), e.CID.Digest...), Offset: uint64(e.Off)}
			if o.IndexCodec == MultihashIndexSortedCodec {
				r.Mh = e.CID.HashCode
			}
			recs = append(recs, r)
		}
		out = append(out, BuildIndex(o.IndexCodec, recs)...)
	}
	return out
}

// GenSpec describes a deterministic block set: N sections, leaf payloads of
// Min..Max bytes (raw codec) and a dag-cbor link node over every Fan leaves.
type GenSpec struct {
	Seed     uint64
	N        int
	Min, Max int
	Fan      int
}

// Gen returns spec's blocks in generation order and a root over the last
// nodes. Payload bytes are incompressible and unique per (Seed, index).
func Gen(sp GenSpec) (blocks []Block, root []byte) {
	rng := rand.New(rand.NewPCG(sp.Seed, 0x7a33_c4a7))
	fan := max(sp.Fan, 2)
	var pend [][]byte
	var nodes [][]byte
	flush := func() {
		var d []byte
		d = append(d, cborUint(4, uint64(len(pend)))...)
		for _, c := range pend {
			d = append(d, cborLink(c)...)
		}
		b := NewBlock(CodecDagCBR, d)
		blocks = append(blocks, b)
		nodes = append(nodes, b.CID)
		pend = nil
	}
	for len(blocks) < sp.N {
		n := sp.Min
		if sp.Max > sp.Min {
			n += rng.IntN(sp.Max - sp.Min + 1)
		}
		d := make([]byte, n)
		for i := 0; i < n; i += 8 {
			var w [8]byte
			binary.LittleEndian.PutUint64(w[:], rng.Uint64())
			copy(d[i:], w[:])
		}
		b := NewBlock(CodecRaw, d)
		blocks = append(blocks, b)
		pend = append(pend, b.CID)
		if len(pend) == fan {
			flush()
		}
	}
	if len(pend) > 0 {
		flush()
	}
	var top []byte
	top = append(top, cborUint(4, uint64(len(nodes)))...)
	for _, c := range nodes {
		top = append(top, cborLink(c)...)
	}
	rb := NewBlock(CodecDagCBR, top)
	blocks = append(blocks, rb)
	return blocks, rb.CID
}

// Shuffled returns a seeded permutation of blocks.
func Shuffled(seed uint64, blocks []Block) []Block {
	out := append([]Block(nil), blocks...)
	rng := rand.New(rand.NewPCG(seed, 0x51ff))
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// SortedByCID returns blocks ordered by CID bytes.
func SortedByCID(blocks []Block) []Block {
	out := append([]Block(nil), blocks...)
	sort.SliceStable(out, func(i, j int) bool { return bytes.Compare(out[i].CID, out[j].CID) < 0 })
	return out
}

// Recode returns the same payloads with CIDs rebuilt under codec/mh (or as
// CIDv0 when v0 is set): the payload is unchanged, the native identity is not.
func Recode(blocks []Block, codec, mh uint64, v0 bool) []Block {
	out := make([]Block, len(blocks))
	for i, b := range blocks {
		out[i] = Block{CID: CIDv1(codec, mh, b.Data), Data: b.Data}
		if v0 {
			out[i].CID = CIDv0(b.Data)
		}
	}
	return out
}
