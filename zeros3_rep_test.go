package main

import (
	"archive/tar"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fixtures ---------------------------------------------------------------

// repTestSQLite builds a SQLite-shaped file: the real header fields the
// representation depends on, pages that are leaf-like (0x0d) or overflow-like
// (0x00 head), deterministic per seed.
func repTestSQLite(pages, ps int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, pages*ps)
	r.Read(b)
	copy(b, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(b[16:], uint16(ps))
	b[18], b[19] = 1, 1
	for i := 0; i < pages; i++ {
		switch {
		case i == 0:
			b[100] = 0x0d
		case i%5 == 0: // overflow / freelist style page: starts with a page number
			binary.BigEndian.PutUint32(b[i*ps:], uint32(i+2))
		default:
			b[i*ps] = 0x0d
		}
	}
	return b
}

// repTestSQLiteUpdate returns db with n pages rewritten (sparse update).
func repTestSQLiteUpdate(db []byte, ps int, pages []int, seed int64) []byte {
	out := append([]byte(nil), db...)
	r := rand.New(rand.NewSource(seed))
	for _, p := range pages {
		pg := out[p*ps : (p+1)*ps]
		r.Read(pg[16:])
		pg[0] = 0x0d
	}
	return out
}

func repCIDv1(block []byte) []byte {
	d := sha256.Sum256(block)
	return append([]byte{0x01, 0x55, 0x12, 0x20}, d[:]...)
}

// repTestCAR builds a CARv1 with the given blocks in order (first block's CID is the root).
func repTestCAR(blocks [][]byte) []byte {
	root := repCIDv1(blocks[0])
	hdr := []byte{0xa2, 0x65, 'r', 'o', 'o', 't', 's', 0x81, 0xd8, 0x2a, 0x58, byte(len(root) + 1), 0x00}
	hdr = append(hdr, root...)
	hdr = append(hdr, 0x67, 'v', 'e', 'r', 's', 'i', 'o', 'n', 0x01)
	out := binary.AppendUvarint(nil, uint64(len(hdr)))
	out = append(out, hdr...)
	for _, b := range blocks {
		cid := repCIDv1(b)
		out = binary.AppendUvarint(out, uint64(len(cid)+len(b)))
		out = append(out, cid...)
		out = append(out, b...)
	}
	return out
}

// repTestCARv2 wraps a CARv1 payload as CARv2 with a derivable MultihashIndexSorted index.
func repTestCARv2(v1 []byte, blocks [][]byte) []byte {
	var recs []carIndexRec
	off := uint64(0)
	hl, n := binary.Uvarint(v1)
	off = uint64(n) + hl
	for _, b := range blocks {
		cid := repCIDv1(b)
		recs = append(recs, carIndexRec{mh: carMhSHA2_256, digest: cid[4:], offset: off})
		sl := uint64(len(cid) + len(b))
		off += uint64(len(binary.AppendUvarint(nil, sl))) + sl
	}
	idx := buildCARIndex(carIdxMultihash, recs)
	hdr := carDeriveHeader(make([]byte, 16), 0, int64(len(v1)), 0, true)
	out := append(append([]byte(nil), carV2Pragma...), hdr[:]...)
	out = append(out, v1...)
	return append(out, idx...)
}

func repTestBlocks(n, size int, seed int64) [][]byte {
	r := rand.New(rand.NewSource(seed))
	bs := make([][]byte, n)
	for i := range bs {
		bs[i] = make([]byte, size/2+r.Intn(size))
		r.Read(bs[i])
	}
	return bs
}

// repTestMerkle builds a snapshot of a 16-ary trie over the keys with the named
// native hash tag (1 sha3-256, 3 sha256). Values are derived from (key, version).
type repMerkleBuilder struct {
	hash  func([]byte) []byte
	nodes [][]byte
	ids   map[string][]byte
}

func (mb *repMerkleBuilder) leaf(keys []uint64, val func(uint64) []byte) string {
	p := []byte{0x01, 0x00}
	p = binary.AppendUvarint(p, uint64(len(keys)))
	for _, k := range keys {
		v := val(k)
		p = binary.BigEndian.AppendUint64(p, k)
		p = binary.AppendUvarint(p, uint64(len(v)))
		p = append(p, v...)
	}
	return mb.add(p)
}

func (mb *repMerkleBuilder) add(p []byte) string {
	id := string(mb.hash(p))
	if _, ok := mb.ids[id]; !ok {
		mb.ids[id] = p
	}
	return id
}

func (mb *repMerkleBuilder) build(keys []uint64, shift uint, val func(uint64) []byte) string {
	if len(keys) <= 8 || shift == 0 {
		return mb.leaf(keys, val)
	}
	shift -= 4
	var kids [16][]uint64
	for _, k := range keys {
		kids[(k>>shift)&0xf] = append(kids[(k>>shift)&0xf], k)
	}
	p := []byte{0x02, 0x00}
	var bm uint16
	var body []byte
	for i := range kids {
		if len(kids[i]) == 0 {
			continue
		}
		bm |= 1 << uint(i)
		id := mb.build(kids[i], shift, val)
		body = binary.AppendUvarint(body, uint64(len(id)))
		body = append(body, id...)
	}
	p = binary.BigEndian.AppendUint16(p, bm)
	return mb.add(append(p, body...))
}

// repTestMerkleSnapshot returns the exact snapshot archive for the key set.
func repTestMerkleSnapshot(hashTag byte, keys []uint64, version map[uint64]int) []byte {
	mb := &repMerkleBuilder{ids: map[string][]byte{}, hash: merkleHashes[hashTag].sum}
	val := func(k uint64) []byte {
		return []byte(fmt.Sprintf("value-%d-v%d-%s", k, version[k], strings.Repeat("x", int(k%37))))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	root := mb.build(keys, 64, val)
	var out bytes.Buffer
	st := repTestMStore{mb}
	if err := merkleSnapshot(st, root, &out); err != nil {
		panic(err)
	}
	return out.Bytes()
}

type repTestMStore struct{ mb *repMerkleBuilder }

func (s repTestMStore) get(id string) ([]byte, []string, error) {
	p, ok := s.mb.ids[id]
	if !ok {
		return nil, nil, errRepMissing
	}
	kids, err := merkleKids(p)
	return p, kids, err
}

func repTestTar(t testing.TB, entries map[string][]byte, order []string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range order {
		data := entries[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func repTestKeys(n int, seed int64) []uint64 {
	r := rand.New(rand.NewSource(seed))
	seen := map[uint64]bool{}
	var ks []uint64
	for len(ks) < n {
		k := r.Uint64()
		if !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	return ks
}

// ---- harness helpers -----------------------------------------------------------

func repOpenStore(t *testing.T, mode repMode) *Store {
	t.Helper()
	s := newTestStore(t)
	s.setRepMode(mode)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	return s
}

// repReadAll reads [start,end] through the production read-path selection.
func repReadAll(t testing.TB, s *Store, man manifestV1, rng byteRange) []byte {
	t.Helper()
	return repCollectReader(t, s.openObjectReader(man, rng))
}

func repCollectReader(t testing.TB, rd objectReader) []byte {
	t.Helper()
	defer rd.close()
	var out []byte
	for {
		b, err := rd.next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		out = append(out, b...)
	}
}

func repManifestOf(t testing.TB, s *Store, bucket, key string) (*objectEntry, manifestV1) {
	t.Helper()
	e, man, err := s.HeadObject(bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	return e, man
}

func repMustRoot(t testing.TB, s *Store, uuid string) *repRoot {
	t.Helper()
	r, err := s.loadRepRoot(uuid)
	if err != nil {
		t.Fatalf("root %s: %v", uuid, err)
	}
	return r
}

func repHasRoot(s *Store, uuid string) bool {
	_, err := os.Stat(s.repRootPath(uuid))
	return err == nil
}

func repVerifyOK(t testing.TB, s *Store, deep bool) VerifyResult {
	t.Helper()
	res, err := s.Verify(deep)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("verify (deep=%v) not OK: %+v", deep, res.Issues)
	}
	return res
}

// ---- smoke ---------------------------------------------------------------------

func TestRepSmokeSQLiteRoundTrip(t *testing.T) {
	s := repOpenStore(t, repModePrefer)
	db := repTestSQLite(300, 4096, 1)
	if _, err := s.PutObject("b", "db", db, "application/x-sqlite3", nil); err != nil {
		t.Fatal(err)
	}
	e, man := repManifestOf(t, s, "b", "db")
	if !repHasRoot(s, e.manifestUUID) {
		t.Fatal("no representation root published")
	}
	if s.format.StoreFormatVersion != storeFormatVersionRepresentations {
		t.Fatalf("format %d", s.format.StoreFormatVersion)
	}
	got := repReadAll(t, s, man, byteRange{0, int64(len(db)) - 1})
	if !bytes.Equal(got, db) {
		t.Fatal("whole read differs")
	}
	if s.rep.reads.Load() != 0 {
		t.Fatal("whole GET should use authoritative CDC")
	}
	// The whole decoder remains exact and independently available to verification.
	rd, err := s.repOpenReader(man, byteRange{0, int64(len(db)) - 1})
	if err != nil || rd == nil {
		t.Fatalf("direct decoder: %v", err)
	}
	if got := repCollectReader(t, rd); !bytes.Equal(got, db) || s.rep.reads.Load() != 1 {
		t.Fatal("direct representation decoder differs or was not used")
	}
	part := repReadAll(t, s, man, byteRange{5000, 9999})
	if !bytes.Equal(part, db[5000:10000]) || s.rep.rangeReads.Load() != 1 {
		t.Fatalf("range read differs or not structural (%d)", s.rep.rangeReads.Load())
	}
	res := repVerifyOK(t, s, true)
	if res.RepresentationsVerified != 1 {
		t.Fatalf("verified %d", res.RepresentationsVerified)
	}
}

type repFixture struct {
	name string
	data []byte
	kind repKind
	// derived is the number of DERIVED members the top representation must have.
	derived int
}

func repFixtures(t testing.TB) []repFixture {
	db1 := repTestSQLite(200, 4096, 11)
	db2 := repTestSQLite(120, 8192, 12)
	blocks := repTestBlocks(300, 3000, 21)
	car1 := repTestCAR(blocks)
	car2 := repTestCARv2(repTestCAR(blocks), blocks)
	big := repTestBlocks(3, 700<<10, 22) // blocks above the direct-component size
	car3 := repTestCAR(big)
	keys := repTestKeys(3000, 31)
	mk3 := repTestMerkleSnapshot(3, append([]uint64(nil), keys...), nil)
	mk1 := repTestMerkleSnapshot(1, append([]uint64(nil), keys...), nil)
	rnd := make([]byte, 600<<10)
	rand.New(rand.NewSource(41)).Read(rnd)
	tarMixed := repTestTar(t, map[string][]byte{"a.db": db1, "b.car": car1, "c.bin": rnd, "d.txt": []byte("hello tar"), "e.snap": mk1, "empty": nil},
		[]string{"a.db", "b.car", "c.bin", "d.txt", "e.snap", "empty"})
	tarFake := repTestTar(t, map[string][]byte{"x.db": append([]byte("SQLite format 3\x00"), make([]byte, 5000)...), "y": rnd[:3000]}, []string{"x.db", "y"})
	return []repFixture{
		{"sqlite-4k", db1, repKindSQLite, 0},
		{"sqlite-8k", db2, repKindSQLite, 0},
		{"car1", car1, repKindCAR, 0},
		{"car2", car2, repKindCAR, 0},
		{"car-large-blocks", car3, repKindCAR, 0},
		{"merkle-sha256", mk3, repKindMerkle, 0},
		{"merkle-sha3", mk1, repKindMerkle, 0},
		{"tar-mixed", tarMixed, repKindConcat, 3},
		{"tar-fake-sqlite", tarFake, repKindConcat, 0},
	}
}

func TestRepKindMatrixRoundTrip(t *testing.T) {
	for _, mode := range []repMode{repModeShadow, repModePrefer} {
		t.Run(mode.String(), func(t *testing.T) {
			s := repOpenStore(t, mode)
			for _, f := range repFixtures(t) {
				if _, err := s.PutObject("b", f.name, f.data, "application/octet-stream", nil); err != nil {
					t.Fatalf("%s: %v", f.name, err)
				}
				e, man := repManifestOf(t, s, "b", f.name)
				if !repHasRoot(s, e.manifestUUID) {
					t.Fatalf("%s: no root", f.name)
				}
				root := repMustRoot(t, s, e.manifestUUID)
				src := s.newRepSource(root.Descs, nil)
				env := &repEnv{Src: src, Limits: repProdLimits}
				rep, err := env.loadRep(root.Rep)
				if err != nil {
					t.Fatal(err)
				}
				if rep.Kind != f.kind {
					t.Fatalf("%s: kind %d, want %d", f.name, rep.Kind, f.kind)
				}
				nd := 0
				for _, m := range rep.Members {
					if m.Role == repDerived {
						nd++
					}
				}
				if nd != f.derived {
					t.Fatalf("%s: %d derived members, want %d", f.name, nd, f.derived)
				}
				var out bytes.Buffer
				n, sum, err := env.reconstruct(root.Rep, &out)
				src.close()
				if err != nil || n != int64(len(f.data)) || sum != sha256.Sum256(f.data) || !bytes.Equal(out.Bytes(), f.data) {
					t.Fatalf("%s: reconstruct: n=%d err=%v", f.name, n, err)
				}
				if got := repReadAll(t, s, man, byteRange{0, int64(len(f.data)) - 1}); !bytes.Equal(got, f.data) {
					t.Fatalf("%s: read differs", f.name)
				}
				for _, r := range []byteRange{{0, 99}, {4096, 4096 + 8191}, {int64(len(f.data)) - 700, int64(len(f.data)) - 1}, {1000, 1000 + 300<<10}} {
					if r.end >= int64(len(f.data)) {
						continue
					}
					if got := repReadAll(t, s, man, r); !bytes.Equal(got, f.data[r.start:r.end+1]) {
						t.Fatalf("%s: range %v differs", f.name, r)
					}
				}
			}
			res := repVerifyOK(t, s, true)
			if res.RepresentationsVerified != len(repFixtures(t)) {
				t.Fatalf("verified %d", res.RepresentationsVerified)
			}
		})
	}
}

// ---- generic substrate: in-memory source --------------------------------------

type repMem struct {
	mu    sync.Mutex
	objs  map[[32]byte][]byte
	reads int
	seen  map[[32]byte]bool
}

func newRepMem() *repMem { return &repMem{objs: map[[32]byte][]byte{}, seen: map[[32]byte]bool{}} }

func (m *repMem) put(b []byte) [32]byte {
	id := sha256.Sum256(b)
	m.mu.Lock()
	m.objs[id] = append([]byte(nil), b...)
	m.mu.Unlock()
	return id
}

func (m *repMem) Size(id [32]byte) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[id]
	if !ok {
		return 0, fmt.Errorf("%w: %x", errRepMissing, id[:6])
	}
	return int64(len(b)), nil
}

func (m *repMem) ReadAt(id [32]byte, p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[id]
	if !ok {
		return 0, fmt.Errorf("%w: %x", errRepMissing, id[:6])
	}
	if off < 0 || off > int64(len(b)) {
		return 0, fmt.Errorf("offset %d", off)
	}
	m.reads++
	m.seen[id] = true
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *repMem) putRep(r *repObject) [32]byte {
	raw, err := r.marshal()
	if err != nil {
		panic(err)
	}
	return m.put(raw)
}

// concatOf stores the parts as components and returns the Concat representation.
func (m *repMem) concatOf(parts ...[]byte) (id [32]byte, data []byte) {
	r := &repObject{Kind: repKindConcat}
	for _, p := range parts {
		sum := m.put(p)
		r.Members = append(r.Members, repMember{Role: repStored, SHA: sum, Size: int64(len(p))})
		data = append(data, p...)
	}
	r.Size = int64(len(data))
	return m.putRep(r), data
}

// wrap returns a Concat representation whose only member is the DERIVED child.
func (m *repMem) wrap(child [32]byte, data []byte, prefix []byte, zeros int) (id [32]byte, out []byte) {
	out = append(append(append([]byte(nil), prefix...), data...), make([]byte, zeros)...)
	r := &repObject{Kind: repKindConcat, Size: int64(len(out)), Members: []repMember{{
		Role: repDerived, SHA: sha256.Sum256(out), Size: int64(len(out)), Child: child, Frame: repFrame{Prefix: prefix, Zeros: zeros}}}}
	return m.putRep(r), out
}

// chain builds a nesting of d representations (the outermost is depth 1).
func (m *repMem) chain(d int, seed string) (id [32]byte, data []byte) {
	id, data = m.concatOf([]byte("leaf-"+seed), bytes.Repeat([]byte{7}, 100))
	for i := 1; i < d; i++ {
		id, data = m.wrap(id, data, []byte{byte(i)}, i%3)
	}
	return id, data
}

func (m *repMem) env(lim repLimits) *repEnv { return &repEnv{Src: m, Limits: lim} }

// ---- generic substrate: wire format ----------------------------------------------

func repSampleObject() *repObject {
	return &repObject{Kind: repKindSQLite, Size: 9000, Members: []repMember{
		{Role: repStored, SHA: sha256.Sum256([]byte("n0")), Size: 300},
		{Role: repDerived, SHA: sha256.Sum256([]byte("d")), Size: 5000, Child: sha256.Sum256([]byte("c")), Frame: repFrame{Prefix: []byte("hdr"), Zeros: 12}},
	}, Recipe: []byte("recipe"), Indexes: []repIndex{
		{KeyWidth: 8, Count: 3, Ref: [32]byte{1}, Owns: true},
		{KeyWidth: 32, Count: 1 << 20, Ref: [32]byte{2}},
	}}
}

func TestRepWireGoldenAndRoundTrip(t *testing.T) {
	// Production Representation Format v1 is frozen: this golden pins its bytes.
	r := &repObject{Kind: repKindConcat, Size: 7, Members: []repMember{
		{Role: repStored, SHA: [32]byte{0xaa}, Size: 4},
		{Role: repDerived, SHA: [32]byte{0xbb}, Size: 3, Child: [32]byte{0xcc}, Frame: repFrame{Prefix: []byte{1}, Zeros: 1}},
	}}
	raw, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	const golden = "5a533352010107" + "02" + "01aa00000000000000000000000000000000000000000000000000000000000000" + "04" +
		"03bb00000000000000000000000000000000000000000000000000000000000000" + "03" +
		"cc00000000000000000000000000000000000000000000000000000000000000" + "01" + "01" + "01" + "00" + "00"
	if got := hex.EncodeToString(raw); got != golden {
		t.Fatalf("wire changed:\n got %s\nwant %s", got, golden)
	}
	back, err := unmarshalRep(raw, repLimits{})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := back.marshal()
	if !bytes.Equal(again, raw) {
		t.Fatal("round trip is not canonical")
	}
	o := repSampleObject()
	raw, err = o.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:5]) != "ZS3R\x01" {
		t.Fatalf("magic/version %q", raw[:5])
	}
	b2, err := unmarshalRep(raw, repLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := b2.marshal(); !bytes.Equal(again, raw) {
		t.Fatal("sample round trip is not canonical")
	}
}

func TestRepWireKindNumbers(t *testing.T) {
	want := map[repKind]bool{1: true, 2: true, 3: true, 4: false, 5: true, 6: true, 7: true, 8: false, 0: false, 255: false}
	for k, ok := range want {
		if k.supported() != ok {
			t.Fatalf("kind %d supported=%v", k, k.supported())
		}
		raw := []byte("ZS3R\x01")
		raw = append(raw, byte(k), 0, 0, 0, 0)
		_, err := unmarshalRep(raw, repLimits{})
		if !ok && !errors.Is(err, errRepUnsupported) {
			t.Fatalf("kind %d: want unsupported, got %v", k, err)
		}
	}
	if repKindConcat != 1 || repKindCAR != 2 || repKindSQLite != 3 || repKindMerkle != 5 || repKindGzip != 6 || repKindZstd != 7 {
		t.Fatal("kind numbering moved")
	}
}

func TestRepWireCorruptionMatrix(t *testing.T) {
	valid, _ := repSampleObject().marshal()
	// every strict prefix is rejected, none panics
	for n := 0; n < len(valid); n++ {
		if _, err := unmarshalRep(valid[:n], repLimits{}); err == nil {
			t.Fatalf("prefix of %d bytes accepted", n)
		}
	}
	mut := func(name string, f func(b []byte) []byte, want error) {
		t.Helper()
		b := f(append([]byte(nil), valid...))
		_, err := unmarshalRep(b, repLimits{})
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if want != nil && !errors.Is(err, want) {
			t.Fatalf("%s: %v is not %v", name, err, want)
		}
	}
	mut("magic", func(b []byte) []byte { b[0] = 'X'; return b }, errRepCorrupt)
	mut("version", func(b []byte) []byte { b[4] = 2; return b }, errRepUnsupported)
	mut("kind", func(b []byte) []byte { b[5] = 9; return b }, errRepUnsupported)
	mut("reserved kind", func(b []byte) []byte { b[5] = 4; return b }, errRepUnsupported)
	mut("trailing", func(b []byte) []byte { return append(b, 0) }, errRepCorrupt)
	mut("role", func(b []byte) []byte { b[9] = 2; return b }, errRepCorrupt)
	mut("nonminimal size varint", func(b []byte) []byte {
		// size 9000 = 0xa846 as varint; re-encode with a padding continuation byte
		out := append([]byte(nil), b[:6]...)
		out = append(out, 0xc8, 0xc6, 0x80, 0x00)
		return append(out, b[8:]...)
	}, errRepCorrupt)
	// hostile counts never allocate beyond the bytes present
	hostile := []byte("ZS3R\x01\x01\x00\xff\xff\xff\xff\x07")
	if _, err := unmarshalRep(hostile, repLimits{}); err == nil {
		t.Fatal("hostile member count accepted")
	}
	if _, err := unmarshalRep(append([]byte("ZS3R\x01\x01\x00\xff\xff\xff\xff\x07"), make([]byte, 64)...), repLimits{MaxMembers: 10}); !errors.Is(err, errRepLimit) {
		t.Fatalf("member limit: %v", err)
	}
	// every single-bit flip either fails or parses to an object that re-marshals canonically
	for i := range valid {
		for bit := 0; bit < 8; bit++ {
			b := append([]byte(nil), valid...)
			b[i] ^= 1 << bit
			if o, err := unmarshalRep(b, repLimits{}); err == nil {
				if again, merr := o.marshal(); merr != nil || !bytes.Equal(again, b) {
					t.Fatalf("flip %d.%d parsed but is not canonical", i, bit)
				}
			}
		}
	}
	// structural validation shared with Marshal
	bad := []*repObject{
		{Kind: repKindConcat, Size: 5, Members: []repMember{{Role: repStored, SHA: [32]byte{1}, Size: 4}}},                                            // size mismatch
		{Kind: repKindConcat, Size: 4, Members: []repMember{{Role: repStored, SHA: [32]byte{1}, Size: 4}}, Recipe: []byte{1}},                         // recipe on concat
		{Kind: repKindCAR, Size: 1, Members: []repMember{{Role: repVia, SHA: [32]byte{1}, Size: 1}}},                                                  // VIA has no wire form
		{Kind: repKindCAR, Size: 1, Members: []repMember{{Role: repDerived, Size: 2, Frame: repFrame{Prefix: []byte{1, 2, 3}}}}},                      // frame exceeds
		{Kind: repKindCAR, Size: 1, Members: []repMember{{Role: repStored, SHA: [32]byte{1}, Size: 1}, {Role: repStored, SHA: [32]byte{1}, Size: 2}}}, // inconsistent lengths
		{Kind: repKindCAR, Size: 1, Indexes: []repIndex{{KeyWidth: 8, Count: 1, Ref: [32]byte{2}}, {KeyWidth: 8, Count: 1, Ref: [32]byte{1}}}},        // unordered sections
		{Kind: repKindCAR, Size: 1, Indexes: []repIndex{{KeyWidth: 0, Count: 1}}},                                                                     // geometry
		{Kind: repKindCAR, Size: -1},
	}
	for i, o := range bad {
		if _, err := o.marshal(); err == nil {
			t.Fatalf("invalid object %d marshalled", i)
		}
	}
}

// ---- generic substrate: reconstruction, depth, sharing ----------------------------------

func TestRepReconstructFraming(t *testing.T) {
	m := newRepMem()
	child, data := m.concatOf([]byte("hello "), []byte("world"))
	id, out := m.wrap(child, data, []byte("PRE"), 5)
	var buf bytes.Buffer
	n, sum, err := m.env(repLimits{}).reconstruct(id, &buf)
	if err != nil || n != int64(len(out)) || sum != sha256.Sum256(out) || !bytes.Equal(buf.Bytes(), out) {
		t.Fatalf("n=%d err=%v out=%q", n, err, buf.Bytes())
	}
}

func TestRepDepthConsistentAcrossSubsystems(t *testing.T) {
	m := newRepMem()
	for d := 1; d <= 6; d++ {
		id, data := m.chain(d, fmt.Sprint(d))
		lim := repLimits{MaxDepth: 99} // clamps to the hard maximum
		env := m.env(lim)
		var buf bytes.Buffer
		_, _, recErr := env.reconstruct(id, &buf)
		_, reachErr := env.reachable(id)
		_, catErr := rebuildRepCatalog(m, [][32]byte{id})
		okExpect := d <= repHardDepth
		for name, err := range map[string]error{"reconstruct": recErr, "reachable": reachErr, "catalog": catErr} {
			if okExpect && err != nil {
				t.Fatalf("depth %d %s: %v", d, name, err)
			}
			if !okExpect && !errors.Is(err, errRepDepth) {
				t.Fatalf("depth %d %s: want depth error, got %v", d, name, err)
			}
			if !okExpect && !errors.Is(err, errRepLimit) {
				t.Fatalf("depth error must be a limit error: %v", err)
			}
		}
		if okExpect && !bytes.Equal(buf.Bytes(), data) {
			t.Fatalf("depth %d reconstructed wrong bytes", d)
		}
	}
	// the default (3) rejects what the hard limit admits
	id, _ := m.chain(4, "x")
	if _, _, err := m.env(repLimits{}).reconstruct(id, io.Discard); !errors.Is(err, errRepDepth) {
		t.Fatalf("default depth 3 accepted a depth-4 graph: %v", err)
	}
}

func TestRepCyclesTerminate(t *testing.T) {
	// A hash cycle cannot be built honestly; a source that lies about digests can.
	mkSelf := func(self [32]byte) []byte {
		r := &repObject{Kind: repKindConcat, Size: 1, Members: []repMember{{Role: repDerived, SHA: [32]byte{1}, Size: 1, Child: self}}}
		raw, _ := r.marshal()
		return raw
	}
	a, b, c := [32]byte{0xa}, [32]byte{0xb}, [32]byte{0xc}
	cases := map[string]map[[32]byte][]byte{
		"self":  {a: mkSelf(a)},
		"two":   {a: mkSelf(b), b: mkSelf(a)},
		"three": {a: mkSelf(b), b: mkSelf(c), c: mkSelf(a)},
	}
	for name, objs := range cases {
		m := newRepMem()
		m.objs = objs
		env := &repEnv{Src: m, Limits: repLimits{MaxDepth: 4}, noVerify: true}
		if _, _, err := env.reconstruct(a, io.Discard); !errors.Is(err, errRepCycle) || !errors.Is(err, errRepCorrupt) {
			t.Fatalf("%s reconstruct: %v", name, err)
		}
		if _, err := env.reachable(a); !errors.Is(err, errRepCycle) {
			t.Fatalf("%s reachable: %v", name, err)
		}
		cat := newRepCatalog(m)
		cat.env.noVerify = true
		if err := cat.ingest(a); !errors.Is(err, errRepCycle) {
			t.Fatalf("%s catalog: %v", name, err)
		}
	}
}

// TestRepSharedChildHeightIndependentOfRootOrder is the Z3-28 regression: a
// shared node first met on a shallow path must not hide a later deeper path.
func TestRepSharedChildHeightIndependentOfRootOrder(t *testing.T) {
	m := newRepMem()
	s, sData := m.chain(2, "shared") // S: height 2
	shallow := s                     // root 1 uses S at depth 1
	deep, data := s, sData
	for i := 0; i < 3; i++ { // root 2 reaches S at depth 4 => depth 5 in total
		deep, data = m.wrap(deep, data, []byte{byte(i + 1)}, 0)
	}
	// (the loop wraps S three times: S sits at depth 4 with height 2 => 5)
	env := m.env(repLimits{MaxDepth: 4})
	_, e1 := env.reachableAll([][32]byte{shallow, deep})
	_, e2 := env.reachableAll([][32]byte{deep, shallow})
	if !errors.Is(e1, errRepDepth) || !errors.Is(e2, errRepDepth) {
		t.Fatalf("order dependent liveness: shallow-first %v, deep-first %v", e1, e2)
	}
	if _, _, err := env.reconstruct(deep, io.Discard); !errors.Is(err, errRepDepth) {
		t.Fatalf("reconstruct accepts what reachability rejects: %v", err)
	}
	// the catalog agrees
	cat := newRepCatalog(m)
	if err := cat.ingest(shallow); err != nil {
		t.Fatal(err)
	}
	if err := cat.ingest(deep); !errors.Is(err, errRepDepth) {
		t.Fatalf("catalog accepts a root reconstruction rejects: %v", err)
	}
	_ = data
}

func TestRepSharedChildStoredOnceAndLiveness(t *testing.T) {
	m := newRepMem()
	child, cdata := m.concatOf([]byte("shared-child-payload"), bytes.Repeat([]byte{9}, 500))
	p1, _ := m.wrap(child, cdata, []byte("A"), 1)
	p2, _ := m.wrap(child, cdata, []byte("BB"), 0)
	third, _ := m.wrap(child, cdata, nil, 7)
	env := m.env(repLimits{})
	r1, _ := env.reachable(p1)
	r2, _ := env.reachable(p2)
	all, err := env.reachableAll([][32]byte{p1, p2, third})
	if err != nil {
		t.Fatal(err)
	}
	// union of two == |r1| + |r2| - shared; the child rep and its components are counted once
	shared := 0
	for id := range r1.Role {
		if _, ok := r2.Role[id]; ok {
			shared++
		}
	}
	if shared < 3 { // child rep + two components
		t.Fatalf("expected shared child rep and components, shared=%d", shared)
	}
	two, _ := env.reachableAll([][32]byte{p1, p2})
	if len(two.Role) != len(r1.Role)+len(r2.Role)-shared {
		t.Fatalf("union %d != %d+%d-%d", len(two.Role), len(r1.Role), len(r2.Role), shared)
	}
	// removing one root frees exactly Reach(p1) \ Reach(rest); the shared child stays
	rest, _ := env.reachableAll([][32]byte{p2, third})
	freed := 0
	for id := range all.Role {
		if _, ok := rest.Role[id]; !ok {
			freed++
		}
	}
	if freed != 1 { // only p1 itself
		t.Fatalf("freed %d objects removing one parent", freed)
	}
	if _, ok := rest.Role[child]; !ok {
		t.Fatal("shared child dropped while still referenced")
	}
}

func TestRepWriterFailureIsNotCorruption(t *testing.T) {
	m := newRepMem()
	id, data := m.concatOf(bytes.Repeat([]byte("x"), 200000), bytes.Repeat([]byte("y"), 200000))
	wid, _ := m.wrap(id, data, []byte("p"), 3)
	boom := errors.New("destination closed")
	for _, target := range []int{1, 2, 3} {
		n := 0
		w := writerFunc(func(p []byte) (int, error) {
			n++
			if n == target {
				return 0, boom
			}
			return len(p), nil
		})
		_, _, err := m.env(repLimits{}).reconstruct(wid, w)
		if !errors.Is(err, boom) {
			t.Fatalf("write %d: writer error lost: %v", target, err)
		}
		if errors.Is(err, errRepCorrupt) || errors.Is(err, errRepMismatch) {
			t.Fatalf("destination failure reclassified as corruption: %v", err)
		}
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestRepChildCausePreserved(t *testing.T) {
	for _, damage := range []string{"missing", "mismatch", "depth"} {
		m := newRepMem()
		id, data := m.chain(4, damage)
		// find the innermost component and damage it
		var leaf [32]byte
		for k, v := range m.objs {
			if bytes.HasPrefix(v, []byte("leaf-")) {
				leaf = k
			}
		}
		env := m.env(repLimits{MaxDepth: 4})
		want := errRepMissing
		switch damage {
		case "missing":
			delete(m.objs, leaf)
		case "mismatch":
			m.objs[leaf] = append([]byte(nil), m.objs[leaf]...)
			m.objs[leaf][0] ^= 1
			want = errRepMismatch
		case "depth":
			id, data = m.wrap(id, data, nil, 0)
			want = errRepDepth
		}
		_, _, err := env.reconstruct(id, io.Discard)
		if !errors.Is(err, want) {
			t.Fatalf("%s: nested failure lost its class: %v", damage, err)
		}
	}
}

// ---- generic substrate: catalog ----------------------------------------------------

func (m *repMem) merkleRepWithSection(width int, pairs map[string][32]byte, rootSeed string) [32]byte {
	recs, n, err := repBuildIndex(width, pairs)
	if err != nil {
		panic(err)
	}
	ref := m.put(recs)
	rootNode := m.put([]byte("root-node-" + rootSeed))
	key := make([]byte, width)
	rep := &repObject{Kind: repKindMerkle, Size: 1,
		Members: []repMember{{Role: repStored, SHA: rootNode, Size: int64(len("root-node-" + rootSeed))}},
		Recipe:  append(append([]byte{1}, byte(width)), key...),
		Indexes: []repIndex{{KeyWidth: width, Count: n, Ref: ref, Owns: true}}}
	return m.putRep(rep)
}

func TestRepCatalogAmbiguityAndOrderIndependence(t *testing.T) {
	m := newRepMem()
	key := string(bytes.Repeat([]byte{5}, 8))
	k2 := string(bytes.Repeat([]byte{6}, 8))
	a, b := sha256.Sum256([]byte("A")), sha256.Sum256([]byte("B"))
	m.put([]byte("A"))
	m.put([]byte("B"))
	r1 := m.merkleRepWithSection(8, map[string][32]byte{key: a, k2: a}, "1")
	r2 := m.merkleRepWithSection(8, map[string][32]byte{key: b}, "2")
	dom := repMerkleDomain(1, 8)
	c12, err := rebuildRepCatalog(m, [][32]byte{r1, r2})
	if err != nil {
		t.Fatal(err)
	}
	c21, err := rebuildRepCatalog(m, [][32]byte{r2, r1})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*repCatalog{c12, c21} {
		if _, _, err := c.resolve(dom, []byte(key)); !errors.Is(err, errRepAmbiguous) {
			t.Fatalf("same native key, different component must be ambiguous: %v", err)
		}
		if got, ok, err := c.resolve(dom, []byte(k2)); err != nil || !ok || got != a {
			t.Fatalf("unambiguous key: %v %v %v", got, ok, err)
		}
	}
	if fmt.Sprint(c12.mappings()) != fmt.Sprint(c21.mappings()) {
		t.Fatal("rebuild depends on root order")
	}
	// incremental == rebuilt; removing the conflicting root clears the ambiguity
	inc := newRepCatalog(m)
	inc.ingest(r1)
	inc.ingest(r2)
	if fmt.Sprint(inc.mappings()) != fmt.Sprint(c12.mappings()) {
		t.Fatal("incremental catalog differs from rebuilt")
	}
	inc.remove(r1)
	if got, ok, err := inc.resolve(dom, []byte(key)); err != nil || !ok || got != b {
		t.Fatalf("after removing r1: %v %v %v", got, ok, err)
	}
	if _, ok, _ := inc.resolve(dom, []byte(k2)); ok {
		t.Fatal("mapping outlived its last owner")
	}
	// a stale or discarded catalog never decides liveness: reachability reads the sections
	reach, err := m.env(repLimits{}).reachableAll([][32]byte{r1, r2})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reach.Role[a]; !ok || reach.Role[a] != repDepOwned {
		t.Fatalf("owned target not live: %v", reach.Role[a])
	}
	// a corrupt section fails the ingest and leaves the catalog unchanged
	for id, v := range m.objs {
		if len(v) == 80 { // r1's two-record section
			m.objs[id] = append([]byte(nil), v...)
			m.objs[id][3] ^= 1
		}
	}
	bad := newRepCatalog(m)
	if err := bad.ingest(r1); err == nil {
		t.Fatal("corrupt section accepted")
	}
	if bad.entries() != 0 || bad.nSections() != 0 {
		t.Fatal("failed ingest changed the catalog")
	}
}

// ---- generic substrate: descriptor and root --------------------------------------------

func repTestDesc(refs ...uint32) *repDesc {
	d := &repDesc{}
	for i, l := range refs {
		d.Refs = append(d.Refs, repChunkRef{SHA: sha256.Sum256([]byte{byte(i), byte(l)}), Len: l})
		d.Size += int64(l)
	}
	return d
}

func repTestRoot() *repRoot {
	return &repRoot{ManifestUUID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f", ObjSHA: [32]byte{1}, ObjSize: 1 << 20, Rep: [32]byte{2},
		Descs: map[[32]byte]*repDesc{
			{9}: repTestDesc(repDirectMax, 5000),
			{3}: repTestDesc(100000, 100000, 100000),
		}}
}

func repReseal(body []byte) []byte {
	return binary.LittleEndian.AppendUint32(append([]byte(nil), body...), crc32.Checksum(body, castagnoliTable))
}

func TestRepRootFormatAndCorruptionMatrix(t *testing.T) {
	r := repTestRoot()
	raw, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:5]) != "ZS3T\x01" {
		t.Fatalf("root magic/version %q", raw[:5])
	}
	back, err := unmarshalRepRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := back.marshal(); !bytes.Equal(again, raw) {
		t.Fatal("root is not canonical")
	}
	if len(back.Descs) != 2 || back.Descs[[32]byte{3}].Size != 300000 {
		t.Fatalf("descriptors lost: %+v", back.Descs)
	}
	// every strict prefix and every bit flip is rejected (CRC32C + structure)
	for n := 0; n < len(raw); n++ {
		if _, err := unmarshalRepRoot(raw[:n]); err == nil {
			t.Fatalf("prefix %d accepted", n)
		}
	}
	for i := range raw {
		b := append([]byte(nil), raw...)
		b[i] ^= 0x10
		if _, err := unmarshalRepRoot(b); err == nil {
			t.Fatalf("flip at %d accepted", i)
		}
	}
	body := raw[:len(raw)-4]
	if _, err := unmarshalRepRoot(repReseal(append(append([]byte(nil), body...), 0))); err == nil {
		t.Fatal("trailing byte accepted")
	}
	mutate := func(name string, f func(b []byte) []byte, want error) {
		t.Helper()
		_, err := unmarshalRepRoot(repReseal(f(append([]byte(nil), body...))))
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		if want != nil && !errors.Is(err, want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	mutate("magic", func(b []byte) []byte { b[0] = 'Y'; return b }, errRepCorrupt)
	mutate("version", func(b []byte) []byte { b[4] = 2; return b }, errRepUnsupported)
	mutate("hostile descriptor count", func(b []byte) []byte {
		uuidEnd := 5 + 1 + len(r.ManifestUUID) + 32
		_, n := binary.Uvarint(b[uuidEnd:])
		cnt := uuidEnd + n + 32
		return append(append(b[:cnt:cnt], 0xff, 0xff, 0xff, 0xff, 0x0f), b[cnt+1:]...)
	}, errRepCorrupt)
	// descriptor-level structure, sealed with a valid checksum
	badRoots := map[string]*repRoot{}
	mk := func(name string, d *repDesc) {
		badRoots[name] = &repRoot{ManifestUUID: "u", Descs: map[[32]byte]*repDesc{{1}: d}}
	}
	mk("sum mismatch", &repDesc{Size: repDirectMax + 10, Refs: []repChunkRef{{Len: repDirectMax}, {Len: 11}}})
	mk("direct-sized component", &repDesc{Size: 1000, Refs: []repChunkRef{{Len: 500}, {Len: 500}}})
	mk("oversize chunk", &repDesc{Size: 2*repDirectMax + 2, Refs: []repChunkRef{{Len: repDirectMax + 1}, {Len: repDirectMax + 1}}})
	mk("zero chunk", &repDesc{Size: repDirectMax + 1, Refs: []repChunkRef{{Len: repDirectMax + 1}, {Len: 0}}})
	mk("single ref", &repDesc{Size: repDirectMax + 1, Refs: []repChunkRef{{Len: repDirectMax + 1}}})
	for name, br := range badRoots {
		if _, err := br.marshal(); err == nil {
			t.Fatalf("%s: invalid descriptor marshalled", name)
		}
	}
	// strictly ascending, no duplicates: hand-build an unordered body
	two := repTestRoot()
	rawTwo, _ := two.marshal()
	bodyTwo := rawTwo[:len(rawTwo)-4]
	i3 := bytes.Index(bodyTwo, append([]byte{3}, make([]byte, 31)...))
	i9 := bytes.Index(bodyTwo, append([]byte{9}, make([]byte, 31)...))
	if i3 < 0 || i9 < 0 || i3 > i9 {
		t.Fatal("test setup: descriptor order")
	}
	swapped := append([]byte(nil), bodyTwo...)
	swapped[i3], swapped[i9] = 9, 3
	swapped[i3+0] = 9
	swapped[i9+0] = 3
	if _, err := unmarshalRepRoot(repReseal(swapped)); err == nil {
		// both keys swapped in place keeps them ascending-descending: now 9 then 3
		t.Fatal("unordered descriptors accepted")
	}
	dup := append([]byte(nil), bodyTwo...)
	dup[i9] = 3
	if _, err := unmarshalRepRoot(repReseal(dup)); err == nil {
		t.Fatal("duplicate descriptor accepted")
	}
	// the manifest identity is bounded
	if _, err := (&repRoot{ManifestUUID: strings.Repeat("u", 65)}).marshal(); err == nil {
		t.Fatal("oversized manifest identity marshalled")
	}
}

func TestRepRootNamesAndTemp(t *testing.T) {
	for name, want := range map[string]bool{
		"018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f.root": true,
		".root":                  false,
		"x.root.tmp":             false,
		"rep-123.tmp":            false,
		".hidden.root":           false,
		"a/b.root":               false,
		"018f4d2e-6b1a.root.bak": false,
	} {
		if _, ok := validRepRootName(name); ok != want {
			t.Fatalf("%q: root=%v want %v", name, ok, want)
		}
	}
}

func FuzzRepUnmarshal(f *testing.F) {
	valid, _ := repSampleObject().marshal()
	f.Add(valid)
	f.Add([]byte("ZS3R\x01\x01\x00\x00\x00\x00"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		o, err := unmarshalRep(b, repLimits{MaxMembers: 1 << 12})
		if err != nil {
			return
		}
		again, merr := o.marshal()
		if merr != nil {
			t.Fatalf("parsed object does not marshal: %v", merr)
		}
		if !bytes.Equal(again, b) {
			t.Fatal("accepted a non-canonical encoding")
		}
	})
}

func FuzzRepRootUnmarshal(f *testing.F) {
	raw, _ := repTestRoot().marshal()
	f.Add(raw)
	f.Add(repReseal([]byte("ZS3T\x01")))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := unmarshalRepRoot(b)
		if err != nil {
			return
		}
		again, merr := r.marshal()
		if merr != nil || !bytes.Equal(again, b) {
			t.Fatalf("accepted a non-canonical root: %v", merr)
		}
	})
}

// ---- lifecycle ---------------------------------------------------------------------

// repLab is an on-disk store that is closed for offline maintenance and reopened.
type repLab struct {
	t    testing.TB
	dir  string
	s    *Store
	mode repMode
	want map[string][]byte // key -> expected current bytes
}

func newRepLab(t testing.TB, mode repMode) *repLab {
	t.Helper()
	l := &repLab{t: t, dir: t.TempDir(), mode: mode, want: map[string][]byte{}}
	l.open()
	if err := l.s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.close() })
	return l
}

func (l *repLab) open() {
	l.t.Helper()
	s, err := OpenStore(l.dir)
	if err != nil {
		l.t.Fatal(err)
	}
	s.setRepMode(l.mode)
	l.s = s
}

func (l *repLab) close() {
	if l.s != nil {
		l.s.Close()
		l.s = nil
	}
}

func (l *repLab) put(key string, data []byte) {
	l.t.Helper()
	if _, err := l.s.PutObject("b", key, data, "application/octet-stream", nil); err != nil {
		l.t.Fatal(err)
	}
	l.want[key] = data
}

// offline closes the store, runs fn on the directory (exclusive maintenance) and reopens.
func (l *repLab) offline(fn func()) {
	l.t.Helper()
	l.close()
	fn()
	l.open()
}

func (l *repLab) manifest(key string) (*objectEntry, manifestV1) {
	return repManifestOf(l.t, l.s, "b", key)
}

// exact checks, for every live key, the CDC path, the representation path and the
// deep verify, and returns the verify result.
func (l *repLab) exact(stage string) VerifyResult {
	l.t.Helper()
	for key, want := range l.want {
		_, man := l.manifest(key)
		var cdc []byte
		rd := l.s.newManifestReader(man, byteRange{0, int64(len(want)) - 1})
		for {
			b, err := rd.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				l.t.Fatalf("%s: %s CDC: %v", stage, key, err)
			}
			cdc = append(cdc, b...)
		}
		if !bytes.Equal(cdc, want) {
			l.t.Fatalf("%s: %s CDC differs", stage, key)
		}
		if int64(len(want)) != man.TotalLength {
			l.t.Fatalf("%s: %s stored %d bytes, expected %d", stage, key, man.TotalLength, len(want))
		}
		if got := repReadAll(l.t, l.s, man, byteRange{0, int64(len(want)) - 1}); !bytes.Equal(got, want) {
			l.t.Fatalf("%s: %s representation read differs", stage, key)
		}
		if l.s.repMode() == repModePrefer && repHasRoot(l.s, man.ManifestUUID) {
			rd, err := l.s.repOpenReader(man, byteRange{0, man.TotalLength - 1})
			if err != nil || rd == nil {
				l.t.Fatalf("%s: %s direct decoder: %v", stage, key, err)
			}
			if got := repCollectReader(l.t, rd); !bytes.Equal(got, want) {
				l.t.Fatalf("%s: %s direct representation differs", stage, key)
			}
		}
		if len(want) > 12400 {
			end := min(12345+9999, int64(len(want))-1)
			if got := repReadAll(l.t, l.s, man, byteRange{12345, end}); !bytes.Equal(got, want[12345:end+1]) {
				l.t.Fatalf("%s: %s range differs", stage, key)
			}
		}
	}
	res, err := l.s.Verify(true)
	if err != nil {
		l.t.Fatal(err)
	}
	if !res.OK() {
		l.t.Fatalf("%s: verify: %+v", stage, res.Issues)
	}
	return res
}

func (l *repLab) reach() reachabilityResult {
	l.t.Helper()
	rr, err := l.s.computeReachability(false)
	if err != nil {
		l.t.Fatal(err)
	}
	if !rr.OK() {
		l.t.Fatalf("reachability: %+v", rr.Issues)
	}
	return rr
}

func (l *repLab) packIDs() []packInfo { return l.s.packSnap().packs }

func repLooseSet(t testing.TB, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	filepath.WalkDir(filepath.Join(dir, "chunks"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out[d.Name()] = true
		}
		return nil
	})
	return out
}

