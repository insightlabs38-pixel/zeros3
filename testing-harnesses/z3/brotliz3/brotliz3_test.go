package brotliz3_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"

	"zeros3-z3/brotliz3"
	_ "zeros3-z3/brotliz3/brdict"
)

// Encoder conformance: testdata/golden.txt holds SHA-256 of google/brotli's output (oracle/brotli/
// refenc.py, brotli 1.2.0) for deterministic inputs. BROTLIZ3_ORACLE=<path to refenc.py> compares
// bytes with the oracle directly; with BROTLIZ3_WRITE_GOLDEN=1 it rewrites the golden file from
// the oracle output (never from this package).

type vec struct {
	name string
	data []byte
}

func gen(kind string, n int, r *rand.Rand) []byte {
	out := make([]byte, 0, n+64)
	words := []string{"alpha", "bravo", "charlie", "delta", "the ", "and ", "<div>", "</div>", "\n", " ", "http://", "value", "information", "available"}
	switch kind {
	case "text":
		for len(out) < n {
			out = append(out, words[r.Intn(len(words))]...)
			if r.Intn(3) == 0 {
				out = append(out, fmt.Sprintf("%04x", r.Intn(65536))...)
			}
		}
	case "rand":
		out = out[:n]
		r.Read(out)
	case "zeros":
		out = out[:n]
	case "ints":
		for i := 0; len(out) < n; i++ {
			out = binary.LittleEndian.AppendUint64(out, uint64(i*1000+r.Intn(3)))
		}
	case "floats":
		for len(out) < n {
			out = binary.LittleEndian.AppendUint64(out, math.Float64bits(r.NormFloat64()))
		}
	case "low":
		for len(out) < n {
			out = append(out, "abcd"[r.Intn(4)])
		}
	case "mixed":
		kinds := []string{"text", "rand", "zeros", "ints", "low"}
		for len(out) < n {
			out = append(out, gen(kinds[r.Intn(len(kinds))], []int{100, 3000, 70000}[r.Intn(3)], r)...)
		}
	}
	return out[:n]
}

func vectors() []vec {
	var v []vec
	r := rand.New(rand.NewSource(11))
	for _, k := range []string{"text", "rand", "zeros", "ints", "floats", "low", "mixed"} {
		for _, n := range []int{1, 2, 3, 10, 63, 64, 65, 300, 4096, 65535, 65536, 65537, 200000, 1048575, 1048576, 1300000} {
			v = append(v, vec{fmt.Sprintf("%s-%d", k, n), gen(k, n, r)})
		}
	}
	return v
}

var params = [][2]int{{8, 22}, {9, 22}, {8, 18}, {8, 24}, {5, 22}, {6, 20}, {7, 17}}

