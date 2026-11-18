package brotliz3_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"zeros3-z3/brotliz3"
)

// Streaming conformance: every scenario is a call sequence on BrotliEncoderCompressStream (process
// chunks, flushes, finish) with explicit parameters. testdata/golden_stream.txt holds the SHA-256 of
// libbrotlienc's output (oracle/brotli/streamref.py; system 1.1.0 and a build of brotli 1.2.0 agree).
// BROTLIZ3_STREAMREF (brotli 1.2.0) and BROTLIZ3_STREAMREF_110 (<= 1.1.0) hold "python3 streamref.py LIB" and
// compare bytes live; BROTLIZ3_WRITE_GOLDEN=1 rewrites the golden file from them.

type scenario struct {
	legacy         bool
	kind           string
	n              int
	q, w, lb, hint int
	ops            string
	data           []byte
}

func (s scenario) name() string {
	h := sha256.Sum256(append([]byte(s.ops), s.data...))
	v := "v120"
	if s.legacy {
		v = "v110"
	}
	return fmt.Sprintf("%s/%s-%d/q%d/w%d/lb%d/h%d/%s", v, s.kind, s.n, s.q, s.w, s.lb, s.hint, hex.EncodeToString(h[:6]))
}

// runOps drives a Stream through ops, returning the compressed bytes.
func runOps(t *testing.T, s scenario) []byte {
	var out bytes.Buffer
	st, err := brotliz3.NewStream(brotliz3.Params{Quality: s.q, LgWin: s.w, LgBlock: s.lb, SizeHint: uint32(s.hint), Legacy: s.legacy}, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pos := 0
	for _, o := range strings.Split(s.ops, ",") {
		switch o[0] {
		case 'P':
			n, _ := strconv.Atoi(o[1:])
			if err := st.Process(s.data[pos : pos+n]); err != nil {
				t.Fatal(err)
			}
			pos += n
		case 'F':
			if err := st.Flush(); err != nil {
				t.Fatal(err)
			}
		case 'Z':
			if err := st.Write(brotliz3.OpFinish, s.data[pos:]); err != nil {
				t.Fatal(err)
			}
			pos = len(s.data)
		}
	}
	if pos != len(s.data) {
		t.Fatalf("ops consume %d of %d", pos, len(s.data))
	}
	return out.Bytes()
}

// chunked splits n bytes into process chunks of random sizes below max, with flushes at the given rate.
func chunked(r *rand.Rand, n, max int, flushEvery int) string {
	var ops []string
	for left := n; left > 0; {
		c := min(left, 1+r.Intn(max))
		ops = append(ops, "P"+strconv.Itoa(c))
		left -= c
		if flushEvery > 0 && r.Intn(flushEvery) == 0 {
			ops = append(ops, "F")
		}
	}
	return strings.Join(append(ops, "Z"), ",")
}

var chunkMax = []int{50, 5000, 100000, 400000}

func scenarios() []scenario {
	var sc []scenario
	r := rand.New(rand.NewSource(77))
	add := func(kind string, n, q, w, lb, hint int, ops string) {
		sc = append(sc, scenario{kind: kind, n: n, q: q, w: w, lb: lb, hint: hint, ops: ops, data: gen(kind, n, r)})
	}
	// finish-only (dynamic and explicit hints), block multiples, windows smaller than the stream (ring wrap)
	for _, q := range []int{5, 7, 8, 9} {
		for _, w := range []int{17, 18, 22} {
			for _, n := range []int{65535, 65536, 65537, 131072, 262144, 300000, 700001} {
				add("text", n, q, w, 0, 0, "Z")
				add("mixed", n, q, w, 0, 0, "P"+strconv.Itoa(n)+",Z")
			}
		}
	}
	for _, kind := range []string{"text", "ints", "low", "mixed", "floats"} {
		add(kind, 1500000, 8, 17, 0, 0, "Z") // several laps of a 256 KiB ring
		add(kind, 1500000, 9, 18, 0, 0, "Z")
		add(kind, 2300000, 6, 19, 0, 1<<20, "Z")
	}
	// explicit hints and block sizes
	for _, hint := range []int{1, 1 << 19, 1 << 20, 1 << 22} {
		for _, lb := range []int{0, 16, 18, 20, 22} {
			add("text", 600000, 8, 22, lb, hint, "Z")
			add("mixed", 600000, 9, 20, lb, hint, "Z")
		}
	}
	// process chunking and flushes
	for i := 0; i < 24; i++ {
		kind := []string{"text", "mixed", "ints", "rand", "low"}[i%5]
		n := []int{1000, 70000, 200000, 700000, 1300000}[i%5]
		q, w := 5+i%5, []int{17, 18, 20, 22, 24}[(i/2)%5]
		add(kind, n, q, w, 0, 0, chunked(r, n, max(chunkMax[i%4], n/4000), 0))
		add(kind, n, q, w, 0, 0, chunked(r, n, max(chunkMax[i%4], n/4000), 3))
	}
	// flush positions at and around block boundaries, doubled flushes, flush before any data and before finish
	add("text", 300000, 8, 22, 0, 0, "F,P65536,F,P65535,F,F,P1,P100000,F,P34464,F,Z")
	add("mixed", 300000, 5, 17, 0, 0, "P65536,F,P65536,P65536,Z,")
	add("text", 131072, 8, 22, 0, 0, "P65536,P65536,Z")
	add("text", 131072, 8, 22, 0, 0, "P131072,Z")
	add("text", 131072, 8, 22, 0, 1<<20, "P131072,Z")
	add("low", 200000, 9, 22, 0, 0, "P64,F,P64,F,P64,F,P199808,Z")
	add("rand", 200000, 8, 22, 0, 0, "P100000,F,P100000,F,Z")
	add("zeros", 400000, 7, 22, 0, 0, "P200000,F,P200000,Z")
	add("text", 10, 8, 22, 0, 0, "P10,F,Z")
	add("text", 1, 8, 22, 0, 0, "F,P1,Z")
	var keep []scenario
	for _, s := range sc {
		s.ops = strings.TrimSuffix(s.ops, ",")
		keep = append(keep, s)
		s.legacy = true
		keep = append(keep, s)
	}
	return keep
}

func TestStreamConformance(t *testing.T) {
	write := os.Getenv("BROTLIZ3_WRITE_GOLDEN") != ""
	golden := map[string]string{}
	if !write {
		f, err := os.Open("testdata/golden_stream.txt")
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
	for _, s := range scenarios() {
		if testing.Short() && s.n > 400000 {
			continue
		}
		name := s.name()
		oracle := os.Getenv("BROTLIZ3_STREAMREF")
		if s.legacy {
			oracle = os.Getenv("BROTLIZ3_STREAMREF_110")
		}
		got := runOps(t, s)
		if dec, err := brotliz3.Decode(got, len(s.data)); err != nil || !bytes.Equal(dec, s.data) {
			t.Fatalf("%s: own output does not decode: %v", name, err)
		}
		sum := sha256.Sum256(got)
		h := hex.EncodeToString(sum[:])
		if oracle != "" {
			a := strings.Fields(oracle)
			cmd := exec.Command(a[0], append(a[1:], strconv.Itoa(s.q), strconv.Itoa(s.w), strconv.Itoa(s.lb), strconv.Itoa(s.hint), "@env")...)
			cmd.Env = append(os.Environ(), "STREAMOPS="+s.ops)
			cmd.Stdin = bytes.NewReader(s.data)
			want, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: oracle: %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: differs from the reference (%d vs %d bytes)", name, len(got), len(want))
				if d := os.Getenv("BROTLIZ3_DEBUG"); d != "" {
					n := strings.NewReplacer("/", "_").Replace(name)
					os.WriteFile(d+"/"+n+".in", s.data, 0o644)
					os.WriteFile(d+"/"+n+".got", got, 0o644)
					os.WriteFile(d+"/"+n+".want", want, 0o644)
				}
			}
			ws := sha256.Sum256(want)
			fmt.Fprintf(&w, "%s %s\n", name, hex.EncodeToString(ws[:]))
		} else if golden[name] != h {
			t.Errorf("%s: %s != reference %s", name, h[:16], golden[name])
		}
	}
	if write {
		if err := os.WriteFile("testdata/golden_stream.txt", w.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
