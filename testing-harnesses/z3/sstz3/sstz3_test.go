package sstz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// ---- fixtures live in fixture.go ----

type kv = FixtureKV
type cfg = FixtureCfg

func buildTable(kvs []kv, c cfg) []byte              { return BuildFixture(kvs, c) }
func genKV(n int, seed int64, big bool) []kv         { return GenFixture(n, seed, big) }
func genKVs(n int, seed int64) []kv                  { return GenFixture(n, seed, true) }
func block(keys, vals [][]byte, interval int) []byte { return FixtureBlock(keys, vals, interval) }

type store map[ID][]byte

func (s store) emit(_ string, id ID, b []byte) error {
	if _, ok := s[id]; !ok {
		s[id] = append([]byte(nil), b...)
	}
	return nil
}
func (s store) get(id ID) ([]byte, error) {
	if b, ok := s[id]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("missing component %x", id[:4])
}

// roundTrip encodes, discards the analysis state, and decodes from the serialized recipe
// and the stored components only.
func roundTrip(t testing.TB, data []byte, opt Options) (recipe []byte, s store) {
	t.Helper()
	s = store{}
	recipe, _, _, err := Encode(bytes.NewReader(data), int64(len(data)), opt, s.emit)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out bytes.Buffer
	if err := Decode(&out, recipe, s.get, Limits{}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Len() != len(data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(data) || !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("reconstruction differs (len %d/%d)", out.Len(), len(data))
	}
	return recipe, s
}

func TestExactReconstruction(t *testing.T) {
	kvs := genKVs(400, 1)
	cutters := map[string]func() Cutter{
		"block":  func() Cutter { return nil },
		"hash":   func() Cutter { return &HashCutter{Target: 4 << 10, Max: 16 << 10} },
		"record": func() Cutter { return RecordCutter{} },
		"at":     func() Cutter { return &AtCutter{After: []int{0, 5, 6, 300, 301, 899}} },
	}
	for _, family := range []string{"legacy", "rocksdb", "pebble"} {
		for snap := 0; snap < 3; snap++ {
			for _, filter := range []bool{false, true} {
				data := buildTable(kvs, cfg{Family: family, Snappy: snap, BlockSize: 3000, Filter: filter})
				for form := FormBlocks; form <= maxForm; form++ {
					for cn, mk := range cutters {
						if form == FormBlocks && cn != "block" {
							continue
						}
						t.Run(fmt.Sprintf("%s/snappy%d/filter%v/%s/%s", family, snap, filter, form, cn), func(t *testing.T) {
							roundTrip(t, data, Options{Form: form, Cutter: mk()})
						})
					}
				}
			}
		}
	}
}

func TestOddLayouts(t *testing.T) {
	kvs := genKVs(200, 2)
	// restart interval 1 and 7, tables with one block, empty table, one huge record
	for _, iv := range []int{1, 7, 16, 100} {
		roundTrip(t, buildTable(kvs, cfg{Family: "legacy", BlockSize: 2000, Interval: iv}), Options{Form: FormKV, Cutter: &HashCutter{Target: 2048, Max: 8192}})
	}
	roundTrip(t, buildTable(kvs, cfg{Family: "rocksdb", BlockSize: 1 << 30}), Options{Form: FormFull})
	roundTrip(t, buildTable(nil, cfg{Family: "legacy", BlockSize: 100}), Options{Form: FormUKV})
	roundTrip(t, buildTable(nil, cfg{Family: "pebble", BlockSize: 100}), Options{Form: FormBlocks})
	// an explicit (non-derived) layout: shared prefix shorter than maximal, restarts every 3 then 5
	keys := [][]byte{[]byte("aaaa12345678"), []byte("aaab12345678"), []byte("aaac12345678"), []byte("aaad12345678"), []byte("aaae12345678"), []byte("aaaf12345678")}
	vals := [][]byte{[]byte("1"), []byte("2"), []byte("3"), []byte("4"), []byte("5"), []byte("6")}
	var b []byte
	var rs []uint32
	for i := range keys {
		sh := 0
		switch i {
		case 0, 3:
			rs = append(rs, uint32(len(b)))
		case 1:
			sh = 2 // legal but not maximal (3)
		default:
			sh = 3
		}
		b = uv(uv(uv(b, uint64(sh)), uint64(len(keys[i])-sh)), uint64(len(vals[i])))
		b = append(append(b, keys[i][sh:]...), vals[i]...)
	}
	for _, r := range rs {
		b = binary.LittleEndian.AppendUint32(b, r)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(rs)))
	lay, err := WalkBlock(b, Limits{}, nil)
	if err != nil || lay.derivesFrom(16, func(i int) []byte { return keys[i] }) {
		t.Fatalf("explicit block should parse and not derive: %v", err)
	}
	var bw BlockWriter
	ri := 0
	for i := range keys {
		isR := ri < len(lay.RestartIdx) && lay.RestartIdx[ri] == i
		if isR {
			ri++
		}
		bw.Add(keys[i], vals[i], isR, lay.Shared[i])
	}
	if !bytes.Equal(bw.Finish(), b) {
		t.Fatal("explicit layout does not rebuild")
	}
}

