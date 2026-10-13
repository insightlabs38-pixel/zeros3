package lab

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"zeros3-z3/corpus"
)

// dumpSrc is compiled together with an unmodified copy of zeros3.go. It adds
// a second source file to package main (zeros3.go itself is not touched) that
// prints the production CDC constants, a digest of the gear table, and the
// chunk lengths production newCDCChunker produces for each file argument.
const dumpSrc = `package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
)

type z3frag struct {
	r io.Reader
	n int
}

func (f z3frag) Read(p []byte) (int, error) {
	if len(p) > f.n {
		p = p[:f.n]
	}
	return f.r.Read(p)
}

func init() {
	if os.Getenv("Z3_CDC_DUMP") == "" {
		return
	}
	fmt.Println("const", cdcMinChunkSize, cdcTargetChunkSize, cdcMaxChunkSize, uint64(cdcMaskS), uint64(cdcMaskL), cdcGearSeed)
	h := sha256.New()
	for _, g := range gearTable {
		fmt.Fprintf(h, "%d,", g)
	}
	fmt.Printf("gear %x\n", h.Sum(nil))
	frag, _ := strconv.Atoi(os.Getenv("Z3_FRAG"))
	for _, p := range os.Args[1:] {
		f, err := os.Open(p)
		if err != nil {
			panic(err)
		}
		var r io.Reader = f
		if frag > 0 {
			r = z3frag{f, frag}
		}
		c := newCDCChunker(r)
		fmt.Print("file")
		for {
			ch, err := c.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				panic(err)
			}
			fmt.Print(" ", len(ch))
		}
		fmt.Println()
		f.Close()
	}
	os.Exit(0)
}
`

type fragReader struct {
	r io.Reader
	n int
}

func (f fragReader) Read(p []byte) (int, error) {
	if len(p) > f.n {
		p = p[:f.n]
	}
	return f.r.Read(p)
}