// repPackedLiveSet is every chunk digest some pack holds as a live primary record.
func repPackedSet(s *Store) map[string]bool {
	out := map[string]bool{}
	for sum := range s.packSnap().primaries() {
		out[hex.EncodeToString(sum[:])] = true
	}
	return out
}

func repSmallCompact() compactOptions {
	o := defaultCompactOptions()
	o.TargetBytes = 1 << 20
	o.MinBytes = 1
	return o
}

func TestRepLifecycleChain(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	dbA := repTestSQLite(400, 4096, 71)
	dbB := repTestSQLiteUpdate(dbA, 4096, []int{3, 17, 200, 399}, 72)
	big := make([]byte, 700<<10)
	rand.New(rand.NewSource(73)).Read(big)
	tarT := repTestTar(t, map[string][]byte{"a.db": dbA, "big": big, "note": []byte("n")}, []string{"a.db", "big", "note"})
	l.put("db", dbA)
	l.put("db", dbB) // dbA is archived into history, with its own representation root
	l.put("loose", dbA)
	l.put("tar", tarT)
	rr := l.reach()
	if rr.RepRoots < 4 {
		t.Fatalf("roots = %d", rr.RepRoots)
	}
	first := l.exact("initial")
	if first.RepresentationsVerified < 4 {
		t.Fatalf("verified = %d", first.RepresentationsVerified)
	}
	// loose chunks: thousands of tiny page components are ordinary loose CAS files
	looseBefore := len(repLooseSet(t, l.dir))

	l.offline(func() {
		res, err := compactStore(l.dir, repSmallCompact())
		if err != nil {
			t.Fatal(err)
		}
		if res.PacksWritten == 0 {
			t.Fatal("nothing compacted")
		}
	})
	l.exact("compact")
	if loose := len(repLooseSet(t, l.dir)); loose >= looseBefore {
		t.Fatalf("compaction did not pack representation components: %d -> %d loose", looseBefore, loose)
	}

	// spread the packs over the three tiers
	ids := l.packIDs()
	if len(ids) < 2 {
		t.Fatalf("only %d packs", len(ids))
	}
	l.offline(func() {
		for i, p := range ids {
			to := []tier{tierWarm, tierCold, tierHot}[i%3]
			if to == tierHot {
				continue
			}
			if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: to, IDs: []string{p.id}, Apply: true}); err != nil {
				t.Fatal(err)
			}
		}
	})
	l.exact("tier spread")

	// history prune: A's manifest loses its last root, so its optional root is garbage
	l.offline(func() {
		zero := 0
		if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "db", KeepLast: &zero}, true); err != nil {
			t.Fatal(err)
		}
		dry, err := gcCollect(l.dir, false)
		if err != nil {
			t.Fatal(err)
		}
		if dry.RepresentationOrphanRoots != 1 || !dry.LiveSetOK {
			t.Fatalf("dry-run: orphan roots %d ok=%v %+v", dry.RepresentationOrphanRoots, dry.LiveSetOK, dry.Issues)
		}
		res, err := gcCollect(l.dir, true)
		if err != nil {
			t.Fatal(err)
		}
		if res.RepresentationRootsDeleted != 1 {
			t.Fatalf("orphan roots deleted %d", res.RepresentationRootsDeleted)
		}
	})
	rr = l.reach()
	files, _ := l.s.repRootFiles()
	if len(files) != 3 || len(rr.RepOrphanRoots) != 0 {
		t.Fatalf("after gc: %d root files, %d orphans", len(files), len(rr.RepOrphanRoots))
	}
	l.exact("history prune + gc")

	l.offline(func() {
		if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true}); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("repack")
	l.offline(func() {})
	res := l.exact("restart")
	if res.RepresentationsVerified != 3 {
		t.Fatalf("verified %d after restart", res.RepresentationsVerified)
	}
	// set-wise: after gc nothing outside the live union remains loose
	l.offline(func() {
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	rr = l.reach()
	for sha := range repLooseSet(t, l.dir) {
		if !rr.ReferencedChunks[sha] {
			t.Fatalf("loose chunk %s outlived gc but is not live", sha[:12])
		}
	}
	for sha := range rr.ReferencedChunks {
		sum, _ := decodeHexSHA256(sha)
		if _, err := l.s.casStat(sum); err != nil {
			t.Fatalf("live chunk %s missing: %v", sha[:12], err)
		}
	}
}

// ---- crash matrix ---------------------------------------------------------------------

// repCrashPut PUTs data while the hook panics at the nth firing of point (after
// analysis began), then discards the Store and reopens the directory.
func repCrashPut(t *testing.T, l *repLab, key string, data []byte, point string, nth int) {
	t.Helper()
	started, seen := false, 0
	withTestHook(t, func(p string) {
		if p == hookRepAnalysisStarted {
			started = true
		}
		if p == point && (started || point == hookRepAnalysisStarted) {
			if seen++; seen == nth {
				panic(simulatedCrash{point: p})
			}
		}
	})
	runExpectingSimulatedCrash(t, func() { _, _ = l.s.PutObject("b", key, data, "application/octet-stream", nil) })
	testHook = nil
	l.close()
	l.open()
}

func repRootFilesOf(t *testing.T, l *repLab) []string {
	files, err := l.s.repRootFiles()
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRepCrashMatrix(t *testing.T) {
	base := repTestSQLite(900, 4096, 81)
	crashDB := repTestSQLiteUpdate(base, 4096, []int{1, 2, 3, 400, 800}, 82)
	cases := []struct {
		name      string
		point     string
		nth       int
		visible   bool
		wantRoot  bool // a root file exists after the crash
		orphanGC  bool // gc must find and remove an orphan root
		cleanable bool // a dry run reports unreachable chunks
	}{
		{"C1 before components publish", hookRepAnalysisStarted, 1, false, false, false, true},
		{"C2 some components published", hookCASAfterFlush, 1, false, false, false, true},
		{"C3 representation object published, root absent", hookRepObjectPublished, 1, false, false, false, true},
		{"C3b root temp written", hookRepRootWritten, 1, false, false, false, true},
		{"C3c root renamed, not yet synced", hookRepRootAfterRename, 1, false, true, true, true},
		{"C4 root published, no namespace commit", hookRepBeforeNamespace, 1, false, true, true, true},
		{"C5 root and namespace committed", hookAfterApplyBeforeResponse, 1, true, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("base", base) // committed earlier, shares most chunks
			l.exact("before crash")
			rootsBefore := len(repRootFilesOf(t, l))
			repCrashPut(t, l, "crash", crashDB, c.point, c.nth)
			_, _, err := l.s.GetObject("b", "crash")
			if visible := err == nil; visible != c.visible {
				t.Fatalf("visible=%v want %v (%v)", visible, c.visible, err)
			}
			roots := repRootFilesOf(t, l)
			if has := len(roots) > rootsBefore; has != c.wantRoot {
				t.Fatalf("root published=%v want %v (%d -> %d)", has, c.wantRoot, rootsBefore, len(roots))
			}
			if c.visible {
				l.want["crash"] = crashDB
			}
			// the committed object is untouched and everything still verifies
			l.exact("after crash")
			rr := l.reach()
			if got := len(rr.RepOrphanRoots) > 0; got != c.orphanGC {
				t.Fatalf("orphan roots = %v want %v", rr.RepOrphanRoots, c.orphanGC)
			}
			before := repLooseSet(t, l.dir)
			var res GCResult
			l.offline(func() {
				var err error
				if res, err = gcCollect(l.dir, true); err != nil {
					t.Fatal(err)
				}
			})
			if c.orphanGC && res.RepresentationRootsDeleted == 0 {
				t.Fatal("gc did not remove the orphan root")
			}
			if c.cleanable && res.ChunksDeleted == 0 {
				t.Fatalf("gc found nothing to reclaim: %+v", res)
			}
			l.exact("after gc")
			after := repLooseSet(t, l.dir)
			rr = l.reach()
			for sha := range after {
				if !rr.ReferencedChunks[sha] {
					t.Fatalf("dead chunk %s survived gc", sha[:12])
				}
			}
			if c.visible && len(after) != len(before) {
				t.Fatalf("gc removed live state: %d -> %d", len(before), len(after))
			}
			if _, err := l.s.Verify(true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// repNewestManifest returns the most recently written manifest in the store.
func repNewestManifest(t testing.TB, dir string) manifestV1 {
	ents, err := os.ReadDir(filepath.Join(dir, "manifests"))
	if err != nil || len(ents) == 0 {
		t.Fatalf("no manifests: %v", err)
	}
	var best string
	var bestT int64
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && fi.ModTime().UnixNano() >= bestT {
			best, bestT = e.Name(), fi.ModTime().UnixNano()
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifests", best))
	if err != nil {
		t.Fatal(err)
	}
	var m manifestV1
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// C6: building or publishing the optional root fails; the namespace commit still succeeds.
func TestRepOptionalFailureNeverFailsPut(t *testing.T) {
	db := repTestSQLite(300, 4096, 91)
	t.Run("root directory blocked: fail closed until cleared", func(t *testing.T) {
		l := newRepLab(t, repModePrefer)
		if err := os.WriteFile(filepath.Join(l.dir, "representations"), []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		l.put("db", db)
		if got := l.s.rep.optionalFailures.Load(); got != 1 {
			t.Fatalf("optional failures = %d", got)
		}
		// the format level was raised before the failed publication: acceptable, never rolled back
		if l.s.format.StoreFormatVersion != storeFormatVersionRepresentations {
			t.Fatalf("format %d", l.s.format.StoreFormatVersion)
		}
		// the ordinary object is complete and exact
		_, man := l.manifest("db")
		var cdc []byte
		rd := l.s.newManifestReader(man, byteRange{0, int64(len(db)) - 1})
		for b, err := rd.next(); err != io.EOF; b, err = rd.next() {
			if err != nil {
				t.Fatal(err)
			}
			cdc = append(cdc, b...)
		}
		if !bytes.Equal(cdc, db) || !bytes.Equal(repReadAll(t, l.s, man, byteRange{0, int64(len(db)) - 1}), db) {
			t.Fatal("object differs while the optional layer is blocked")
		}
		// an unreadable optional namespace is reported and blocks destructive maintenance
		if res, _ := l.s.Verify(false); res.OK() {
			t.Fatal("verify did not notice the blocked representation namespace")
		}
		l.close()
		if _, err := gcCollect(l.dir, true); !errors.Is(err, errGCUnsafe) {
			t.Fatalf("gc -apply on an untrusted optional namespace: %v", err)
		}
		os.Remove(filepath.Join(l.dir, "representations"))
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
		l.open()
		l.exact("recovered")
	})
	t.Run("root staging fails", func(t *testing.T) {
		l := newRepLab(t, repModePrefer)
		tmp := filepath.Join(l.dir, "tmp")
		withTestHook(t, func(p string) {
			if p == hookRepRootBeforeWrite {
				os.Rename(tmp, tmp+".gone")
			}
		})
		l.put("db", db)
		testHook = nil
		os.Rename(tmp+".gone", tmp)
		if l.s.rep.optionalFailures.Load() != 1 || len(repRootFilesOf(t, l)) != 0 {
			t.Fatalf("expected one optional failure and no root")
		}
		l.exact("cdc only")
		// the next object gets its representation normally
		l.put("db2", repTestSQLite(100, 4096, 92))
		if len(repRootFilesOf(t, l)) != 1 {
			t.Fatal("later PUT did not publish a root")
		}
		l.exact("later object")
	})
	t.Run("proof failure", func(t *testing.T) {
		l := newRepLab(t, repModePrefer)
		withTestHook(t, func(p string) {
			if p != hookRepComponentsFlushed {
				return
			}
			man := repNewestManifest(t, l.dir)
			own := map[string]bool{}
			for _, c := range man.Chunks {
				own[c.SHA256] = true
			}
			var victim string
			filepath.WalkDir(filepath.Join(l.dir, "chunks"), func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && !own[d.Name()] && victim == "" {
					victim = path
				}
				return nil
			})
			if victim == "" {
				t.Errorf("no representation-only component to damage")
				return
			}
			b, _ := os.ReadFile(victim)
			b[0] ^= 0xff
			os.WriteFile(victim, b, 0o644)
		})
		l.put("db", db)
		testHook = nil
		if l.s.rep.optionalFailures.Load() != 1 || len(repRootFilesOf(t, l)) != 0 {
			t.Fatalf("an unproven representation was published (failures=%d)", l.s.rep.optionalFailures.Load())
		}
		l.exact("cdc only")
	})
	t.Run("mode off creates nothing", func(t *testing.T) {
		l := newRepLab(t, repModeOff)
		l.put("db", db)
		if len(repRootFilesOf(t, l)) != 0 || l.s.format.StoreFormatVersion != storeFormatVersion {
			t.Fatalf("off mode created representation state (format %d)", l.s.format.StoreFormatVersion)
		}
		if _, err := os.Stat(filepath.Join(l.dir, "representations")); !os.IsNotExist(err) {
			t.Fatal("off mode created the representations directory")
		}
	})
}

// ---- live-root faults: CDC stays exact, destructive maintenance fails closed ----------------

func repCorruptFile(t testing.TB, path string, f func([]byte) []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepLiveRootFaultsFailClosed(t *testing.T) {
	db := repTestSQLite(300, 4096, 101)
	faults := map[string]func(t *testing.T, l *repLab, uuid string){
		"bit flip": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte { b[len(b)/2] ^= 1; return b })
		},
		"truncated": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte { return b[:len(b)-9] })
		},
		"empty": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte { return nil })
		},
		"filed under another manifest": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte {
				r, _ := unmarshalRepRoot(b)
				r.ManifestUUID = "00000000-0000-7000-8000-000000000000"
				out, _ := r.marshal()
				return out
			})
		},
		"names a different object": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte {
				r, _ := unmarshalRepRoot(b)
				r.ObjSHA[0] ^= 1
				out, _ := r.marshal()
				return out
			})
		},
		"wrong object size": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte {
				r, _ := unmarshalRepRoot(b)
				r.ObjSize++
				out, _ := r.marshal()
				return out
			})
		},
		"representation object missing": func(t *testing.T, l *repLab, uuid string) {
			r := repMustRoot(t, l.s, uuid)
			os.Remove(l.s.chunkPath(r.Rep))
		},
		"component missing": func(t *testing.T, l *repLab, uuid string) {
			r := repMustRoot(t, l.s, uuid)
			src := l.s.newRepSource(r.Descs, nil)
			defer src.close()
			reach, err := (&repEnv{Src: src, Limits: repProdLimits}).reachable(r.Rep)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range reach.Order {
				if reach.Role[id] == repDepVia {
					os.Remove(l.s.chunkPath(id))
					return
				}
			}
			t.Fatal("no VIA component")
		},
		"root points at another representation": func(t *testing.T, l *repLab, uuid string) {
			repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte {
				r, _ := unmarshalRepRoot(b)
				r.Rep[5] ^= 1
				out, _ := r.marshal()
				return out
			})
		},
	}
	for name, damage := range faults {
		t.Run(name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("db", db)
			l.put("other", repTestSQLite(150, 4096, 102))
			e, _ := l.manifest("db")
			l.exact("healthy")
			damage(t, l, e.manifestUUID)
			l.offline(func() {})
			// normal reads are served by CDC; the representation path falls back or is unavailable
			_, man := l.manifest("db")
			got := repReadAll(t, l.s, man, byteRange{0, int64(len(db)) - 1})
			if !bytes.Equal(got, db) {
				t.Fatal("object unreadable while only the optional layer is damaged")
			}
			res, err := l.s.Verify(true)
			if err != nil {
				t.Fatal(err)
			}
			if res.OK() {
				t.Fatal("verify did not detect the representation fault")
			}
			found := false
			for _, iss := range res.Issues {
				found = found || strings.Contains(iss.Subject, "representation "+e.manifestUUID)
			}
			if !found {
				t.Fatalf("fault not attributed to the representation: %+v", res.Issues)
			}
			// destructive maintenance must not guess
			l.close()
			dry, err := gcCollect(l.dir, false)
			if err != nil || dry.LiveSetOK {
				t.Fatalf("dry-run: ok=%v err=%v", dry.LiveSetOK, err)
			}
			if _, err := gcCollect(l.dir, true); !errors.Is(err, errGCUnsafe) {
				t.Fatalf("gc -apply: %v", err)
			}
			if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
				// compaction is non-destructive for the referenced set; it may proceed or refuse, never lose data
				t.Logf("compact: %v", err)
			}
			if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100}); err == nil {
				t.Log("repack proceeded (no dead records to drop)")
			}
			// the supported recovery: retire the optional root, then maintenance proceeds
			l.open()
			rr, err := l.s.computeReachability(false)
			if err != nil {
				t.Fatal(err)
			}
			retired, err := l.s.retireFaultyRepresentations(rr)
			if err != nil || len(retired) != 1 || retired[0] != e.manifestUUID {
				t.Fatalf("retired %v err %v", retired, err)
			}
			l.close()
			if _, err := gcCollect(l.dir, true); err != nil {
				t.Fatalf("gc after retiring the bad root: %v", err)
			}
			l.open()
			l.exact("recovered") // db now reads through CDC; other keeps its representation
			if repHasRoot(l.s, e.manifestUUID) {
				t.Fatal("retired root still present")
			}
		})
	}
}

