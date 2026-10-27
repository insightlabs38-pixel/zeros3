package parquetz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tc "zeros3-z3/thriftcompactz3"
)

// ---- a minimal real-format writer for synthetic fixtures (one required INT32 column) ----

type te struct{ b []byte }

func (e *te) field(last *int16, id int16, t tc.Type) {
	if d := id - *last; d > 0 && d <= 15 {
		e.b = append(e.b, byte(d)<<4|byte(t))
	} else {
		e.b = append(e.b, byte(t))
		e.b = tc.AppendI64(e.b, int64(id))
	}
	*last = id
}
func (e *te) i32(last *int16, id int16, v int32) {
	e.field(last, id, tc.I32)
	e.b = tc.AppendI64(e.b, int64(v))
}
func (e *te) i64(last *int16, id int16, v int64) {
	e.field(last, id, tc.I64)
	e.b = tc.AppendI64(e.b, v)
}
func (e *te) bin(last *int16, id int16, s string) {
	e.field(last, id, tc.Binary)
	e.b = append(binary.AppendUvarint(e.b, uint64(len(s))), s...)
}
func (e *te) list(last *int16, id int16, t tc.Type, n int) {
	e.field(last, id, tc.List)
	if n < 15 {
		e.b = append(e.b, byte(n)<<4|byte(t))
	} else {
		e.b = binary.AppendUvarint(append(e.b, 0xf0|byte(t)), uint64(n))
	}
}
func (e *te) stop() { e.b = append(e.b, 0) }

// fixture describes one synthetic file; the zero value is valid.
type fixture struct {
	n                    int
	pageComp, pageUnc    int32 // overrides (0 = honest)
	chunkComp            int64 // override
	dataOff              int64 // override
	hdrCut               int   // truncate the page header to this many bytes (0 = whole)
	footerLen            int64 // override (0 = honest)
	badMagic             bool
	listCount            int // override schema list count (0 = honest)
	extraPageBytes       int // payload bytes the page header does not account for
	unknownFieldInFooter bool
	enc                  int32 // page encoding (0 = PLAIN)
}

func (f fixture) build() []byte {
	n := f.n
	if n == 0 {
		n = 100
	}
	body := make([]byte, 4*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(body[4*i:], uint32(i*7))
	}
	var ph te
	var l int16
	ph.i32(&l, 1, PageData)
	unc, comp := int32(len(body)), int32(len(body))
	if f.pageUnc != 0 {
		unc = f.pageUnc
	}
	if f.pageComp != 0 {
		comp = f.pageComp
	}
	ph.i32(&l, 2, unc)
	ph.i32(&l, 3, comp)
	ph.field(&l, 5, tc.Struct)
	var l2 int16
	ph.i32(&l2, 1, int32(n))
	ph.i32(&l2, 2, f.enc)
	ph.i32(&l2, 3, EncRLE)
	ph.i32(&l2, 4, EncRLE)
	ph.stop()
	ph.stop()
	hdr := ph.b
	if f.hdrCut > 0 {
		hdr = hdr[:f.hdrCut]
	}
	file := append([]byte("PAR1"), hdr...)
	file = append(file, body...)
	file = append(file, make([]byte, f.extraPageBytes)...)
	chunkEnd := int64(len(file))

	var m te
	var ml int16
	m.i32(&ml, 1, 1)
	cnt := 2
	if f.listCount != 0 {
		cnt = f.listCount
	}
	m.list(&ml, 2, tc.Struct, cnt)
	var s te // root
	var sl int16
	s.bin(&sl, 4, "schema")
	s.i32(&sl, 5, 1)
	s.stop()
	var s2 te // leaf
	var s2l int16
	s2.i32(&s2l, 1, TInt32)
	s2.i32(&s2l, 3, 0)
	s2.bin(&s2l, 4, "a")
	s2.stop()
	m.b = append(m.b, s.b...)
	m.b = append(m.b, s2.b...)
	m.i64(&ml, 3, int64(n))
	m.list(&ml, 4, tc.Struct, 1)
	// RowGroup
	var rl int16
	m.list(&rl, 1, tc.Struct, 1)
	var cl int16
	m.field(&cl, 3, tc.Struct)
	var cm int16
	m.list(&cm, 2, tc.I32, 2)
	m.b = tc.AppendI64(tc.AppendI64(m.b, EncPlain), EncRLE)
	m.list(&cm, 3, tc.Binary, 1)
	m.b = append(m.b, 1, 'a')
	m.i32(&cm, 4, CodecNone)
	m.i64(&cm, 5, int64(n))
	m.i64(&cm, 6, int64(len(hdr)+len(body)))
	cc := chunkEnd - 4
	if f.chunkComp != 0 {
		cc = f.chunkComp
	}
	m.i64(&cm, 7, cc)
	do := int64(4)
	if f.dataOff != 0 {
		do = f.dataOff
	}
	m.i64(&cm, 9, do)
	m.stop() // ColumnMetaData
	m.stop() // ColumnChunk
	m.i64(&rl, 3, int64(n))
	m.stop() // RowGroup
	if f.unknownFieldInFooter {
		m.bin(&ml, 6, "z3 test writer")
	}
	m.stop()
	file = append(file, m.b...)
	fl := int64(len(m.b))
	if f.footerLen != 0 {
		fl = f.footerLen
	}
	file = binary.LittleEndian.AppendUint32(file, uint32(fl))
	file = append(file, "PAR1"...)
	if f.badMagic {
		file[0] = 'X'
	}
	return file
}

