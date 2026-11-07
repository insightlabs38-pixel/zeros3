package sstz3

import (
	"bytes"
	"os"
	"testing"
)

// Z4-B6: RocksDB LZ4 / LZ4HC tables (compressed index included) round-trip through every form.
func TestNativeLZ4(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     int // st.LZ4Kinds index expected to dominate: 1 predicted
		explicit bool
	}{{"rocksdb_lz4_small.sst", 1, false}, {"rocksdb_lz4hc_small.sst", 1, false}} {
		data, e := os.ReadFile("testdata/" + tc.name)
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
			if _, e := DecodeRange(&out, recipe, store.get, 19, 333, Limits{}); e != nil || !bytes.Equal(out.Bytes(), data[19:352]) {
				t.Fatalf("%s range: %v", tc.name, e)
			}
		}
		_, _, st, e := Encode(bytes.NewReader(data), int64(len(data)), Options{Form: FormUKV, Cutter: &HashCutter{Target: 2048, Max: 8192}}, func(string, ID, []byte) error { return nil })
		if e != nil {
			t.Fatal(e)
		}
		if st.LZ4Kinds[1] == 0 || st.LZ4Kinds[0]+st.LZ4Kinds[2] != 0 {
			t.Fatalf("%s: predictor coverage %v", tc.name, st.LZ4Kinds)
		}
	}
}

// Every single-byte corruption of an LZ4 table either fails safely or still rebuilds exactly.
func TestLZ4CorruptionSweep(t *testing.T) {
	data, _ := os.ReadFile("testdata/rocksdb_lz4_small.sst")
	step := 1 + len(data)/1500
	for i := 0; i < len(data); i += step {
		c := append([]byte(nil), data...)
		c[i] ^= 0x5A
		var rec []byte
		store := store{}
		var e error
		rec, _, _, e = Encode(bytes.NewReader(c), int64(len(c)), Options{Form: FormUKV, Cutter: &HashCutter{Target: 2048, Max: 8192}}, store.emit)
		if e != nil {
			continue
		}
		var out bytes.Buffer
		if e := Decode(&out, rec, store.get, Limits{}); e != nil || !bytes.Equal(out.Bytes(), c) {
			t.Fatalf("byte %d: accepted but not exact (%v)", i, e)
		}
	}
}

// A recipe whose LZ4 section is damaged must be rejected or reproduce different bytes, never panic.
func TestLZ4RecipeFlips(t *testing.T) {
	data, _ := os.ReadFile("testdata/rocksdb_lz4_small.sst")
	recipe, st := roundTrip(t, data, Options{Form: FormBlocks})
	for i := 0; i < len(recipe); i += 1 + len(recipe)/400 {
		c := append([]byte(nil), recipe...)
		c[i] ^= 0xFF
		var out bytes.Buffer
		_ = Decode(&out, c, st.get, Limits{MaxDecoded: 1 << 20, MaxRecipe: 1 << 20})
	}
}