func TestRepFailureAfterOutputStartsDoesNotPanic(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	db := repTestSQLite(600, 4096, 111)
	l.put("db", db)
	e, man := l.manifest("db")
	r := repMustRoot(t, l.s, e.manifestUUID)
	// damage a page component in the middle of the object: the first bytes are fine
	src := l.s.newRepSource(r.Descs, nil)
	reach, err := (&repEnv{Src: src, Limits: repProdLimits}).reachable(r.Rep)
	src.close()
	if err != nil {
		t.Fatal(err)
	}
	var victim [32]byte
	n := 0
	for _, id := range reach.Order {
		if reach.Role[id] == repDepVia {
			if n++; n == 500 {
				victim = id
			}
		}
	}
	repCorruptFile(t, l.s.chunkPath(victim), func(b []byte) []byte { b[7] ^= 1; return b })
	rd, err := l.s.repOpenReader(man, byteRange{0, int64(len(db)) - 1})
	if err != nil || rd == nil {
		t.Fatalf("direct representation decoder: %v", err)
	}
	defer rd.close()
	var got []byte
	var rerr error
	for {
		b, err := rd.next()
		if err != nil {
			rerr = err
			break
		}
		got = append(got, b...)
	}
	if rerr == io.EOF {
		t.Fatal("corrupt component read as success")
	}
	if !bytes.Equal(got, db[:len(got)]) {
		t.Fatal("unverified bytes were emitted")
	}
	if !errors.Is(rerr, errRepMismatch) {
		t.Fatalf("error class: %v", rerr)
	}
}

