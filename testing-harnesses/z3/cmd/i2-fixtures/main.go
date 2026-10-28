// i2-fixtures mechanically reuses the I1B lab-to-production envelope bridge.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/ggufz3"
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
	dir := os.Args[1]
	check(os.MkdirAll(dir, 0755))
	var fixtures []fixture
	for _, name := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		data, e := os.ReadFile(filepath.Join("dexz3/testdata", name))
		check(e)
		b, _, e := kinds.EncodeDEX(data, dexz3.Config{K0: 3, Cut: econx.UnitCutBytes(4096)})
		check(e)
		fixtures = append(fixtures, freeze(name, "PRODUCER_WORKLOAD", data, b))
	}
	for _, name := range []string{"fixture-r0.dll", "fixture-earlytype.dll", "fixture-body.dll"} {
		data, e := os.ReadFile(filepath.Join("clrz3/testdata", name))
		check(e)
		b, _, e := kinds.EncodeCLR(data, clrz3.Config{Auto: 1, Cut: econx.UnitCutBytes(4096)})
		check(e)
		fixtures = append(fixtures, freeze(name, "PRODUCER_WORKLOAD", data, b))
	}
	var prev *kinds.Bundle
	var plain []byte
	for hop := 1; hop <= 3; hop++ {
		next := bytes.Repeat([]byte{0, 60, 1, 60, 2, 60, 3, 60}, 8192)
		for i := hop; i < len(next); i += 997 {
			next[i] ^= byte(hop)
		}
		res := append([]byte(nil), next...)
		if prev != nil {
			for i := range res {
				res[i] ^= plain[i]
			}
		}
		res = kinds.PlaneChunks(res, 2, 2048)
		r := &repz3.Rep{Kind: repz3.KindModel, Size: int64(len(next))}
		op := kinds.ModelOp{Tag: kinds.ModelPlanes, W: 2, E: 2048, Name: "tensor"}
		var children []*kinds.Bundle
		if prev != nil {
			r.Members = append(r.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(plain), Size: int64(len(plain)), Child: prev.ID})
			children = append(children, prev)
			op.Tag = kinds.ModelXORPlanes
			op.Base = 0
		}
		op.Res = len(r.Members)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(res), Size: int64(len(res))})
		r.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{op})
		b, e := kinds.NewBundle(r, [][]byte{res}, children...)
		check(e)
		fixtures = append(fixtures, freeze(fmt.Sprintf("model-hop%d", hop), "SYNTHETIC_FIXTURE", next, b))
		prev, plain = b, next
	}
	gg := ggufz3.Build(3, 32, []ggufz3.BuildTensor{{Name: "quant", Dims: []uint64{256, 3}, Type: 14, Data: bytes.Repeat([]byte{7}, 630)}, {Name: "fp32", Dims: []uint64{64}, Type: 0, Data: bytes.Repeat([]byte{9}, 256)}})
	check(os.WriteFile(filepath.Join(dir, "model.gguf"), gg, 0644))
	plainModel := bytes.Repeat([]byte{0, 60, 1, 60, 2, 60, 3, 60}, 1<<17)
	hdr := fmt.Sprintf(`{"weights":{"dtype":"BF16","shape":[%d],"data_offsets":[0,%d]}}`, len(plainModel)/2, len(plainModel))
	stBytes := binary.LittleEndian.AppendUint64(nil, uint64(len(hdr)))
	stBytes = append(stBytes, hdr...)
	stBytes = append(stBytes, plainModel...)
	check(os.WriteFile(filepath.Join(dir, "model.safetensors"), stBytes, 0644))
	f, e := os.Create(filepath.Join(dir, "lab-i2.json.gz"))
	check(e)
	gz := gzip.NewWriter(f)
	check(json.NewEncoder(gz).Encode(fixtures))
	check(gz.Close())
	check(f.Close())
	fmt.Printf("froze %d fixtures\n", len(fixtures))
}
