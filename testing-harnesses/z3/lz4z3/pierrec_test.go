package lz4z3

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// PredPierrec against github.com/pierrec/lz4/v4 v4.1.22. testdata/pierrec_golden.txt holds the
// SHA-256 of the producer's block for deterministic inputs; LZ4Z3_PIERREC=<oracle/lz4/pierrecgen
// binary> compares bytes directly and, with LZ4Z3_WRITE_GOLDEN=1, rewrites the file from the
// producer (never from this package).
func pierrecVectors() map[string][]byte {
	r := rand.New(rand.NewSource(21))
	v := map[string][]byte{}
	words := []string{"alpha", "bravo", "charlie", "delta ", "echo-", "0000", "\n", "the quick brown fox "}
	for _, n := range []int{0, 1, 13, 14, 15, 16, 17, 64, 1000, 4096, 65535, 65536, 65537, 70000, 300000, 1 << 20} {
		var text []byte
		for len(text) < n {
			text = append(text, words[r.Intn(len(words))]...)
		}
		v[fmt.Sprintf("text-%d", n)] = text[:n]
		rnd := make([]byte, n)
		r.Read(rnd)
		v[fmt.Sprintf("rand-%d", n)] = rnd
		low := make([]byte, n)
		for i := range low {
			low[i] = "ab"[r.Intn(2)]
		}
		v[fmt.Sprintf("low-%d", n)] = low
		v[fmt.Sprintf("zero-%d", n)] = make([]byte, n)
		mix := make([]byte, 0, n)
		for len(mix) < n {
			if r.Intn(2) == 0 {
				mix = append(mix, text[:min(len(text), 1+r.Intn(500))]...)
			} else {
				mix = append(mix, rnd[:min(len(rnd), 1+r.Intn(300))]...)
			}
			if len(mix) == 0 {
				break
			}
		}
		v[fmt.Sprintf("mix-%d", n)] = mix[:min(len(mix), n)]
	}
	return v
}

func TestPierrecPredictor(t *testing.T) {
	oracle := os.Getenv("LZ4Z3_PIERREC")
	write := os.Getenv("LZ4Z3_WRITE_GOLDEN") != ""
	golden := map[string]string{}
	if !write {
		f, err := os.Open("testdata/pierrec_golden.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for sc := bufio.NewScanner(f); sc.Scan(); {
			if p := strings.Fields(sc.Text()); len(p) == 2 {
				golden[p[0]] = p[1]
			}
		}
	}
	var out bytes.Buffer
	dir := t.TempDir()
	vs := pierrecVectors()
	names := make([]string, 0, len(vs))
	for k := range vs {
		names = append(names, k)
	}
	for i := 1; i < len(names); i++ { // deterministic order
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, name := range names {
		data := vs[name]
		got, ok := PredictBlock(PredPierrec, data, 1)
		if !ok {
			t.Fatalf("%s: predictor failed", name)
		}
		if dec, _, err := Decode(got, len(data), Limits{}); err != nil || !bytes.Equal(dec, data) {
			t.Fatalf("%s: predicted block does not decode: %v", name, err)
		}
		sum := sha256.Sum256(got)
		h := hex.EncodeToString(sum[:])
		if oracle != "" {
			in, o := filepath.Join(dir, "in"), filepath.Join(dir, "out")
			os.WriteFile(in, data, 0o644)
			if err := exec.Command(oracle, in, o).Run(); err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(o)
			if len(want) == 0 { // the producer declines incompressible input (returns 0): no block to compare
				continue
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: differs from pierrec/lz4 (%d vs %d bytes)", name, len(got), len(want))
			}
			ws := sha256.Sum256(want)
			fmt.Fprintf(&out, "%s %s\n", name, hex.EncodeToString(ws[:]))
		} else if g, has := golden[name]; has && g != h {
			t.Errorf("%s: %s != producer %s", name, h[:16], g[:16])
		}
	}
	if write && oracle != "" {
		if err := os.WriteFile("testdata/pierrec_golden.txt", out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The memoizing analyzer must return recipes that replay exactly, hit the remembered profile, and
// stop sweeping for an unsupported producer.
func TestAnalyzerMemoAndGiveUp(t *testing.T) {
	r := rand.New(rand.NewSource(22))
	a := &Analyzer{GiveUp: 3}
	for i := 0; i < 20; i++ {
		data := pierrecVectors()["text-70000"][i*100 : i*100+20000]
		blk, _ := PredictBlock(PredContinue, data, 1)
		plain, seqs, err := Decode(blk, len(data), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		rc := a.Analyze(plain, seqs, true)
		if rep, err := rc.Replay(nil, plain, Limits{}); err != nil || !bytes.Equal(rep, blk) || rc.Kind != KindPredicted {
			t.Fatalf("block %d: kind %d err %v", i, rc.Kind, err)
		}
	}
	if a.Sweeps != 1 || a.Hits != 19 {
		t.Fatalf("memo not used: sweeps %d hits %d", a.Sweeps, a.Hits)
	}
	// an encoder no predictor models: random but valid sequence choices
	b := &Analyzer{GiveUp: 3}
	for i := 0; i < 12; i++ {
		data := bytes.Repeat([]byte("abcdefgh"), 500)
		seqs := []Seq{{Lit: 8 + r.Intn(8), Match: 100 + r.Intn(50), Off: 8}, {Lit: 0, Match: 200, Off: 16}}
		used := seqs[0].Lit + seqs[0].Match + 200
		seqs = append(seqs, Seq{Lit: len(data) - used})
		blk, err := Build(data, seqs)
		if err != nil {
			t.Fatal(err)
		}
		rc := b.Analyze(data, seqs, true)
		if rep, err := rc.Replay(nil, data, Limits{}); err != nil || !bytes.Equal(rep, blk) {
			t.Fatalf("unsupported block %d: %v", i, err)
		}
	}
	if b.Sweeps != 3 || b.Rejected != 9 {
		t.Fatalf("give-up not applied: sweeps %d rejected %d", b.Sweeps, b.Rejected)
	}
}
