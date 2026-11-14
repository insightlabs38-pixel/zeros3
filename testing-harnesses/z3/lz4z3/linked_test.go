package lz4z3

import (
	"bytes"
	"os"
	"testing"
)

// Fixtures come from oracle/lz4/linkref.py (python-lz4 4.4.5, liblz4 1.9.4, block_linked=True)
// and from the lz4 1.9.4 command line (cli_BD_*: lz4 -BD -B4 -1 / -9).
func TestLinkedFrames(t *testing.T) {
	want := map[string]struct {
		pred, stored int
		id, accel    byte
	}{
		"link_oneshot_records.lz4":       {5, 0, PredLinkPrefix, 1}, // LZ4F_compressFrame; the saved-dictionary profile misses a block
		"link_oneshot_records_fast4.lz4": {4, 0, PredLinkPrefix, 5}, // level -4
		"link_stream40k_mixed.lz4":       {4, 1, PredLinkExt, 1},    // 40 kB flushes: small dictionaries, a stored block inside the chain
		"link_hc9_mixed.lz4":             {3, 0, PredLinkHC, 9},     // LZ4_compress_HC_continue, hash chains carried
		"link_hc12_records.lz4":          {3, 0, PredLinkHC, 12},    // optimal parser, carried
		"cli_BD_fast_budget.lz4":         {1, 1, PredLinkPrefix, 1}, // lz4 1.9.4 CLI -BD: the table state left by a stored block (overflow point) decides the next block
		"cli_BD_hc9_records.lz4":         {4, 0, PredLinkHCExt, 9},  // lz4 CLI -BD -9: LZ4_saveDictHC + external dictionary
	}
	for name, w := range want {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		plain, rec, st, err := AnalyzeFrames(data, Limits{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Linked != 1 || st.Predicted != w.pred || st.Stored != w.stored || st.Hybrid+st.Explicit != 0 {
			t.Errorf("%s: %+v", name, st)
		}
		if len(rec) > 64 {
			t.Errorf("%s: recipe %d bytes", name, len(rec))
		}
		var out bytes.Buffer
		if err := ReplayFrames(&out, rec, bytes.NewReader(plain), Limits{}); err != nil || !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("%s: replay differs (%v)", name, err)
		}
		pat := []byte{KindPredicted, w.id, w.accel}
		if w.id >= PredLinkHC { // neighbouring LZ4HC levels can be indistinguishable on a frame
			pat = pat[:2]
		}
		if n := bytes.Count(rec, pat); n != w.pred {
			t.Errorf("%s: %d blocks carry profile %d/accel %d, want %d", name, n, w.id, w.accel, w.pred)
		}
		// corrupted recipes: an error or other bytes (caught by the caller's digest), never a panic
		for i := range rec {
			bad := append([]byte(nil), rec...)
			bad[i] ^= 1
			out.Reset()
			_ = ReplayFrames(&out, bad, bytes.NewReader(plain), Limits{})
		}
	}
}

// The first block of a linked chain is an ordinary fresh-stream block.
func TestLinkedFirstBlockIsContinue(t *testing.T) {
	plain := bytes.Repeat([]byte("linked blocks carry the table; "), 3000)
	for _, ac := range []int{1, 5} {
		for _, ext := range []bool{false, true} {
			l := &Linked{Ext: ext, Accel: ac}
			got, ok := l.Next(plain, len(plain))
			ref, _ := Predict(PredContinue, plain, ac)
			if !ok || len(got) != len(ref) {
				t.Fatalf("accel %d ext %v: %d vs %d sequences", ac, ext, len(got), len(ref))
			}
			for i := range ref {
				if got[i] != ref[i] {
					t.Fatalf("accel %d ext %v: sequence %d differs", ac, ext, i)
				}
			}
		}
	}
}
