package tensorgrid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGridUnalignedEdgesAndCorruption(t *testing.T) {
	const rows, cols, width = 53, 99, 4
	src := make([]byte, rows*cols*width)
	for i := range src {
		src[i] = byte(i*i + 59*i + i/11)
	}
	reordered := make([]byte, len(src))
	for r := range rows {
		for c := range cols {
			in, out := (r*cols+c)*width, (c*rows+r)*width
			copy(reordered[out:out+width], src[in:in+width])
		}
	}
	var file, transposedFile bytes.Buffer
	idx, err := Build(&file, bytes.NewReader(src), rows, cols, width, false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Build(&transposedFile, bytes.NewReader(reordered), rows, cols, width, true)
	if err != nil || !reflect.DeepEqual(idx, other) || !bytes.Equal(file.Bytes(), transposedFile.Bytes()) {
		t.Fatal("orientation changed canonical tile bytes or digests", err)
	}
	for _, view := range []struct {
		want       []byte
		transposed bool
	}{{src, false}, {reordered, true}} {
		g, err := Open(bytes.NewReader(file.Bytes()), int64(file.Len()), idx, view.transposed)
		if err != nil {
			t.Fatal(err)
		}
		var all bytes.Buffer
		if n, err := g.WriteTo(&all); err != nil || n != int64(len(src)) || !bytes.Equal(all.Bytes(), view.want) {
			t.Fatalf("whole edge tile replay wrote %d bytes, %v", n, err)
		}
		for off := 0; off < len(src); off += 317 {
			b := make([]byte, 769)
			n, err := g.ReadAt(b, int64(off))
			want := view.want[off:min(off+len(b), len(src))]
			if !bytes.Equal(b[:n], want) || (n < len(b) && !errors.Is(err, io.EOF)) || (n == len(b) && err != nil) {
				t.Fatalf("offset %d read %d: %v", off, n, err)
			}
		}
	}
	bad := bytes.Clone(file.Bytes())
	bad[31] ^= 1
	g, err := Open(bytes.NewReader(bad), int64(len(bad)), idx, true)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := g.ReadAt(make([]byte, 10), 0); n != 0 || err == nil {
		t.Fatalf("damaged tile emitted %d bytes with error %v", n, err)
	}
}

func TestPinnedTransposeRanges(t *testing.T) {
	dir := os.Getenv("ZEROS3_C5_GRID_FIXTURES")
	if dir == "" {
		t.Skip("set ZEROS3_C5_GRID_FIXTURES to the verified extracted tensor pairs")
	}
	var cases []struct {
		Name, BaseSHA256, TransposeSHA256 string
		Rows, Cols, Width                 int
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatal("empty or invalid tensor pairs", err)
	}
	type sample struct {
		Offset int64 `json:"offset"`
		Size   int   `json:"length"`
		ReadStats
		Micros int64 `json:"us"`
	}
	type evidence struct {
		Name             string   `json:"tensor"`
		Orientation      string   `json:"orientation"`
		DataFileBytes    int64    `json:"shared_tile_file_bytes"`
		IndexBytes       int      `json:"digest_table_bytes"`
		Samples          []sample `json:"samples"`
		WholeMiBPS       float64  `json:"whole_mib_s"`
		SliceMiBPS       float64  `json:"sliced_whole_mib_s"`
		ReconstructedSHA string   `json:"whole_sha256"`
	}
	var reports []evidence
	for _, c := range cases {
		p := filepath.Join(dir, filepath.Base(c.Name))
		base, err := os.ReadFile(p + ".base")
		if err != nil {
			t.Fatal(err)
		}
		reordered, err := os.ReadFile(p + ".transpose")
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range []struct {
			data     []byte
			expected string
		}{{base, c.BaseSHA256}, {reordered, c.TransposeSHA256}} {
			stamp := sha256.Sum256(pair.data)
			if hex.EncodeToString(stamp[:]) != pair.expected {
				t.Fatalf("%s fixture checksum mismatch", c.Name)
			}
		}
		f, err := os.Create(filepath.Join(t.TempDir(), "tiles"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		idx, err := Build(f, bytes.NewReader(base), c.Rows, c.Cols, c.Width, false)
		if err != nil {
			t.Fatal(err)
		}
		other, err := Build(io.Discard, bytes.NewReader(reordered), c.Rows, c.Cols, c.Width, true)
		if err != nil || !reflect.DeepEqual(idx, other) {
			t.Fatalf("%s did not produce the same tiled basis: %v", c.Name, err)
		}
		info, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range []struct {
			raw        []byte
			name, hash string
			reversed   bool
		}{{base, "original", c.BaseSHA256, false}, {reordered, "transpose", c.TransposeSHA256, true}} {
			g, err := Open(f, info.Size(), idx, view.reversed)
			if err != nil {
				t.Fatal(err)
			}
			m := evidence{Name: c.Name, Orientation: view.name, DataFileBytes: info.Size(), IndexBytes: 32 * len(idx.Tiles)}
			n := int64(len(view.raw))
			for _, off := range []int64{0, n/5 + 1, n/2 - 7, n - 4096} {
				b := make([]byte, 4096)
				start := time.Now()
				_, reads, err := g.ReadAtMeasured(b, off)
				elapsed := time.Since(start)
				if err != nil || !bytes.Equal(b, view.raw[off:off+int64(len(b))]) {
					t.Fatalf("%s %s range %d differs: %v", c.Name, view.name, off, err)
				}
				m.Samples = append(m.Samples, sample{off, len(b), reads, elapsed.Microseconds()})
			}
			h := sha256.New()
			start := time.Now()
			got, err := io.CopyBuffer(h, io.NewSectionReader(g, 0, n), make([]byte, 64<<10))
			elapsed := time.Since(start)
			m.ReconstructedSHA = hex.EncodeToString(h.Sum(nil))
			if err != nil || got != n || m.ReconstructedSHA != view.hash {
				t.Fatalf("%s %s whole replay mismatch: %v", c.Name, view.name, err)
			}
			m.SliceMiBPS = float64(n) / (1 << 20) / elapsed.Seconds()
			start = time.Now()
			h.Reset()
			got, err = g.WriteTo(h)
			elapsed = time.Since(start)
			if err != nil || got != n || hex.EncodeToString(h.Sum(nil)) != view.hash {
				t.Fatalf("%s %s row-band replay mismatch: %v", c.Name, view.name, err)
			}
			m.WholeMiBPS = float64(n) / (1 << 20) / elapsed.Seconds()
			reports = append(reports, m)
		}
	}
	if out := os.Getenv("ZEROS3_C5_GRID_OUT"); out != "" {
		raw, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