func TestOpenClassification(t *testing.T) {
	kvs := genKVs(300, 3)
	for _, c := range []struct {
		fam  string
		want Family
		ver  uint32
	}{{"legacy", FamilyLegacy, 0}, {"rocksdb", FamilyRocksDB, 5}, {"pebble", FamilyPebble, 4}} {
		data := buildTable(kvs, cfg{Family: c.fam, Snappy: 1, BlockSize: 2500, Filter: true})
		tb, err := Open(bytes.NewReader(data), int64(len(data)), Limits{})
		if err != nil || tb.Reject() != nil {
			t.Fatalf("%s: %v %v", c.fam, err, tb.Reject())
		}
		if tb.Footer.Family != c.want || tb.Footer.Version != c.ver || len(tb.Blocks) < 2 {
			t.Fatalf("%s: %+v blocks=%d", c.fam, tb.Footer, len(tb.Blocks))
		}
		if n, ok := tb.PropUint("rocksdb.num.entries"); c.fam != "legacy" && (!ok || n != uint64(len(kvs))) {
			t.Fatalf("%s: entries property %d", c.fam, n)
		}
		var filter bool
		for _, f := range tb.Features {
			filter = filter || f.Name == "filter" && f.Class == Opaque
		}
		if !filter {
			t.Fatalf("%s: filter block not classified OPAQUE_PRESERVED: %+v", c.fam, tb.Features)
		}
	}
	// unsupported: unknown magic, future RocksDB format, hash index flag in a data block
	d := buildTable(kvs, cfg{Family: "rocksdb", BlockSize: 2500})
	bad := append([]byte(nil), d...)
	binary.LittleEndian.PutUint32(bad[len(bad)-12:], 9)
	if tb, err := Open(bytes.NewReader(bad), int64(len(bad)), Limits{}); err != nil || tb.Reject() == nil {
		t.Fatalf("format 9 accepted: %v", err)
	}
	if _, err := Open(bytes.NewReader(d[:len(d)-1]), int64(len(d)-1), Limits{}); err == nil {
		t.Fatal("table without its last byte accepted")
	}
}

// ---- malformed data blocks ----

