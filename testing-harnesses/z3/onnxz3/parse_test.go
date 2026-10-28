package onnxz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/scientificz3"
)

type fixtureMeta struct {
	File, SHA256 string
	Tensors      []struct {
		Name     string
		Type     int
		Dims     []uint64
		RawBytes int    `json:"raw_bytes"`
		RawSHA   string `json:"raw_sha256"`
		External bool
	}
}

func fixtures(t testing.TB) []fixtureMeta {
	t.Helper()
	b, e := os.ReadFile("testdata/fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	var m struct{ Fixtures []fixtureMeta }
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	return m.Fixtures
}
func TestNativeMetadataExactModesAndExternalIsolation(t *testing.T) {
	if _, e := os.Stat("testdata/weights.bin"); !os.IsNotExist(e) {
		t.Fatal("external sidecar must be absent from graph-byte test")
	}
	for _, f := range fixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			data, e := os.ReadFile("testdata/" + f.File)
			if e != nil {
				t.Fatal(e)
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != f.SHA256 {
				t.Fatal("native fixture identity")
			}
			p, e := Parse(data)
			if e != nil {
				t.Fatal(e)
			}
			raw, external, opaque := 0, 0, 0
			for _, want := range f.Tensors {
				if want.External {
					external++
					continue
				}
				if want.RawBytes == 0 {
					opaque++
					continue
				}
				raw++
				var got *Tensor
				for i := range p.Tensors {
					if p.Tensors[i].Name == want.Name {
						got = &p.Tensors[i]
					}
				}
				if got == nil || got.Type != want.Type || !reflect.DeepEqual(got.Dims, want.Dims) || got.Len != int64(want.RawBytes) || fmt.Sprintf("%x", sha256.Sum256(data[got.Off:got.Off+got.Len])) != want.RawSHA {
					t.Fatal("native initializer metadata/payload mismatch")
				}
			}
			if len(p.Tensors) != raw || p.External != external || p.Opaque != opaque {
				t.Fatal("native profile count mismatch")
			}
			for _, mode := range []string{"raw", "fine", "planes"} {
				b, e := Encode(data, p, mode)
				if e != nil {
					t.Fatal(e)
				}
				if e = kinds.ProveLimits(nil, b, data, repz3.Limits{MaxDepth: 4}); e != nil {
					t.Fatal(e)
				}
				st := repz3.NewMemStore()
				id, _, e := b.Persist(st)
				if e != nil {
					t.Fatal(e)
				}
				for _, off := range []int64{0, int64(len(data)) / 2} {
					env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 4, MaxWhole: MaxTensor})
					call, e := env.Open(id)
					if e != nil {
						t.Fatal(e)
					}
					n := min(int64(128), int64(len(data))-off)
					got, e := scientificz3.ReadRange(call, off, n)
					if e != nil || !bytes.Equal(got, data[off:off+n]) {
						t.Fatalf("fresh serialized range %s: %v", mode, e)
					}
				}
			}
		})
	}
}
func v(n uint64) []byte {
	var b [10]byte
	k := binary.PutUvarint(b[:], n)
	return append([]byte(nil), b[:k]...)
}
func numeric(n, value uint64) []byte { return append(v(n<<3), v(value)...) }
func blob(n uint64, b []byte) []byte { return append(append(v(n<<3|2), v(uint64(len(b)))...), b...) }
func tensor(dims []uint64, raw []byte) []byte {
	var b []byte
	for _, d := range dims {
		b = append(b, numeric(1, d)...)
	}
	b = append(b, numeric(2, 1)...)
	b = append(b, blob(8, []byte("weight"))...)
	return append(b, blob(9, raw)...)
}
func model(tensor []byte) []byte { return blob(7, blob(5, tensor)) }
func TestMalformedShapesAndOpaqueFields(t *testing.T) {
	valid := tensor([]uint64{4}, make([]byte, 16))
	for _, b := range [][]byte{nil, {0}, {0x3b}, bytes.Repeat([]byte{255}, 11), model(tensor([]uint64{MaxTensor + 1}, nil)), model(tensor([]uint64{4}, make([]byte, 12))), model(append(valid, numeric(2, 1)...)), blob(7, append(blob(5, valid), blob(5, valid)...)), model(append(valid, blob(9, make([]byte, 16))...)), model(append(valid, numeric(14, 1)...))} {
		if _, e := Parse(b); e == nil {
			t.Fatal("malformed framing/shape accepted")
		}
	}
	b := model(valid)
	b = append(b, blob(1000, []byte("opaque unknown field"))...)
	p, e := Parse(b)
	if e != nil || len(p.Tensors) != 1 {
		t.Fatal("unknown field not preserved")
	}
	out, e := Encode(b, p, "planes")
	if e != nil {
		t.Fatal(e)
	}
	if e = kinds.ProveLimits(nil, out, b, repz3.Limits{MaxDepth: 4}); e != nil {
		t.Fatal(e)
	}
	// A protobuf length cannot allocate or walk past the source, even if huge.
	bad := append(v(7<<3|2), v(^uint64(0))...)
	if _, e := Parse(bad); e == nil {
		t.Fatal("unbounded protobuf span")
	}
}
func FuzzInitializerFraming(f *testing.F) {
	for _, m := range fixtures(f) {
		b, e := os.ReadFile("testdata/" + m.File)
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Add(model(tensor([]uint64{4}, make([]byte, 16))))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		p, e := Parse(b)
		if e != nil {
			return
		}
		for _, mode := range []string{"raw", "fine", "planes"} {
			out, e := Encode(b, p, mode)
			if e != nil {
				return
			}
			if e = kinds.ProveLimits(nil, out, b, repz3.Limits{MaxDepth: 4}); e != nil {
				t.Fatal(e)
			}
		}
	})
}