func equivInputs(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	add := func(name string, b []byte) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	rng := corpus.NewRNG(7, "cdc-equiv")
	sizes := []int{0, 1, 100, cdcMinChunkSize - 1, cdcMinChunkSize, cdcMinChunkSize + 1, cdcTargetChunkSize - 1, cdcTargetChunkSize,
		cdcTargetChunkSize + 1, cdcMaxChunkSize - 1, cdcMaxChunkSize, cdcMaxChunkSize + 1, 2*cdcMaxChunkSize + 5, 700_003, 1_500_000, 3_333_333}
	for _, n := range sizes {
		add(fmt.Sprintf("rand-%d", n), rng.Bytes(n))
	}
	for _, n := range []int{cdcMinChunkSize, cdcMaxChunkSize + 7, 1 << 20} {
		add(fmt.Sprintf("zero-%d", n), make([]byte, n))
		add(fmt.Sprintf("text-%d", n), corpus.Text(rng, n))
		pat := bytes.Repeat([]byte("0123456789abcdef"), n/16+1)[:n]
		add(fmt.Sprintf("pattern-%d", n), pat)
	}
	// Edits of one stream: boundary resynchronisation must agree as well.
	base := rng.Bytes(1_200_000)
	add("edit-base", base)
	add("edit-insert", append(append(append([]byte(nil), base[:500_000]...), rng.Bytes(777)...), base[500_000:]...))
	add("edit-delete", append(append([]byte(nil), base[:300_000]...), base[301_000:]...))

	// A few real corpus files from different workload classes.
	cfg := corpus.DefaultConfig()
	cfg.SizeKiB = 256
	cfg.Revisions = 1
	cfg.Families = []string{"zip-deflate-change", "st-modify-sparse", "bin-rebase", "car-add", "gzip-L6-append"}
	m, err := corpus.WriteDir(cfg, filepath.Join(dir, "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mem := range m.Members {
		paths = append(paths, filepath.Join(dir, "corpus", filepath.FromSlash(mem.Path)))
	}
	return paths
}

// TestCDCMatchesProduction builds the production zeros3.go (unmodified) with
// a dump hook and compares constants, the gear table and chunk lengths for
// many streams, with whole and fragmented reads.
func TestCDCMatchesProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the production binary")
	}
	root := filepath.Join("..", "..", "..")
	prod, err := os.ReadFile(filepath.Join(root, "zeros3.go"))
	if err != nil {
		t.Fatalf("production source not found: %v", err)
	}
	work := t.TempDir()
	build := filepath.Join(work, "build")
	os.MkdirAll(build, 0o755)
	os.WriteFile(filepath.Join(build, "zeros3.go"), prod, 0o644)
	os.WriteFile(filepath.Join(build, "z3dump.go"), []byte(dumpSrc), 0o644)
	os.WriteFile(filepath.Join(build, "go.mod"), []byte("module zeros3\n\ngo 1.27.0\n"), 0o644)
	bin := filepath.Join(work, "dump")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = build
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building production helper: %v\n%s", err, out)
	}

	inputs := equivInputs(t, work)
	for _, frag := range []int{0, 4099} {
		run := exec.Command(bin, inputs...)
		run.Env = append(os.Environ(), "Z3_CDC_DUMP=1", "Z3_FRAG="+strconv.Itoa(frag))
		out, err := run.Output()
		if err != nil {
			t.Fatalf("running helper: %v", err)
		}
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if len(lines) != 2+len(inputs) {
			t.Fatalf("helper printed %d lines for %d inputs", len(lines), len(inputs))
		}
		wantConst := fmt.Sprintf("const %d %d %d %d %d %s", cdcMinChunkSize, cdcTargetChunkSize, cdcMaxChunkSize, uint64(cdcMaskS), uint64(cdcMaskL), cdcGearSeed)
		if lines[0] != wantConst {
			t.Fatalf("CDC constants drifted:\n production %q\n lab        %q", lines[0], wantConst)
		}
		h := sha256.New()
		for _, g := range gearTable {
			fmt.Fprintf(h, "%d,", g)
		}
		if lines[1] != fmt.Sprintf("gear %x", h.Sum(nil)) {
			t.Fatal("gear table differs from production")
		}
		for i, p := range inputs {
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			var r io.Reader = f
			if frag > 0 {
				r = fragReader{f, frag}
			}
			sizes, err := CDCSizes(r)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			sb.WriteString("file")
			for _, n := range sizes {
				sb.WriteString(" " + strconv.Itoa(n))
			}
			if got := lines[2+i]; got != sb.String() {
				t.Errorf("%s (frag %d): chunk boundaries differ from production\n production %.200s\n lab        %.200s", filepath.Base(p), frag, got, sb.String())
			}
		}
	}
}

// Production-independent invariants of the lab copy; these run under -short.
func TestCDCSizeInvariants(t *testing.T) {
	rng := corpus.NewRNG(1, "inv")
	for _, n := range []int{0, 1, cdcMinChunkSize, 1_000_000} {
		data := rng.Bytes(n)
		sizes, err := CDCSizes(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for i, s := range sizes {
			total += s
			if s > cdcMaxChunkSize || (s < cdcMinChunkSize && i != len(sizes)-1) {
				t.Fatalf("n=%d chunk %d has size %d", n, i, s)
			}
		}
		if total != n {
			t.Fatalf("chunks cover %d of %d bytes", total, n)
		}
		frag, _ := CDCSizes(fragReader{bytes.NewReader(data), 1000})
		if fmt.Sprint(frag) != fmt.Sprint(sizes) {
			t.Fatalf("n=%d: boundaries depend on read size", n)
		}
	}
	zeros, _ := CDCSizes(bytes.NewReader(make([]byte, 3*cdcMaxChunkSize+1)))
	if fmt.Sprint(zeros) != fmt.Sprint([]int{cdcMaxChunkSize, cdcMaxChunkSize, cdcMaxChunkSize, 1}) {
		t.Fatalf("zero stream chunks: %v", zeros)
	}
}