func TestRepModesAndFallbackCounters(t *testing.T) {
	db := repTestSQLite(200, 4096, 121)
	for _, mode := range []repMode{repModeShadow, repModePrefer} {
		l := newRepLab(t, mode)
		l.put("db", db)
		e, man := l.manifest("db")
		_ = repReadAll(t, l.s, man, byteRange{0, int64(len(db)) - 1})
		_ = repReadAll(t, l.s, man, byteRange{100, 5000})
		wantReads := int64(0)
		if mode == repModePrefer {
			wantReads = 1
		}
		if l.s.rep.reads.Load() != 0 || l.s.rep.rangeReads.Load() != wantReads {
			t.Fatalf("%s: reads=%d range=%d", mode, l.s.rep.reads.Load(), l.s.rep.rangeReads.Load())
		}
		// a root that is gone falls back silently; a damaged one falls back and is counted
		repCorruptFile(t, l.s.repRootPath(e.manifestUUID), func(b []byte) []byte { b[10] ^= 1; return b })
		got := repReadAll(t, l.s, man, byteRange{100, 5000})
		if !bytes.Equal(got, db[100:5001]) {
			t.Fatal("fallback read differs")
		}
		if mode == repModePrefer && l.s.rep.fallbacks.Load() != 1 {
			t.Fatalf("fallbacks = %d", l.s.rep.fallbacks.Load())
		}
		if mode == repModeShadow && l.s.rep.fallbacks.Load() != 0 {
			t.Fatal("shadow mode must not touch the representation on reads")
		}
	}
	// invalid mode names are rejected, off is the default
	if m, err := parseRepMode(""); err != nil || m != repModeOff {
		t.Fatal("default mode must be off")
	}
	if _, err := parseRepMode("on"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestRepTempFilesAreNeverRootsOrLiveness(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	l.put("db", repTestSQLite(120, 4096, 131))
	l.close()
	roots := filepath.Join(l.dir, "representations", "roots")
	os.WriteFile(filepath.Join(roots, "stale.root.tmp"), []byte("garbage"), 0o644)
	os.WriteFile(filepath.Join(roots, ".partial.root"), []byte("garbage"), 0o644)
	os.WriteFile(filepath.Join(l.dir, "tmp", "rep-123456.tmp"), []byte("half a root"), 0o644)
	l.open()
	rr := l.reach()
	if rr.RepRoots != 1 || len(rr.RepOrphanRoots) != 0 {
		t.Fatalf("roots=%d orphans=%v", rr.RepRoots, rr.RepOrphanRoots)
	}
	l.exact("with leftovers")
	l.close()
	if _, err := gcCollect(l.dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "tmp", "rep-123456.tmp")); !os.IsNotExist(err) {
		t.Fatal("stale root staging file survived gc")
	}
}

func TestRepStatsFields(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	base, _ := l.s.computeStats(statsScope{})
	if base.RepresentationRootCount != 0 || base.RepresentationMetadataFileBytes != 0 {
		t.Fatal("representation fields must be zero on a store without representations")
	}
	l.put("db", repTestSQLite(250, 4096, 141))
	l.put("car", repTestCAR(repTestBlocks(200, 2000, 142)))
	st, err := l.s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if st.RepresentationRootCount != 2 || st.RepresentationObjectCount != 2 || st.RepresentationComponentCount < 400 ||
		st.RepresentationReachableChunkCount < 400 || st.RepresentationMetadataFileBytes == 0 || st.RepresentationOrphanRootCount != 0 {
		t.Fatalf("%+v", st)
	}
	b, _ := json.Marshal(st)
	for _, k := range []string{"representation_root_count", "representation_object_count", "representation_component_count",
		"representation_component_logical_bytes", "representation_metadata_file_bytes", "representation_reachable_chunk_count",
		"representation_reachable_chunk_bytes", "representation_orphan_root_count", "unique_reachable_chunk_bytes"} {
		if !bytes.Contains(b, []byte(`"`+k+`"`)) {
			t.Fatalf("stats JSON lacks %s", k)
		}
	}
	// representation chunks are ordinary reachable chunks of the store
	if st.UniqueReachableChunkBytes < st.RepresentationReachableChunkBytes {
		t.Fatal("representation chunks not part of the reachable total")
	}
}

// ---- cross-format shared child, shared roots, virtual components ----------------------------

// repPhysical is the set of chunk digests the live representation roots occupy.
func repPhysicalOf(t testing.TB, s *Store, uuid string) map[string]bool {
	r := repMustRoot(t, s, uuid)
	cl, err := s.repClosureOf(r)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range cl.chunks {
		out[c.SHA256] = true
	}
	return out
}

func TestRepSharedChildAcrossFormatsAndRootLifecycle(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	x := repTestSQLite(180, 4096, 151) // the shared child
	y := make([]byte, 20000)
	rand.New(rand.NewSource(152)).Read(y)
	tarA := repTestTar(t, map[string][]byte{"x.db": x, "pad": y[:5000]}, []string{"x.db", "pad"})
	tarB := repTestTar(t, map[string][]byte{"other": y[5000:12000], "x.db": x}, []string{"other", "x.db"})
	l.put("tarA", tarA)
	l.put("loose", x)
	l.put("tarB", tarB)
	l.exact("initial")

	ea, _ := l.manifest("tarA")
	el, _ := l.manifest("loose")
	eb, _ := l.manifest("tarB")
	pa, pl, pb := repPhysicalOf(t, l.s, ea.manifestUUID), repPhysicalOf(t, l.s, el.manifestUUID), repPhysicalOf(t, l.s, eb.manifestUUID)
	// the child representation and its page components are stored once and shared by all three roots
	shared := 0
	for c := range pl {
		if pa[c] && pb[c] {
			shared++
		}
	}
	if shared != len(pl) {
		t.Fatalf("loose SQLite closure is not fully shared: %d of %d chunks", shared, len(pl))
	}
	rootsAreIndependent := repMustRoot(t, l.s, ea.manifestUUID).Rep != repMustRoot(t, l.s, el.manifestUUID).Rep
	if !rootsAreIndependent {
		t.Fatal("a tar and a loose database must have different top representations")
	}
	// delete the roots' manifests one at a time (overwrite then prune history) and gc:
	del := func(key string) {
		if err := l.s.DeleteObject("b", key); err != nil {
			t.Fatal(err)
		}
		delete(l.want, key)
		l.offline(func() {
			zero := 0
			if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: key, KeepLast: &zero}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := gcCollect(l.dir, true); err != nil {
				t.Fatal(err)
			}
		})
	}
	physical := func() map[string]bool {
		rr := l.reach()
		return rr.ReferencedChunks
	}
	del("tarA") // shared child stays: loose and tarB still reference it
	live := physical()
	for c := range pl {
		if !live[c] {
			t.Fatal("shared child collected while two roots still need it")
		}
	}
	l.exact("after removing tarA")
	del("tarB") // loose still holds the child
	live = physical()
	for c := range pl {
		if !live[c] {
			t.Fatal("shared child collected while the loose root remains")
		}
	}
	l.exact("after removing tarB")
	del("loose") // last root: the child's chunks become reclaimable and are removed
	live = physical()
	for c := range pl {
		if live[c] {
			t.Fatal("last root removed but its chunks are still live")
		}
	}
	for c := range pl {
		sum, _ := decodeHexSHA256(c)
		if _, err := l.s.casStat(sum); err == nil {
			t.Fatalf("chunk %s survived the last root", c[:12])
		}
	}
	if files, _ := l.s.repRootFiles(); len(files) != 0 {
		t.Fatalf("%d root files left", len(files))
	}
}

