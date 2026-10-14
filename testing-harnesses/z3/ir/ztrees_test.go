package ir

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/deflate"
)

// zlib 1.3 and GNU gzip 1.12 streams: every non-stored block's BTYPE and
// dynamic header must be reproduced from its tokens alone.
func TestZlibHeaderPrediction(t *testing.T) {
	files, _ := filepath.Glob("testdata/zlibfast/*.deflate")
	if len(files) < 5 {
		t.Fatalf("fixtures missing: %v", files)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s, err := deflate.Parse(raw, deflate.Options{})
		if err != nil {
			t.Fatal(err)
		}
		ok, n, stored := 0, 0, 0
		for i := range s.Blocks {
			if s.Blocks[i].Kind == deflate.Stored {
				stored++
				continue
			}
			n++
			if blockPredicted(&s.Blocks[i]) {
				ok++
			}
		}
		t.Logf("%s: %d blocks (%d stored): %d/%d predicted", filepath.Base(f), len(s.Blocks), stored, ok, n)
		if ok != n {
			t.Errorf("%s: %d of %d compressed blocks mispredicted", f, n-ok, n)
		}
	}
}

// The predicted-header skeleton must be (much) smaller on tiny-block streams
// and reconstruct exactly; NoHeaderPred keeps the Z3-06/07 skeleton.
func TestPredictedSkeleton(t *testing.T) {
	for _, name := range []string{"mem1", "gzip-rsync", "gzip-9", "L1"} {
		raw, err := os.ReadFile("testdata/zlibfast/" + name + ".deflate")
		if err != nil {
			t.Fatal(err)
		}
		s, err := deflate.Parse(raw, deflate.Options{})
		if err != nil {
			t.Fatal(err)
		}
		a, err := Encode(s, Config{Codec: Pred, Policy: Policy{PTokRun, 16 << 10}, Profile: AutoProfile, NoHeaderPred: true})
		if err != nil {
			t.Fatal(err)
		}
		b, err := Encode(s, Config{Codec: Pred, Policy: Policy{PTokRun, 16 << 10}, Profile: AutoProfile})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d blocks, stream %d B: skeleton exact %d B -> predicted %d B", name, len(s.Blocks), len(raw), len(a.Skeleton), len(b.Skeleton))
		if len(b.Skeleton) > len(a.Skeleton) {
			t.Errorf("%s: predicted skeleton is larger", name)
		}
		if name == "mem1" && len(b.Skeleton)*8 > len(a.Skeleton) {
			t.Errorf("mem1: header prediction should shrink the skeleton at least 8x (%d -> %d)", len(a.Skeleton), len(b.Skeleton))
		}
		for _, pk := range []*Package{a, b} {
			var out bytes.Buffer
			if err := Reconstruct(&out, pk.Skeleton, func(i int) ([]byte, error) { return pk.Units[i], nil }, s.Plain, DecodeOptions{VerifyMatches: true}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !bytes.Equal(out.Bytes(), raw) {
				t.Fatalf("%s: reconstruction differs", name)
			}
		}
	}
}