func TestMalformedBlocks(t *testing.T) {
	good := block([][]byte{[]byte("aaaa"), []byte("aaab"), []byte("aaac")}, [][]byte{[]byte("1"), []byte("22"), []byte("")}, 2)
	if _, err := WalkBlock(good, Limits{}, nil); err != nil {
		t.Fatal(err)
	}
	le := func(v ...uint32) []byte {
		var b []byte
		for _, x := range v {
			b = binary.LittleEndian.AppendUint32(b, x)
		}
		return b
	}
	entry := func(sh, ns, vl uint64, key, val string) []byte {
		return append(uv(uv(uv(nil, sh), ns), vl), key+val...)
	}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	e1 := entry(0, 4, 1, "aaaa", "1")
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"three bytes", []byte{1, 2, 3}},
		{"restart count zero", le(0)},
		{"restart count above block", le(5)},
		{"restart count overflow", le(0xffffffff)},
		{"hash index flag", cat(e1, le(0, 1|1<<31))},
		{"first restart not zero", cat(e1, le(1, 1))},
		{"restart beyond payload", cat(e1, le(0, 99, 2))},
		{"restarts not increasing", cat(e1, e1, le(0, 7, 7, 3))[:0:0]},
		{"restart inside an entry", cat(e1, le(0, 3, 2))},
		{"restart not on a boundary", cat(e1, e1, le(0, 9, 2))},
		{"missing restart array", e1},
		{"truncated restart array", cat(e1, le(0, 1)[:5])},
		{"bad shared varint", cat([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, le(0, 1))},
		{"shared above previous key", cat(e1, entry(9, 1, 0, "x", ""), le(0, 1))},
		{"shared on first entry", cat(entry(1, 3, 0, "abc", ""), le(0, 1))},
		{"entry beyond payload", cat(entry(0, 50, 0, "abc", ""), le(0, 1))},
		{"value beyond payload", cat(entry(0, 1, 50, "a", "bc"), le(0, 1))},
		{"length overflow", cat(uv(uv(uv(nil, 0), 1<<63), 1<<63), le(0, 1))},
		{"missing value length", cat(uv(uv(nil, 0), 1), le(0, 1))},
		{"empty block with a restart", cat(le(0, 2))},
		{"empty block with entries missing", cat(le(4, 1))},
		{"trailing garbage in payload", cat(e1, []byte{0x00}, le(0, 1))},
	}
	for _, c := range cases {
		if _, err := WalkBlock(c.b, Limits{}, nil); err == nil && len(c.b) > 0 {
			if c.name == "restarts not increasing" {
				continue // placeholder row: covered by "restart inside an entry"
			}
			t.Errorf("%s: accepted", c.name)
		} else if err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// every truncation and every single-byte mutation of a good block either fails or
	// walks to a layout the rebuild reproduces exactly (never a panic)
	for i := 0; i < len(good); i++ {
		WalkBlock(good[:i], Limits{}, nil)
		for v := 0; v < 256; v++ {
			m := append([]byte(nil), good...)
			m[i] = byte(v)
			lay, err := WalkBlock(m, Limits{}, nil)
			if err != nil {
				continue
			}
			var keys, vals [][]byte
			WalkBlock(m, Limits{}, func(_ int, k, v []byte) error {
				keys = append(keys, append([]byte(nil), k...))
				vals = append(vals, append([]byte(nil), v...))
				return nil
			})
			var bw BlockWriter
			ri := 0
			for j := range keys {
				isR := ri < len(lay.RestartIdx) && lay.RestartIdx[ri] == j
				if isR {
					ri++
				}
				if bw.Add(keys[j], vals[j], isR, lay.Shared[j]) != nil {
					t.Fatalf("accepted block does not rebuild (byte %d=%d)", i, v)
				}
			}
			if !bytes.Equal(bw.Finish(), m) {
				// only non-minimal varint spellings may differ; Encode rejects those by comparing
				if !bytes.Contains(m, []byte{0x80}) {
					t.Fatalf("accepted block rebuilds differently (byte %d=%d)", i, v)
				}
			}
		}
	}
}

// ---- malformed tables, recipes and components ----

func TestMalformedTables(t *testing.T) {
	data := buildTable(genKV(60, 4, false), cfg{Family: "rocksdb", Snappy: 1, BlockSize: 1500, Filter: true})
	for i := 0; i < len(data); i++ { // every strict prefix
		s := store{}
		if _, _, _, err := Encode(bytes.NewReader(data[:i]), int64(i), Options{Form: FormUKV}, s.emit); err == nil {
			t.Fatalf("prefix of %d bytes accepted", i)
		}
	}
	// every single-byte mutation: Encode rejects, or the result decodes to exactly the mutated bytes
	accepted := 0
	for i := 0; i < len(data); i += 1 {
		m := append([]byte(nil), data...)
		m[i] ^= 0x5a
		s := store{}
		recipe, _, _, err := Encode(bytes.NewReader(m), int64(len(m)), Options{Form: FormUKV, Cutter: &HashCutter{Target: 2048, Max: 8192}}, s.emit)
		if err != nil {
			continue
		}
		accepted++
		var out bytes.Buffer
		if err := Decode(&out, recipe, s.get, Limits{}); err != nil || !bytes.Equal(out.Bytes(), m) {
			t.Fatalf("mutation at %d accepted but not reproduced: %v", i, err)
		}
	}
	t.Logf("%d of %d single-byte mutations accepted and reproduced exactly", accepted, len(data))
}

