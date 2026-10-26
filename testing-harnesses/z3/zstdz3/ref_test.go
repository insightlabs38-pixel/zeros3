package zstdz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// refZstd returns a pinned reference binary (outside Git, see oracle/containers/setup.sh)
// or skips the test.
func refZstd(t testing.TB, ver string) string {
	t.Helper()
	root := os.Getenv("Z3_TOOLS")
	if root == "" {
		root = "/root/z3cache"
	}
	p := filepath.Join(root, "tools", "zstd-"+ver, "zstd")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("reference zstd %s not installed (%s)", ver, p)
	}
	return p
}

func runZstd(t testing.TB, bin string, in []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, append(args, "-q", "-c")...)
	cmd.Stdin = bytes.NewReader(in)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("zstd %v: %v\n%s", args, err, errb.String())
	}
	return out.Bytes()
}

// roundTrip parses comp, replays it and checks plaintext against want (nil = reference
// zstd -d) and the replayed bytes against comp.
func roundTrip(t testing.TB, comp, want []byte) *Result {
	t.Helper()
	var plain, units bytes.Buffer
	res, err := Parse(bytes.NewReader(comp), &plain, &units, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want != nil && !bytes.Equal(plain.Bytes(), want) {
		t.Fatalf("plaintext differs: %d vs %d bytes", plain.Len(), len(want))
	}
	sk, err := Unmarshal(res.Raw, Limits{})
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(sk.Marshal(), res.Raw) {
		t.Fatal("skeleton is not canonical")
	}
	var out bytes.Buffer
	if _, err := Replay(&out, res.Raw, bytes.NewReader(units.Bytes()), bytes.NewReader(plain.Bytes()), Limits{}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !bytes.Equal(out.Bytes(), comp) {
		i := 0
		for i < len(comp) && i < out.Len() && comp[i] == out.Bytes()[i] {
			i++
		}
		t.Fatalf("replay differs at %d of %d (sha %x vs %x)", i, len(comp), sha256.Sum256(out.Bytes()), sha256.Sum256(comp))
	}
	return res
}

func sampleText(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta", "iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi", "rho", "sigma", "tau"}
	var b bytes.Buffer
	for b.Len() < n {
		fmt.Fprintf(&b, "%s-%d %s/%s %x\n", words[r.Intn(len(words))], r.Intn(10000), words[r.Intn(len(words))], words[r.Intn(len(words))], r.Uint32())
	}
	return b.Bytes()[:n]
}

func TestRefBasic(t *testing.T) {
	bin := refZstd(t, "1.5.5")
	for _, lvl := range []string{"-1", "-3", "-9", "-19"} {
		in := sampleText(1<<20, 1)
		comp := runZstd(t, bin, in, lvl, "-T1")
		res := roundTrip(t, comp, in)
		t.Logf("%s: comp %d blocks %v fallback %d", lvl, len(comp), res.Stats.Blocks, res.Stats.FallbackBytes)
	}
}

// corpus generates deterministic inputs that, between them, drive every coding mode.
func corpus() map[string][]byte {
	r := rand.New(rand.NewSource(7))
	rnd := func(n int) []byte { b := make([]byte, n); r.Read(b); return b }
	alpha := func(n, k int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Intn(k))
		}
		return b
	}
	skew := func(n int) []byte { // geometric-ish byte distribution: strong Huffman, few matches
		b := make([]byte, n)
		for i := range b {
			v := 0
			for r.Intn(3) != 0 && v < 255 {
				v++
			}
			b[i] = byte(v)
		}
		return b
	}
	rec := func(n int) []byte { // fixed-width records with counters and constant fields
		var b bytes.Buffer
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, "REC%08d|%-12s|%06d|AAAA|", i, []string{"north", "south", "east", "west"}[i%4], r.Intn(1000))
			b.Write([]byte{0, 0, 0, byte(i), 0, 0, byte(i >> 8), 1})
		}
		return b.Bytes()[:n]
	}
	jsonl := func(n int) []byte {
		var b bytes.Buffer
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, `{"id":%d,"name":"user%d","tags":["a","b","c%d"],"score":%.3f,"ok":%v}`+"\n", i, r.Intn(500), i%7, r.Float64(), i%2 == 0)
		}
		return b.Bytes()[:n]
	}
	rep := func(n int) []byte { // long repeats at varying distances, with literal-free matches (zero literal length)
		base := rnd(3000)
		var b []byte
		for len(b) < n {
			k := r.Intn(len(base) - 100)
			b = append(b, base[k:k+50+r.Intn(50)]...)
			if r.Intn(4) == 0 {
				b = append(b, byte(r.Intn(256)))
			}
		}
		return b[:n]
	}
	m := map[string][]byte{
		"empty":    {},
		"one":      {'x'},
		"tiny":     []byte("hello hello hello hello hello"),
		"text1M":   sampleText(1<<20, 1),
		"text300K": sampleText(300000, 2),
		"json":     jsonl(600000),
		"rec":      rec(500000),
		"alpha4":   alpha(200000, 4),
		"alpha16":  alpha(200000, 16),
		"alpha100": alpha(70000, 100),
		"skew":     skew(300000),
		"zeros":    make([]byte, 1<<20),
		"random":   rnd(300000),
		"rep":      rep(400000),
		"mixed":    bytes.Join([][]byte{sampleText(200000, 3), rnd(150000), make([]byte, 200000), rec(200000), skew(100000), jsonl(100000), rep(100000)}, nil),
	}
	for i, n := range []int{2, 5, 17, 40, 100, 255, 300, 1000, 3000, 10000} {
		m[fmt.Sprintf("small%d", n)] = sampleText(n, int64(10+i))
		m[fmt.Sprintf("smallrnd%d", n)] = rnd(n)
	}
	return m
}

