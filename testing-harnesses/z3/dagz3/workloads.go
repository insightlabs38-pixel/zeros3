package dagz3

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"time"

	"zeros3-z3/carz3"
	"zeros3-z3/zstdz3"
)

// Deterministic artifact builders for the DAG workloads. They build small, real-shaped
// inputs (a row-text SQLite file, a CAR, tar archives, gzip and zstd streams) so every
// nesting combination is made from the existing exact mechanisms, not from wrappers.

type rng uint64

func (x *rng) next() uint64 {
	*x ^= *x << 13
	*x ^= *x >> 7
	*x ^= *x << 17
	return uint64(*x)
}

func (x *rng) fill(b []byte) {
	for i := 0; i < len(b); i += 8 {
		v := x.next()
		for j := 0; j < 8 && i+j < len(b); j++ {
			b[i+j] = byte(v >> (8 * uint(j)))
		}
	}
}

// Random returns n pseudo-random (incompressible) bytes.
func Random(seed uint64, n int) []byte {
	r := rng(seed | 1)
	b := make([]byte, n)
	r.fill(b)
	return b
}

var words = func() []string {
	var w []string
	for _, a := range []string{"user", "order", "item", "event", "session", "token", "price", "stock"} {
		for _, b := range []string{"id", "name", "ts", "state", "note", "ref", "qty", "kind"} {
			w = append(w, a+"_"+b)
		}
	}
	return w
}()

const pageSize = 4096

func textPage(r *rng) []byte {
	var b bytes.Buffer
	for b.Len() < pageSize {
		fmt.Fprintf(&b, "%s=%d;%s=%x;", words[r.next()%uint64(len(words))], r.next()%1000000007, words[r.next()%uint64(len(words))], r.next()%(1<<40))
	}
	return b.Bytes()[:pageSize]
}

// DB is a database-shaped file: page 1 carries the SQLite header fields, the other pages are
// row text (so DEFLATE and Zstandard find real matches) with the b-tree or overflow lead
// bytes. sqlitez3 is byte-level; no real b-tree is needed.
func DB(seed uint64, pages int) [][]byte {
	r := rng(seed | 1)
	out := make([][]byte, pages)
	for i := range out {
		p := textPage(&r)
		switch {
		case i == 0:
			copy(p, "SQLite format 3\x00")
			p[16], p[17] = 0x10, 0x00
		case r.next()%5 == 0:
			p[0], p[1], p[2], p[3] = 0, 0, 0, 0
		default:
			p[0] = 0x0d
		}
		out[i] = p
	}
	return out
}

// Join concatenates pages into a file.
func Join(pages [][]byte) []byte { return bytes.Join(pages, nil) }

// Mutate rewrites a 160-byte region of n distinct non-header pages (a sparse update).
func Mutate(pages [][]byte, seed uint64, n int) [][]byte {
	out := append([][]byte(nil), pages...)
	r := rng(seed | 1)
	done := map[int]bool{}
	for len(done) < n && len(done) < len(out)-1 {
		i := 1 + int(r.next()%uint64(len(out)-1))
		if done[i] {
			continue
		}
		done[i] = true
		q := append([]byte(nil), out[i]...)
		off := 16 + int(r.next()%uint64(len(q)-200))
		t := textPage(&r)[:160]
		copy(q[off:], t)
		out[i] = q
	}
	return out
}

var epoch = time.Unix(1700000000, 0).UTC()

// Tar writes a USTAR archive with fixed metadata.
func Tar(names []string, files map[string][]byte) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, n := range names {
		w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: epoch, Format: tar.FormatUSTAR})
		w.Write(files[n])
	}
	w.Close()
	return b.Bytes()
}

// Gzip compresses with the Go writer (deterministic: zero mtime, no name).
func Gzip(b []byte, level int) []byte {
	var o bytes.Buffer
	w, _ := gzip.NewWriterLevel(&o, level)
	w.Write(b)
	w.Close()
	return o.Bytes()
}