func TestEncoderConformance(t *testing.T) {
	oracle := os.Getenv("BROTLIZ3_ORACLE")
	write := os.Getenv("BROTLIZ3_WRITE_GOLDEN") != ""
	golden := map[string]string{}
	if !write {
		f, err := os.Open("testdata/golden.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if p := strings.Fields(sc.Text()); len(p) == 2 {
				golden[p[0]] = p[1]
			}
		}
	}
	var w bytes.Buffer
	for _, v := range vectors() {
		for _, p := range params {
			if len(v.data) > 300000 && p != params[0] && p != params[1] {
				continue
			}
			if testing.Short() && len(v.data) > 300000 {
				continue
			}
			if p[1] != 22 && len(v.data)%65536 == 0 { // the non-22 oracle is the streaming API (see refenc.py)
				continue
			}
			name := fmt.Sprintf("%s/q%d/w%d", v.name, p[0], p[1])
			got, err := brotliz3.Encode(v.data, p[0], p[1])
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if dec, err := brotliz3.Decode(got, len(v.data)); err != nil || !bytes.Equal(dec, v.data) {
				t.Fatalf("%s: own output does not decode: %v", name, err)
			}
			sum := sha256.Sum256(got)
			h := hex.EncodeToString(sum[:])
			if oracle != "" {
				cmd := exec.Command("python3", oracle, fmt.Sprint(p[0]), fmt.Sprint(p[1]))
				cmd.Stdin = bytes.NewReader(v.data)
				want, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s: differs from the reference (%d vs %d bytes)", name, len(got), len(want))
				}
				ws := sha256.Sum256(want)
				fmt.Fprintf(&w, "%s %s\n", name, hex.EncodeToString(ws[:]))
			} else if golden[name] != h {
				t.Errorf("%s: %s != reference %s", name, h[:16], golden[name])
			}
		}
	}
	if write && oracle != "" {
		if err := os.WriteFile("testdata/golden.txt", w.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncodeRejectsUnsupported(t *testing.T) {
	for _, p := range [][2]int{{4, 22}, {10, 22}, {11, 22}, {8, 16}, {8, 25}, {-1, 22}} {
		if _, err := brotliz3.Encode([]byte("abc"), p[0], p[1]); err != brotliz3.ErrUnsupported {
			t.Fatalf("q%d w%d: %v", p[0], p[1], err)
		}
	}
	if _, err := brotliz3.Encode(make([]byte, brotliz3.MaxInput(17)+1), 8, 17); err != brotliz3.ErrUnsupported {
		t.Fatalf("oversized input: %v", err)
	}
	if out, err := brotliz3.Encode(nil, 8, 22); err != nil || !bytes.Equal(out, []byte{6}) {
		t.Fatalf("empty input: %v %x", err, out)
	}
	if brotliz3.SetDictionary(make([]byte, 122784)) != brotliz3.ErrDictionary {
		t.Fatal("wrong dictionary accepted")
	}
}

// The decoder must reject or exactly decode any mutation, never panic, and honour the limit.
func TestDecoderHostile(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for _, k := range []string{"text", "mixed", "ints"} {
		data := gen(k, 30000, r)
		enc, _ := brotliz3.Encode(data, 8, 22)
		if _, err := brotliz3.Decode(enc, len(data)-1); err != brotliz3.ErrLimit {
			t.Fatalf("limit not enforced: %v", err)
		}
		for n := 0; n < len(enc); n += 1 + len(enc)/300 {
			if out, err := brotliz3.Decode(enc[:n], len(data)); err == nil && !bytes.Equal(out, data) {
				t.Fatalf("truncation to %d decoded to different bytes", n)
			}
		}
		for i := 0; i < 3000; i++ {
			m := append([]byte(nil), enc...)
			m[r.Intn(len(m))] ^= 1 << uint(r.Intn(8))
			out, err := brotliz3.Decode(m, 1<<20)
			if err == nil && len(out) > 1<<20 {
				t.Fatal("limit exceeded")
			}
		}
	}
	// a stream that claims 16 MiB of output per meta-block cannot allocate past the limit
	bomb := []byte{0x1b, 0xff, 0xff, 0xff, 0x00}
	if out, err := brotliz3.Decode(bomb, 1000); err == nil && len(out) > 1000 {
		t.Fatal("bomb accepted")
	}
}

func FuzzDecode(f *testing.F) {
	r := rand.New(rand.NewSource(4))
	for _, k := range []string{"text", "rand", "low"} {
		enc, _ := brotliz3.Encode(gen(k, 2000, r), 8, 22)
		f.Add(enc)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		out, err := brotliz3.Decode(b, 1<<18)
		if err == nil && len(out) > 1<<18 {
			t.Fatal("limit exceeded")
		}
	})
}

func FuzzEncodeDecode(f *testing.F) {
	f.Add([]byte("hello hello hello"), uint8(8))
	f.Fuzz(func(t *testing.T, b []byte, q uint8) {
		enc, err := brotliz3.Encode(b, 5+int(q%5), 22)
		if err != nil {
			t.Fatal(err)
		}
		if dec, err := brotliz3.Decode(enc, len(b)); err != nil || !bytes.Equal(dec, b) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func BenchmarkEncodeQ8(b *testing.B) {
	data := gen("mixed", 1<<20, rand.New(rand.NewSource(1)))
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		brotliz3.Encode(data, 8, 22)
	}
}

func BenchmarkDecode(b *testing.B) {
	data := gen("mixed", 1<<20, rand.New(rand.NewSource(1)))
	enc, _ := brotliz3.Encode(data, 8, 22)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		brotliz3.Decode(enc, len(data))
	}
}
