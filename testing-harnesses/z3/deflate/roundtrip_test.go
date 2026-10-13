package deflate

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"hash/crc32"
	"io"
	"math/rand"
	"testing"

	"zeros3-z3/corpus"
)

func goDeflate(p []byte, level int) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, level)
	w.Write(p)
	w.Close()
	return buf.Bytes()
}

func testData() map[string][]byte {
	r := rand.New(rand.NewSource(11))
	rnd := make([]byte, 100000)
	r.Read(rnd)
	text := corpus.Text(corpus.NewRNG(1, "rt"), 200000)
	rep := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	runs := bytes.Repeat([]byte{7}, 400000)
	return map[string][]byte{
		"empty": {}, "one": {42}, "two": {1, 2}, "abc": []byte("abcabcabcabc"),
		"w32767": rnd[:32767], "w32768": append(append([]byte(nil), rnd[:32768]...), rnd[:300]...), "w32769": append(append([]byte(nil), rnd[:32769]...), rnd[1:200]...),
		"random": rnd, "text": text, "repeat": rep, "runs": runs,
	}
}

func TestRoundTripGoProducers(t *testing.T) {
	for name, p := range testData() {
		for _, lvl := range []int{flate.NoCompression, flate.HuffmanOnly, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
			b := goDeflate(p, lvl)
			s := check(t, name, b, p)
			st := s.Stats()
			if st.PlainBytes != len(p) || st.CompressedBytes != len(b) || st.Tokens != st.Literals+st.Matches {
				t.Fatalf("%s L%d: stats %+v", name, lvl, st)
			}
			if lvl == flate.NoCompression && (st.DynamicBlocks != 0 || st.StoredBytes != len(p)) {
				t.Fatalf("%s: L0 did not store the input: %+v", name, st)
			}
		}
	}
}

func TestRoundTripFixedEmitter(t *testing.T) {
	for name, p := range testData() {
		b := corpus.DeflateFixed(p)
		s := check(t, "fixed-"+name, b, p)
		if st := s.Stats(); st.DynamicBlocks != 0 || st.StoredBlocks != 0 {
			t.Fatalf("%s: %+v", name, st)
		}
	}
}

func TestRoundTripCorpusStreams(t *testing.T) {
	cfg := corpus.DefaultConfig()
	cfg.SizeKiB = 64
	cfg.Revisions = 2
	cfg.Families = []string{"deflate-*", "zip-deflate-change", "gzip-L6-insert-mid"}
	n := 0
	sink := &collectSink{}
	if _, err := corpus.Generate(cfg, sink); err != nil {
		t.Fatal(err)
	}
	for _, m := range sink.m {
		data := sink.data[m.Path]
		switch m.Format {
		case "deflate":
			check(t, m.Path, data, nil)
			n++
		case "gzip":
			g, rest, err := ParseGzip(data, Options{}, false)
			if err != nil || len(rest) != 0 {
				t.Fatalf("%s: %v rest %d", m.Path, err, len(rest))
			}
			b, _ := g.Bytes()
			if !bytes.Equal(b, data) {
				t.Fatalf("%s: gzip replay differs", m.Path)
			}
			n++
		}
	}
	if n < 20 {
		t.Fatalf("only %d streams", n)
	}
}

type collectSink struct {
	m    []corpus.Member
	data map[string][]byte
}

func (s *collectSink) Put(m corpus.Member, d []byte) error {
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.m = append(s.m, m)
	s.data[m.Path] = d
	return nil
}

