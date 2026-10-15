package sqlitez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"testing"
)

type store map[ID][]byte

func (s store) put(id ID, b []byte) error { s[id] = append([]byte(nil), b...); return nil }
func (s store) get(id ID) ([]byte, error) {
	if b, ok := s[id]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("missing component %x", id[:4])
}

type spyAt struct {
	b     []byte
	limit int64
	oob   bool
}

func (s *spyAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > s.limit {
		s.oob = true
	}
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func roundTrip(t *testing.T, db []byte, fan int) (store, []byte, Stats) {
	t.Helper()
	st := store{}
	spy := &spyAt{b: db, limit: int64(len(db))}
	rec, stats, err := Encode(spy, int64(len(db)), Options{Fanout: fan}, st.put)
	if err != nil {
		t.Fatal(err)
	}
	if spy.oob {
		t.Fatal("read past the declared size")
	}
	var out bytes.Buffer
	if err := Decode(&out, rec, st.get, Limits{}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != len(db) || sha256.Sum256(out.Bytes()) != sha256.Sum256(db) || !bytes.Equal(out.Bytes(), db) {
		t.Fatal("rebuild mismatch")
	}
	return st, rec, stats
}

func TestRoundTripFixtures(t *testing.T) {
	for _, n := range []string{"ps1024.sqlite", "ps4096.sqlite", "ps65536.sqlite"} {
		db := fixture(t, n)
		for _, fan := range []int{0, 2, 7, 64, 4096} {
			_, rec, st := roundTrip(t, db, fan)
			if st.Pages*st.PageSize != len(db) {
				t.Fatalf("%s: geometry %+v", n, st)
			}
			if n == "ps4096.sqlite" && fan == 0 && st.Split == 0 {
				t.Errorf("%s: expected overflow/freelist pages to be head-split", n)
			}
			// every page readable on its own, with one node + one body
			if fan == 7 {
				st2 := store{}
				rec2, _, _ := Encode(bytes.NewReader(db), int64(len(db)), Options{Fanout: 7}, st2.put)
				if !bytes.Equal(rec, rec2) {
					t.Fatal("recipe not deterministic")
				}
				for p := int64(0); p < int64(st.Pages); p++ {
					touched := 0
					pg, err := ReadPage(rec, p, func(id ID) ([]byte, error) { touched++; return st2.get(id) }, Limits{})
					if err != nil || !bytes.Equal(pg, db[p*int64(st.PageSize):(p+1)*int64(st.PageSize)]) || touched != 2 {
						t.Fatalf("page %d: err=%v touched=%d", p, err, touched)
					}
				}
				if _, err := ReadPage(rec, int64(st.Pages), st2.get, Limits{}); err == nil {
					t.Fatal("page past the end accepted")
				}
			}
		}
	}
}

// A moved overflow chain keeps its bodies: only the heads (deltas) change.
func TestMovedChainSharesBodies(t *testing.T) {
	const ps = 4096
	r := rand.New(rand.NewSource(1))
	mk := func(chainStart int) []byte {
		db := make([]byte, 12*ps)
		copy(db, "SQLite format 3\x00")
		binary.BigEndian.PutUint16(db[16:], ps)
		bodies := make([][]byte, 5)
		rr := rand.New(rand.NewSource(7))
		for i := range bodies {
			bodies[i] = make([]byte, ps-4)
			rr.Read(bodies[i])
		}
		for i := 1; i < 12; i++ {
			r.Read(db[i*ps+4 : (i+1)*ps]) // filler pages
			db[i*ps] = 5                  // b-tree interior type: never split
		}
		for i, b := range bodies {
			pg := chainStart + i
			copy(db[pg*ps+4:], b)
			next := uint32(0)
			if i < len(bodies)-1 {
				next = uint32(pg + 2) // page numbers are 1-based
			}
			binary.BigEndian.PutUint32(db[pg*ps:], next)
		}
		return db
	}
	a, b := mk(2), mk(6)
	sa, sb := store{}, store{}
	Encode(bytes.NewReader(a), int64(len(a)), Options{}, sa.put)
	Encode(bytes.NewReader(b), int64(len(b)), Options{}, sb.put)
	shared := 0
	for id := range sb {
		if _, ok := sa[id]; ok {
			shared++
		}
	}
	if shared < 5 {
		t.Fatalf("only %d shared components for a relocated 5-page chain", shared)
	}
	// the chain's nodes encode sequential next pointers as zero deltas
	_, _, st := roundTrip(t, a, 64)
	if st.Split != 5 {
		t.Fatalf("split pages %d, want 5", st.Split)
	}
}

func TestUnsupportedAndShortSources(t *testing.T) {
	db := fixture(t, "ps4096.sqlite")
	put := func(ID, []byte) error { return nil }
	for _, c := range []struct {
		name string
		b    []byte
	}{
		{"empty", nil}, {"not sqlite", bytes.Repeat([]byte{7}, 8192)}, {"short header", db[:99]}, {"under one page", db[:4000]},
	} {
		if _, _, err := Encode(bytes.NewReader(c.b), int64(len(c.b)), Options{}, put); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	bad := append([]byte(nil), db...)
	binary.BigEndian.PutUint16(bad[16:], 3000) // not a power of two
	if _, _, err := Encode(bytes.NewReader(bad), int64(len(bad)), Options{}, put); !errors.Is(err, ErrUnsupported) {
		t.Errorf("page size 3000: %v", err)
	}
	// source shorter than declared size, and a size above the limit
	if _, _, err := Encode(bytes.NewReader(db[:len(db)-1]), int64(len(db)), Options{}, put); !errors.Is(err, ErrCorrupt) {
		t.Errorf("short source: %v", err)
	}
	if _, _, err := Encode(bytes.NewReader(db), int64(len(db)), Options{Lim: Limits{MaxSize: 4096}}, put); !errors.Is(err, ErrLimit) {
		t.Errorf("limit: %v", err)
	}
	if _, _, err := Encode(bytes.NewReader(db), int64(len(db)), Options{Fanout: 1}, put); err == nil {
		t.Error("fanout 1 accepted")
	}
	// a sink failure and an I/O failure stop the encoder
	if _, _, err := Encode(bytes.NewReader(db), int64(len(db)), Options{}, func(ID, []byte) error { return errors.New("full") }); err == nil {
		t.Error("sink error swallowed")
	}
	// a non-multiple tail is stored in the recipe and survives
	odd := append(append([]byte(nil), db...), 1, 2, 3)
	roundTrip(t, odd, 0)
}

func TestMalformedRecipeAndComponents(t *testing.T) {
	db := fixture(t, "ps4096.sqlite")
	st, rec, _ := roundTrip(t, db, 8)
	// every truncation and every single-byte mutation of the recipe fails cleanly or still decodes without panic
	for n := 0; n < len(rec); n++ {
		if err := Decode(io.Discard, rec[:n], st.get, Limits{}); err == nil {
			t.Fatalf("truncation at %d of %d accepted", n, len(rec))
		}
	}
	for i := range rec {
		m := append([]byte(nil), rec...)
		m[i] ^= 0x5a
		var out bytes.Buffer
		if err := Decode(&out, m, st.get, Limits{}); err == nil && bytes.Equal(out.Bytes(), db) {
			t.Fatalf("mutation at %d produced the original", i)
		}
	}
	// hostile declared size: a tiny recipe naming terabytes allocates nothing and fails
	h := append([]byte(magic), binary.AppendUvarint(nil, 4096)...)
	h = binary.AppendUvarint(h, 64)
	h = binary.AppendUvarint(h, 1<<50)
	if _, err := ParseRecipe(h, Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("hostile size: %v", err)
	}
	h = append([]byte(magic), binary.AppendUvarint(nil, 4096)...)
	h = binary.AppendUvarint(h, 64)
	h = binary.AppendUvarint(h, 1<<39)
	if _, err := ParseRecipe(h, Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("recipe shorter than its geometry: %v", err)
	}
	// a component returned with the wrong bytes, a missing component, and a failing writer
	var any ID
	for id := range st {
		any = id
		break
	}
	bad := store{}
	for id, b := range st {
		bad[id] = b
	}
	bad[any] = append([]byte{9}, bad[any]...)
	if err := Decode(io.Discard, rec, bad.get, Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("tampered component: %v", err)
	}
	delete(bad, any)
	if err := Decode(io.Discard, rec, bad.get, Limits{}); err == nil {
		t.Error("missing component accepted")
	}
	if err := Decode(&limitedWriter{left: 5000}, rec, st.get, Limits{}); err == nil {
		t.Error("write error swallowed")
	}
	// a node whose entry count or flags are wrong is corrupt, never a panic
	node := append([]byte{0}, make([]byte, 32)...)
	for _, m := range []struct {
		b []byte
		n int64
	}{{node[:32], 1}, {append(append([]byte(nil), node...), 0), 1}, {append([]byte{2}, node[1:]...), 1}, {node, 2}, {[]byte{1, 0x80}, 1}} {
		if _, err := parseNode(m.b, m.n); !errors.Is(err, ErrCorrupt) {
			t.Errorf("node %x: %v", m.b, err)
		}
	}
	if _, err := parseNode(node, 1); err != nil {
		t.Error(err)
	}
}

type limitedWriter struct{ left int }

func (w *limitedWriter) Write(b []byte) (int, error) {
	if len(b) > w.left {
		return 0, errors.New("sink full")
	}
	w.left -= len(b)
	return len(b), nil
}

func FuzzDecodeRecipe(f *testing.F) {
	db, _ := os.ReadFile("testdata/ps1024.sqlite")
	st := store{}
	rec, _, _ := Encode(bytes.NewReader(db), int64(len(db)), Options{Fanout: 8}, st.put)
	f.Add(rec)
	f.Fuzz(func(t *testing.T, b []byte) {
		var out bytes.Buffer
		if err := Decode(&out, b, st.get, Limits{MaxSize: 1 << 24}); err == nil {
			rc, err := ParseRecipe(b, Limits{MaxSize: 1 << 24})
			if err != nil || int64(out.Len()) != rc.Size {
				t.Fatalf("decoded %d bytes for a recipe declaring %v", out.Len(), err)
			}
		}
	})
}
