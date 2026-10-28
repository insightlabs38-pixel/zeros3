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
				st := repz3.NewMemStore()
				id, _, err := b.Persist(st)
				if err != nil {
					t.Fatal(err)
				}
				for _, off := range []int64{0, int64(len(data)) / 2, max(0, int64(len(data))-4096)} {
					env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 4, MaxWhole: MaxRegion})
					call, err := env.Open(id)
					if err != nil {
						t.Fatal(err)
					}
					n := min(int64(4096), int64(len(data))-off)
					got, err := ReadRange(call, off, n)
					if err != nil || !bytes.Equal(got, data[off:off+n]) {
						t.Fatalf("cold serialized range: %v", err)
					}
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

func TestRangeAuthenticatesModelResidual(t *testing.T) {
	data, e := os.ReadFile("testdata/hdf5-raw.h5")
	if e != nil {
		t.Fatal(e)
	}
	plan, e := ParseHDF5(data)
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := Encode(data, plan, true)
	if e != nil {
		t.Fatal(e)
	}
	st := repz3.NewMemStore()
	id, _, e := b.Persist(st)
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Children) == 0 {
		t.Fatal("missing plane child")
	}
	leaf := b.Children[0].Rep.Members[0].SHA
	stored := st.Bytes(leaf)
	if len(stored) == 0 {
		t.Fatal("missing residual")
	}
	stored[0] ^= 0x80
	env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 4, MaxWhole: MaxRegion})
	call, e := env.Open(id)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ReadRange(call, plan.Regions[0].Off, min(int64(4096), plan.Regions[0].Len))
	if !errors.Is(e, repz3.ErrMismatch) {
		t.Fatalf("corrupt range residual not rejected: %v", e)
	}
}

func TestCumulativeEmbeddedWorkBeforeDecode(t *testing.T) {
	p := &Plan{}
	for i := int64(0); i < 65; i++ {
		p.Regions = append(p.Regions, Region{Off: i, Len: 1, Codec: "zstd", Decoded: MaxRegion})
	}
	// The bad frame bytes would otherwise take the local opaque fallback. The
	// aggregate declared-work policy rejects before entering a frame decoder.
	_, _, e := Encode(make([]byte, 65), p, true)
	if !errors.Is(e, ErrLimit) {
		t.Fatalf("aggregate embedded admission: %v", e)
	}
}
