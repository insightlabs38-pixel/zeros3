package qcore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Conformance vectors: deterministic inputs whose expected outputs come from llama.cpp's own
// ggml-quants.c (tag b3991) through oracle/quant/qoracle.c. testdata/golden.txt holds SHA-256 of the
// oracle output; with QCORE_ORACLE=<qoracle binary> the test also runs the oracle directly and
// QCORE_WRITE_GOLDEN=1 rewrites the golden file from the oracle (never from this package).

type vec struct {
	name    string
	t       Type
	npr     int
	x, qw   []float32
	special bool // contains NaN/Inf: C behaviour is conversion-UB, compared separately
}

func f32bytes(x []float32) []byte {
	b := make([]byte, 4*len(x))
	for i, v := range x {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

func vectors() []vec {
	var out []vec
	types := []Type{Q4_0, Q4_1, Q5_0, Q5_1, Q8_0, Q2_K, Q3_K, Q4_K, Q5_K, Q6_K, IQ4_NL, IQ4_XS}
	gens := []struct {
		name    string
		special bool
		f       func(r *rand.Rand, n int) []float32
	}{
		{"gauss", false, func(r *rand.Rand, n int) []float32 {
			x := make([]float32, n)
			for i := range x {
				x[i] = float32(r.NormFloat64() * 0.05)
			}
			return x
		}},
		{"heavy", false, func(r *rand.Rand, n int) []float32 {
			x := make([]float32, n)
			for i := range x {
				x[i] = float32(r.NormFloat64() * math.Exp(3*r.NormFloat64()))
			}
			return x
		}},
		{"f16grid", false, func(r *rand.Rand, n int) []float32 { // values a BF16/F16 checkpoint holds
			x := make([]float32, n)
			for i := range x {
				x[i] = F16to32(F16(float32(r.NormFloat64() * 0.02)))
			}
			return x
		}},
		{"ties", false, func(r *rand.Rand, n int) []float32 { // small integers and halves: rounding boundaries
			x := make([]float32, n)
			for i := range x {
				x[i] = float32(r.Intn(65)-32) * 0.5
			}
			return x
		}},
		{"sparse", false, func(r *rand.Rand, n int) []float32 { // zero blocks, signed zeros, constants
			x := make([]float32, n)
			for b := 0; b < n; b += 16 {
				switch r.Intn(6) {
				case 0:
				case 1:
					for i := b; i < b+16 && i < n; i++ {
						x[i] = float32(math.Copysign(0, -1))
					}
				case 2:
					c := float32(r.NormFloat64())
					for i := b; i < b+16 && i < n; i++ {
						x[i] = c
					}
				case 3:
					x[b+r.Intn(16)] = float32(r.NormFloat64())
				default:
					for i := b; i < b+16 && i < n; i++ {
						x[i] = float32(r.NormFloat64() * 1e-3)
					}
				}
			}
			return x
		}},
		{"extreme", false, func(r *rand.Rand, n int) []float32 { // subnormals, tiny, huge finite
			x := make([]float32, n)
			for i := range x {
				switch r.Intn(5) {
				case 0:
					x[i] = math.Float32frombits(uint32(r.Intn(1 << 23))) // subnormal
				case 1:
					x[i] = float32(r.NormFloat64() * 1e-20)
				case 2:
					x[i] = float32(r.NormFloat64() * 1e18)
				case 3:
					x[i] = float32(r.NormFloat64() * 1e-30)
				default:
					x[i] = float32(r.NormFloat64())
				}
			}
			return x
		}},
		{"nonfinite", true, func(r *rand.Rand, n int) []float32 {
			x := make([]float32, n)
			for i := range x {
				x[i] = float32(r.NormFloat64())
			}
			for k := 0; k < n/40; k++ {
				v := []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))}[r.Intn(3)]
				x[r.Intn(n)] = v
			}
			return x
		}},
	}
	for _, t := range types {
		for gi, g := range gens {
			for _, im := range []bool{false, true} {
				if im && t == Q8_0 {
					continue
				}
				r := rand.New(rand.NewSource(int64(t)*1000 + int64(gi)*10 + 7))
				npr := 768
				x := g.f(r, npr*12)
				v := vec{name: fmt.Sprintf("%s/%s", t, g.name), t: t, npr: npr, x: x, special: g.special}
				if im {
					v.name += "/imatrix"
					v.qw = make([]float32, npr)
					for i := range v.qw {
						v.qw[i] = float32(math.Exp(2 * r.NormFloat64()))
						if r.Intn(50) == 0 {
							v.qw[i] = 0
						}
					}
				}
				out = append(out, v)
			}
		}
	}
	return out
}

func runOracle(t *testing.T, bin string, v vec) []byte {
	args := []string{v.t.String(), fmt.Sprint(v.npr)}
	if v.qw != nil {
		f, err := os.CreateTemp(t.TempDir(), "qw")
		if err != nil {
			t.Fatal(err)
		}
		f.Write(f32bytes(v.qw))
		f.Close()
		args = append(args, f.Name())
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(f32bytes(v.x))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle %v: %v", args, err)
	}
	return out
}

func loadGolden(t *testing.T, name string) map[string]string {
	g := map[string]string{}
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p := strings.Fields(sc.Text()); len(p) == 2 {
			g[p[0]] = p[1]
		}
	}
	return g
}

