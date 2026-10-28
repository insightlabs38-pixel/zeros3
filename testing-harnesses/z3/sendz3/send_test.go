package sendz3

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

func fixture(t testing.TB) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/native-v1.zsend")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativeOracleAndExactSerialization(t *testing.T) {
	data := fixture(t)
	raw, e := os.ReadFile("testdata/native-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var meta struct {
		SHA256 string
		Spans  [][2]int64 `json:"chunk_spans"`
	}
	if e = json.Unmarshal(raw, &meta); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != meta.SHA256 {
		t.Fatal("producer identity")
	}
	spans, e := Payloads(data)
	if e != nil || !reflect.DeepEqual(spans, meta.Spans) {
		t.Fatalf("native framing oracle: %v", e)
	}
	b, e := Encode(data)
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
	for _, off := range []int64{0, int64(len(data)) / 2, int64(len(data)) - 4096} {
		env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 4, MaxWhole: MaxPayload})
		call, e := env.Open(id)
		if e != nil {
			t.Fatal(e)
		}
		got, e := scientificz3.ReadRange(call, off, 4096)
		if e != nil || !bytes.Equal(got, data[off:off+4096]) {
			t.Fatalf("fresh serialized range: %v", e)
		}
	}
}
func TestFramingChecksumAndBounds(t *testing.T) {
	data := fixture(t)
	for _, off := range []int{0, 4, 8, 16, 40, 312, len(data) - 312 + 8, len(data) - 1} {
		bad := append([]byte(nil), data...)
		bad[off] ^= 0xff
		if _, e := Payloads(bad); e == nil {
			t.Fatalf("corruption accepted at %d", off)
		}
	}
	for _, n := range []int{0, 17, 311, 312, len(data) - 1, len(data) - 312} {
		if _, e := Payloads(data[:n]); e == nil {
			t.Fatal("truncation")
		}
	}
	raw, _ := os.ReadFile("testdata/native-v1.json")
	var meta struct {
		Records [][2]int64 `json:"resource_spans"`
	}
	if e := json.Unmarshal(raw, &meta); e != nil {
		t.Fatal(e)
	}
	for _, r := range meta.Records {
		off := int(r[0])
		typ := binary.LittleEndian.Uint32(data[off:])
		if typ == 3 {
			bad := append([]byte(nil), data...)
			binary.LittleEndian.PutUint64(bad[off+32:], ^uint64(0))
			if _, e := Payloads(bad); e == nil {
				t.Fatal("unbounded write")
			}
			break
		}
	}
	if _, e := Payloads(append(append([]byte(nil), data...), make([]byte, 312)...)); e == nil {
		t.Fatal("trailing record")
	}
}
func FuzzPayloadFraming(f *testing.F) {
	f.Add(fixture(f))
	f.Add([]byte("unsupported"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		spans, e := Payloads(b)
		if e != nil {
			return
		}
		var end int64
		for _, s := range spans {
			if s[0] < end || s[1] <= 0 || s[1] > MaxPayload || s[0]+s[1] > int64(len(b)) {
				t.Fatal("unsafe span")
			}
			end = s[0] + s[1]
		}
		out, e := Encode(b)
		if e != nil {
			t.Fatal(e)
		}
		if e = kinds.ProveLimits(nil, out, b, repz3.Limits{MaxDepth: 4}); e != nil {
			t.Fatal(e)
		}
	})
}