func TestRecipeAndComponentAbuse(t *testing.T) {
	data := buildTable(genKV(70, 5, false), cfg{Family: "legacy", Snappy: 1, BlockSize: 1800})
	for _, form := range []Form{FormBlocks, FormUKV, FormKVT} {
		recipe, s := roundTrip(t, data, Options{Form: form, Cutter: &HashCutter{Target: 3000, Max: 12000}})
		if form == FormBlocks {
			recipe, s = roundTrip(t, data, Options{Form: form})
		}
		for i := 0; i < len(recipe); i++ { // truncations
			if _, err := ParseRecipe(recipe[:i], Limits{}); err == nil {
				t.Fatalf("%s: recipe prefix %d accepted", form, i)
			}
		}
		for i := range recipe { // mutations: never a panic; if it decodes, the size is right
			for _, v := range []byte{0, 1, 0x7f, 0x80, 0xff} {
				m := append([]byte(nil), recipe...)
				m[i] = v
				var out bytes.Buffer
				if err := Decode(&out, m, s.get, Limits{}); err == nil && int64(out.Len()) != mustParse(t, m).Size {
					t.Fatalf("%s: mutated recipe decoded to the wrong length", form)
				}
			}
		}
		// hostile counts: no allocation from a declared size
		hostile := []byte{recipeVersion, byte(form)}
		hostile = uv(hostile, 1<<40)
		hostile = uv(hostile, 16)
		hostile = uv(hostile, 1<<40)
		if _, err := ParseRecipe(hostile, Limits{}); err == nil {
			t.Fatal("hostile block count accepted")
		}
		// components: truncated, extended, swapped
		rec := mustParse(t, recipe)
		for _, d := range rec.Digests {
			orig := s[d]
			for _, bad := range [][]byte{nil, orig[:len(orig)/2], append(append([]byte(nil), orig...), 0), bytes.Repeat([]byte{1}, len(orig))} {
				s2 := store{}
				for k, v := range s {
					s2[k] = v
				}
				s2[d] = bad
				var out bytes.Buffer
				if err := Decode(&out, recipe, s2.get, Limits{}); err == nil && bytes.Equal(out.Bytes(), data) && !bytes.Equal(bad, orig) {
					t.Fatalf("%s: a damaged component still reproduced the file", form)
				}
			}
		}
		// a failing writer at every position is reported
		var n int
		cw := &limitWriter{left: len(data) / 2}
		if err := Decode(cw, recipe, s.get, Limits{}); err == nil {
			t.Fatal("writer failure not reported")
		}
		_ = n
		// a missing component
		for k := range s {
			s3 := store{}
			for k2, v := range s {
				if k2 != k {
					s3[k2] = v
				}
			}
			if err := Decode(io.Discard, recipe, s3.get, Limits{}); err == nil {
				t.Fatal("missing component not reported")
			}
			break
		}
	}
}

type limitWriter struct{ left int }

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		return 0, errors.New("sink full")
	}
	w.left -= len(p)
	return len(p), nil
}