func TestRefCoverage(t *testing.T) {
	var agg Stats
	add := func(s *Stats) {
		for i := range agg.Blocks {
			agg.Blocks[i] += s.Blocks[i]
			agg.StreamBytes[i] += s.StreamBytes[i]
			agg.LitModes[i%4] += s.LitModes[i%4] * 0
		}
		for i := range s.LitModes {
			agg.LitModes[i] += s.LitModes[i]
			for k := 0; k < 3; k++ {
				agg.SeqModes[k][i] += s.SeqModes[k][i]
			}
			agg.RepeatCodes[i] += s.RepeatCodes[i]
		}
		agg.LitStreams[1] += s.LitStreams[1]
		agg.LitStreams[4] += s.LitStreams[4]
		agg.HuffDirect += s.HuffDirect
		agg.HuffFSE += s.HuffFSE
		agg.ZeroLLRepeat += s.ZeroLLRepeat
		agg.Empty += s.Empty
	}
	c := corpus()
	for _, ver := range []string{"1.5.5", "1.5.7"} {
		bin := refZstd(t, ver)
		for name, in := range c {
			for _, lvl := range []string{"--fast=3", "-1", "-3", "-9", "-19"} {
				comp := runZstd(t, bin, in, lvl, "-T1")
				var res *Result
				t.Run(fmt.Sprintf("%s/%s/%s", ver, name, lvl), func(t *testing.T) { res = roundTrip(t, comp, in) })
				if res == nil {
					continue
				}
				if res.Stats.Blocks[BlockFallback] != 0 {
					t.Errorf("%s %s %s: %d fallback blocks", ver, name, lvl, res.Stats.Blocks[BlockFallback])
				}
				add(&res.Stats)
			}
		}
	}
	t.Logf("blocks raw/rle/comp/fallback %v; literal modes %v streams 1/4 %d/%d huff direct/fse %d/%d", agg.Blocks, agg.LitModes, agg.LitStreams[1], agg.LitStreams[4], agg.HuffDirect, agg.HuffFSE)
	t.Logf("sequence modes LL %v OF %v ML %v; offset codes %v zeroLLrep %d empty %d", agg.SeqModes[0], agg.SeqModes[1], agg.SeqModes[2], agg.RepeatCodes, agg.ZeroLLRepeat, agg.Empty)
}