func TestGzipWrapperExact(t *testing.T) {
	plain := corpus.Text(corpus.NewRNG(2, "gz"), 50000)
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, 6)
	w.Header = gzip.Header{Name: "a.txt", Comment: "note", Extra: []byte{1, 2, 3}, ModTime: timeOf(1700000000), OS: 3}
	w.Write(plain)
	w.Close()
	data := buf.Bytes()

	g, rest, err := ParseGzip(data, Options{}, false)
	if err != nil || len(rest) != 0 {
		t.Fatal(err, len(rest))
	}
	if string(g.Name) != "a.txt" || string(g.Comment) != "note" || !bytes.Equal(g.Extra, []byte{1, 2, 3}) ||
		g.MTime != 1700000000 || g.OS != 3 || g.CRC32 != crc32.ChecksumIEEE(plain) || int(g.ISize) != len(plain) || g.Flags != 4|8|16 {
		t.Fatalf("fields: %+v", g)
	}
	ref, _ := io.ReadAll(mustGzip(t, data))
	if !bytes.Equal(ref, g.Stream.Plain) {
		t.Fatal("plaintext differs from compress/gzip")
	}
	if b, err := g.Bytes(); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("replay differs: %v", err)
	}

	// header from a different producer is never regenerated: odd XFL/OS/MTIME, FHCRC
	h := append([]byte{0x1f, 0x8b, 8, 2, 9, 8, 7, 6, 0xAA, 0x55}, 0, 0)
	hc := crc32.ChecksumIEEE(h[:10])
	h[10], h[11] = byte(hc), byte(hc>>8)
	body := goDeflate(plain, 9)
	tr := make([]byte, 8)
	c := crc32.ChecksumIEEE(plain)
	tr[0], tr[1], tr[2], tr[3] = byte(c), byte(c>>8), byte(c>>16), byte(c>>24)
	n := len(plain)
	tr[4], tr[5], tr[6], tr[7] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	odd := append(append(append([]byte(nil), h...), body...), tr...)
	g, _, err = ParseGzip(odd, Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if g.MTime != 0x06070809 || g.XFL != 0xAA || g.OS != 0x55 || g.Flags != 2 {
		t.Fatalf("%+v", g)
	}
	if b, _ := g.Bytes(); !bytes.Equal(b, odd) {
		t.Fatal("odd header not preserved")
	}
	// bad header CRC and bad data CRC
	bad := append([]byte(nil), odd...)
	bad[10] ^= 1
	if _, _, err := ParseGzip(bad, Options{}, false); err == nil {
		t.Fatal("bad FHCRC accepted")
	}
	if g, _, err := ParseGzip(bad, Options{}, true); err != nil || g.ChecksumsMatch {
		t.Fatalf("ignoreChecksums: %v %v", err, g)
	}
	bad = append([]byte(nil), odd...)
	bad[len(bad)-8] ^= 1
	if _, _, err := ParseGzip(bad, Options{}, false); err == nil {
		t.Fatal("bad CRC32 accepted")
	}
	if g, _, err := ParseGzip(bad, Options{}, true); err != nil {
		t.Fatal(err)
	} else if b, _ := g.Bytes(); !bytes.Equal(b, bad) {
		t.Fatal("bad-CRC member must still replay exactly")
	}

	// multiple members, exact concatenation
	multi := append(append([]byte(nil), data...), odd...)
	ms, err := ParseGzipAll(multi, Options{}, false)
	if err != nil || len(ms) != 2 {
		t.Fatal(err, len(ms))
	}
	var out []byte
	for _, m := range ms {
		b, _ := m.Bytes()
		out = append(out, b...)
	}
	if !bytes.Equal(out, multi) {
		t.Fatal("multi-member replay differs")
	}
	if _, err := ParseGzipAll(append(multi, 0xde, 0xad), Options{}, false); err == nil {
		t.Fatal("trailing garbage accepted")
	}
	// truncations
	for _, k := range []int{0, 5, 11, len(data) - 9, len(data) - 1} {
		if _, _, err := ParseGzip(data[:k], Options{}, false); err == nil {
			t.Fatalf("truncated to %d accepted", k)
		}
	}
}

func TestZlibWrapperExact(t *testing.T) {
	plain := corpus.Text(corpus.NewRNG(3, "zl"), 30000)
	for _, lvl := range []int{zlib.NoCompression, zlib.BestSpeed, zlib.DefaultCompression, zlib.BestCompression, zlib.HuffmanOnly} {
		var buf bytes.Buffer
		w, _ := zlib.NewWriterLevel(&buf, lvl)
		w.Write(plain)
		w.Close()
		z, err := ParseZlib(buf.Bytes(), Options{})
		if err != nil {
			t.Fatal(lvl, err)
		}
		if !bytes.Equal(z.Stream.Plain, plain) {
			t.Fatal("plaintext")
		}
		if b, _ := z.Bytes(); !bytes.Equal(b, buf.Bytes()) {
			t.Fatalf("level %d: zlib replay differs", lvl)
		}
	}
	good := []byte{0x78, 0x9c, 0x03, 0x00, 0, 0, 0, 1}
	if _, err := ParseZlib(good, Options{}); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"bad-fcheck": {0x78, 0x9d, 0x03, 0, 0, 0, 0, 1}, "dict": {0x78, 0xbb, 0x03, 0, 0, 0, 0, 1},
		"bad-adler": {0x78, 0x9c, 0x03, 0, 0, 0, 0, 2}, "short": {0x78, 0x9c},
	} {
		if _, err := ParseZlib(b, Options{}); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// ZIP member payloads parse and replay on their own, without any container reconstruction.
func TestZipMemberPayloads(t *testing.T) {
	cfg := corpus.DefaultConfig()
	cfg.SizeKiB = 64
	cfg.Revisions = 1
	cfg.Families = []string{"zip-deflate-change", "zip-deflate-add"}
	sink := &collectSink{}
	if _, err := corpus.Generate(cfg, sink); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range sink.m {
		data := sink.data[m.Path]
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if f.Method != zip.Deflate {
				continue
			}
			off, err := f.DataOffset()
			if err != nil {
				t.Fatal(err)
			}
			payload := data[off : off+int64(f.CompressedSize64)]
			s := check(t, m.Path+"/"+f.Name, payload, nil)
			rc, _ := f.Open()
			want, _ := io.ReadAll(rc)
			rc.Close()
			if !bytes.Equal(s.Plain, want) || crc32.ChecksumIEEE(s.Plain) != f.CRC32 {
				t.Fatalf("%s/%s: member content differs from archive/zip", m.Path, f.Name)
			}
			n++
		}
	}
	if n < 10 {
		t.Fatalf("only %d members", n)
	}
}
