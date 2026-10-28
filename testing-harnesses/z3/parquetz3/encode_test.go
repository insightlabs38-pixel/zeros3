package parquetz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// store is a content-addressed component store for the tests.
type store map[ID][]byte

func (s store) emit(_ string, id ID, b []byte) error {
	s[id] = append([]byte(nil), b...)
	return nil
}

func (s store) get(id ID) ([]byte, error) {
	b, ok := s[id]
	if !ok {
		return nil, fmt.Errorf("component %x missing", id[:6])
	}
	return b, nil
}

func encodeFile(t testing.TB, b []byte, opt Options) ([]byte, store, Stats) {
	t.Helper()
	st := store{}
	rec, _, stats, err := Encode(bytes.NewReader(b), int64(len(b)), opt, st.emit)
	if err != nil {
		t.Fatal(err)
	}
	return rec, st, stats
}

var logicalFixtures = []string{"pa_delta.parquet", "pa_snappy_v1.parquet", "pa_snappy_v2.parquet", "pa_zstd_v1.parquet", "pa_none_plain.parquet", "pa_bss.parquet",
	"pa_rg500.parquet", "pa_nested.parquet", "dd_snappy.parquet", "dd_zstd.parquet", "dd_none.parquet"}

// The whole contract: serialized recipe + stored components + stdlib -> the original bytes.
func TestEncodeDecodeExact(t *testing.T) {
	modes := map[string]Options{
		"content":   {},
		"small":     {Target: 1 << 10},
		"fixed":     {Cut: CutFixed, FixedCount: 100},
		"page":      {Cut: CutPage},
		"record":    {Cut: CutRecord},
		"nopredict": {NoPredict: true},
	}
	for _, name := range logicalFixtures {
		b := load(t, name)
		for mode, opt := range modes {
			if mode == "record" && len(b) > 300<<10 {
				continue // one component per value: an oracle, kept to the small files
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				rec, st, stats := encodeFile(t, b, opt)
				var out bytes.Buffer
				if err := Decode(&out, rec, st.get, Limits{}); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out.Bytes(), b) {
					t.Fatalf("rebuilt %d bytes, differs from the %d byte original", out.Len(), len(b))
				}
				if stats.LogicalChunks == 0 {
					t.Fatal("nothing logical")
				}
			})
		}
	}
}

// Unsupported files fail cleanly (the caller falls back to CDC); a partly supported file is
// accepted with its unsupported chunks kept as opaque payloads.
func TestUnsupportedFallsBack(t *testing.T) {
	b := load(t, "pa_gzip.parquet")
	if _, _, _, err := Encode(bytes.NewReader(b), int64(len(b)), Options{}, func(string, ID, []byte) error { return nil }); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gzip file: %v", err)
	}
	if _, _, _, err := Encode(bytes.NewReader(b[:len(b)-3]), int64(len(b)-3), Options{}, func(string, ID, []byte) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated file: %v", err)
	}
	nb := load(t, "pa_nested.parquet")
	rec, st, stats := encodeFile(t, nb, Options{})
	if stats.OpaqueChunks == 0 || stats.LogicalChunks == 0 {
		t.Fatalf("nested file: %+v", stats)
	}
	var out bytes.Buffer
	if err := Decode(&out, rec, st.get, Limits{}); err != nil || !bytes.Equal(out.Bytes(), nb) {
		t.Fatalf("nested file does not round trip: %v", err)
	}
}

// Ranges: any [off, off+n) equals the same slice of the original, whole segments only.
func TestDecodeRange(t *testing.T) {
	for _, name := range []string{"pa_snappy_v2.parquet", "dd_zstd.parquet"} {
		b := load(t, name)
		rec, st, _ := encodeFile(t, b, Options{})
		r := rand.New(rand.NewSource(7))
		for i := 0; i < 60; i++ {
			off := r.Int63n(int64(len(b)))
			n := r.Int63n(int64(len(b)) - off + 1)
			if i%3 == 0 {
				n = min(n, 4096)
			}
			var out bytes.Buffer
			stats, err := DecodeRange(&out, rec, st.get, off, n, Limits{})
			if err != nil || !bytes.Equal(out.Bytes(), b[off:off+n]) {
				t.Fatalf("%s [%d,+%d): %v", name, off, n, err)
			}
			if n > 0 && stats.Components == 0 && stats.Pages > 0 {
				t.Fatal("pages rebuilt without fetching a component")
			}
		}
	}
}