// TestCodingModeCoverage is the coverage gate: every dictionary-free coding mode must be
// exercised by committed fixtures plus (when installed) the reference producers, so
// broad support is never claimed from a lucky corpus.
func TestCodingModeCoverage(t *testing.T) {
	var agg Stats
	add := func(s *Stats) {
		for i := range s.LitModes {
			agg.LitModes[i] += s.LitModes[i]
			for k := 0; k < 3; k++ {
				agg.SeqModes[k][i] += s.SeqModes[k][i]
			}
			agg.RepeatCodes[i] += s.RepeatCodes[i]
			agg.Blocks[i] += s.Blocks[i]
		}
		agg.LitStreams[1] += s.LitStreams[1]
		agg.LitStreams[4] += s.LitStreams[4]
		agg.HuffDirect += s.HuffDirect
		agg.HuffFSE += s.HuffFSE
		agg.ZeroLLRepeat += s.ZeroLLRepeat
	}
	ents, _ := os.ReadDir("testdata")
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".zst" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join("testdata", e.Name()))
		res, err := Parse(bytes.NewReader(b), io.Discard, nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		add(&res.Stats)
	}
	if bin := os.Getenv("Z3_TOOLS"); bin != "" || fileExists("/root/z3cache/tools/zstd-1.5.7/zstd") {
		ref := refZstd(t, "1.5.7")
		for name, in := range corpus() {
			if len(in) < 1000 {
				continue
			}
			for _, lvl := range []string{"-3", "-19"} {
				res, err := Parse(bytes.NewReader(runZstd(t, ref, in, lvl, "-T1")), io.Discard, nil, Options{})
				if err != nil {
					t.Fatalf("%s %s: %v", name, lvl, err)
				}
				add(&res.Stats)
			}
		}
	}
	need := []struct {
		name string
		n    int
	}{
		{"raw block", agg.Blocks[BlockRaw]}, {"rle block", agg.Blocks[BlockRLE]}, {"compressed block", agg.Blocks[BlockCompressed]},
		{"raw literals", agg.LitModes[litRaw]}, {"rle literals", agg.LitModes[litRLE]}, {"compressed literals", agg.LitModes[litComp]}, {"treeless literals", agg.LitModes[litTreeles]},
		{"direct huffman weights", agg.HuffDirect}, {"fse huffman weights", agg.HuffFSE}, {"1 stream", agg.LitStreams[1]}, {"4 streams", agg.LitStreams[4]},
		{"zero-LL repeat", agg.ZeroLLRepeat}, {"rep offset 1", agg.RepeatCodes[0]}, {"rep offset 2", agg.RepeatCodes[1]}, {"rep offset 3", agg.RepeatCodes[2]}, {"explicit offsets", agg.RepeatCodes[3]},
	}
	for k, kn := range []string{"LL", "OF", "ML"} {
		for m, mn := range []string{"predefined", "rle", "fse", "repeat"} {
			need = append(need, struct {
				name string
				n    int
			}{kn + " " + mn, agg.SeqModes[k][m]})
		}
	}
	for _, c := range need {
		if c.n == 0 {
			t.Errorf("coding mode not exercised: %s", c.name)
		}
	}
	t.Logf("coverage: blocks %v literal modes %v streams %d/%d huffman direct/fse %d/%d zeroLL %d offcodes %v LL %v OF %v ML %v",
		agg.Blocks, agg.LitModes, agg.LitStreams[1], agg.LitStreams[4], agg.HuffDirect, agg.HuffFSE, agg.ZeroLLRepeat, agg.RepeatCodes, agg.SeqModes[0], agg.SeqModes[1], agg.SeqModes[2])
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
