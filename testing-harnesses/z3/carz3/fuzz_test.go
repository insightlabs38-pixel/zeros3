package carz3

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzBuildReconstruct: whatever Build accepts must reconstruct to the input
// byte for byte, through the serialised recipe, and must never panic.
func FuzzBuildReconstruct(f *testing.F) {
	bs, root := Gen(GenSpec{Seed: 3, N: 8, Min: 20, Max: 200, Fan: 3})
	v1 := V1([][]byte{root}, bs)
	f.Add(v1)
	f.Add(V2From(v1, V2Opts{PadBefore: 3, IndexCodec: MultihashIndexSortedCodec}))
	f.Add(V2From(v1, V2Opts{IndexCodec: IndexSortedCodec}))
	f.Add(V1(nil, Recode(bs, CodecDagPB, MhSHA2_256, true)))
	f.Add(V1([][]byte{root}, Recode(bs, CodecRaw, MhSHA3_256, false)))
	for _, n := range []string{"sample-rw-bs-v2.car", "sample-unixfs-v2.car"} {
		if b, err := os.ReadFile(filepath.Join("testdata", n)); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		opt := DefaultOptions()
		opt.MaxBlock, opt.MaxBlocks, opt.MaxTotal = 1<<20, 1<<12, 1<<24
		for _, inline := range []int{0, 64} {
			opt.InlineMax = inline
			rep, _, err := Build(bytes.NewReader(data), int64(len(data)), opt)
			if err != nil {
				if !IsFallback(err) {
					t.Fatalf("unclassified error: %v", err)
				}
				return
			}
			get := func(s [32]byte) ([]byte, error) {
				for _, r := range rep.Refs {
					if r.Sum == s {
						return data[r.SrcOff : r.SrcOff+r.Len], nil
					}
				}
				return nil, bytes.ErrTooLarge
			}
			m, _ := rep.Marshal()
			rep2, err := Unmarshal(m)
			if err != nil {
				t.Fatalf("recipe does not parse back: %v", err)
			}
			var out bytes.Buffer
			if err := rep2.Reconstruct(&out, get); err != nil || !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("reconstruct differs (err %v)", err)
			}
		}
	})
}
