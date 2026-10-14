package tarz3

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
)

type fileSpec struct {
	name    string
	typ     byte
	body    []byte
	link    string
	mode    int64
	mtime   int64
	uid     int
	pax     map[string]string
	hdrOnly bool
}

func build(t testing.TB, format tar.Format, files []fileSpec, trailer int) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Typeflag: f.typ, Linkname: f.link, Mode: f.mode, Uid: f.uid, Format: format}
		if format == tar.FormatPAX {
			h.PAXRecords = f.pax
		}
		h.ModTime = timeUnix(f.mtime)
		if f.typ == tar.TypeReg || f.typ == 0 {
			h.Size = int64(len(f.body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			w.Write(f.body)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// optional extra record padding of zero blocks (GNU tar pads to 10240)
	return append(b, make([]byte, trailer)...)
}

func rnd(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	r.Read(b)
	return b
}

func corpusFiles(r *rand.Rand) []fileSpec {
	long := strings.Repeat("deep/", 30) + "file-with-a-long-name.txt" // > 100 bytes: PAX / GNU long
	return []fileSpec{
		{name: "dir/", typ: tar.TypeDir, mode: 0o755, mtime: 1700000000},
		{name: "dir/empty", typ: tar.TypeReg, mode: 0o644, mtime: 1700000001},
		{name: "dir/a.txt", typ: tar.TypeReg, body: rnd(r, 100), mode: 0o644, mtime: 1700000002},
		{name: "dir/exact512", typ: tar.TypeReg, body: rnd(r, 512), mode: 0o600, mtime: 1700000003},
		{name: "dir/b.bin", typ: tar.TypeReg, body: rnd(r, 1025), mode: 0o755, mtime: 1700000004, uid: 1000},
		{name: long, typ: tar.TypeReg, body: rnd(r, 700), mode: 0o644, mtime: 1700000005},
		{name: "dir/link", typ: tar.TypeSymlink, link: "a.txt", mode: 0o777, mtime: 1700000006},
		{name: "dir/hard", typ: tar.TypeLink, link: "dir/a.txt", mode: 0o644, mtime: 1700000007},
		{name: "dir/ünïcode-名前", typ: tar.TypeReg, body: rnd(r, 33), mode: 0o644, mtime: 1700000008, pax: map[string]string{"SCHILY.xattr.user.k": "v"}},
		{name: "big", typ: tar.TypeReg, body: rnd(r, 300_000), mode: 0o644, mtime: 1700000009},
	}
}

func joinAll(t testing.TB, b []byte, a *Archive) []byte {
	s, pays := Split(b, a)
	var out bytes.Buffer
	if err := Join(&out, s, func(i int) ([]byte, error) { return pays[i], nil }, Options{}); err != nil {
		t.Fatalf("join: %v", err)
	}
	return out.Bytes()
}

func TestParseAllFormatsExact(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, f := range []struct {
		name string
		fmt  tar.Format
	}{{"pax", tar.FormatPAX}, {"gnu", tar.FormatGNU}, {"ustar-short", tar.FormatUSTAR}} {
		files := corpusFiles(r)
		if f.fmt == tar.FormatUSTAR { // USTAR cannot carry the long name / pax records
			files = append(files[:5:5], files[6:8]...)
			files = append(files, fileSpec{name: "big", typ: tar.TypeReg, body: rnd(r, 70000), mode: 0o644})
		}
		for _, trailer := range []int{0, 10240 - 1024} {
			b := build(t, f.fmt, files, trailer)
			a, err := Parse(b, Options{})
			if err != nil {
				t.Fatalf("%s: %v", f.name, err)
			}
			if got := joinAll(t, b, a); !bytes.Equal(got, b) {
				t.Fatalf("%s trailer=%d: rebuild differs", f.name, trailer)
			}
			// agree with the standard reader on names, sizes, types and contents
			tr := tar.NewReader(bytes.NewReader(b))
			_, pays := Split(b, a)
			pi := 0
			for i := 0; ; i++ {
				h, err := tr.Next()
				if err == io.EOF {
					if i != len(a.Members) {
						t.Fatalf("%s: %d members, std reader %d", f.name, len(a.Members), i)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				m := a.Members[i]
				if m.Path != h.Name || m.PayLen != h.Size || (h.Typeflag == tar.TypeReg && m.Type != '0') {
					t.Fatalf("%s member %d: got %q/%d/%c, std %q/%d/%c", f.name, i, m.Path, m.PayLen, m.Type, h.Name, h.Size, h.Typeflag)
				}
				if h.Typeflag == tar.TypeSymlink && m.Link != h.Linkname {
					t.Fatalf("link %q vs %q", m.Link, h.Linkname)
				}
				if m.PayLen > 0 {
					body, _ := io.ReadAll(tr)
					if !bytes.Equal(body, pays[pi]) {
						t.Fatalf("%s member %d payload differs", f.name, i)
					}
					pi++
				}
			}
			if a.TrailerLen() < 1024 {
				t.Errorf("%s: trailer %d", f.name, a.TrailerLen())
			}
		}
	}
}

func TestPaxAndGnuPrefixesStayInStructure(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	files := corpusFiles(r)
	for _, fm := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		b := build(t, fm, files, 0)
		a, err := Parse(b, Options{})
		if err != nil {
			t.Fatal(err)
		}
		prefixed := 0
		for _, m := range a.Members {
			if m.Prefix > 0 {
				prefixed++
				if m.HdrLen < 2*BlockSize {
					t.Errorf("prefixed member with HdrLen %d", m.HdrLen)
				}
			}
		}
		if prefixed == 0 {
			t.Errorf("format %v: no prefix entries found", fm)
		}
		s, pays := Split(b, a)
		var n int
		for _, p := range pays {
			n += len(p)
		}
		if int64(len(s)) >= a.Size-int64(n) { // structure excludes padding
			t.Errorf("structure %d bytes, expected < %d", len(s), a.Size-int64(n))
		}
	}
}

func TestMalformedFailsSafe(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	b := build(t, tar.FormatPAX, corpusFiles(r)[:6], 0)
	// truncation at every block-ish offset must be unsupported or an exact structure
	for cut := 1; cut < len(b); cut += 211 {
		a, err := Parse(b[:cut], Options{})
		if err != nil {
			var u *UnsupportedError
			if !errors.As(err, &u) {
				t.Fatalf("cut %d: %T", cut, err)
			}
			continue
		}
		if got := joinAll(t, b[:cut], a); !bytes.Equal(got, b[:cut]) {
			t.Fatalf("cut %d accepted but rebuild differs", cut)
		}
	}
	// single-bit corruption anywhere: reject, or accept and still reconstruct exactly
	for i := 0; i < 4000; i++ {
		c := append([]byte(nil), b...)
		c[r.Intn(len(c))] ^= byte(1 << r.Intn(8))
		a, err := Parse(c, Options{})
		if err != nil {
			continue
		}
		if got := joinAll(t, c, a); !bytes.Equal(got, c) {
			t.Fatalf("corrupt input accepted but rebuild differs")
		}
	}
	// specific rejects
	bad := map[string]func([]byte){
		"checksum":    func(x []byte) { x[148] ^= 1 },
		"size":        func(x []byte) { copy(x[124:136], []byte("77777777777\x00")); fixSum(x[:512]) },
		"sparse type": func(x []byte) { x[156] = 'S'; fixSum(x[:512]) },
		"bad size":    func(x []byte) { copy(x[124:136], []byte("zzzzzzzzzzz\x00")); fixSum(x[:512]) },
	}
	for name, f := range bad {
		c := append([]byte(nil), build(t, tar.FormatUSTAR, []fileSpec{{name: "f", typ: tar.TypeReg, body: []byte("hello"), mode: 0o644}}, 0)...)
		f(c)
		if _, err := Parse(c, Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// non-zero padding after the payload
	c := build(t, tar.FormatUSTAR, []fileSpec{{name: "f", typ: tar.TypeReg, body: []byte("hello"), mode: 0o644}}, 0)
	c[512+5] = 1
	if _, err := Parse(c, Options{}); err == nil {
		t.Error("non-zero padding accepted")
	}
	// payload size changed in structure must be refused by Join
	good := build(t, tar.FormatUSTAR, []fileSpec{{name: "f", typ: tar.TypeReg, body: []byte("hello"), mode: 0o644}}, 0)
	a, _ := Parse(good, Options{})
	s, _ := Split(good, a)
	if err := Join(io.Discard, s, func(int) ([]byte, error) { return []byte("hi"), nil }, Options{}); err == nil {
		t.Error("short payload accepted by Join")
	}
}

func fixSum(h []byte) {
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	var s int64
	for _, c := range h {
		s += int64(c)
	}
	v := []byte(strings.Repeat("0", 6) + "")
	oct := []byte(strings.TrimLeft(string(append([]byte{}, []byte(itoaOct(s))...)), "0"))
	copy(v[6-len(oct):], oct)
	copy(h[148:154], v)
	h[154], h[155] = 0, ' '
}

func itoaOct(n int64) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 8 {
		d = append([]byte{byte('0' + n%8)}, d...)
	}
	return string(d)
}

func TestEmptyAndTrailerOnly(t *testing.T) {
	for _, b := range [][]byte{nil, make([]byte, 1024), make([]byte, 10240)} {
		a, err := Parse(b, Options{})
		if err != nil || len(a.Members) != 0 || a.TrailOff != 0 {
			t.Fatalf("len %d: %v %+v", len(b), err, a)
		}
		if !bytes.Equal(joinAll(t, b, a), b) {
			t.Fatal("rebuild differs")
		}
	}
}

func TestBase256Size(t *testing.T) {
	// a header whose size field uses GNU base-256 (as for files > 8 GiB), small value here
	b := build(t, tar.FormatGNU, []fileSpec{{name: "f", typ: tar.TypeReg, body: []byte("hello"), mode: 0o644}}, 0)
	for i := 124; i < 136; i++ {
		b[i] = 0
	}
	b[124] = 0x80
	b[135] = 5
	fixSum(b[:512])
	a, err := Parse(b, Options{})
	if err != nil || a.Members[0].PayLen != 5 {
		t.Fatalf("%v %+v", err, a)
	}
	if !bytes.Equal(joinAll(t, b, a), b) {
		t.Fatal("rebuild differs")
	}
}

// FuzzParseJoin: whatever Parse accepts, Split+Join rebuilds byte for byte.
func FuzzParseJoin(f *testing.F) {
	r := rand.New(rand.NewSource(4))
	f.Add(build(f, tar.FormatPAX, corpusFiles(r)[:7], 0))
	f.Add(build(f, tar.FormatGNU, corpusFiles(r)[:7], 1024))
	f.Add(make([]byte, 1024))
	f.Fuzz(func(t *testing.T, b []byte) {
		a, err := Parse(b, Options{})
		if err != nil {
			return
		}
		if got := joinAll(t, b, a); !bytes.Equal(got, b) {
			t.Fatalf("accepted input does not rebuild")
		}
	})
}
