package sstz3

import (
	"bytes"
	"os"
	"testing"
)

func TestNativeZstd(t *testing.T) {
	for _, name := range []string{"rocksdb", "pebble"} {
		data, e := os.ReadFile("testdata/" + name + "-zstd.sst")
		if e != nil {
			t.Fatal(e)
		}
		table, e := Open(bytes.NewReader(data), int64(len(data)), Limits{})
		if e != nil {
			t.Fatal(e)
		}
		if e := table.Reject(); e != nil {
			t.Fatal(e)
		}
		for form := FormBlocks; form <= maxForm; form++ {
			recipe, store := roundTrip(t, data, Options{Form: form, Cutter: &HashCutter{Target: 2048, Max: 8192}})
			var out bytes.Buffer
			_, e := DecodeRange(&out, recipe, store.get, 19, 333, Limits{})
			if e != nil || !bytes.Equal(out.Bytes(), data[19:352]) {
				t.Fatalf("range: %v", e)
			}
		}
		for _, h := range table.Blocks {
			block, e := ReadBlock(bytes.NewReader(data), int64(len(data)), h, table.Footer.Checksum, true, Limits{})
			if e != nil {
				t.Fatal(e)
			}
			if block.Comp() != CompZstd {
				continue
			}
			if _, _, e := analyzeZstdBlock(block.Payload, Limits{MaxDecoded: 16}); e == nil {
				t.Fatal("decoded bound ignored")
			}
			plain, recipe, e := analyzeZstdBlock(block.Payload, Limits{})
			if e != nil {
				t.Fatal(e)
			}
			plain[0] ^= 1
			var out bytes.Buffer
			if e := replayZstdBlock(&out, recipe, plain, Limits{}); e == nil && bytes.Equal(out.Bytes(), block.Payload) {
				t.Fatal("mutation rebuilt original")
			}
		}
	}
}
func FuzzZstdBlock(f *testing.F) {
	for _, name := range []string{"rocksdb", "pebble"} {
		data, e := os.ReadFile("testdata/" + name + "-zstd.sst")
		if e != nil {
			f.Fatal(e)
		}
		table, e := Open(bytes.NewReader(data), int64(len(data)), Limits{})
		if e != nil {
			f.Fatal(e)
		}
		for _, h := range table.Blocks {
			b, e := ReadBlock(bytes.NewReader(data), int64(len(data)), h, table.Footer.Checksum, true, Limits{})
			if e != nil {
				f.Fatal(e)
			}
			if b.Comp() == CompZstd {
				f.Add(b.Payload)
			}
		}
	}
	f.Add([]byte{255, 255, 255, 255, 127})
	f.Fuzz(func(t *testing.T, p []byte) {
		if len(p) > 1<<16 {
			return
		}
		plain, recipe, e := analyzeZstdBlock(p, Limits{MaxDecoded: 1 << 20, MaxRecipe: 2 << 20})
		if e != nil {
			return
		}
		var out bytes.Buffer
		if e := replayZstdBlock(&out, recipe, plain, Limits{MaxDecoded: 1 << 20, MaxRecipe: 2 << 20}); e != nil || !bytes.Equal(out.Bytes(), p) {
			t.Fatalf("replay: %v", e)
		}
	})
}

func TestNewPebbleFooterFallsBack(t *testing.T) {
	data, e := os.ReadFile("testdata/pebble-v7.sst")
	if e != nil {
		t.Fatal(e)
	}
	table, e := Open(bytes.NewReader(data), int64(len(data)), Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if table.Footer.Version != 7 || table.Reject() == nil || table.DataEnd != 0 {
		t.Fatal("future footer was interpreted")
	}
	if _, _, _, e := Encode(bytes.NewReader(data), int64(len(data)), Options{Form: FormUKV}, store{}.emit); e == nil {
		t.Fatal("future table encoded")
	}
}
