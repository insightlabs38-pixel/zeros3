// i1c-fixtures freezes lab Git contracts for production cross-checks.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"zeros3-z3/gitz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
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
		panic("i1c-fixtures output-directory")
	}
	dir := os.Args[1]
	check(os.MkdirAll(dir, 0755))
	var fixtures []fixture
	for _, a := range []struct {
		name string
		algo gitz3.Algo
	}{{"sha1", gitz3.SHA1}, {"sha256", gitz3.SHA256}} {
		for _, name := range []string{"v1", "ofs", "ref"} {
			file := a.name + "-" + name + ".pack"
			data, err := os.ReadFile(filepath.Join("gitz3", "testdata", file))
			check(err)
			b, _, err := kinds.EncodeGit(data, a.algo)
			check(err)
			fixtures = append(fixtures, freeze(file, "REFERENCE_FIXTURE", data, b))
			if name == "v1" {
				v3 := append([]byte(nil), data...)
				binary.BigEndian.PutUint32(v3[4:8], 3)
				sum := a.algo.Sum(v3[:len(v3)-a.algo.Size()])
				copy(v3[len(v3)-len(sum):], sum)
				b, _, err := kinds.EncodeGit(v3, a.algo)
				check(err)
				fixtures = append(fixtures, freeze(a.name+"-pack-v3", "SYNTHETIC_FIXTURE", v3, b))
			}
		}
	}
	raw, err := json.MarshalIndent(fixtures, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(dir, "lab-git.json"), append(raw, '\n'), 0644))
	fmt.Printf("froze %d lab Git fixtures (%d bytes)\n", len(fixtures), len(raw))
}