// Zstd produces a Zstandard frame with the laboratory synthesizer (libzstd-decodable).
func Zstd(b []byte) []byte {
	z, err := zstdz3.Synth(b)
	if err != nil {
		panic(err)
	}
	return z
}

// CAR returns revision i of the first tiny-block CAR family (reorder family).
func CAR(i int) []byte { return carz3.Families(carz3.TinyBlocks)[0].Revs[i].Data }

// CARAdd returns revision i (0..3) of the tiny-block growth family: blocks appended, inserted
// and prepended, so every revision shares most blocks with its predecessor.
func CARAdd(i int) []byte {
	for _, f := range carz3.Families(carz3.TinyBlocks) {
		if f.Name == "tiny-v1-add" {
			return f.Revs[i].Data
		}
	}
	panic("dagz3: CAR family tiny-v1-add missing")
}

// ELFish is a synthetic ELF-looking executable (header magic, random body). The repz3 kind
// table has no ELF/PE kind, so it exercises the "recognised but not a transform" fallback.
func ELFish(seed uint64, n int) []byte {
	b := Random(seed, n)
	copy(b, "\x7fELF\x02\x01\x01\x00")
	return b
}

// ChainA is the depth-4 chain  Zstd -> tar -> Gzip -> SQLite  (revision rev of the database).
func ChainA(rev, pages, changed int) []byte {
	pg := DB(28, pages)
	for k := 1; k <= rev; k++ {
		pg = Mutate(pg, uint64(2*k+101), changed) // odd seeds: rng(seed|1) would alias neighbours
	}
	tarb := Tar([]string{"etc/app.conf", "data/app.db.gz", "README"}, map[string][]byte{
		"etc/app.conf":   []byte("mode=prod\nretries=3\n"),
		"data/app.db.gz": Gzip(Join(pg), 6),
		"README":         bytes.Repeat([]byte("keep this file\n"), 40),
	})
	return Zstd(tarb)
}

// ChainB is the depth-4 chain  Gzip -> tar -> Zstd -> CAR  (revision rev of the CAR family).
func ChainB(rev int) []byte {
	tarb := Tar([]string{"manifest.json", "export/blocks.car.zst"}, map[string][]byte{
		"manifest.json":         []byte(`{"format":"car","compression":"zstd"}` + "\n"),
		"export/blocks.car.zst": Zstd(CARAdd(rev)),
	})
	return Gzip(tarb, 6)
}

// FanOut is one tar-like outer holding independently transformable members: a SQLite
// database, a byte-identical second copy of it, a CAR, a synthetic ELF, a gzip member that
// holds text, a database with a valid magic and an unparseable header (analysis must fail
// for it alone), a large raw blob and a small raw note.
func FanOut() (tarBytes []byte, names []string, files map[string][]byte) {
	db := Join(DB(51, 48))
	bad := Join(DB(53, 8))
	bad[16], bad[17] = 0x00, 0x07           // page size 7: SQLite magic, not a database sqlitez3 accepts
	text := Join(DB(54, 6))                 // row text: compresses, and the member stays above the analysis floor
	copy(text, "LOGDATA\n\n\n\n\n\n\n\n\n") // not a database
	files = map[string][]byte{
		"db/main.sqlite":    db,
		"db/replica.sqlite": db,
		"db/broken.sqlite":  bad,
		"export/blocks.car": CAR(0),
		"bin/app":           ELFish(7, 24<<10),
		"logs/app.log.gz":   Gzip(text, 6),
		"raw/blob.bin":      Random(9, 40<<10),
		"README":            []byte("fan-out members are independent\n"),
	}
	names = []string{"README", "db/main.sqlite", "export/blocks.car", "bin/app", "db/replica.sqlite", "raw/blob.bin", "logs/app.log.gz", "db/broken.sqlite"}
	return Tar(names, files), names, files
}