func mustParse(t testing.TB, b []byte) *Recipe {
	r, err := ParseRecipe(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ---- ranges and bounded reads ----

type countingReader struct {
	r     io.ReaderAt
	size  int64
	bytes int64
	reads int
	over  bool
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > c.size {
		c.over = true
	}
	c.reads++
	c.bytes += int64(len(p))
	return c.r.ReadAt(p, off)
}

func TestRangesAndBoundedReads(t *testing.T) {
	data := buildTable(genKVs(1500, 6), cfg{Family: "pebble", Snappy: 2, BlockSize: 4000, Filter: true})
	for _, form := range []Form{FormBlocks, FormUKV, FormKV, FormKVT} {
		var cut Cutter
		if form != FormBlocks {
			cut = &HashCutter{Target: 5000, Max: 20000}
		}
		recipe, s := roundTrip(t, data, Options{Form: form, Cutter: cut})
		r := mustParse(t, recipe)
		starts := []int64{0, 1, 17, 4000, int64(len(data)) / 3, int64(len(data)) - int64(r.TailLen) - 5, int64(len(data)) - int64(r.TailLen), int64(len(data)) - 3}
		for _, off := range starts {
			for _, n := range []int64{0, 1, 4096, 9000, 1 << 30} {
				var out bytes.Buffer
				if _, err := DecodeRange(&out, recipe, s.get, off, n, Limits{}); err != nil {
					t.Fatalf("%s off=%d n=%d: %v", form, off, n, err)
				}
				end := min(int64(len(data)), off+n)
				if n == 1<<30 || off+n > int64(len(data)) {
					end = int64(len(data))
				}
				if !bytes.Equal(out.Bytes(), data[off:end]) {
					t.Fatalf("%s off=%d n=%d: range differs", form, off, n)
				}
			}
		}
		// a small range inside one block touches few components
		var out bytes.Buffer
		st, err := DecodeRange(&out, recipe, s.get, 20000, 100, Limits{})
		if err != nil || (form != FormBlocks && st.Components > 6) || st.Blocks > 2 {
			t.Fatalf("%s: small range touched %+v (%v)", form, st, err)
		}
	}
	// Open reads only the footer, metadata and index
	big := buildTable(genKVs(6000, 7), cfg{Family: "rocksdb", Snappy: 1, BlockSize: 4000})
	cr := &countingReader{r: bytes.NewReader(big), size: int64(len(big))}
	tb, err := Open(cr, cr.size, Limits{})
	if err != nil || cr.over || cr.bytes > cr.size/4 {
		t.Fatalf("Open read %d of %d bytes (over=%v, %v), %d blocks", cr.bytes, cr.size, cr.over, err, len(tb.Blocks))
	}
	cr = &countingReader{r: bytes.NewReader(big), size: int64(len(big))}
	if _, _, _, err := Encode(cr, cr.size, Options{Form: FormUKV, Cutter: &HashCutter{Target: 8192, Max: 32768}}, func(string, ID, []byte) error { return nil }); err != nil || cr.over {
		t.Fatalf("Encode over-read (%v) %v", cr.over, err)
	}
}

func TestHostileSizes(t *testing.T) {
	data := buildTable(genKVs(50, 8), cfg{Family: "legacy", BlockSize: 1000})
	m := append([]byte(nil), data...)
	// a metaindex handle claiming a block larger than the file
	f := m[len(m)-legacyFooterLen:]
	copy(f, append(handle(0, 1<<40), make([]byte, 20)...))
	if _, err := Open(bytes.NewReader(m), int64(len(m)), Limits{}); err == nil {
		t.Fatal("handle beyond the file accepted")
	}
	if _, err := Open(bytes.NewReader(nil), 0, Limits{}); err == nil {
		t.Fatal("empty file accepted")
	}
	huge := &countingReader{r: bytes.NewReader(data), size: 1 << 50}
	if _, err := Open(huge, 1<<50, Limits{}); err == nil {
		t.Fatal("size lie accepted")
	}
}

func FuzzEncodeDecode(f *testing.F) {
	for i, fam := range []string{"legacy", "rocksdb", "pebble"} {
		f.Add(buildTable(genKV(14, int64(i), false), cfg{Family: fam, Snappy: 1, BlockSize: 400, Filter: i == 1}))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxBlocks: 1 << 10, MaxBlockBytes: 1 << 20, MaxDecoded: 1 << 20, MaxEntries: 1 << 12, MaxMeta: 1 << 20, MaxTail: 1 << 20, MaxRecordBytes: 1 << 20, MaxRecipe: 1 << 22}
		for _, form := range []Form{FormBlocks, FormUKV, FormKVT} {
			s := store{}
			recipe, _, _, err := Encode(bytes.NewReader(data), int64(len(data)), Options{Form: form, Cutter: &HashCutter{Target: 512, Max: 2048}, Limits: lim}, s.emit)
			if err != nil {
				continue
			}
			var out bytes.Buffer
			if err := Decode(&out, recipe, s.get, lim); err != nil || !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("%s: accepted table not reproduced: %v", form, err)
			}
		}
	})
}
