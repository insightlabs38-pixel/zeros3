// i3-fixtures preserves exact lab structured recipes for draft port verification.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
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
		panic("i3-fixtures output-directory")
	}
	dir := os.Args[1]
	check(os.MkdirAll(dir, 0755))
	var fixtures []fixture
	for _, name := range []string{"rocksdb-zstd.sst", "pebble-zstd.sst"} {
		data, err := os.ReadFile(filepath.Join("sstz3", "testdata", name))
		check(err)
		b, _, _, err := kinds.EncodeSST(bytes.NewReader(data), int64(len(data)), sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 4 << 10, Max: 64 << 10}})
		check(err)
		fixtures = append(fixtures, freeze(name, "NATIVE_PRODUCER_FIXTURE", data, b))
	}
	for _, name := range []string{"dd_none.parquet", "dd_snappy.parquet", "dd_zstd.parquet", "pa_delta.parquet", "pa_snappy_v1.parquet", "pa_snappy_v2.parquet", "pa_zstd_v1.parquet"} {
		data, err := os.ReadFile(filepath.Join("parquetz3", "testdata", name))
		check(err)
		b, _, _, err := kinds.EncodeParquet(bytes.NewReader(data), int64(len(data)), parquetz3.Options{Target: 4 << 10})
		check(err)
		fixtures = append(fixtures, freeze(name, "NATIVE_PRODUCER_FIXTURE", data, b))
	}
	raw, err := json.MarshalIndent(fixtures, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(dir, "lab-structured.json"), append(raw, '\n'), 0644))
	fmt.Printf("froze %d fixtures (%d bytes)\n", len(fixtures), len(raw))
}
