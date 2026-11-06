package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
	"zeros3-z3/modelz3"
	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
)

const structHeader = 76

// Export existing exact format representations, not new parsers. Every byte of
// the reachable serialized closure is carried, including recipes and indexes.
func exportStructural(families []Family, dir string) {
	var exported []Family
	for _, f := range families {
		var kind string
		switch f.Name {
		case "clr-newtonsoft":
			kind = "clr"
		case "dex-fdroid":
			kind = "dex"
		case "model-pythia":
			kind = "model"
		case "parquet-chain":
			kind = "parquet"
		case "rocksdb-sst", "pebble-sst":
			kind = "sst"
		case "source-archives":
			kind = "tar"
		case "git-repack", "git-history":
			kind = "git"
		case "oci-transport":
			kind = "zstd"
		default:
			continue
		}
		next := Family{Name: f.Name + "-struct", Kind: f.Kind + "_EXACT_COMPONENTS"}
		seen := map[[32]byte]bool{}
		var cumulative int64
		for i, path := range f.Paths {
			d, e := os.ReadFile(path)
			must(e)
			var bundle *kinds.Bundle
			switch kind {
			case "tar":
				bundle, _, e = kinds.EncodeTarLayer(d, 1<<20)
			case "git":
				bundle, _, e = kinds.EncodeGit(d, gitz3.SHA1)
			case "zstd":
				bundle, _, e = kinds.EncodeZstd(d, func(p []byte) (*kinds.Bundle, error) { b, _, e := kinds.EncodeTarLayer(p, 1<<20); return b, e })
			case "clr":
				bundle, _, e = kinds.EncodeCLR(d, clrz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
			case "dex":
				bundle, _, e = kinds.EncodeDEX(d, dexz3.Config{Auto: 12, Cut: econx.UnitCutBytes(4096)})
			case "sst":
				bundle, _, _, e = kinds.EncodeSST(bytes.NewReader(d), int64(len(d)), sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 8192, Max: 32768}})
			case "parquet":
				bundle, _, _, e = kinds.EncodeParquet(bytes.NewReader(d), int64(len(d)), parquetz3.Options{Target: 8192})
			case "model":
				src := &modelz3.Src{R: bytes.NewReader(d), Size: int64(len(d))}
				var lay *modelz3.Layout
				lay, _, e = modelz3.STLayout(src.R, src.Size)
				if e == nil {
					bundle, _, _, e = modelz3.EncodeModel(src, lay, nil, func(modelz3.SHA) bool { return false }, modelz3.Policy{Planes: true, MaxHops: 2})
				}
			}
			must(e)
			store := repz3.NewMemStore()
			root, _, e := bundle.Persist(store)
			must(e)
			env := structEnv(store)
			var original bytes.Buffer
			n, sum, e := env.Reconstruct(root, &original)
			must(e)
			if n != int64(len(d)) || sum != sha256.Sum256(d) || !bytes.Equal(original.Bytes(), d) {
				panic("structural source proof")
			}
			reach, e := env.Reachable(root)
			must(e)
			stream := append([]byte("B1S1"), root[:]...)
			stream = append(stream, sum[:]...)
			stream = binary.LittleEndian.AppendUint64(stream, uint64(n))
			var unique, physical int64
			for _, id := range reach.Order {
				b := store.Bytes(id)
				stream = binary.LittleEndian.AppendUint32(stream, uint32(len(b)))
				stream = append(stream, b...)
				if !seen[id] {
					seen[id] = true
					unique++
					physical += lab.PackedLen(b) + 96
				}
			}
			cumulative += physical + 36
			path := filepath.Join(dir, fmt.Sprintf("%s-%02d.components", f.Name, i))
			must(os.WriteFile(path, stream, 0644))
			next.Paths = append(next.Paths, path)
			must(verifyStructural(stream))
			emit(map[string]any{"family": f.Name, "revision": i, "source_sha": fmt.Sprintf("%x", sum), "root": fmt.Sprintf("%x", root), "bytes": len(d), "objects": len(reach.Order), "new_objects": unique, "format_physical_cumulative": cumulative, "stream": path})
		}
		exported = append(exported, next)
	}
	b, e := json.MarshalIndent(exported, "", "  ")
	must(e)
	must(os.WriteFile(filepath.Join(dir, "struct-manifest.json"), append(b, '\n'), 0644))
}
func structEnv(store *repz3.MemStore) *repz3.Env {
	return kinds.NewEnvLimits(store, repz3.Limits{MaxDepth: 4, MaxWhole: 128 << 20, MaxObjects: 1 << 20, MaxRepBytes: 32 << 20})
}
func verifyStructural(stream []byte) error {
	if len(stream) < structHeader || string(stream[:4]) != "B1S1" {
		return fmt.Errorf("structural header")
	}
	var root, want [32]byte
	copy(root[:], stream[4:36])
	copy(want[:], stream[36:68])
	size := binary.LittleEndian.Uint64(stream[68:76])
	store := repz3.NewMemStore()
	for b := stream[structHeader:]; len(b) > 0; {
		if len(b) < 4 {
			return io.ErrUnexpectedEOF
		}
		n := int(binary.LittleEndian.Uint32(b))
		b = b[4:]
		if n > len(b) || n == 0 {
			return io.ErrUnexpectedEOF
		}
		store.Put(b[:n])
		b = b[n:]
	}
	n, sum, e := structEnv(store).Reconstruct(root, io.Discard)
	if e != nil {
		return e
	}
	if uint64(n) != size || sum != want {
		return fmt.Errorf("structural native hash")
	}
	return nil
}

// Components larger than the codec's bound are subdivided with production CDC,
// without hiding those headers or bytes from accounting or exact replay.
func structuralChunks(stream []byte) [][]byte {
	if len(stream) < structHeader {
		panic("structural short")
	}
	out := [][]byte{stream[:structHeader]}
	for b := stream[structHeader:]; len(b) > 0; {
		if len(b) < 4 {
			panic("structural length")
		}
		n := int(binary.LittleEndian.Uint32(b))
		if n > len(b)-4 {
			panic("structural length")
		}
		record := b[:4+n]
		if len(record) > simMax {
			out = append(out, lab.ChunkScaled(record, 16<<10, 64<<10, 256<<10)...)
		} else {
			out = append(out, record)
		}
		b = b[4+n:]
	}
	return out
}

const simMax = 16 << 20