// Missing or tampered components, and every truncation and byte flip of the recipe, are
// errors or (for flips inside inline framing) a different file, never a panic or an overrun.
func TestRecipeAndComponentAbuse(t *testing.T) {
	b := load(t, "dd_snappy.parquet")
	rec, st, _ := encodeFile(t, b, Options{Target: 4 << 10})
	var out bytes.Buffer
	for id := range st {
		bad := store{}
		for k, v := range st {
			bad[k] = v
		}
		delete(bad, id)
		if err := Decode(&out, rec, bad.get, Limits{}); err == nil {
			t.Fatalf("decode succeeded without component %x", id[:6])
		}
		out.Reset()
		break
	}
	// tamper: flip a byte of every component class in turn
	n := 0
	for id, v := range st {
		bad := store{}
		for k, w := range st {
			bad[k] = w
		}
		c := append([]byte(nil), v...)
		c[len(c)/2] ^= 0xff
		bad[id] = c
		out.Reset()
		err := Decode(&out, rec, bad.get, Limits{})
		if err == nil && bytes.Equal(out.Bytes(), b) {
			t.Fatalf("tampered component %x went unnoticed", id[:6])
		}
		if n++; n >= 40 {
			break
		}
	}
	for i := 0; i < len(rec); i += max(1, len(rec)/700) {
		out.Reset()
		if err := Decode(&out, rec[:i], st.get, Limits{}); err == nil {
			t.Fatalf("recipe prefix of %d bytes decoded", i)
		}
	}
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 400; i++ {
		c := append([]byte(nil), rec...)
		for k := 0; k < 1+r.Intn(3); k++ {
			c[r.Intn(len(c))] ^= byte(1 + r.Intn(255))
		}
		out.Reset()
		if err := Decode(&out, c, st.get, Limits{}); err == nil && int64(out.Len()) != int64(len(b)) {
			t.Fatalf("flipped recipe decoded to %d bytes, declared %d", out.Len(), len(b))
		}
	}
	// writer failure
	if err := Decode(failWriter{}, rec, st.get, Limits{}); err == nil {
		t.Fatal("a failing writer was ignored")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// The same logical values stored under another codec, page size or layout share their
// value components: this is the property the representation exists for.
func TestValueComponentsAreSharedAcrossLayouts(t *testing.T) {
	a := load(t, "pa_snappy_v1.parquet")
	_, sa, _ := encodeFile(t, a, Options{})
	for _, name := range []string{"pa_zstd_v1.parquet", "pa_none_plain.parquet", "pa_snappy_v2.parquet"} {
		_, sb, _ := encodeFile(t, load(t, name), Options{})
		shared, total := 0, 0
		for id, v := range sb {
			total += len(v)
			if _, ok := sa[id]; ok {
				shared += len(v)
			}
		}
		if shared*100 < total*50 {
			t.Errorf("%s shares only %d of %d component bytes with the Snappy v1 file", name, shared, total)
		}
	}
	_ = sha256.Size
}

// FuzzDecode: arbitrary recipe bytes against a fixed store never panic and never write
// more than the recipe declares.
func FuzzDecode(f *testing.F) {
	b, err := os.ReadFile(filepath.Join("testdata", "pa_nested.parquet"))
	if err != nil {
		f.Fatal(err)
	}
	st := store{}
	rec, _, _, err := Encode(bytes.NewReader(b), int64(len(b)), Options{Target: 2 << 10}, st.emit)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(rec)
	f.Add(rec[:len(rec)/2])
	f.Fuzz(func(t *testing.T, r []byte) {
		var out bytes.Buffer
		lim := Limits{MaxPage: 8 << 20, MaxDict: 8 << 20}
		if err := Decode(&out, r, st.get, lim); err == nil {
			if pr, err := ParseRecipe(r, lim); err != nil || int64(out.Len()) != pr.Size {
				t.Fatalf("decoded %d bytes without error (%v)", out.Len(), err)
			}
		}
	})
}
