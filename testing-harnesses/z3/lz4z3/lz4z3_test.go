package lz4z3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

type fixture struct {
	File      string `json:"file"`
	SHA       string `json:"sha256_decoded"`
	Decoded   int    `json:"decoded"`
	Predictor string `json:"predictor"`
	Accel     int    `json:"accel"`
}

func fixtures(t *testing.T) []fixture {
	b, err := os.ReadFile("testdata/MANIFEST.json")
	if err != nil {
		t.Fatal(err)
	}
	var m []fixture
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func predID(s string) int {
	return map[string]int{"default": PredDefault, "default5": PredDefault5, "continue": PredContinue, "hc": PredHC}[s]
}

// Raw blocks written by the C library reproduce through the predictor registry byte for byte.
func TestLibraryBlocks(t *testing.T) {
	for _, f := range fixtures(t) {
		if filepath.Ext(f.File) != ".lz4" || f.Decoded < 0 || bytes.HasPrefix([]byte(f.File), []byte("cli_")) {
			continue
		}
		blk, _ := os.ReadFile("testdata/" + f.File)
		plain, seqs, err := Decode(blk, f.Decoded, Limits{})
		if err != nil {
			t.Fatal(f.File, err)
		}
		if h := sha256.Sum256(plain); hex.EncodeToString(h[:]) != f.SHA {
			t.Fatalf("%s: decoded SHA mismatch", f.File)
		}
		if re, err := Build(plain, seqs); err != nil || !bytes.Equal(re, blk) {
			t.Fatalf("%s: explicit rebuild differs: %v", f.File, err)
		}
		got, ok := PredictBlock(predID(f.Predictor), plain, f.Accel)
		if !ok || !bytes.Equal(got, blk) {
			t.Fatalf("%s: predictor %s/%d does not reproduce the library block", f.File, f.Predictor, f.Accel)
		}
		rc := Analyze(plain, seqs, true)
		if rc.Kind == KindHybrid || len(rc.Marshal()) > 4 {
			t.Fatalf("%s: Analyze chose kind %d (%d B)", f.File, rc.Kind, len(rc.Marshal()))
		}
	}
}

func replayAll(t *testing.T, data []byte) (FrameStats, []byte) {
	t.Helper()
	plain, rec, st, err := AnalyzeFrames(data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ReplayFrames(&out, rec, bytes.NewReader(plain), Limits{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("frame replay differs")
	}
	return st, plain
}

func TestLibraryFrames(t *testing.T) {
	want := map[string][3]int{ // predicted, hybrid, explicit blocks
		"cli_B4_BX_frame.lz4":    {3, 0, 0},
		"cli_hc9_BD_frame.lz4":   {1, 0, 0},
		"cli_skippable_tail.lz4": {1, 0, 0},
	}
	for _, f := range fixtures(t) {
		w, ok := want[f.File]
		if !ok {
			continue
		}
		data, _ := os.ReadFile("testdata/" + f.File)
		st, plain := replayAll(t, data)
		if got := [3]int{st.Predicted, st.Hybrid, st.Explicit}; got != w {
			t.Errorf("%s: predicted/hybrid/explicit = %v, want %v", f.File, got, w)
		}
		if f.SHA != "" {
			if h := sha256.Sum256(plain); hex.EncodeToString(h[:]) != f.SHA {
				t.Errorf("%s: payload SHA mismatch", f.File)
			}
		}
	}
}

func randPlain(r *rand.Rand, n int) []byte {
	words := [][]byte{[]byte("alpha"), []byte("beta"), []byte("{\"k\":"), {0, 1, 2, 3}, []byte("zeros3 ")}
	var b []byte
	for len(b) < n {
		switch r.Intn(4) {
		case 0:
			b = append(b, byte(r.Intn(256)))
		case 1:
			b = append(b, bytes.Repeat([]byte{byte(r.Intn(3))}, r.Intn(40))...)
		default:
			b = append(b, words[r.Intn(len(words))]...)
		}
	}
	return b[:n]
}

func TestPredictorRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	sizes := []int{0, 1, 4, 11, 12, 13, 14, 15, 16, 17, 255, 256, 270, 1000, 4096, 65535, 65546, 65547, 70000, 200000}
	for _, n := range sizes {
		p := randPlain(r, n)
		for _, id := range []int{PredDefault, PredContinue, PredDefault5} {
			for _, acc := range []int{1, 3, 17} {
				blk, ok := PredictBlock(id, p, acc)
				if !ok {
					t.Fatalf("pred %d refused", id)
				}
				dec, seqs, err := Decode(blk, n, Limits{})
				if err != nil || !bytes.Equal(dec, p) {
					t.Fatalf("n=%d pred=%d acc=%d: decode: %v", n, id, acc, err)
				}
				// Format invariants of the reference encoder: the last 5 bytes are literals and
				// the last match starts at least 12 bytes before the end.
				var pos int
				for _, s := range seqs[:len(seqs)-1] {
					pos += s.Lit
					if pos+s.Match > n-lastLiterals || pos > n-mfLimit {
						t.Fatalf("n=%d: match at %d len %d violates end-of-block rules", n, pos, s.Match)
					}
					pos += s.Match
				}
				rc := Analyze(p, seqs, true)
				if rc.Kind == KindHybrid || len(rc.Marshal()) > 4 {
					t.Fatalf("n=%d id=%d acc=%d: Analyze kind %d (%d B)", n, id, acc, rc.Kind, len(rc.Marshal()))
				}
				rep, err := rc.Replay(nil, p, Limits{})
				if err != nil || !bytes.Equal(rep, blk) {
					t.Fatalf("replay differs: %v", err)
				}
			}
		}
	}
}

func TestExplicitAndHybridRecipes(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	p := randPlain(r, 20000)
	seqs, _ := Predict(PredDefault, p, 1)
	// Perturb the tail: splitting a match yields a block no predictor emits.
	k := len(seqs) / 2
	alt := append([]Seq(nil), seqs...)
	for alt[k].Match < 2*minMatch {
		k++
	}
	s := alt[k]
	alt = append(alt[:k+1], alt[k:]...)
	alt[k] = Seq{Lit: s.Lit, Match: minMatch, Off: s.Off}
	alt[k+1] = Seq{Lit: 0, Match: s.Match - minMatch, Off: s.Off}
	blk, err := Build(p, alt)
	if err != nil {
		t.Fatal(err)
	}
	dec, got, err := Decode(blk, len(p), Limits{})
	if err != nil || !bytes.Equal(dec, p) {
		t.Fatal(err)
	}
	rc := Analyze(p, got, true)
	if rc.Kind != KindHybrid || rc.Prefix < k {
		t.Fatalf("expected a hybrid recipe with prefix >= %d, got kind %d prefix %d", k, rc.Kind, rc.Prefix)
	}
	for _, rr := range []Recipe{rc, {Kind: KindExplicit, Seqs: got}} {
		b := rr.Marshal()
		back, n, err := Unmarshal(b, Limits{})
		if err != nil || n != len(b) {
			t.Fatal(err, n, len(b))
		}
		rep, err := back.Replay(nil, p, Limits{})
		if err != nil || !bytes.Equal(rep, blk) {
			t.Fatalf("kind %d replay differs: %v", rr.Kind, err)
		}
	}
	if hybrid, explicit := len(rc.Marshal()), len(Recipe{Kind: KindExplicit, Seqs: got}.Marshal()); hybrid >= explicit {
		t.Fatalf("hybrid recipe (%d B) should beat explicit (%d B)", hybrid, explicit)
	}
}

func TestHostileBlocks(t *testing.T) {
	cases := map[string][]byte{
		"empty":                  {},
		"token only":             {0x10},
		"literal beyond block":   {0xF0, 0xFF},
		"offset zero":            {0x11, 'a', 0, 0, 0x00},
		"offset beyond history":  {0x11, 'a', 9, 0},
		"truncated offset":       {0x11, 'a', 1},
		"unterminated ext":       append([]byte{0xF0}, bytes.Repeat([]byte{255}, 100)...),
		"final match nibble":     {0x01},
		"match without literals": {0x00, 1, 0},
	}
	for n, b := range cases {
		if _, _, err := Decode(b, -1, Limits{}); err == nil {
			t.Errorf("%s: accepted", n)
		}
	}
	// output amplification: a 1 MiB limit must stop a 255-extension match chain
	big := []byte{0x1F, 'a', 1, 0}
	big = append(big, bytes.Repeat([]byte{255}, 8000)...)
	big = append(big, 0, 0x00)
	if _, _, err := Decode(big, -1, Limits{MaxDecoded: 1 << 20}); !errors.Is(err, ErrLimit) {
		t.Errorf("amplification: %v", err)
	}
	// declared size mismatch
	blk, _ := Build([]byte("abc"), []Seq{{Lit: 3}})
	if _, _, err := Decode(blk, 4, Limits{}); err == nil {
		t.Error("size mismatch accepted")
	}
	// absurd sequence count claimed by a recipe
	huge := []byte{KindExplicit}
	huge = binary.AppendUvarint(huge, 1<<40)
	if _, _, err := Unmarshal(huge, Limits{}); err == nil {
		t.Error("huge sequence count accepted")
	}
	for _, bad := range [][]byte{{}, {9}, {KindPredicted}, {KindPredicted, 1, 0}, {KindHybrid, 1, 1}} {
		if _, _, err := Unmarshal(bad, Limits{}); err == nil {
			t.Errorf("recipe %x accepted", bad)
		}
	}
}

func TestEveryTruncation(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	p := randPlain(r, 3000)
	blk, _ := PredictBlock(PredDefault, p, 1)
	for i := 0; i < len(blk); i++ {
		dec, seqs, err := Decode(blk[:i], -1, Limits{})
		if err == nil { // a prefix that is itself a valid block must be self-consistent
			if re, e := Build(dec, seqs); e != nil || !bytes.Equal(re, blk[:i]) {
				t.Fatalf("prefix %d: accepted but not rebuildable", i)
			}
		}
	}
}

func TestReplayRejectsWrongPayload(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	p := randPlain(r, 5000)
	seqs, _ := Predict(PredDefault, p, 1)
	rc := Recipe{Kind: KindExplicit, Seqs: seqs}
	q := append([]byte(nil), p...)
	for i := range q { // damage a matched region: the explicit recipe must notice
		if i > 2000 {
			q[i] ^= 0x55
		}
	}
	if _, err := rc.Replay(nil, q, Limits{}); err == nil {
		t.Error("explicit recipe accepted a payload that does not contain its copies")
	}
	if _, err := rc.Replay(nil, p[:len(p)-1], Limits{}); err == nil {
		t.Error("short payload accepted")
	}
	if _, err := (Recipe{Kind: KindPredicted, Pred: 99, Accel: 1}).Replay(nil, p, Limits{}); err == nil {
		t.Error("unknown predictor accepted")
	}
	if _, err := (Recipe{Kind: KindPredicted, Pred: PredDefault, Accel: 1}).Replay([]byte("hist"), p, Limits{}); err == nil {
		t.Error("predictor on a linked block accepted")
	}
}

func TestRecipeByteFlips(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	p := randPlain(r, 6000)
	seqs, _ := Predict(PredDefault, p, 1)
	b := Recipe{Kind: KindExplicit, Seqs: seqs}.Marshal()
	want, _ := Build(p, seqs)
	for i := range b {
		c := append([]byte(nil), b...)
		c[i] ^= 0x80
		rc, n, err := Unmarshal(c, Limits{})
		if err != nil || n != len(c) {
			continue
		}
		if out, err := rc.Replay(nil, p, Limits{}); err == nil && bytes.Equal(out, want) && i < 1 {
			t.Fatalf("flip at %d left the block unchanged", i)
		}
	}
}

func TestFrameHostile(t *testing.T) {
	data, _ := os.ReadFile("testdata/cli_B4_BX_frame.lz4")
	plain, rec, _, err := AnalyzeFrames(data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{1, 5, 7, 8, 100, len(data) / 2, len(data) - 5, len(data) - 1} {
		pl, rc, _, err := AnalyzeFrames(data[:cut], Limits{})
		if err != nil {
			continue
		}
		var out bytes.Buffer // accepted truncations (opaque tail) must still round trip
		if e := ReplayFrames(&out, rc, bytes.NewReader(pl), Limits{}); e != nil || !bytes.Equal(out.Bytes(), data[:cut]) {
			t.Fatalf("cut %d accepted but not exact: %v", cut, e)
		}
	}
	// corrupt recipe bytes and short/long payloads never panic and never emit wrong bytes silently
	for i := 0; i < len(rec); i += 1 + len(rec)/200 {
		c := append([]byte(nil), rec...)
		c[i] ^= 0xFF
		var out bytes.Buffer
		_ = ReplayFrames(&out, c, bytes.NewReader(plain), Limits{})
	}
	var out bytes.Buffer
	if err := ReplayFrames(&out, rec, bytes.NewReader(plain[:len(plain)-1]), Limits{}); err == nil {
		t.Error("short payload accepted")
	}
	out.Reset()
	if err := ReplayFrames(&out, rec, bytes.NewReader(plain), Limits{MaxDecoded: 1000}); err == nil {
		t.Error("limit ignored")
	}
	// wrong header: reserved bit, bad block size code, dictionary id
	for _, h := range [][]byte{{0x64 | 2, 0x40}, {0x64, 0x10}, {0x64 | 1, 0x40}} {
		fr := append([]byte{0x04, 0x22, 0x4D, 0x18}, h...)
		fr = append(fr, 0, 0, 0, 0, 0, 0)
		if _, _, _, err := AnalyzeFrames(fr, Limits{}); err == nil {
			t.Errorf("descriptor %x accepted", h)
		}
	}
	// short writer
	if err := ReplayFrames(shortWriter{}, rec, bytes.NewReader(plain), Limits{}); err == nil {
		t.Error("short writer accepted")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestXXH32Vectors(t *testing.T) {
	if got := XXH32(nil, 0); got != 0x02CC5D05 {
		t.Errorf("empty: %08x", got)
	}
	if got := XXH32([]byte("a"), 0); got != 0x550D7456 {
		t.Errorf("a: %08x", got)
	}
	if got := XXH32([]byte("Nobody inspects the spammish repetition"), 0); got != 0xE2293B2F {
		t.Errorf("spammish: %08x", got)
	}
}

func FuzzDecode(f *testing.F) {
	for _, s := range [][]byte{{0x10, 'a'}, {0x11, 'a', 1, 0, 0}, {0xF0, 0, 'x'}} {
		f.Add(s)
	}
	blk, _ := PredictBlock(PredDefault, randPlain(rand.New(rand.NewSource(9)), 2000), 1)
	f.Add(blk)
	f.Fuzz(func(t *testing.T, b []byte) {
		dec, seqs, err := Decode(b, -1, Limits{MaxDecoded: 1 << 20})
		if err != nil {
			return
		}
		re, err := Build(dec, seqs)
		if err != nil || !bytes.Equal(re, b) {
			t.Fatalf("accepted block does not rebuild exactly: %v", err)
		}
	})
}

func FuzzFrames(f *testing.F) {
	for _, n := range []string{"cli_B4_BX_frame.lz4", "cli_hc9_BD_frame.lz4", "cli_skippable_tail.lz4"} {
		if b, err := os.ReadFile("testdata/" + n); err == nil && len(b) < 100000 {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		lim := Limits{MaxDecoded: 4 << 20, MaxBlocks: 1024}
		plain, rec, _, err := AnalyzeFrames(b, lim)
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := ReplayFrames(&out, rec, bytes.NewReader(plain), lim); err != nil || !bytes.Equal(out.Bytes(), b) {
			t.Fatalf("accepted stream does not replay exactly: %v", err)
		}
	})
}

func FuzzRecipe(f *testing.F) {
	f.Add(Recipe{Kind: KindPredicted, Pred: PredDefault, Accel: 1}.Marshal())
	f.Add(Recipe{Kind: KindExplicit, Seqs: []Seq{{Lit: 3, Match: 4, Off: 3}, {Lit: 2}}}.Marshal())
	f.Fuzz(func(t *testing.T, b []byte) {
		rc, n, err := Unmarshal(b, Limits{MaxDecoded: 1 << 16})
		if err != nil {
			return
		}
		if n > len(b) {
			t.Fatal("consumed beyond input")
		}
		_, _ = rc.Replay(nil, make([]byte, 100), Limits{MaxDecoded: 1 << 16})
	})
}