func TestRepVirtualComponentDescriptor(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	big := make([]byte, 900<<10) // a tar member far above one pack record
	rand.New(rand.NewSource(161)).Read(big)
	tarT := repTestTar(t, map[string][]byte{"big.bin": big, "tiny": []byte("x")}, []string{"big.bin", "tiny"})
	l.put("t", tarT)
	e, _ := l.manifest("t")
	root := repMustRoot(t, l.s, e.manifestUUID)
	if len(root.Descs) == 0 {
		t.Fatal("no virtual component descriptor in the root")
	}
	var bigID [32]byte
	var d *repDesc
	for id, dd := range root.Descs {
		if dd.Size >= int64(len(big)) {
			bigID, d = id, dd
		}
	}
	if d == nil {
		t.Fatal("no descriptor for the large member")
	}
	// the logical identity is the SHA-256 of the exact component bytes, never of a descriptor
	padded := (len(big) + 511) / 512 * 512
	want := sha256.Sum256(append(append([]byte(nil), big...), make([]byte, padded-len(big))...))
	if bigID != want {
		t.Fatal("virtual component identity is not SHA-256 of its bytes")
	}
	for _, r := range d.Refs {
		if r.Len > repDirectMax || r.Len == 0 {
			t.Fatalf("chunk of %d bytes", r.Len)
		}
		if _, err := l.s.casStat(r.SHA); err != nil {
			t.Fatalf("descriptor chunk missing: %v", err)
		}
	}
	if _, err := l.s.casStat(bigID); err == nil {
		t.Fatal("a >256 KiB logical component must not exist as one loose CAS object")
	}
	// a point read fetches one or two physical chunks, not the component
	src := l.s.newRepSource(root.Descs, nil)
	buf := make([]byte, 4096)
	if n, err := src.ReadAt(bigID, buf, 400000); err != nil || n != len(buf) || !bytes.Equal(buf, big[400000:404096]) {
		t.Fatalf("point read: %d %v", n, err)
	}
	if src.stats.Chunks > 2 {
		t.Fatalf("a 4 KiB read touched %d physical chunks", src.stats.Chunks)
	}
	src.close()
	// compaction, tiers and repack leave the logical identity and reads unchanged
	l.offline(func() {
		if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("compact")
	l.offline(func() {
		if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: tierCold, All: true, Apply: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100}); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("tier + repack")
	root2 := repMustRoot(t, l.s, e.manifestUUID)
	if root2.Descs[bigID] == nil || root2.Rep != root.Rep {
		t.Fatal("maintenance changed the logical identity")
	}
	// a descriptor whose chunk length disagrees with the physical chunk is rejected
	badLen := *d
	badLen.Refs = append([]repChunkRef(nil), d.Refs...)
	badLen.Refs[1].Len--
	badLen.Size--
	bad := map[[32]byte]*repDesc{bigID: &badLen}
	for id, dd := range root2.Descs {
		if id != bigID {
			bad[id] = dd
		}
	}
	src = l.s.newRepSource(bad, nil)
	defer src.close()
	if _, err := repReadObject(src, bigID, -1, 64<<20); err == nil || !(errors.Is(err, errRepMismatch) || errors.Is(err, errRepCorrupt)) {
		t.Fatalf("descriptor with a wrong chunk length: %v", err)
	}
}

func TestRepSharedVirtualComponentAcrossRoots(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	big := make([]byte, 700<<10)
	rand.New(rand.NewSource(171)).Read(big)
	t1 := repTestTar(t, map[string][]byte{"big": big, "a": []byte("one")}, []string{"big", "a"})
	t2 := repTestTar(t, map[string][]byte{"big": big, "b": []byte("two two")}, []string{"big", "b"})
	l.put("t1", t1)
	l.put("t2", t2)
	e1, _ := l.manifest("t1")
	e2, _ := l.manifest("t2")
	r1, r2 := repMustRoot(t, l.s, e1.manifestUUID), repMustRoot(t, l.s, e2.manifestUUID)
	common := 0
	for id, d := range r1.Descs {
		if d2 := r2.Descs[id]; d2 != nil && len(d.Refs) == len(d2.Refs) {
			common++
		}
	}
	if common != 1 {
		t.Fatalf("expected one shared virtual component, got %d", common)
	}
	// each root carries its own small descriptor; the physical chunks are stored once
	p1, p2 := repPhysicalOf(t, l.s, e1.manifestUUID), repPhysicalOf(t, l.s, e2.manifestUUID)
	both := 0
	for c := range p1 {
		if p2[c] {
			both++
		}
	}
	if both < 3 {
		t.Fatalf("shared chunks = %d", both)
	}
	if err := l.s.DeleteObject("b", "t1"); err != nil {
		t.Fatal(err)
	}
	delete(l.want, "t1")
	l.offline(func() {
		zero := 0
		pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "t1", KeepLast: &zero}, true)
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("one root removed")
	rr := l.reach()
	for c := range p2 {
		if !rr.ReferencedChunks[c] {
			t.Fatalf("t2's chunk %s died with t1", c[:12])
		}
	}
}

// ---- Merkle revision chains, snapshots, copy, multipart --------------------------------------

func TestRepMerkleRevisionChainSections(t *testing.T) {
	for _, tag := range []byte{1, 3} { // sha3-256 (owning sections) and sha256 (graph walk)
		t.Run(fmt.Sprintf("hash-%d", tag), func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			keys := repTestKeys(4000, 181)
			ver := map[uint64]int{}
			var revs [][]byte
			var sizes []int
			for r := 0; r < 6; r++ {
				for i := 0; i < 20; i++ { // a few updates per revision
					ver[keys[(r*997+i*131)%len(keys)]]++
				}
				snap := repTestMerkleSnapshot(tag, append([]uint64(nil), keys...), ver)
				revs = append(revs, snap)
				before, _ := l.s.computeStats(statsScope{})
				l.put(fmt.Sprintf("rev%d", r), snap)
				after, _ := l.s.computeStats(statsScope{})
				sizes = append(sizes, int(after.RepresentationReachableChunkBytes-before.RepresentationReachableChunkBytes))
			}
			l.exact("chain")
			// the first revision pays for the graph; later ones add only what changed
			if sizes[0] < len(revs[0])/2 {
				t.Fatalf("first revision stored %d of %d bytes", sizes[0], len(revs[0]))
			}
			for r := 1; r < len(sizes); r++ {
				if sizes[r] > sizes[0]/3 {
					t.Fatalf("revision %d added %d bytes against %d for the first", r, sizes[r], sizes[0])
				}
			}
			e0, _ := l.manifest("rev0")
			rep0, _ := (&repEnv{Src: l.s.newRepSource(nil, l.s.repDescLookup), Limits: repProdLimits}).loadRep(repMustRoot(t, l.s, e0.manifestUUID).Rep)
			e5, _ := l.manifest("rev5")
			rep5, _ := (&repEnv{Src: l.s.newRepSource(nil, l.s.repDescLookup), Limits: repProdLimits}).loadRep(repMustRoot(t, l.s, e5.manifestUUID).Rep)
			if tag == 3 && (len(rep0.Indexes) != 0 || len(rep5.Indexes) != 0) {
				t.Fatal("a SHA-256 native hash needs no record sections")
			}
			if tag == 1 && (len(rep5.Indexes) < 2 || len(rep5.Indexes) > merkleMaxSections) {
				t.Fatalf("revision 5 lists %d sections", len(rep5.Indexes))
			}
			for _, x := range rep5.Indexes {
				if !x.Owns {
					t.Fatal("Merkle sections own their targets")
				}
			}
			// delete the oldest revisions: later revisions stay exact (their chain is closed under reuse)
			for _, k := range []string{"rev0", "rev1", "rev2"} {
				if err := l.s.DeleteObject("b", k); err != nil {
					t.Fatal(err)
				}
				delete(l.want, k)
			}
			l.offline(func() {
				zero := 0
				for _, k := range []string{"rev0", "rev1", "rev2"} {
					if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: k, KeepLast: &zero}, true); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := gcCollect(l.dir, true); err != nil {
					t.Fatal(err)
				}
			})
			l.exact("older revisions pruned")
			// dropping the catalog and the descriptor union changes no liveness
			rrA := l.reach()
			l.s.repInvalidate()
			l.s.rep.cat = nil
			l.s.rep.desc = nil
			rrB := l.reach()
			if len(rrA.ReferencedChunks) != len(rrB.ReferencedChunks) {
				t.Fatal("the catalog changed reachability")
			}
			l.s.repEnsure()
			if l.s.rep.cat.records() == 0 && tag == 1 {
				t.Fatal("catalog did not rebuild from the live roots")
			}
		})
	}
}