// TestConformanceGolden: Strict arithmetic against a plain -O2 build of the oracle (all vectors).
func TestConformanceGolden(t *testing.T) {
	conformance(t, Strict, "golden.txt", os.Getenv("QCORE_ORACLE"), func(v vec) bool { return true })
}

// TestConformanceGoldenFMA: the GCCFMA profile against the oracle built with
// `gcc -O3 -mavx2 -mfma -mf16c` (contraction on). Importance-weighted paths, finite inputs.
func TestConformanceGoldenFMA(t *testing.T) {
	conformance(t, GCCFMA, "golden_fma.txt", os.Getenv("QCORE_ORACLE_FMA"), func(v vec) bool {
		return v.qw != nil && !v.special
	})
}

func conformance(t *testing.T, ar Arith, file, bin string, use func(vec) bool) {
	write := os.Getenv("QCORE_WRITE_GOLDEN") != ""
	var golden map[string]string
	if !write {
		golden = loadGolden(t, file)
	}
	var w bytes.Buffer
	for _, v := range vectors() {
		if !use(v) {
			continue
		}
		n, err := OutLen(v.t, len(v.x), v.npr)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, n)
		if err := Quantize(got, v.t, v.x, v.npr, v.qw, ar); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(got)
		h := hex.EncodeToString(sum[:])
		if bin != "" {
			want := runOracle(t, bin, v)
			if !bytes.Equal(got, want) {
				bad, first := 0, -1
				_, bs := Block(v.t)
				for b := 0; b*bs < len(want) && b*bs < len(got); b++ {
					if !bytes.Equal(got[b*bs:(b+1)*bs], want[b*bs:(b+1)*bs]) {
						if bad++; first < 0 {
							first = b
						}
					}
				}
				t.Errorf("%s: %d/%d blocks differ from oracle (first %d)", v.name, bad, len(want)/bs, first)
			}
			ws := sha256.Sum256(want)
			fmt.Fprintf(&w, "%s %s\n", v.name, hex.EncodeToString(ws[:]))
		} else if golden[v.name] != h {
			t.Errorf("%s: output hash %s != oracle golden %s", v.name, h[:16], golden[v.name])
		}
	}
	if write && bin != "" {
		if err := os.WriteFile("testdata/"+file, w.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFMA32Exact(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	ref := func(a, b, c float32) float32 {
		x := new(big.Float).SetPrec(400).SetFloat64(float64(a))
		x.Mul(x, new(big.Float).SetPrec(400).SetFloat64(float64(b)))
		x.Add(x, new(big.Float).SetPrec(400).SetFloat64(float64(c)))
		f, _ := x.Float32()
		return f
	}
	rnd := func() float32 {
		switch r.Intn(6) {
		case 0:
			return float32(r.Intn(2048)-1024) * 0.5
		case 1:
			return math.Float32frombits(r.Uint32()&0x807fffff | uint32(100+r.Intn(60))<<23)
		case 2:
			return 12582912.0
		case 3:
			return math.Float32frombits(r.Uint32() & 0x807fffff) // subnormal
		}
		return float32(r.NormFloat64() * math.Exp(8*r.NormFloat64()))
	}
	for i := 0; i < 2000000; i++ {
		a, b, c := rnd(), rnd(), rnd()
		if r.Intn(4) == 0 { // near cancellation and near-tie sums
			c = -float32(float64(a)*float64(b)) + float32(r.Intn(5)-2)*math.Float32frombits(math.Float32bits(float32(float64(a)*float64(b)))&0x7f800000)/float32(int(1)<<uint(20+r.Intn(8)))
		}
		got, want := fma32(a, b, c), ref(a, b, c)
		if math.Float32bits(got) != math.Float32bits(want) && !(got != got && want != want) {
			t.Fatalf("fma32(%g,%g,%g) = %g want %g", a, b, c, got, want)
		}
	}
}

func TestF16(t *testing.T) {
	// every float16 round-trips; float32 halfway cases round to even
	for h := 0; h < 1<<16; h++ {
		f := F16to32(uint16(h))
		if f != f {
			continue
		}
		if got := F16(f); got != uint16(h) {
			t.Fatalf("F16(F16to32(%#x)) = %#x", h, got)
		}
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 3000000; i++ {
		b := r.Uint32()
		if r.Intn(2) == 0 { // bias into the half-precision range incl. subnormals
			b = b&0x807fffff | uint32(127-30+r.Intn(48))<<23
		}
		f := math.Float32frombits(b)
		if f != f {
			continue
		}
		got := F16(f)
		// reference: nearest float16 by value with ties to even
		lo, hi := got, got
		af := float64(f)
		v := float64(F16to32(got))
		if math.IsInf(v, 0) {
			if math.Abs(af) < 65520 {
				t.Fatalf("F16(%g) overflowed", f)
			}
			continue
		}
		if got&0x7fff != 0 {
			lo = got - 1
		}
		hi = got + 1
		for _, o := range []uint16{lo, hi} {
			ov := float64(F16to32(o))
			if math.IsInf(ov, 0) {
				ov = math.Copysign(65536, ov)
			}
			if d0, d1 := math.Abs(af-v), math.Abs(af-ov); d1 < d0 || (d1 == d0 && o&1 == 0 && got&1 == 1) {
				t.Fatalf("F16(%g) = %#x (%g) but %#x (%g) is nearer", f, got, v, o, ov)
			}
		}
	}
}
