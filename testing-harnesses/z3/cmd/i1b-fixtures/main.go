// i1b-fixtures freezes lab-serialized codec contracts for production cross-checks.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/zstdz3"
)

type fixture struct {
	Name, Class                     string
	Input, Root, LabRoot, LabRecipe []byte
	Objects                         map[string][]byte
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func freeze(name, class string, data []byte, b *kinds.Bundle) fixture {
	check(kinds.Prove(nil, b, data))
	out := fixture{Name: name, Class: class, Input: data, LabRoot: b.Raw, LabRecipe: b.Rep.Recipe, Objects: map[string][]byte{}}
	// Production's pre-existing envelope differs only in its magic/version.
	// Re-address derived children after translating their envelopes; leave every
	// codec recipe, unit, table and stored/plaintext component unchanged.
	translated := map[repz3.SHA]repz3.SHA{}
	var visit func(*kinds.Bundle) []byte
	visit = func(b *kinds.Bundle) []byte {
		for _, c := range b.Children {
			visit(c)
		}
		for _, raw := range b.Objects {
			sum := sha256.Sum256(raw)
			out.Objects[hex.EncodeToString(sum[:])] = raw
		}
		r := *b.Rep
		r.Members = append([]repz3.Member(nil), b.Rep.Members...)
		for i := range r.Members {
			if r.Members[i].Role == repz3.Derived {
				r.Members[i].Child = translated[r.Members[i].Child]
			}
		}
		raw, err := r.Marshal()
		check(err)
		copy(raw[:4], "ZS3R")
		raw[4] = 1
		sum := sha256.Sum256(raw)
		translated[b.ID] = sum
		out.Objects[hex.EncodeToString(sum[:])] = raw
		return raw
	}
	out.Root = visit(b)
	return out
}

func main() {
	if len(os.Args) != 2 {
		panic("i1b-fixtures output-directory")
	}
	dir := os.Args[1]
	check(os.MkdirAll(dir, 0755))
	var fixtures []fixture
	block := make([]byte, 8192)
	rand.New(rand.NewSource(1410)).Read(block)
	plain := bytes.Repeat(block, 16)
	for _, level := range []int{gzip.NoCompression, gzip.BestSpeed, gzip.DefaultCompression} {
		var gz bytes.Buffer
		w, err := gzip.NewWriterLevel(&gz, level)
		check(err)
		w.Name = "fixture.db"
		w.Comment = "exact wrapper"
		_, err = w.Write(plain)
		check(err)
		check(w.Close())
		b, _, err := kinds.EncodeGzip(gz.Bytes(), kinds.CDC)
		check(err)
		fixtures = append(fixtures, freeze(fmt.Sprintf("gzip-%d", level), "SYNTHETIC_FIXTURE", gz.Bytes(), b))
	}
	for _, name := range []string{"ref155_text.zst", "ref155_zeros.zst", "ref157_json.zst", "fx_rle_literals.zst"} {
		data, err := os.ReadFile(filepath.Join("zstdz3", "testdata", name))
		check(err)
		b, _, err := kinds.EncodeZstd(data, kinds.CDC)
		check(err)
		fixtures = append(fixtures, freeze(name, "REFERENCE_FIXTURE", data, b))
	}
	// Hermetic, pinned compressed container with both structural child families.
	plainTar := kinds.CompoundChain(32).Stages[0].Data
	zst, err := zstdz3.Synth(plainTar)
	check(err)
	check(os.WriteFile(filepath.Join(dir, "nested.tar"), plainTar, 0644))
	check(os.WriteFile(filepath.Join(dir, "nested.zst"), zst, 0644))
	raw, err := json.MarshalIndent(fixtures, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(dir, "lab-codecs.json"), append(raw, '\n'), 0644))
	fmt.Printf("froze %d lab codec fixtures (%d bytes)\n", len(fixtures), len(raw))
}
