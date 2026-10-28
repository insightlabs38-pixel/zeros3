package scientificz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/zstdz3"
)

type fixture struct {
	File, Format, SHA256 string
	Supported            bool
	Spans                [][2]int64
}

func fixtures(t testing.TB) []fixture {
	t.Helper()
	b, e := os.ReadFile("testdata/fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct{ Fixtures []fixture }
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	return data.Fixtures
}
func TestNativeFixturesAndExactComposition(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			data, e := os.ReadFile(filepath.Join("testdata", f.File))
			if e != nil {
				t.Fatal(e)
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != f.SHA256 {
				t.Fatal("producer bytes changed")
			}
			var p *Plan
			if f.Format == "hdf5" {
				p, e = ParseHDF5(data)
			} else {
				p, e = ParseArrow(data)
			}
			if !f.Supported {
				if !errors.Is(e, ErrUnsupported) {
					t.Fatalf("want conservative unsupported: %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if f.Format == "hdf5" {
				if len(p.Regions) != len(f.Spans) {
					t.Fatal("native chunk count")
				}
				for i, r := range p.Regions {
					if [2]int64{r.Off, r.Len} != f.Spans[i] {
						t.Fatal("native chunk offset/size")
					}
				}
			}
			for _, deep := range []bool{false, true} {
				b, _, e := Encode(data, p, deep)
				if e != nil {
					t.Fatal(e)
				}
				if e = kinds.ProveLimits(nil, b, data, repz3.Limits{MaxDepth: 4}); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestMetadataCorruptionAndLimits(t *testing.T) {
	for _, f := range fixtures(t) {
		if !f.Supported {
			continue
		}
		b, e := os.ReadFile(filepath.Join("testdata", f.File))
		if e != nil {
			t.Fatal(e)
		}
		if f.Format == "hdf5" {
			for _, sig := range []string{"\x89HDF", "OHDR", "FAHD", "FADB"} {
				i := bytes.Index(b, []byte(sig))
				if i < 0 {
					t.Fatal("missing producer header")
				}
				bad := append([]byte(nil), b...)
				bad[i+6] ^= 1
				if _, e = ParseHDF5(bad); e == nil {
					t.Fatalf("accepted corrupt %s", sig)
				}
			}
		} else {
			for _, n := range []int{0, 1, 4, 7, len(b) / 2, len(b) - 1} {
				if _, e = ParseArrow(b[:n]); e == nil {
					t.Fatalf("accepted truncation %d", n)
				}
			}
		}
	}
	// Independent native frame header declares128MiB; embedding limits reject it
	// before invoking a child builder or allocating its claimed history window.
	_, _, e := kinds.EncodeZstdLimited([]byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0x88}, func([]byte) (*kinds.Bundle, error) { panic("child before window validation") }, zstdz3.Limits{MaxWindow: 16 << 20, MaxTotalDecoded: 16 << 20})
	if e == nil {
		t.Fatal("accepted oversized embedded window")
	}
	p := &Plan{Regions: []Region{{Off: 1, Len: 2}, {Off: 2, Len: 2}}}
	if _, _, e = Encode(make([]byte, 8), p, true); e == nil {
		t.Fatal("accepted aliased regions")
	}
}
func FuzzScientificFraming(f *testing.F) {
	for _, v := range fixtures(f) {
		b, e := os.ReadFile(filepath.Join("testdata", v.File))
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		_, _ = ParseHDF5(b)
		_, _ = ParseArrow(b)
	})
}
