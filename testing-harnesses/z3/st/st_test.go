package st

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func sampleFile(t testing.TB) ([]byte, []BuildTensor) {
	r := rand.New(rand.NewSource(1))
	mk := func(n int) []byte { b := make([]byte, n); r.Read(b); return b }
	ts := []BuildTensor{
		{"embed.weight", "F16", []int64{10, 8}, mk(160)},
		{"layer.0.attn.q.weight", "BF16", []int64{4, 4}, mk(32)},
		{"layer.0.norm.bias", "F32", []int64{4}, mk(16)},
		{"empty", "F32", []int64{0, 3}, nil},
		{"scalar", "I64", []int64{}, mk(8)},
		{"flags", "BOOL", []int64{5}, mk(5)},
		{"q4", "F4", []int64{16}, mk(8)},
	}
	return Build([]KV{{"format", "pt"}, {"note", "héllo \"q\""}}, ts), ts
}

func TestParseRoundtrip(t *testing.T) {
	b, ts := sampleFile(t)
	cr := &CountingReaderAt{R: bytes.NewReader(b)}
	f, err := Parse(cr, int64(len(b)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Bytes != 8+f.HeaderLen || cr.Reads != 2 {
		t.Fatalf("parse read %d bytes in %d reads, want header only (%d)", cr.Bytes, cr.Reads, 8+f.HeaderLen)
	}
	if len(f.Tensors) != len(ts) || f.Metadata["format"] != "pt" || f.Metadata["note"] != "héllo \"q\"" {
		t.Fatalf("tensors %d metadata %v", len(f.Tensors), f.Metadata)
	}
	for _, w := range ts {
		g, ok := f.Lookup(w.Name)
		if !ok || g.DType != w.DType || int(g.Size()) != len(w.Data) {
			t.Fatalf("tensor %s: %+v", w.Name, g)
		}
		got, err := f.ReadTensor(bytes.NewReader(b), g, 0, g.Size())
		if err != nil || !bytes.Equal(got, w.Data) {
			t.Fatalf("tensor %s payload", w.Name)
		}
	}
	if f.PadLen == 0 && len(f.Header)%8 != 0 {
		t.Fatal("padding not detected")
	}
	var out bytes.Buffer
	if err := f.Rebuild(&out, bytes.NewReader(b)); err != nil || !bytes.Equal(out.Bytes(), b) {
		t.Fatalf("rebuild differs (%v)", err)
	}
	var total int64
	for _, s := range f.Segments() {
		total += s.Len
	}
	if total != int64(len(b)) {
		t.Fatalf("segments cover %d of %d bytes", total, len(b))
	}
	if _, err := f.ReadTensor(bytes.NewReader(b), &f.Tensors[0], 0, f.Tensors[0].Size()+1); err == nil {
		t.Fatal("read beyond a tensor accepted")
	}
}

func hdrFile(hdr string, data int) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(len(hdr)))
	b = append(b, hdr...)
	return append(b, make([]byte, data)...)
}