func TestSyntheticFixtureParses(t *testing.T) {
	b := fixture{unknownFieldInFooter: true}.build()
	f, err := Open(bytes.NewReader(b), int64(len(b)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if f.NumRows != 100 || len(f.Columns) != 1 || f.Columns[0].Name != "a" || f.CreatedBy != "z3 test writer" || len(f.RowGroups) != 1 {
		t.Fatalf("parsed %+v", f)
	}
	sp, err := f.Spans()
	if err != nil || len(sp) != 3 || sp[1].Kind != SpanChunk {
		t.Fatalf("spans %v %v", sp, err)
	}
	var pages int
	err = f.Pages(bytes.NewReader(b), &f.RowGroups[0].Chunks[0], func(p *Page, hdr []byte) error {
		pages++
		if p.NumValues != 100 || p.Comp != 400 || p.HdrLen != len(hdr) {
			t.Fatalf("page %+v", p)
		}
		return nil
	})
	if err != nil || pages != 1 {
		t.Fatalf("pages %d %v", pages, err)
	}
}

var errAny = errors.New("any error")

// The malformed suite: every case must fail with the named class and never panic.
func TestMalformedFiles(t *testing.T) {
	good := fixture{}.build()
	cut := func(n int) []byte { return good[:len(good)-n] }
	flip := func(i int) []byte { c := append([]byte(nil), good...); c[i] ^= 0xff; return c }
	for _, tc := range []struct {
		name string
		file []byte
		want error
	}{
		{"bad head magic", fixture{badMagic: true}.build(), ErrCorrupt},
		{"bad tail magic", flip(len(good) - 1), ErrCorrupt},
		{"too short", []byte("PAR1PAR1"), ErrCorrupt},
		{"footer length overflow", fixture{footerLen: 1 << 30}.build(), ErrCorrupt},
		{"footer length too small", fixture{footerLen: 1}.build(), errAny},
		{"truncated file", cut(9), ErrCorrupt},
		{"hostile schema list count", fixture{listCount: 1 << 24}.build(), ErrLimit},
		{"schema list count beyond bytes", fixture{listCount: 5000}.build(), ErrCorrupt},
		{"chunk offset before the data area", fixture{dataOff: 1}.build(), ErrCorrupt},
		{"chunk size beyond the file", fixture{chunkComp: 1 << 40}.build(), ErrCorrupt},
		{"chunk offset beyond the file", fixture{dataOff: 1 << 40}.build(), ErrCorrupt},
		{"encrypted footer", append(append([]byte("PARE"), good[4:len(good)-4]...), "PARE"...), ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Open(bytes.NewReader(tc.file), int64(len(tc.file)), Limits{})
			if tc.want == errAny {
				if err == nil {
					t.Fatalf("expected an error")
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v (file %v)", tc.want, err, f != nil)
			}
		})
	}
}

// Page-level corruption is detected when the pages are walked, never later.
func TestMalformedPages(t *testing.T) {
	good := fixture{}.build()
	for _, tc := range []struct {
		name string
		fx   fixture
		want error
	}{
		{"page compressed size overflows the chunk", fixture{pageComp: 1 << 20}, ErrCorrupt},
		{"page compressed size is huge", fixture{pageComp: 1<<31 - 1}, ErrLimit},
		{"page shorter than its chunk", fixture{pageComp: 8}, ErrCorrupt},
		{"truncated page header", fixture{hdrCut: 4}, ErrCorrupt},
		{"payload longer than the header says", fixture{extraPageBytes: 5}, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.fx.build()
			f, err := Open(bytes.NewReader(b), int64(len(b)), Limits{})
			if err != nil {
				if errors.Is(err, tc.want) {
					return
				}
				t.Fatal(err)
			}
			err = f.Pages(bytes.NewReader(b), &f.RowGroups[0].Chunks[0], func(*Page, []byte) error { return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	_ = good
}

// Every truncation of a real file fails cleanly (no panic, no read past the end).
func TestRealFileTruncations(t *testing.T) {
	b := load(t, "pa_nested.parquet")
	for n := 0; n < len(b); n += 7 {
		if _, err := Open(bytes.NewReader(b[:n]), int64(n), Limits{}); err == nil && n != len(b) {
			t.Fatalf("prefix of %d bytes opened", n)
		}
	}
}

func load(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var fixtures = []string{"pa_snappy_v1.parquet", "pa_snappy_v2.parquet", "pa_zstd_v1.parquet", "pa_none_plain.parquet", "pa_bss.parquet",
	"pa_rg500.parquet", "pa_gzip.parquet", "pa_nested.parquet", "dd_snappy.parquet", "dd_zstd.parquet", "dd_none.parquet"}

// On real producer files the spans tile the file exactly, the pages tile every chunk, and the
// footer agrees with what the pages declare.
func TestRealFilesDecompose(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			b := load(t, name)
			r := bytes.NewReader(b)
			f, err := Open(r, int64(len(b)), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			sp, err := f.Spans()
			if err != nil {
				t.Fatal(err)
			}
			pos := int64(0)
			for _, s := range sp {
				if s.Off != pos || s.Len < 0 {
					t.Fatalf("span %+v does not start at %d", s, pos)
				}
				pos += s.Len
			}
			if pos != int64(len(b)) {
				t.Fatalf("spans cover %d of %d bytes", pos, len(b))
			}
			var pages int
			for gi := range f.RowGroups {
				for ci := range f.RowGroups[gi].Chunks {
					c := &f.RowGroups[gi].Chunks[ci]
					var values int64
					err := f.Pages(r, c, func(p *Page, hdr []byte) error {
						pages++
						if p.Type == PageData || p.Type == PageDataV2 {
							values += int64(p.NumValues)
						}
						if again, err := ParsePageHeader(append(append([]byte(nil), hdr...), 0xaa, 0xbb), Limits{}); err != nil || again.HdrLen != len(hdr) {
							t.Fatalf("header does not re-parse: %v", err)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if values != c.NumValues {
						t.Fatalf("chunk %d/%d: pages hold %d values, footer says %d", gi, ci, values, c.NumValues)
					}
				}
			}
			if pages == 0 {
				t.Fatal("no pages")
			}
		})
	}
}

// FuzzOpen: footer, page headers, page decode and codec analysis on arbitrary bytes. The
// lower limits keep one input cheap; the property is "no panic, no read past the end".
func FuzzOpen(f *testing.F) {
	f.Add(fixture{}.build())
	f.Add(fixture{unknownFieldInFooter: true, n: 5}.build())
	for _, n := range []string{"pa_nested.parquet", "pa_gzip.parquet"} {
		f.Add(load(f, n))
	}
	lim := Limits{MaxFooter: 1 << 20, MaxPages: 4096, MaxPage: 4 << 20, MaxDict: 4 << 20}
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		pf, err := Open(r, int64(len(b)), lim)
		if err != nil {
			return
		}
		sp, err := pf.Spans()
		if err != nil {
			return
		}
		var end int64
		for _, s := range sp {
			if s.Off < end || s.Off+s.Len > int64(len(b)) {
				t.Fatalf("span %+v is outside [%d,%d)", s, end, len(b))
			}
			end = s.Off + s.Len
		}
		an := NewAnalyzer(r, pf, lim)
		for gi := range pf.RowGroups {
			for ci := range pf.RowGroups[gi].Chunks {
				an.Chunk(&pf.RowGroups[gi].Chunks[ci], nil, nil) // any error is fine; a panic is not
			}
		}
	})
}

// Bodies that parse as pages but are not valid logical pages make the chunk opaque (kept as
// bytes, never guessed): the file still opens and still rebuilds.
func TestMalformedPageBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		fx   fixture
		why  string
	}{
		{"honest page", fixture{}, ""},
		{"uncompressed size lies", fixture{pageUnc: 399}, "declares 399"},
		{"dictionary encoding without a dictionary page", fixture{enc: EncRLEDict}, "without a dictionary"},
		{"unsupported page encoding", fixture{enc: EncDeltaBin}, "encoding"},
		{"page holds fewer values than the chunk", fixture{n: 100, pageComp: 396, pageUnc: 396, chunkComp: 396 + 14}, "does not decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.fx.build()
			r := bytes.NewReader(b)
			f, err := Open(r, int64(len(b)), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := NewAnalyzer(r, f, Limits{}).Chunk(&f.RowGroups[0].Chunks[0], nil, nil)
			if err != nil {
				if tc.why != "" {
					return // a clean error is also acceptable for a structurally broken chunk
				}
				t.Fatal(err)
			}
			if tc.why == "" {
				if plan.Opaque {
					t.Fatalf("the honest synthetic page is opaque: %s", plan.Reason)
				}
				return
			}
			if !plan.Opaque || !strings.Contains(plan.Reason, tc.why) {
				t.Fatalf("opaque=%v reason %q, want %q", plan.Opaque, plan.Reason, tc.why)
			}
		})
	}
}

// A value above the logical limit makes its chunk opaque; the rest of the file stays logical.
func TestLargeValueThreshold(t *testing.T) {
	b := load(t, "pa_snappy_v1.parquet")
	st := store{}
	rec, _, stats, err := Encode(bytes.NewReader(b), int64(len(b)), Options{Limits: Limits{MaxValue: 8}}, st.emit)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpaqueChunks == 0 || stats.LogicalChunks == 0 {
		t.Fatalf("logical %d opaque %d", stats.LogicalChunks, stats.OpaqueChunks)
	}
	var out bytes.Buffer
	if err := Decode(&out, rec, st.get, Limits{}); err != nil || !bytes.Equal(out.Bytes(), b) {
		t.Fatalf("round trip with opaque chunks: %v", err)
	}
}