func TestRepSnapshotRetainsRepresentation(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	a := repTestSQLite(160, 4096, 191)
	b := repTestSQLiteUpdate(a, 4096, []int{5, 6}, 192)
	l.put("db", a)
	entries, err := l.s.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := l.s.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}
	ea, _ := l.manifest("db")
	l.put("db", b) // A leaves current and history below
	if err := l.s.DeleteObject("b", "db"); err != nil {
		t.Fatal(err)
	}
	delete(l.want, "db")
	l.offline(func() {
		zero := 0
		if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "db", KeepLast: &zero}, true); err != nil {
			t.Fatal(err)
		}
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	// only the snapshot retains manifest A: its representation closure must be kept
	if !repHasRoot(l.s, ea.manifestUUID) {
		t.Fatal("snapshot-retained manifest lost its representation root")
	}
	rr := l.reach()
	if rr.RepRoots != 1 {
		t.Fatalf("live roots = %d", rr.RepRoots)
	}
	phys := repPhysicalOf(t, l.s, ea.manifestUUID)
	for c := range phys {
		if !rr.ReferencedChunks[c] {
			t.Fatal("snapshot-only representation chunk not live")
		}
	}
	res := repVerifyOK(t, l.s, true)
	if res.RepresentationsVerified != 1 {
		t.Fatalf("verified %d", res.RepresentationsVerified)
	}
	// delete the snapshot: the closure becomes reclaimable, with no persistent refcount
	if err := l.s.deleteSnapshot(desc.SnapshotID); err != nil {
		t.Fatal(err)
	}
	l.offline(func() {
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	if files, _ := l.s.repRootFiles(); len(files) != 0 {
		t.Fatalf("root files after the snapshot died: %v", files)
	}
	for c := range phys {
		sum, _ := decodeHexSHA256(c)
		if _, err := l.s.casStat(sum); err == nil {
			t.Fatalf("representation chunk %s survived its last root", c[:12])
		}
	}
}

func TestRepCopyObjectAndMultipart(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	db := repTestSQLite(260, 4096, 201)
	l.put("src", db)
	es, _ := l.manifest("src")
	// CopyObject: byte-identical content gets a small sidecar root pointing at the same representation
	entry, dstMan, err := l.s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	l.want["copy"] = db
	if !repHasRoot(l.s, dstMan.ManifestUUID) || entry.manifestUUID == es.manifestUUID {
		t.Fatal("copy has no independent root")
	}
	rs, rc := repMustRoot(t, l.s, es.manifestUUID), repMustRoot(t, l.s, dstMan.ManifestUUID)
	if rs.Rep != rc.Rep {
		t.Fatal("copy re-analysed instead of reusing the proven representation")
	}
	if entry.etag != es.etag || dstMan.TotalLength != int64(len(db)) {
		t.Fatal("copy changed S3 identity")
	}
	l.exact("copy")
	// multipart: the completed object goes through the same hook
	up2, _ := l.s.CreateMultipartUpload("b", "mp2", "application/octet-stream", nil)
	etag, err := l.s.UploadPart("b", "mp2", up2, 1, repTestSQLite(1600, 4096, 203))
	if err != nil {
		t.Fatal(err)
	}
	mpDB := repTestSQLite(1600, 4096, 203)
	if _, _, err := l.s.CompleteMultipartUpload("b", "mp2", up2, []completedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	l.want["mp2"] = mpDB
	e2, _ := l.manifest("mp2")
	if !repHasRoot(l.s, e2.manifestUUID) {
		t.Fatal("completed multipart object did not get a representation")
	}
	l.exact("multipart")
}

// ---- Source corruption, copy fallback, concurrency ----------------------------------------------

func TestRepSourceCorruptionClassified(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	big := make([]byte, 600<<10)
	rand.New(rand.NewSource(211)).Read(big)
	tarT := repTestTar(t, map[string][]byte{"big": big, "s.db": repTestSQLite(40, 4096, 212)}, []string{"big", "s.db"})
	l.put("t", tarT)
	e, _ := l.manifest("t")
	root := repMustRoot(t, l.s, e.manifestUUID)
	var bigID [32]byte
	for id, d := range root.Descs {
		if d.Size > 500<<10 {
			bigID = id
		}
	}
	d := root.Descs[bigID]
	read := func(descs map[[32]byte]*repDesc, id [32]byte) error {
		src := l.s.newRepSource(descs, nil)
		defer src.close()
		_, err := repReadObject(src, id, -1, 64<<20)
		return err
	}
	if err := read(root.Descs, bigID); err != nil {
		t.Fatal(err)
	}
	with := func(nd *repDesc) map[[32]byte]*repDesc {
		m := map[[32]byte]*repDesc{}
		for k, v := range root.Descs {
			m[k] = v
		}
		m[bigID] = nd
		return m
	}
	clone := func() *repDesc { c := *d; c.Refs = append([]repChunkRef(nil), d.Refs...); c.offs = nil; return &c }
	// wrong chunk length in the descriptor
	nd := clone()
	nd.Refs[0].Len--
	nd.Size--
	if err := read(with(nd), bigID); !errors.Is(err, errRepMismatch) {
		t.Fatalf("wrong descriptor length: %v", err)
	}
	// truncated descriptor: fewer chunks than the component has (identity no longer matches)
	nd = clone()
	nd.Size -= int64(nd.Refs[len(nd.Refs)-1].Len)
	nd.Refs = nd.Refs[:len(nd.Refs)-1]
	if err := read(with(nd), bigID); err == nil {
		t.Fatal("truncated descriptor accepted")
	}
	// a swapped chunk (right length class, wrong content)
	nd = clone()
	nd.Refs[0], nd.Refs[1] = nd.Refs[1], nd.Refs[0]
	if err := read(with(nd), bigID); err == nil {
		t.Fatal("reordered descriptor accepted")
	}
	// missing child chunk
	victim := d.Refs[1].SHA
	path := l.s.chunkPath(victim)
	keep, _ := os.ReadFile(path)
	os.Remove(path)
	if err := read(root.Descs, bigID); !errors.Is(err, errRepMissing) {
		t.Fatalf("missing child chunk: %v", err)
	}
	// wrong content under the right name
	bad := append([]byte(nil), keep...)
	bad[3] ^= 0xff
	os.WriteFile(path, bad, 0o644)
	if err := read(root.Descs, bigID); !errors.Is(err, errRepMismatch) {
		t.Fatalf("wrong chunk content: %v", err)
	}
	os.WriteFile(path, keep, 0o644)
	if err := read(root.Descs, bigID); err != nil {
		t.Fatal(err)
	}
	// reads of a size beyond the component, negative offsets and zero-length reads are bounded
	src := l.s.newRepSource(root.Descs, nil)
	defer src.close()
	if n, err := src.ReadAt(bigID, make([]byte, 10), d.Size); n != 0 || err != io.EOF {
		t.Fatalf("read at end: %d %v", n, err)
	}
	if _, err := src.ReadAt(bigID, make([]byte, 10), -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if n, err := src.ReadAt(bigID, nil, 5); n != 0 || err != nil {
		t.Fatal("zero-length read")
	}
	if _, err := src.Size([32]byte{0xde, 0xad}); !errors.Is(err, errRepMissing) {
		t.Fatalf("unknown component: %v", err)
	}
}

func TestRepPackedCopyFallback(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	db := repTestSQLite(300, 4096, 221)
	l.put("db", db)
	l.offline(func() {
		if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
			t.Fatal(err)
		}
	})
	e, _ := l.manifest("db")
	root := repMustRoot(t, l.s, e.manifestUUID)
	src := l.s.newRepSource(root.Descs, nil)
	reach, err := (&repEnv{Src: src, Limits: repProdLimits}).reachable(root.Rep)
	src.close()
	if err != nil {
		t.Fatal(err)
	}
	var vias [][32]byte
	for _, id := range reach.Order {
		if reach.Role[id] == repDepVia {
			vias = append(vias, id)
		}
	}
	victim := vias[len(vias)/2] // inside the stream: output has started when it fails
	data, err := l.s.casRead(victim)
	if err != nil {
		t.Fatal(err)
	}
	loc, ok := l.s.packLookup(victim)
	if !ok {
		t.Fatal("victim not packed")
	}
	st := l.s.packSnap()
	f, err := os.OpenFile(st.packs[loc.pack].path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	f.ReadAt(b, int64(loc.off)+3)
	b[0] ^= 0xff
	f.WriteAt(b, int64(loc.off)+3)
	f.Close()
	if _, err := l.s.casRead(victim); err == nil {
		t.Fatal("corrupt packed copy served")
	}
	// without another copy, the representation path fails (CDC still reads the object)
	_, man := l.manifest("db")
	rd, err := l.s.repOpenReader(man, byteRange{0, int64(len(db)) - 1})
	if err != nil || rd == nil {
		t.Fatalf("direct representation decoder: %v", err)
	}
	var got []byte
	for {
		p, err := rd.next()
		if err != nil {
			break
		}
		got = append(got, p...)
	}
	rd.close()
	if bytes.Equal(got, db) {
		t.Fatal("read succeeded through a corrupt copy")
	}
	// a second valid physical copy makes the existing fallback work for representation reads too
	p := l.s.chunkPath(victim)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, data, 0o644)
	rd, err = l.s.repOpenReader(man, byteRange{0, int64(len(db)) - 1})
	if err != nil || rd == nil {
		t.Fatalf("direct decoder after adding valid copy: %v", err)
	}
	if got := repCollectReader(t, rd); !bytes.Equal(got, db) {
		t.Fatal("copy fallback did not serve the valid loose copy")
	}
}

func TestRepConcurrentReadsMixedTiers(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	x := repTestSQLite(220, 4096, 231)
	rnd := make([]byte, 400<<10)
	rand.New(rand.NewSource(232)).Read(rnd)
	blocks := repTestBlocks(120, 2500, 233)
	car := repTestCAR(blocks)
	mk := repTestMerkleSnapshot(1, repTestKeys(1500, 234), nil)
	l.put("db", x)
	l.put("car", car)
	l.put("mk", mk)
	l.put("tarA", repTestTar(t, map[string][]byte{"x": x, "r": rnd}, []string{"x", "r"}))
	l.put("tarB", repTestTar(t, map[string][]byte{"x": x, "c": car}, []string{"x", "c"}))
	l.offline(func() {
		if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
			t.Fatal(err)
		}
	})
	ids := l.packIDs()
	l.offline(func() {
		for i, p := range ids {
			if to := []tier{tierWarm, tierCold, tierHot}[i%3]; to != tierHot {
				if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: to, IDs: []string{p.id}, Apply: true}); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	keys := make([]string, 0, len(l.want))
	for k := range l.want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 25; i++ {
				key := keys[r.Intn(len(keys))]
				want := l.want[key]
				_, man, err := l.s.HeadObject("b", key)
				if err != nil {
					errc <- err
					return
				}
				rng := byteRange{0, int64(len(want)) - 1}
				if g%3 == 1 {
					a := int64(r.Intn(len(want) - 1))
					rng = byteRange{a, min(a+int64(1+r.Intn(70000)), int64(len(want))-1)}
				}
				var rd objectReader
				if g%4 == 3 { // plain CDC reads alongside
					rd = l.s.newManifestReader(man, rng)
				} else if g%4 == 0 && rng.start == 0 && rng.end == man.TotalLength-1 {
					rd, err = l.s.repOpenReader(man, rng)
					if err != nil || rd == nil {
						errc <- fmt.Errorf("direct decoder %s: %v", key, err)
						return
					}
				} else {
					rd = l.s.openObjectReader(man, rng)
				}
				var got []byte
				for {
					b, err := rd.next()
					if err == io.EOF {
						break
					}
					if err != nil {
						errc <- fmt.Errorf("%s %v: %w", key, rng, err)
						rd.close()
						return
					}
					got = append(got, b...)
				}
				rd.close()
				if !bytes.Equal(got, want[rng.start:rng.end+1]) {
					errc <- fmt.Errorf("%s %v differs", key, rng)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	// catalog and root lookups racing with publication
	var wg2 sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg2.Add(1)
		go func(g int) {
			defer wg2.Done()
			l.s.repEnsure()
			l.s.rep.catMu.RLock()
			_ = l.s.rep.cat.records()
			l.s.rep.catMu.RUnlock()
			_ = l.s.repDescLookup([32]byte{byte(g)})
		}(g)
	}
	l.put("late", repTestSQLite(90, 4096, 235))
	wg2.Wait()
	l.exact("after concurrent reads")
}

func TestRepConcurrentPutsAndReads(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			data := repTestSQLite(120+g*10, 4096, int64(240+g))
			key := fmt.Sprintf("db%d", g)
			if _, err := l.s.PutObject("b", key, data, "x", nil); err != nil {
				errc <- err
				return
			}
			_, man, err := l.s.HeadObject("b", key)
			if err != nil {
				errc <- err
				return
			}
			rd, err := l.s.repOpenReader(man, byteRange{0, int64(len(data)) - 1})
			if err != nil || rd == nil {
				errc <- fmt.Errorf("direct decoder %s: %v", key, err)
				return
			}
			defer rd.close()
			var got []byte
			for {
				b, err := rd.next()
				if err == io.EOF {
					break
				}
				if err != nil {
					errc <- err
					return
				}
				got = append(got, b...)
			}
			if !bytes.Equal(got, data) {
				errc <- fmt.Errorf("%s differs", key)
			}
		}(g)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	if files, _ := l.s.repRootFiles(); len(files) != 6 {
		t.Fatalf("%d roots", len(files))
	}
}

// ---- hostile and look-alike objects --------------------------------------------------------------

func TestRepNegativeFormatControls(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	rnd := make([]byte, 50000)
	rand.New(rand.NewSource(251)).Read(rnd)
	fakeSQLite := append([]byte("SQLite format 3\x00"), rnd...)
	goodDB := repTestSQLite(40, 4096, 252)
	oddPage := append([]byte(nil), goodDB...)
	binary.BigEndian.PutUint16(oddPage[16:], 3000) // not a power of two
	cars := repTestCAR(repTestBlocks(20, 1500, 253))
	badCAR := append([]byte(nil), cars...)
	badCAR[len(badCAR)-100] ^= 0xff // a block that no longer matches its CID
	mk := repTestMerkleSnapshot(3, repTestKeys(300, 254), nil)
	cases := map[string][]byte{
		"random":            rnd,
		"fake sqlite":       fakeSQLite,
		"sqlite odd page":   oddPage,
		"sqlite truncated":  goodDB[:100],
		"malformed car":     badCAR,
		"car truncated":     cars[:len(cars)-7],
		"merkle truncated":  mk[:len(mk)-9],
		"merkle bad header": append([]byte("Z3SNAP1\x00\x20"), rnd[:200]...),
		"tar truncated":     repTestTar(t, map[string][]byte{"a": rnd}, []string{"a"})[:20000],
		"tar bad checksum":  func() []byte { b := repTestTar(t, map[string][]byte{"a": rnd}, []string{"a"}); b[148] = '9'; return b }(),
		"all zero":          make([]byte, 4096),
		"tiny":              []byte("x"),
	}
	for name, data := range cases {
		l.put("k-"+name, data)
		e, _ := l.manifest("k-" + name)
		if repHasRoot(l.s, e.manifestUUID) {
			t.Fatalf("%s: a root was published for a non-structure", name)
		}
	}
	if l.s.format.StoreFormatVersion != storeFormatVersion {
		t.Fatalf("no root was published, yet the format level moved to %d", l.s.format.StoreFormatVersion)
	}
	l.exact("controls")
}

func TestRepHostileObjectsNeverFailPut(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	base := map[string][]byte{
		"sqlite": repTestSQLite(30, 4096, 261),
		"car":    repTestCAR(repTestBlocks(24, 1200, 262)),
		"car2": func() []byte {
			b := repTestBlocks(24, 1200, 263)
			return repTestCARv2(repTestCAR(b), b)
		}(),
		"merkle": repTestMerkleSnapshot(1, repTestKeys(200, 264), nil),
		"tar":    repTestTar(t, map[string][]byte{"d.db": repTestSQLite(8, 4096, 265), "x": []byte("xx")}, []string{"d.db", "x"}),
	}
	r := rand.New(rand.NewSource(266))
	n := 0
	for name, b := range base {
		for i := 0; i < 40; i++ {
			m := append([]byte(nil), b...)
			switch i % 4 {
			case 0:
				m = m[:r.Intn(len(m))]
			case 1:
				m[r.Intn(len(m))] ^= byte(1 << r.Intn(8))
			case 2:
				at := r.Intn(len(m))
				m = append(m[:at:at], m[min(at+1+r.Intn(50), len(m)):]...)
			case 3:
				m = append(m, byte(r.Intn(256)))
			}
			if len(m) == 0 {
				continue
			}
			key := fmt.Sprintf("%s-%d", name, i)
			l.put(key, m)
			n++
		}
	}
	res := l.exact("hostile")
	t.Logf("%d hostile objects stored, %d kept an exact representation", n, res.RepresentationsVerified)
}

func TestRepAnalysisBudgets(t *testing.T) {
	var bd repBudget
	for i := 0; i < repMaxAttempts; i++ {
		if err := bd.attempt(); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := bd.attempt(); !errors.Is(err, errRepLimit) {
		t.Fatalf("attempt cap: %v", err)
	}
	if err := (&repBudget{}).inspect(repMaxInspect + 1); !errors.Is(err, errRepLimit) {
		t.Fatalf("inspect cap: %v", err)
	}
	if err := (&repBudget{}).inspect(repMaxInspect); err != nil {
		t.Fatalf("inspect at the cap: %v", err)
	}
	if err := (&repBudget{entriesAll: repMaxTarEntries}).entries(1); !errors.Is(err, errRepLimit) {
		t.Fatalf("entries cap: %v", err)
	}
	// 600 look-alike members: only repMaxMembers are ever analysed, the rest are stored plain
	l := newRepLab(t, repModePrefer)
	entries := map[string][]byte{}
	var order []string
	for i := 0; i < 600; i++ {
		name := fmt.Sprintf("f%03d", i)
		b := append([]byte("SQLite format 3\x00"), make([]byte, 2000)...)
		b[40] = byte(i)
		entries[name] = b
		order = append(order, name)
	}
	tarB := repTestTar(t, entries, order)
	start := time.Now()
	l.put("tar", tarB)
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("hostile tar took %v", d)
	}
	e, _ := l.manifest("tar")
	if !repHasRoot(l.s, e.manifestUUID) {
		t.Fatal("tar of plain members must still be represented structurally")
	}
	root := repMustRoot(t, l.s, e.manifestUUID)
	src := l.s.newRepSource(root.Descs, nil)
	rep, err := (&repEnv{Src: src, Limits: repProdLimits}).loadRep(root.Rep)
	src.close()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rep.Members {
		if m.Role == repDerived {
			t.Fatal("a look-alike became a derived child")
		}
	}
	l.exact("budgets")
}

// ---- late creation, retirement, journal independence, upgrades ------------------------------------

func TestRepLateCreationAndRetirementLeaveNamespaceUntouched(t *testing.T) {
	l := newRepLab(t, repModeOff)
	db := repTestSQLite(200, 4096, 271)
	l.put("db", db)
	e, man := l.manifest("db")
	if repHasRoot(l.s, e.manifestUUID) {
		t.Fatal("off mode built a representation")
	}
	journal := func() []byte {
		b, err := os.ReadFile(filepath.Join(l.dir, "journal", "visibility.log"))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	manifestBytes := func() []byte {
		b, _ := os.ReadFile(filepath.Join(l.dir, "manifests", e.manifestUUID+".json"))
		return b
	}
	j0, m0 := journal(), manifestBytes()
	listing := func() string {
		page, err := l.s.ListObjectsV2("b", "", "", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%+v", page)
	}
	ls0 := listing()
	// build the root for an already-live manifest (backfill primitive; no daemon)
	if err := l.s.repBuild(man); err != nil {
		t.Fatal(err)
	}
	if !repHasRoot(l.s, e.manifestUUID) || !bytes.Equal(journal(), j0) || !bytes.Equal(manifestBytes(), m0) || listing() != ls0 {
		t.Fatal("publishing a root changed the journal, the manifest or the listing")
	}
	l.mode = repModePrefer
	l.s.setRepMode(repModePrefer)
	l.exact("late root")
	if l.s.rep.reads.Load() == 0 {
		t.Fatal("late root not used")
	}
	// retire it: only optional metadata goes away
	if err := l.s.retireRepRoot(e.manifestUUID); err != nil {
		t.Fatal(err)
	}
	if repHasRoot(l.s, e.manifestUUID) || !bytes.Equal(journal(), j0) || !bytes.Equal(manifestBytes(), m0) || listing() != ls0 {
		t.Fatal("retiring a root changed authoritative state")
	}
	l.exact("retired")
	// no namespace version was created by either step
	vs, _, err := l.s.ListVersions("b", "db")
	if err != nil || len(vs) != 0 {
		t.Fatalf("versions: %v %v", vs, err)
	}
	// a stale on-disk reader sees either the old root, the new root, or none; never a partial file
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errc := make(chan error, 4)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := repReadAll(t, l.s, man, byteRange{0, int64(len(db)) - 1})
				if !bytes.Equal(got, db) {
					errc <- errors.New("torn read")
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		if err := l.s.repBuild(man); err != nil {
			t.Error(err)
		}
		if err := l.s.retireRepRoot(e.manifestUUID); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

func TestRepCrashAfterFormatRaiseBeforeFirstRoot(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	db := repTestSQLite(150, 4096, 281)
	withTestHook(t, func(p string) {
		if p == hookRepRootAfterFormat {
			panic(simulatedCrash{point: p})
		}
	})
	runExpectingSimulatedCrash(t, func() { _, _ = l.s.PutObject("b", "db", db, "x", nil) })
	testHook = nil
	l.close()
	l.open()
	if l.s.format.StoreFormatVersion != storeFormatVersionRepresentations {
		t.Fatalf("format %d: the level is never rolled back", l.s.format.StoreFormatVersion)
	}
	if len(repRootFilesOf(t, l)) != 0 {
		t.Fatal("a root appeared")
	}
	l.put("ok", db)
	l.exact("level 6, first root later")
}

func TestRepUpgradeLevelsDoNotMoveUntilFirstRoot(t *testing.T) {
	version := func(dir string) int {
		b, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
		if err != nil {
			t.Fatal(err)
		}
		var f storeFormat
		json.Unmarshal(b, &f)
		return f.StoreFormatVersion
	}
	db := repTestSQLite(160, 4096, 291)
	for _, want := range []int{storeFormatVersion, storeFormatVersionPacked, storeFormatVersionCompressed, storeFormatVersionHistoryPrune, storeFormatVersionTiers} {
		t.Run(fmt.Sprintf("level-%d", want), func(t *testing.T) {
			l := newRepLab(t, repModeOff)
			l.put("old", db)
			text := bytes.Repeat([]byte("compressible old object, "), 40000)
			switch want {
			case storeFormatVersionPacked:
				l.offline(func() { compactTestDir(t, l.dir) })
			case storeFormatVersionCompressed:
				l.put("text", text)
				l.offline(func() { compactTestDirWith(t, l.dir, repSmallCompact()) })
			case storeFormatVersionHistoryPrune:
				l.put("text", text)
				l.put("text", append(text, 'x'))
				l.offline(func() {
					zero := 0
					if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "text", KeepLast: &zero}, true); err != nil {
						t.Fatal(err)
					}
				})
			case storeFormatVersionTiers:
				l.offline(func() {
					compactTestDir(t, l.dir)
					if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: tierWarm, All: true, Apply: true}); err != nil {
						t.Fatal(err)
					}
				})
			}
			if got := version(l.dir); got != want {
				t.Fatalf("setup produced level %d, wanted %d", got, want)
			}
			// the new binary opens the old store in every mode without moving the level
			for _, m := range []repMode{repModeOff, repModeShadow, repModePrefer} {
				l.mode = m
				l.offline(func() {})
				l.exact("old store, mode " + m.String())
				if got := version(l.dir); got != want {
					t.Fatalf("mode %s moved the level %d -> %d", m, want, got)
				}
			}
			// the first valid root raises it to 6, once
			l.mode = repModePrefer
			l.offline(func() {})
			l.put("new", repTestSQLite(130, 4096, 292))
			if got := version(l.dir); got != storeFormatVersionRepresentations {
				t.Fatalf("level after the first root: %d", got)
			}
			l.exact("upgraded")
			// turning creation off does not downgrade, and roots stay understood
			l.mode = repModeOff
			l.offline(func() {})
			l.exact("off on a level-6 store")
			if got := version(l.dir); got != storeFormatVersionRepresentations {
				t.Fatalf("level fell to %d", got)
			}
			rr := l.reach()
			if rr.RepRoots != 1 {
				t.Fatalf("roots understood in off mode: %d", rr.RepRoots)
			}
		})
	}
}

// off mode on a level-6 store: maintenance still knows the roots and must not destroy them.
func TestRepOffModeMaintenanceKeepsRoots(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	l.put("db", repTestSQLite(250, 4096, 301))
	l.put("car", repTestCAR(repTestBlocks(100, 3000, 302)))
	l.mode = repModeOff
	l.offline(func() {})
	steps := []struct {
		name string
		run  func()
	}{
		{"gc", func() {
			if _, err := gcCollect(l.dir, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"compact", func() {
			if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
				t.Fatal(err)
			}
		}},
		{"repack", func() {
			if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"tier", func() {
			if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: tierCold, All: true, Apply: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"rebalance", func() {
			if _, err := tierRebalance(l.dir, rebalanceOptions{Apply: true, TargetBytes: 1 << 20, Compress: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"gc again", func() {
			if _, err := gcCollect(l.dir, true); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, st := range steps {
		l.offline(st.run)
		res := l.exact(st.name)
		if res.RepresentationsVerified != 2 {
			t.Fatalf("%s: %d representations survive", st.name, res.RepresentationsVerified)
		}
	}
	// reads in off mode never touch the representation
	_, man := l.manifest("db")
	_ = repReadAll(t, l.s, man, byteRange{0, 1000})
	if l.s.rep.reads.Load()+l.s.rep.rangeReads.Load() != 0 {
		t.Fatal("off mode read through a representation")
	}
}

// ---- S3 behaviour is identical in every mode ----------------------------------------------------------

var repHTTPHeaders = []string{"Content-Length", "Etag", "Content-Type", "Accept-Ranges", "Content-Range", "X-Amz-Meta-Tag", "X-Amz-Checksum-Crc32"}

// repS3Transcript runs one fixed workload against a server and returns its
// observable transcript (status, stable headers, body digest per operation).
func repS3Transcript(t *testing.T, mode repMode) []string {
	t.Helper()
	store := newTestStore(t)
	store.setRepMode(mode)
	srv, signer := newTestServerAndSignerFor(t, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	var out []string
	do := func(label, method, path string, body []byte, hdr map[string]string) []byte {
		resp := doSignedRequest(t, client, ts.URL, signer, method, path, body, hdr)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		line := fmt.Sprintf("%s: %d", label, resp.StatusCode)
		for _, h := range repHTTPHeaders {
			if v := resp.Header.Get(h); v != "" {
				line += fmt.Sprintf(" %s=%s", h, v)
			}
		}
		if strings.Contains(resp.Header.Get("Content-Type"), "xml") && resp.StatusCode >= 300 {
			line += " body=" + regexp.MustCompile(`<Code>([^<]*)</Code>`).FindString(string(b))
		} else if strings.Contains(label, "list") {
			line += fmt.Sprintf(" body=%x", sha256.Sum256(regexp.MustCompile(`<LastModified>[^<]*</LastModified>`).ReplaceAll(b, nil)))
		} else if method == http.MethodGet || method == http.MethodHead {
			line += fmt.Sprintf(" body=%x", sha256.Sum256(b))
		}
		out = append(out, line)
		return b
	}
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	db := repTestSQLite(250, 4096, 311)
	cars := repTestCAR(repTestBlocks(80, 3000, 312))
	mk := repTestMerkleSnapshot(1, repTestKeys(800, 313), nil)
	tarB := repTestTar(t, map[string][]byte{"d.db": db, "c.car": cars, "n": []byte("note")}, []string{"d.db", "c.car", "n"})
	objs := map[string][]byte{"a.db": db, "b.car": cars, "c.snap": mk, "d.tar": tarB, "e.txt": []byte("plain text object\n")}
	names := []string{"a.db", "b.car", "c.snap", "d.tar", "e.txt"}
	for _, n := range names {
		do("put "+n, http.MethodPut, "/b/"+n, objs[n], map[string]string{"Content-Type": "application/x-test", "X-Amz-Meta-Tag": "m-" + n})
	}
	for _, n := range names {
		do("get "+n, http.MethodGet, "/b/"+n, nil, nil)
		do("head "+n, http.MethodHead, "/b/"+n, nil, nil)
		size := len(objs[n])
		for _, rg := range []string{"bytes=0-99", "bytes=4096-12287", fmt.Sprintf("bytes=%d-", size-500), "bytes=-300", fmt.Sprintf("bytes=%d-%d", size/3, size/3+70000), fmt.Sprintf("bytes=%d-", size+5)} {
			do("range "+n+" "+rg, http.MethodGet, "/b/"+n, nil, map[string]string{"Range": rg})
		}
	}
	etag := ""
	{
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/a.db", nil, nil)
		etag = resp.Header.Get("Etag")
		resp.Body.Close()
	}
	do("cond match", http.MethodGet, "/b/a.db", nil, map[string]string{"If-Match": etag})
	do("cond none-match", http.MethodGet, "/b/a.db", nil, map[string]string{"If-None-Match": etag})
	do("cond mismatch", http.MethodGet, "/b/a.db", nil, map[string]string{"If-Match": `"deadbeef"`})
	do("put if-none-match", http.MethodPut, "/b/a.db", db, map[string]string{"If-None-Match": "*"})
	sum := md5.Sum(db)
	do("put md5 ok", http.MethodPut, "/b/md5ok.db", db, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])})
	do("put md5 bad", http.MethodPut, "/b/md5bad.db", db, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(make([]byte, 16))})
	do("copy", http.MethodPut, "/b/copy.db", nil, map[string]string{"X-Amz-Copy-Source": "/b/a.db"})
	do("get copy", http.MethodGet, "/b/copy.db", nil, nil)
	do("overwrite", http.MethodPut, "/b/a.db", repTestSQLiteUpdate(db, 4096, []int{4, 5}, 314), nil)
	do("get overwritten", http.MethodGet, "/b/a.db", nil, nil)
	do("list", http.MethodGet, "/b?list-type=2", nil, nil)
	do("list prefix", http.MethodGet, "/b?list-type=2&prefix=c&delimiter=.", nil, nil)
	// multipart completion
	initResp := do("mp init", http.MethodPost, "/b/mp.db?uploads", nil, nil)
	var up struct {
		UploadId string `xml:"UploadId"`
	}
	xml.Unmarshal(initResp, &up)
	mpData := repTestSQLite(1600, 4096, 315)
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/mp.db?partNumber=1&uploadId="+up.UploadId, mpData, nil)
	petag := resp.Header.Get("Etag")
	resp.Body.Close()
	complete := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, petag)
	do("mp complete", http.MethodPost, "/b/mp.db?uploadId="+up.UploadId, []byte(complete), nil)
	do("get mp", http.MethodGet, "/b/mp.db", nil, nil)
	do("delete", http.MethodDelete, "/b/e.txt", nil, nil)
	do("get deleted", http.MethodGet, "/b/e.txt", nil, nil)
	do("get missing", http.MethodGet, "/b/nope", nil, nil)
	return out
}

func TestRepS3TranscriptIdenticalInEveryMode(t *testing.T) {
	off := repS3Transcript(t, repModeOff)
	if len(off) < 60 {
		t.Fatalf("workload too small: %d operations", len(off))
	}
	if testing.Verbose() {
		for _, l := range off[:12] {
			t.Log(l)
		}
		for _, l := range off[len(off)-18:] {
			t.Log(l)
		}
	}
	for _, m := range []repMode{repModeShadow, repModePrefer} {
		got := repS3Transcript(t, m)
		if len(got) != len(off) {
			t.Fatalf("%s: %d operations, off had %d", m, len(got), len(off))
		}
		for i := range off {
			a, b := off[i], got[i]
			if a != b {
				// ETags of multipart parts and uuid-derived values are deterministic; nothing else may differ
				t.Fatalf("%s: operation %d differs\n off:  %s\n this: %s", m, i, a, b)
			}
		}
	}
}

// ---- measurements (reported in testing-harnesses/results/Z3_I0_PRODUCTION_REPRESENTATIONS.md) --------------------
//
// ZEROS3_Z3_I0_MEASURE=1 go test -run TestRepMeasure -v -timeout 3000s .

type repWorkload struct {
	name string
	revs [][]byte
}

func repMeasureWorkloads(t *testing.T) []repWorkload {
	var ws []repWorkload
	// SQLite sparse-update history: 24 MiB, 40 pages change per revision
	db := repTestSQLite(6000, 4096, 401)
	w := repWorkload{name: "SQLite 24 MiB sparse updates"}
	cur := db
	for r := 0; r < 5; r++ {
		w.revs = append(w.revs, cur)
		var pages []int
		for i := 0; i < 40; i++ {
			pages = append(pages, (r*1543+i*149+7)%6000)
		}
		cur = repTestSQLiteUpdate(cur, 4096, pages, int64(410+r))
	}
	ws = append(ws, w)
	// CAR: reorder and add
	blocks := repTestBlocks(2000, 3000, 402)
	w = repWorkload{name: "CAR 6 MiB reorder/add"}
	cur2 := blocks
	for r := 0; r < 5; r++ {
		w.revs = append(w.revs, repTestCAR(cur2))
		next := append([][]byte(nil), cur2...)
		rr := rand.New(rand.NewSource(int64(420 + r)))
		rr.Shuffle(len(next), func(i, j int) { next[i], next[j] = next[j], next[i] })
		next = append(next[:len(next):len(next)], repTestBlocks(50, 3000, int64(430+r))...)
		cur2 = next
	}
	ws = append(ws, w)
	// tar -> SQLite and tar -> CAR
	w = repWorkload{name: "tar -> SQLite (8 MiB db)"}
	small := repTestSQLite(2000, 4096, 403)
	cur = small
	for r := 0; r < 4; r++ {
		w.revs = append(w.revs, repTestTar(t, map[string][]byte{"app.db": cur, "README": []byte(fmt.Sprintf("revision %d\n", r))}, []string{"README", "app.db"}))
		cur = repTestSQLiteUpdate(cur, 4096, []int{r*31 + 3, r*57 + 900, r*11 + 1500}, int64(440+r))
	}
	ws = append(ws, w)
	w = repWorkload{name: "tar -> CAR (4 MiB)"}
	cb := repTestBlocks(1300, 3000, 404)
	for r := 0; r < 4; r++ {
		w.revs = append(w.revs, repTestTar(t, map[string][]byte{"data.car": repTestCAR(cb), "meta": []byte(fmt.Sprintf("m%d", r))}, []string{"meta", "data.car"}))
		cb = append(cb[:len(cb):len(cb)], repTestBlocks(20, 3000, int64(450+r))...)
	}
	ws = append(ws, w)
	// Merkle revision chain
	keys := repTestKeys(20000, 405)
	ver := map[uint64]int{}
	w = repWorkload{name: "Merkle 20k keys, sha3-256"}
	for r := 0; r < 5; r++ {
		for i := 0; i < 60; i++ {
			ver[keys[(r*7919+i*271)%len(keys)]]++
		}
		w.revs = append(w.revs, repTestMerkleSnapshot(1, append([]uint64(nil), keys...), ver))
	}
	ws = append(ws, w)
	return ws
}

type repTrace struct{ chunks, stored, opens, readAts int64 }

func repStoreBytes(t *testing.T, s *Store) StatsResult {
	st, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRepMeasure(t *testing.T) {
	if os.Getenv("ZEROS3_Z3_I0_MEASURE") == "" {
		t.Skip("set ZEROS3_Z3_I0_MEASURE=1")
	}
	ws := repMeasureWorkloads(t)
	fmt.Println("\n### storage (stats; bytes)")
	fmt.Println("| workload | revs | object MiB | CDC only (loose) | CDC+rep (loose) | rep marginal | rep chunks | root bytes | CDC only (compacted) | CDC+rep (compacted) | marginal compacted | loose files rep | pack records rep |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	type pair struct{ off, on *repLab }
	stores := map[string]pair{}
	for _, w := range ws {
		var p pair
		p.off = newRepLab(t, repModeOff)
		p.on = newRepLab(t, repModeShadow)
		var objMiB float64
		for r, rev := range w.revs {
			p.off.put(fmt.Sprintf("rev%d", r), rev)
			p.on.put(fmt.Sprintf("rev%d", r), rev)
			objMiB = float64(len(rev)) / (1 << 20)
		}
		a, b := repStoreBytes(t, p.off.s), repStoreBytes(t, p.on.s)
		// loose files that only the representation owns
		compactCfg := defaultCompactOptions()
		compactCfg.TargetBytes, compactCfg.MinBytes = 4<<20, 1
		for _, l := range []*repLab{p.off, p.on} {
			l.offline(func() {
				if _, err := compactStore(l.dir, compactCfg); err != nil {
					t.Fatal(err)
				}
			})
		}
		a2, b2 := repStoreBytes(t, p.off.s), repStoreBytes(t, p.on.s)
		fmt.Printf("| %s | %d | %.1f | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n", w.name, len(w.revs), objMiB,
			a.ChunkStoreFileBytes+a.ManifestFileBytes, b.ChunkStoreFileBytes+b.ManifestFileBytes+b.RepresentationMetadataFileBytes,
			b.ChunkStoreFileBytes-a.ChunkStoreFileBytes, b.RepresentationReachableChunkCount, b.RepresentationMetadataFileBytes,
			a2.ChunkStoreFileBytes+a2.ManifestFileBytes, b2.ChunkStoreFileBytes+b2.ManifestFileBytes+b2.RepresentationMetadataFileBytes,
			b2.ChunkStoreFileBytes-a2.ChunkStoreFileBytes, b.LooseChunkCount-a.LooseChunkCount, b2.PackedChunkCount-a2.PackedChunkCount)
		stores[w.name] = p
	}
	fmt.Println("\n### per-revision representation marginal (rep-reachable chunk bytes added by the revision)")
	for _, w := range ws {
		l := newRepLab(t, repModeShadow)
		prev := int64(0)
		var row []string
		for r, rev := range w.revs {
			l.put(fmt.Sprintf("rev%d", r), rev)
			st := repStoreBytes(t, l.s)
			row = append(row, fmt.Sprint(st.RepresentationReachableChunkBytes-prev))
			prev = st.RepresentationReachableChunkBytes
		}
		fmt.Printf("%s: %s\n", w.name, strings.Join(row, " / "))
	}
	fmt.Println("\n### derived state rebuild (catalog + descriptor union), heap delta after a forced GC")
	fmt.Println("| store | roots | rebuild ms | catalog entries | catalog records | sections | heap delta KiB |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, w := range ws {
		l := stores[w.name].on
		l.mode = repModePrefer
		l.offline(func() {})
		l.s.repInvalidate()
		l.s.rep.cat, l.s.rep.desc = nil, nil
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		t0 := time.Now()
		l.s.repEnsure()
		d := time.Since(t0)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		files, _ := l.s.repRootFiles()
		fmt.Printf("| %s | %d | %.1f | %d | %d | %d | %d |\n", w.name, len(files), float64(d.Microseconds())/1000,
			l.s.rep.cat.entries(), l.s.rep.cat.records(), l.s.rep.cat.nSections(), (int64(m1.HeapAlloc)-int64(m0.HeapAlloc))>>10)
	}
	fmt.Println("\n### root discovery with many small roots (12-page SQLite objects, shadow mode)")
	fmt.Println("| roots | OpenStore ms | repEnsure ms | reachability ms | verify (basic) ms |")
	fmt.Println("|---|---|---|---|---|")
	for _, n := range []int{0, 100, 1000} {
		l := newRepLab(t, repModeShadow)
		for i := 0; i < n; i++ {
			l.put(fmt.Sprintf("s%04d", i), repTestSQLite(12, 4096, int64(600+i)))
		}
		l.close()
		t0 := time.Now()
		l.open()
		openD := time.Since(t0)
		t0 = time.Now()
		l.s.repEnsure()
		ensureD := time.Since(t0)
		t0 = time.Now()
		if _, err := l.s.computeReachability(false); err != nil {
			t.Fatal(err)
		}
		reachD := time.Since(t0)
		t0 = time.Now()
		l.s.Verify(false)
		verD := time.Since(t0)
		fmt.Printf("| %d | %.1f | %.1f | %.1f | %.1f |\n", n, float64(openD.Microseconds())/1000, float64(ensureD.Microseconds())/1000, float64(reachD.Microseconds())/1000, float64(verD.Microseconds())/1000)
	}
	fmt.Println("\n### reads on the compacted store (median of 5; CDC reads vs representation reads)")
	fmt.Println("| workload (last revision) | read | CDC ms | CDC chunks | CDC stored B | CDC pack opens | rep ms | rep chunks | rep stored B | rep pack opens | time ratio |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, w := range ws {
		l := stores[w.name].on
		l.mode = repModePrefer
		l.offline(func() {})
		key := fmt.Sprintf("rev%d", len(w.revs)-1)
		_, man := l.manifest(key)
		size := man.TotalLength
		reads := []struct {
			name string
			rng  byteRange
		}{{"whole", byteRange{0, size - 1}}, {"4 KiB", byteRange{size/2 + 17, size/2 + 17 + 4095}}, {"64 KiB", byteRange{size/3 + 5, size/3 + 5 + 65535}}, {"1 MiB", byteRange{size / 5, size/5 + (1 << 20) - 1}}}
		for _, rd := range reads {
			if rd.rng.end >= size {
				continue
			}
			run := func(rep bool) (time.Duration, repTrace) {
				var best []time.Duration
				var tr repTrace
				for i := 0; i < 5; i++ {
					l.s.packTrace = &packTrace{}
					start := time.Now()
					var r objectReader
					if rep {
						var err error
						var o objectReader
						if o, err = l.s.repOpenReader(man, rd.rng); o == nil || err != nil {
							best = append(best, 0)
							continue
						}
						r = o
					} else {
						r = l.s.newManifestReader(man, rd.rng)
					}
					var n int64
					for {
						b, err := r.next()
						if err != nil {
							break
						}
						n += int64(len(b))
					}
					r.close()
					best = append(best, time.Since(start))
					if n != rd.rng.end-rd.rng.start+1 {
						t.Fatalf("%s %s: short read %d", w.name, rd.name, n)
					}
					tr = repTrace{l.s.packTrace.Chunks.Load(), l.s.packTrace.StoredBytes.Load(), l.s.packTrace.Opens.Load(), l.s.packTrace.ReadAts.Load()}
				}
				sort.Slice(best, func(i, j int) bool { return best[i] < best[j] })
				l.s.packTrace = nil
				return best[len(best)/2], tr
			}
			cd, ct := run(false)
			rdur, rt := run(true)
			ratio := "n/a (CDC)"
			if rdur > 0 {
				ratio = fmt.Sprintf("%.2fx", float64(rdur)/float64(cd))
			}
			fmt.Printf("| %s | %s | %.2f | %d | %d | %d | %.2f | %d | %d | %d | %s |\n", w.name, rd.name,
				float64(cd.Microseconds())/1000, ct.chunks, ct.stored, ct.opens,
				float64(rdur.Microseconds())/1000, rt.chunks, rt.stored, rt.opens, ratio)
		}
	}
}

func benchRepPacked(b *testing.B, data []byte, rng func(size int64) byteRange) {
	dir := b.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	s.setRepMode(repModePrefer)
	s.CreateBucket("b")
	if _, err := s.PutObject("b", "obj", data, "x", nil); err != nil {
		b.Fatal(err)
	}
	s.Close()
	o := defaultCompactOptions()
	o.TargetBytes, o.MinBytes = 4<<20, 1
	if _, err := compactStore(dir, o); err != nil {
		b.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	s.setRepMode(repModePrefer)
	_, man, _ := s.HeadObject("b", "obj")
	r := rng(man.TotalLength)
	b.SetBytes(r.end - r.start + 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rd, err := s.repOpenReader(man, r)
		if err != nil || rd == nil {
			b.Fatalf("direct decoder: %v", err)
		}
		for {
			if _, err := rd.next(); err != nil {
				break
			}
		}
		rd.close()
	}
}

func BenchmarkRepPackedWholeSQLite(b *testing.B) {
	benchRepPacked(b, repTestSQLite(6000, 4096, 401), func(n int64) byteRange { return byteRange{0, n - 1} })
}

func BenchmarkRepPackedWholeCAR(b *testing.B) {
	benchRepPacked(b, repTestCAR(repTestBlocks(2000, 3000, 402)), func(n int64) byteRange { return byteRange{0, n - 1} })
}

func BenchmarkRepPackedWholeMerkle(b *testing.B) {
	benchRepPacked(b, repTestMerkleSnapshot(1, repTestKeys(20000, 405), nil), func(n int64) byteRange { return byteRange{0, n - 1} })
}

func BenchmarkCDCPackedWholeSQLite(b *testing.B) {
	dir := b.TempDir()
	s, _ := OpenStore(dir)
	s.CreateBucket("b")
	data := repTestSQLite(6000, 4096, 401)
	s.PutObject("b", "obj", data, "x", nil)
	s.Close()
	o := defaultCompactOptions()
	o.TargetBytes, o.MinBytes = 4<<20, 1
	compactStore(dir, o)
	s, _ = OpenStore(dir)
	defer s.Close()
	_, man, _ := s.HeadObject("b", "obj")
	b.SetBytes(man.TotalLength)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rd := s.newManifestReader(man, byteRange{0, man.TotalLength - 1})
		for {
			if _, err := rd.next(); err != nil {
				break
			}
		}
		rd.close()
	}
}

func TestRepStreamWriterBatching(t *testing.T) {
	// regression: a write that ends exactly on a piece boundary must not stall the writer
	sizes := []int{1, repStreamPiece - 1, 1, repStreamPiece, repStreamPiece, 3 * repStreamPiece, 17, 2*repStreamPiece + 5, repStreamPiece, 0}
	var want []byte
	r := rand.New(rand.NewSource(1))
	var chunks [][]byte
	for _, n := range sizes {
		b := make([]byte, n)
		r.Read(b)
		chunks = append(chunks, b)
		want = append(want, b...)
	}
	rd := startRepStream(&storeRepSource{}, func(w io.Writer) error {
		for _, c := range chunks {
			if _, err := w.Write(c); err != nil {
				return err
			}
		}
		return nil
	})
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			b, err := rd.next()
			if err != nil {
				return
			}
			got = append(got, b...)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("stream stalled")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream differs: %d of %d bytes", len(got), len(want))
	}
	// cancellation: closing early stops a producer that has more to say
	rd = startRepStream(&storeRepSource{}, func(w io.Writer) error {
		for i := 0; i < 1000; i++ {
			if _, err := w.Write(make([]byte, repStreamPiece)); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := rd.next(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { rd.close(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("close did not stop the producer")
	}
}

// TestRepKindLifecycleMatrix runs every integrated kind (and the tar->kind
// compositions) through loose, compacted, repacked, tier-spread and restarted
// states; each stage checks CDC, representation and deep verification.
func TestRepKindLifecycleMatrix(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	fx := repFixtures(t)
	for _, f := range fx {
		l.put(f.name, f.data)
	}
	check := func(stage string) {
		res := l.exact(stage)
		// tar-fake-sqlite is still a valid tar (structural), so every fixture has a root
		if res.RepresentationsVerified != len(fx) {
			t.Fatalf("%s: %d of %d representations verified", stage, res.RepresentationsVerified, len(fx))
		}
	}
	check("loose")
	l.offline(func() {
		if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
			t.Fatal(err)
		}
	})
	check("compact")
	l.offline(func() {
		if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true}); err != nil {
			t.Fatal(err)
		}
	})
	check("repack")
	ids := l.packIDs()
	l.offline(func() {
		for i, p := range ids {
			if to := []tier{tierWarm, tierCold, tierHot}[i%3]; to != tierHot {
				if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: to, IDs: []string{p.id}, Apply: true}); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	check("mixed tiers")
	l.offline(func() {})
	check("restart")
	l.offline(func() {
		if _, err := tierRebalance(l.dir, rebalanceOptions{Apply: true, TargetBytes: 1 << 20, Compress: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	check("rebalance + gc")
}

// Large objects stream their components into immutable packs, like an ordinary
// large ingest, instead of creating one loose file per component.
func TestRepLargeObjectComponentsGoToPacks(t *testing.T) {
	oldMin, oldTarget := directPackMinBytes, directPackTarget
	directPackMinBytes, directPackTarget = 1<<20, 1<<20
	t.Cleanup(func() { directPackMinBytes, directPackTarget = oldMin, oldTarget })
	l := newRepLab(t, repModePrefer)
	db := repTestSQLite(3000, 4096, 501)
	l.put("db", db)
	st := repStoreBytes(t, l.s)
	if st.PackCount == 0 || st.RepresentationComponentCount < 3000 {
		t.Fatalf("packs=%d components=%d", st.PackCount, st.RepresentationComponentCount)
	}
	if st.LooseChunkCount > 100 {
		t.Fatalf("%d loose files: components were not streamed into packs", st.LooseChunkCount)
	}
	l.exact("pack sink")
	l.offline(func() {})
	l.exact("restart")
	// a tar with a SQLite child proves the child through packs published mid-analysis
	tarT := repTestTar(t, map[string][]byte{"app.db": db, "x": []byte("x")}, []string{"x", "app.db"})
	l.put("tar", tarT)
	l.exact("tar with a packed child")
}

// A section above 256 KiB is a virtual component; a later revision that reuses it
// carries its descriptor, so removing the revision that created it changes nothing.
func TestRepMerkleVirtualSectionReuse(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	keys := repTestKeys(20000, 701)
	ver := map[uint64]int{}
	for r := 0; r < 3; r++ {
		for i := 0; i < 40; i++ {
			ver[keys[(r*977+i*53)%len(keys)]]++
		}
		l.put(fmt.Sprintf("rev%d", r), repTestMerkleSnapshot(1, append([]uint64(nil), keys...), ver))
	}
	e0, _ := l.manifest("rev0")
	e2, _ := l.manifest("rev2")
	r0, r2 := repMustRoot(t, l.s, e0.manifestUUID), repMustRoot(t, l.s, e2.manifestUUID)
	big := 0
	for id, d := range r0.Descs {
		big++
		if r2.Descs[id] == nil {
			t.Fatalf("revision 2 reuses section %x but carries no descriptor for it", id[:6])
		}
		_ = d
	}
	if big == 0 {
		t.Fatal("expected a virtual section in the first revision")
	}
	l.exact("three revisions")
	for _, k := range []string{"rev0", "rev1"} {
		if err := l.s.DeleteObject("b", k); err != nil {
			t.Fatal(err)
		}
		delete(l.want, k)
	}
	l.offline(func() {
		zero := 0
		for _, k := range []string{"rev0", "rev1"} {
			if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: k, KeepLast: &zero}, true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("after removing the revision that created the section")
}

// ---- substrate over a real store: hand-published root, Source, reachability, format 6 ----------------

// repHandPublish stores data as a Concat representation by hand (no analyzer): a
// small direct component, one virtual component of CDC chunks, and a direct tail.
func repHandPublish(t *testing.T, s *Store, bucket, key string, data []byte) (uuid string, root *repRoot) {
	t.Helper()
	if _, err := s.PutObject(bucket, key, data, "x", nil); err != nil {
		t.Fatal(err)
	}
	e, man := repManifestOf(t, s, bucket, key)
	cuts := []int{5000, 5000 + 300<<10, len(data)}
	rep := &repObject{Kind: repKindConcat, Size: int64(len(data))}
	descs := map[[32]byte]*repDesc{}
	prev := 0
	for _, cut := range cuts {
		part := data[prev:cut]
		prev = cut
		sum := sha256.Sum256(part)
		if len(part) <= repDirectMax {
			if _, err := s.casWrite(part); err != nil {
				t.Fatal(err)
			}
		} else {
			d := &repDesc{Size: int64(len(part))}
			ch := newCDCChunker(bytes.NewReader(part))
			for {
				c, err := ch.nextView()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				cs, err := s.casWrite(c)
				if err != nil {
					t.Fatal(err)
				}
				d.Refs = append(d.Refs, repChunkRef{SHA: cs, Len: uint32(len(c))})
			}
			descs[sum] = d
		}
		rep.Members = append(rep.Members, repMember{Role: repStored, SHA: sum, Size: int64(len(part))})
	}
	raw, err := rep.marshal()
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.casWrite(raw)
	if err != nil {
		t.Fatal(err)
	}
	objSHA, _ := decodeHexSHA256(man.ObjectSHA256)
	root = &repRoot{ManifestUUID: e.manifestUUID, ObjSHA: objSHA, ObjSize: int64(len(data)), Rep: id, Descs: descs}
	if err := s.publishRepRoot(root); err != nil {
		t.Fatal(err)
	}
	return e.manifestUUID, root
}

func TestRepSubstrateStoreRoundTrip(t *testing.T) {
	l := newRepLab(t, repModeOff)
	data := make([]byte, 5000+300<<10+4000)
	rand.New(rand.NewSource(801)).Read(data)
	if l.s.format.StoreFormatVersion != storeFormatVersion {
		t.Fatalf("fresh store level %d", l.s.format.StoreFormatVersion)
	}
	uuid, root := repHandPublish(t, l.s, "b", "obj", data)
	// the first root raised the store to level 6, creating the level-5 tier roots it implies
	if l.s.format.StoreFormatVersion != storeFormatVersionRepresentations {
		t.Fatalf("level after the first root: %d", l.s.format.StoreFormatVersion)
	}
	for _, tr := range []tier{tierWarm, tierCold} {
		if err := checkTierRoot(l.dir, l.s.format.StoreID, tr); err != nil {
			t.Fatal(err)
		}
	}
	if len(root.Descs) != 1 {
		t.Fatalf("descriptors: %d", len(root.Descs))
	}
	// the Source resolves direct and virtual components through the production CAS
	src := l.s.newRepSource(root.Descs, nil)
	env := &repEnv{Src: src, Limits: repProdLimits}
	var out bytes.Buffer
	n, sum, err := env.reconstruct(root.Rep, &out)
	src.close()
	if err != nil || n != int64(len(data)) || sum != sha256.Sum256(data) || !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("reconstruct: n=%d err=%v", n, err)
	}
	// reachability: every component chunk is live because the manifest's root names it
	rr := l.reach()
	if rr.RepRoots != 1 || rr.RepComponents != 3 || rr.RepObjects != 1 {
		t.Fatalf("reach: roots=%d objects=%d components=%d", rr.RepRoots, rr.RepObjects, rr.RepComponents)
	}
	for _, d := range root.Descs {
		for _, r := range d.Refs {
			if !rr.ReferencedChunks[hex.EncodeToString(r.SHA[:])] {
				t.Fatal("virtual component chunk not live")
			}
		}
	}
	// the component chunks are not CDC chunks of the object: without the root they would be garbage
	if err := l.s.retireRepRoot(uuid); err != nil {
		t.Fatal(err)
	}
	if rr2 := l.reach(); rr2.RepRoots != 0 || len(rr2.ReferencedChunks) >= len(rr.ReferencedChunks) {
		t.Fatalf("retiring the root did not release its components (%d -> %d)", len(rr.ReferencedChunks), len(rr2.ReferencedChunks))
	}
	// republish, damage the file: reads fall back, destructive maintenance fails closed
	if err := l.s.publishRepRoot(root); err != nil {
		t.Fatal(err)
	}
	repCorruptFile(t, l.s.repRootPath(uuid), func(b []byte) []byte { b[9] ^= 1; return b })
	rr3, err := l.s.computeReachability(false)
	if err != nil || rr3.OK() || rr3.RepFaults[uuid] == "" {
		t.Fatalf("a corrupt live root must make the live set untrusted: ok=%v faults=%v", rr3.OK(), rr3.RepFaults)
	}
	l.close()
	if _, err := gcCollect(l.dir, true); !errors.Is(err, errGCUnsafe) {
		t.Fatalf("gc -apply: %v", err)
	}
	l.open()
	_, man := l.manifest("obj")
	if got := repReadAll(t, l.s, man, byteRange{0, int64(len(data)) - 1}); !bytes.Equal(got, data) {
		t.Fatal("object unreadable")
	}
}

// Portable bundles stay CDC-authoritative: no representation state travels, the
// restored logical content is complete, and representations can be rebuilt later.
func TestRepBundlesCarryOnlyAuthoritativeState(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	fx := repFixtures(t)[:4]
	for _, f := range fx {
		l.put(f.name, f.data)
	}
	entries, err := l.s.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	snap := newUUIDv7()
	if err := l.s.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: snap,
		CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}); err != nil {
		t.Fatal(err)
	}
	distinct := map[string]bool{}
	for _, e := range entries {
		m, _, err := l.s.readManifest(e.ManifestUUID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range m.Chunks {
			distinct[c.SHA256] = true
		}
	}
	if len(repRootFilesOf(t, l)) != len(fx) {
		t.Fatal("source objects lack representations")
	}
	l.close()
	path, ex := bundleExportT(t, l.dir, snap, true)
	raw, _ := os.ReadFile(path)
	if string(raw[:8]) != bundleMagic || int64(len(distinct)) != ex.UniqueChunks {
		t.Fatalf("bundle format or content changed: magic %q, %d chunk records for %d CDC chunks", raw[:8], ex.UniqueChunks, len(distinct))
	}
	dst := t.TempDir()
	bundleImportT(t, dst, path)
	if _, err := os.Stat(filepath.Join(dst, "representations")); !os.IsNotExist(err) {
		t.Fatal("a bundle import created representation state")
	}
	d, err := OpenStore(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.setRepMode(repModePrefer)
	if d.format.StoreFormatVersion >= storeFormatVersionRepresentations {
		t.Fatalf("import raised the destination to level %d", d.format.StoreFormatVersion)
	}
	for i, e := range entries {
		man, _, err := d.readManifest(e.ManifestUUID)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		rd := d.newManifestReader(man, byteRange{0, man.TotalLength - 1})
		for b, err := rd.next(); err != io.EOF; b, err = rd.next() {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, b...)
		}
		if !bytes.Equal(got, fx[slices.IndexFunc(fx, func(f repFixture) bool { return sha256Hex(f.data) == man.ObjectSHA256 })].data) {
			t.Fatalf("entry %d differs after import", i)
		}
		// a representation can be rebuilt on the destination from the CDC state alone
		if err := d.repBuild(man); err != nil {
			t.Fatalf("rebuild %d: %v", i, err)
		}
	}
	if d.format.StoreFormatVersion != storeFormatVersionRepresentations {
		t.Fatalf("rebuilt roots left the level at %d", d.format.StoreFormatVersion)
	}
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type repCapWriter struct{ n, max int64 }

func (w *repCapWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.n > w.max {
		return 0, errors.New("fuzz: output cap")
	}
	return len(p), nil
}

// FuzzRepDecoders feeds arbitrary kind recipes and component bytes to the
// production decoders: they may reject, but never panic, hang or over-allocate,
// and an accepted reconstruction must be exactly the size the object declares.
func FuzzRepDecoders(f *testing.F) {
	l := newRepLab(f, repModePrefer)
	for _, fx := range repFixtures(f) {
		l.put(fx.name, fx.data)
		e, _ := repManifestOf(f, l.s, "b", fx.name)
		root := repMustRoot(f, l.s, e.manifestUUID)
		src := l.s.newRepSource(root.Descs, nil)
		rep, err := (&repEnv{Src: src, Limits: repProdLimits}).loadRep(root.Rep)
		src.close()
		if err != nil {
			f.Fatal(err)
		}
		head := fx.data
		if len(head) > 8192 {
			head = head[:8192]
		}
		f.Add(uint8(rep.Kind), uint32(rep.Size), rep.Recipe, head)
	}
	l.close()
	f.Fuzz(func(t *testing.T, kind uint8, size uint32, recipe, payload []byte) {
		if len(payload) > 1<<16 || len(recipe) > 1<<16 {
			return
		}
		m := newRepMem()
		sha := m.put(payload)
		o := &repObject{Kind: repKind(kind), Size: int64(size) % (1 << 22), Recipe: recipe,
			Members: []repMember{{Role: repStored, SHA: sha, Size: int64(len(payload))}}}
		raw, err := o.marshal()
		if err != nil {
			return
		}
		id := m.put(raw)
		env := m.env(repLimits{MaxDepth: 4})
		w := &repCapWriter{max: 8 << 20}
		if n, _, err := env.reconstruct(id, w); err == nil && n != o.Size {
			t.Fatalf("accepted a reconstruction of %d bytes for a declared size of %d", n, o.Size)
		}
		env.reachable(id)
	})
}