func TestMalformed(t *testing.T) {
	good := `{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`
	cases := []struct {
		name string
		file []byte
		want error
	}{
		{"empty", nil, ErrMalformed},
		{"short prefix", []byte{1, 2, 3}, ErrMalformed},
		{"header longer than file", append(binary.LittleEndian.AppendUint64(nil, 1000), '{', '}'), ErrMalformed},
		{"absurd header length", binary.LittleEndian.AppendUint64(nil, 1<<62), ErrLimit},
		{"max u64 header length", binary.LittleEndian.AppendUint64(nil, ^uint64(0)), ErrLimit},
		{"header too small", hdrFile("{", 0), ErrMalformed},
		{"not an object", hdrFile(`["a"]    `, 0), ErrMalformed},
		{"invalid json", hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]`, 8), ErrMalformed},
		{"trailing data in header", hdrFile(good+`x`, 8), ErrMalformed},
		{"second object in header", hdrFile(good+good, 8), ErrMalformed},
		{"duplicate tensor", hdrFile(`{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]},"a":{"dtype":"F32","shape":[1],"data_offsets":[4,8]}}`, 8), ErrMalformed},
		{"duplicate field", hdrFile(`{"a":{"dtype":"F32","dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"unknown field", hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8],"x":1}}`, 8), ErrMalformed},
		{"missing offsets", hdrFile(`{"a":{"dtype":"F32","shape":[2]}}`, 8), ErrMalformed},
		{"unknown dtype", hdrFile(`{"a":{"dtype":"F128","shape":[2],"data_offsets":[0,8]}}`, 8), ErrUnsupDType},
		{"dtype not a string", hdrFile(`{"a":{"dtype":5,"shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"size mismatch", hdrFile(`{"a":{"dtype":"F32","shape":[3],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"negative dimension", hdrFile(`{"a":{"dtype":"F32","shape":[-1],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"float dimension", hdrFile(`{"a":{"dtype":"F32","shape":[2.0],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"exponent offset", hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8e0]}}`, 8), ErrMalformed},
		{"huge dimensions overflow", hdrFile(`{"a":{"dtype":"F32","shape":[4294967296,4294967296,4294967296],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"huge offsets", hdrFile(`{"a":{"dtype":"U8","shape":[1],"data_offsets":[9223372036854775807,9223372036854775808]}}`, 8), ErrMalformed},
		{"offset past end", hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 4), ErrMalformed},
		{"inverted offsets", hdrFile(`{"a":{"dtype":"F32","shape":[0],"data_offsets":[8,0]}}`, 8), ErrMalformed},
		{"overlap", hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]},"b":{"dtype":"F32","shape":[1],"data_offsets":[4,8]}}`, 8), ErrMalformed},
		{"hole", hdrFile(`{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]},"b":{"dtype":"F32","shape":[1],"data_offsets":[8,12]}}`, 12), ErrMalformed},
		{"not starting at zero", hdrFile(`{"a":{"dtype":"F32","shape":[1],"data_offsets":[4,8]}}`, 8), ErrMalformed},
		{"unreferenced trailing bytes", hdrFile(good, 12), ErrMalformed},
		{"metadata not strings", hdrFile(`{"__metadata__":{"k":1},"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"metadata not an object", hdrFile(`{"__metadata__":"x","a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"duplicate metadata key", hdrFile(`{"__metadata__":{"k":"1","k":"2"},"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"duplicate metadata", hdrFile(`{"__metadata__":{},"__metadata__":{},"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8), ErrMalformed},
		{"sub-byte size", hdrFile(`{"a":{"dtype":"F4","shape":[3],"data_offsets":[0,2]}}`, 2), ErrMalformed},
		{"too many dimensions", hdrFile(`{"a":{"dtype":"U8","shape":[`+strings.Repeat("1,", 40)+`1],"data_offsets":[0,1]}}`, 1), ErrLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseBytes(c.file, Options{})
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("error %v, want %v", err, c.want)
			}
		})
	}
	t.Run("tensor count limit", func(t *testing.T) {
		var h strings.Builder
		h.WriteByte('{')
		for i := 0; i < 50; i++ {
			if i > 0 {
				h.WriteByte(',')
			}
			fmt.Fprintf(&h, `"t%d":{"dtype":"U8","shape":[0],"data_offsets":[0,0]}`, i)
		}
		h.WriteByte('}')
		if _, err := ParseBytes(hdrFile(h.String(), 0), Options{MaxTensors: 10}); !errors.Is(err, ErrLimit) {
			t.Fatalf("got %v", err)
		}
		if _, err := ParseBytes(hdrFile(h.String(), 0), Options{}); err != nil {
			t.Fatalf("50 empty tensors: %v", err)
		}
	})
	t.Run("unknown dtype allowed as opaque", func(t *testing.T) {
		f, err := ParseBytes(hdrFile(`{"a":{"dtype":"F128","shape":[2],"data_offsets":[0,8]}}`, 8), Options{AllowUnknownDType: true})
		if err != nil || f.Tensors[0].Bits != 0 {
			t.Fatalf("%v %+v", err, f)
		}
	})
	t.Run("header limit option", func(t *testing.T) {
		b, _ := sampleFile(t)
		if _, err := ParseBytes(b, Options{MaxHeader: 16}); !errors.Is(err, ErrLimit) {
			t.Fatalf("got %v", err)
		}
	})
}

// Differential check against the reference implementation, when a python with
// the `safetensors` package is available (Z3_PYTHON) and optionally on real
// downloaded files (Z3_ST_FILES, comma separated).
func TestAgainstReference(t *testing.T) {
	py := os.Getenv("Z3_PYTHON")
	if py == "" {
		t.Skip("Z3_PYTHON not set")
	}
	if err := exec.Command(py, "-I", "-c", "import safetensors").Run(); err != nil {
		t.Skip("python has no safetensors package")
	}
	b, _ := sampleFile(t)
	dir := t.TempDir()
	files := []string{dir + "/sample.safetensors"}
	os.WriteFile(files[0], b, 0o644)
	if extra := os.Getenv("Z3_ST_FILES"); extra != "" {
		files = append(files, strings.Split(extra, ",")...)
	}
	for _, p := range files {
		fb, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := ParseBytes(fb, Options{})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out, err := exec.Command(py, "-I", "-c", `import sys, json
from safetensors import safe_open
r = {}
with safe_open(sys.argv[1], framework="numpy") as f:
    r["n"] = len(list(f.keys()))
    r["meta"] = f.metadata() or {}
print(json.dumps(r))`, p).CombinedOutput()
		if err != nil {
			t.Skipf("reference reader unavailable: %v: %s", err, bytes.TrimSpace(out))
		}
		t.Logf("%s reference: %s; ours: %d tensors", p, bytes.TrimSpace(out), len(f.Tensors))
		if !strings.Contains(string(out), fmt.Sprintf(`"n": %d`, len(f.Tensors))) {
			t.Errorf("%s: tensor count differs: %s vs %d", p, bytes.TrimSpace(out), len(f.Tensors))
		}
	}
}

func FuzzParse(f *testing.F) {
	b, _ := sampleFile(f)
	f.Add(b)
	f.Add(hdrFile(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`, 8))
	f.Add(hdrFile(`{"__metadata__":{"k":"v"},"a":{"dtype":"BF16","shape":[0],"data_offsets":[0,0]}}`, 0))
	f.Add(hdrFile(`{"a":{"dtype":"F32","shape":[4294967296,4294967296,4294967296],"data_offsets":[0,8]}}`, 8))
	f.Add(binary.LittleEndian.AppendUint64(nil, ^uint64(0)))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		fl, err := ParseBytes(data, Options{MaxHeader: 1 << 18, MaxTensors: 4096})
		if err != nil {
			return
		}
		// accepted structure never reads out of bounds and tiles the file exactly
		var next int64 = fl.DataStart
		names := map[string]bool{}
		for i := range fl.Tensors {
			tt := &fl.Tensors[i]
			if names[tt.Name] {
				t.Fatalf("duplicate name %q accepted", tt.Name)
			}
			names[tt.Name] = true
			if fl.DataStart+tt.Begin != next || tt.End < tt.Begin || fl.DataStart+tt.End > int64(len(data)) {
				t.Fatalf("tensor %q [%d,%d) breaks the tiling (data starts %d, file %d)", tt.Name, tt.Begin, tt.End, fl.DataStart, len(data))
			}
			next = fl.DataStart + tt.End
			if tt.Bits != 0 && tt.Elems*int64(tt.Bits) != tt.Size()*8 {
				t.Fatalf("tensor %q size does not match dtype x shape", tt.Name)
			}
			if _, err := fl.ReadTensor(bytes.NewReader(data), tt, 0, tt.Size()); err != nil {
				t.Fatalf("read of an accepted tensor failed: %v", err)
			}
		}
		if next != int64(len(data)) {
			t.Fatalf("tensors end at %d of %d", next, len(data))
		}
		var out bytes.Buffer
		if err := fl.Rebuild(&out, bytes.NewReader(data)); err != nil || !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("rebuild differs: %v", err)
		}
	})
}

// FuzzHeader fuzzes the JSON header alone, wrapped in a correctly sized file
// with zero tensor data of the size the header asks for (bounded).
func FuzzHeader(f *testing.F) {
	f.Add(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`)
	f.Add(`{"__metadata__":{"x":"y"}}`)
	f.Add(`{"a":{"dtype":"U8","shape":[1,1],"data_offsets":[0,1]},"b":{"dtype":"U8","shape":[0],"data_offsets":[1,1]}}`)
	f.Fuzz(func(t *testing.T, hdr string) {
		if len(hdr) > 1<<16 {
			return
		}
		for _, data := range []int{0, 8, 1 << 12} {
			fl, err := ParseBytes(hdrFile(hdr, data), Options{MaxTensors: 1024})
			if err != nil {
				continue
			}
			if fl.DataLen != int64(data) {
				t.Fatalf("data length %d, want %d", fl.DataLen, data)
			}
		}
	})
}
