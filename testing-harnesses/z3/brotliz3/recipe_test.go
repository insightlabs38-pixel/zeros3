package brotliz3_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"zeros3-z3/brotliz3"
)

// A recipe derived from a producer's stream and its decoding must regenerate the stream byte for byte, however the
// producer chunked, flushed and sized its calls. The scenario streams are the ones testdata/golden_stream.txt pins
// against libbrotlienc, so they stand for real producer output.
func TestDeriveStreamRecipes(t *testing.T) {
	var total time.Duration
	tampered := 0
	for i, s := range scenarios() {
		if s.n > 700001 || testing.Short() && s.n > 200000 || i%3 != 0 && s.n > 100000 {
			continue
		}
		if f := os.Getenv("BROTLIZ3_FILTER"); f != "" && !strings.Contains(s.name(), f) {
			continue
		}
		payload := runOps(t, s)
		body, tr, err := brotliz3.DecodeTrace(payload, len(s.data))
		if err != nil || !bytes.Equal(body, s.data) {
			t.Fatalf("%s: decode: %v", s.name(), err)
		}
		t0 := time.Now()
		r, ok := brotliz3.Derive(payload, body, tr, 0)
		total += time.Since(t0)
		if !ok {
			t.Errorf("%s: no recipe found (ops %.60s)", s.name(), s.ops)
			continue
		}
		if r.Quality != s.q && !bytes.Equal(mustReplay(t, r, body), payload) {
			t.Errorf("%s: recipe %+v does not replay", s.name(), r)
		}
		enc := r.Marshal()
		r2, n, err := brotliz3.UnmarshalRecipe(enc, int64(len(body)))
		if err != nil || n != len(enc) || !bytes.Equal(mustReplay(t, r2, body), payload) {
			t.Fatalf("%s: marshal round trip: %v", s.name(), err)
		}
		// a stream that was changed by one byte is not reproduced by any recipe
		if s.n <= 70000 && tampered < 6 {
			tampered++
			bad := append([]byte(nil), payload...)
			bad[len(bad)/2] ^= 1
			if _, ok := brotliz3.Derive(bad, body, tr, 0); ok {
				t.Errorf("%s: a recipe was derived for a damaged stream", s.name())
			}
		}
	}
	t.Logf("derivation time %s", total.Round(time.Millisecond))
}

func mustReplay(t *testing.T, r brotliz3.Recipe, body []byte) []byte {
	t.Helper()
	out, err := r.Replay(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecipeHostile(t *testing.T) {
	good := brotliz3.Recipe{Quality: 8, LgWin: 22, Flushes: []uint64{5, 9}}.Marshal()
	for n := 0; n < len(good); n++ {
		if _, _, err := brotliz3.UnmarshalRecipe(good[:n], 100); err == nil && n < 3 {
			t.Fatalf("truncated to %d accepted", n)
		}
	}
	if _, _, err := brotliz3.UnmarshalRecipe(good, 8); err == nil {
		t.Fatal("flush beyond the body accepted")
	}
	for _, b := range [][]byte{{0xff, 0, 0}, {0, 0x08, 0}, {0x40, 0, 0}, {0, 0xf0, 0}, {0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}} {
		if _, _, err := brotliz3.UnmarshalRecipe(b, 100); err == nil {
			t.Fatalf("%x accepted", b)
		}
	}
}

// Real streaming producers (testdata/streams, oracle/brotli/gen_stream_fixtures.py): python-brotli's Compressor with
// small chunks, flushes and an empty finish, Arrow's streaming compressor, an explicit block size, a stream that wraps
// the ring. Each must be explained by a recipe that regenerates its bytes exactly.
func TestRealStreamingProducers(t *testing.T) {
	raw, err := os.ReadFile("testdata/streams/MANIFEST.json")
	if err != nil {
		t.Fatal(err)
	}
	var man []struct {
		File, Producer, Pattern string
		Params                  map[string]int
		Sha256                  string
		Decoded_size            int
		Decoded_sha256          string
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	for _, m := range man {
		pay, err := os.ReadFile("testdata/streams/" + m.File)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(pay)) != m.Sha256 {
			t.Fatalf("%s: fixture damaged: %v", m.File, err)
		}
		body, tr, err := brotliz3.DecodeTrace(pay, m.Decoded_size)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != m.Decoded_sha256 {
			t.Fatalf("%s: decode: %v", m.File, err)
		}
		r, ok := brotliz3.Derive(pay, body, tr, 0)
		if !ok {
			t.Errorf("%s (%s, %s): no recipe", m.File, m.Producer, m.Pattern)
			continue
		}
		if r.Quality != m.Params["quality"] || r.LgWin != m.Params["lgwin"] {
			t.Errorf("%s: recipe %+v disagrees with the producer parameters %v", m.File, r, m.Params)
		}
		t.Logf("%-24s q%d w%d lgblock %d bigHint %v legacy %v emptyLast %v flushes %d recipe %d bytes", m.File, r.Quality, r.LgWin, r.LgBlock, r.BigHint, r.Legacy, r.EmptyLast, len(r.Flushes), len(r.Marshal()))
	}
}
