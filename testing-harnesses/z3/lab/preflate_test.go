package lab

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGzipHeaderLen(t *testing.T) {
	mk := func(h gzip.Header) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Header = h
		w.Write([]byte("payload payload payload"))
		w.Close()
		return b.Bytes()
	}
	cases := []struct {
		name string
		h    gzip.Header
		want int
	}{
		{"plain", gzip.Header{}, 10},
		{"name", gzip.Header{Name: "a.txt"}, 10 + 6},
		{"name+comment", gzip.Header{Name: "a", Comment: "bc"}, 10 + 2 + 3},
		{"extra", gzip.Header{Extra: []byte{1, 2, 3, 4, 5}}, 10 + 2 + 5},
		{"all", gzip.Header{Name: "n", Comment: "c", Extra: []byte{9}}, 10 + 3 + 2 + 2},
	}
	for _, c := range cases {
		b := mk(c.h)
		got, err := gzipHeaderLen(b)
		if err != nil || got != c.want {
			t.Errorf("%s: got %d,%v want %d", c.name, got, err, c.want)
		}
		// everything between header and the 8-byte trailer must be a DEFLATE stream
		if got+8 >= len(b) {
			t.Errorf("%s: no payload", c.name)
		}
	}
	// FHCRC: flag 2 adds two bytes
	b := mk(gzip.Header{})
	b[3] |= 2
	if n, err := gzipHeaderLen(b); err != nil || n != 12 {
		t.Errorf("fhcrc: %d,%v", n, err)
	}
	for _, bad := range [][]byte{nil, {0x1f, 0x8b}, append([]byte{0x1f, 0x8b, 8, 8}, make([]byte, 6)...), {0x1f, 0x8b, 9, 0, 0, 0, 0, 0, 0, 0}} {
		if _, err := gzipHeaderLen(bad); err == nil {
			t.Errorf("accepted %x", bad)
		}
	}
}

func TestFailClass(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		"InvalidDeflate: Invalid Huffman x":  "read-deflate",
		"PredictionFailure: no match":        "PredictionFailure",
		"NoCompressionCandidates: max_chain": "no-candidate-params",
	} {
		if got := FailClass(in); got != want {
			t.Errorf("FailClass(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPreflateMissingIsNotApplicable(t *testing.T) {
	t.Setenv(EnvPreflate, filepath.Join(t.TempDir(), "absent"))
	t.Setenv("Z3_CACHE", t.TempDir())
	if _, ok := PreflateBinary(); ok {
		t.Fatal("binary found in empty cache")
	}
	g := genFamilies(t, "gzip-L6-insert-mid")["gzip-L6-insert-mid"]
	_, err := analyzePreflate(&Input{Family: "x", Format: "gzip", Src: g[0].src, Opts: DefaultOptions()})
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("got %v, want ErrNotApplicable", err)
	}
}

// fakeOracle writes a shell stand-in for z3preflate. identity: analyze keeps the
// stream as both plaintext and reconstruction blob, recreate returns the blob.
// A broken oracle returns wrong bytes from recreate.
func fakeOracle(t *testing.T, broken bool) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	recreate := `cp "$3" "$4"`
	if broken {
		recreate = `printf bad > "$4"`
	}
	p := filepath.Join(t.TempDir(), "z3preflate")
	script := "#!/bin/sh\ncase \"$1\" in\nanalyze) cp \"$2\" \"$3\"; cp \"$2\" \"$4\"; n=$(wc -c < \"$2\" | tr -d ' ');\n" +
		"printf '{\"ok\":true,\"consumed\":%s,\"plain_len\":%s,\"recon_len\":%s,\"ns\":1,\"params\":\"fake\"}\\n' $n $n $n;;\n" +
		"recreate) " + recreate + "; printf '{\"ok\":true,\"out_len\":0,\"ns\":1}\\n';;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func preflateRows(t *testing.T, bin string, reps []string, fams ...string) []Row {
	t.Helper()
	t.Setenv(EnvPreflate, bin)
	cfg := testCfg(tinyCorpus(t, fams...))
	cfg.Reps = reps
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return rep.Rows
}

func TestPreflatePlumbingWithIdentityOracle(t *testing.T) {
	rows := preflateRows(t, fakeOracle(t, false), []string{"preflate", "preflate-rcdc"},
		"gzip-L6-insert-mid", "deflate-L6-insert-mid", "zip-deflate-change")
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		if r.Status != "ok" || r.RoundtripExact != "yes" {
			t.Errorf("%s %s %s: %s %s %s", r.Family, r.Revision, r.Rep, r.Status, r.RoundtripExact, r.Error)
		}
		if asInt(r.Detail["streams"]) == 0 || r.Detail["streams"] != r.Detail["streams_ok"] {
			t.Errorf("%s %s: streams %v ok %v", r.Family, r.Revision, r.Detail["streams"], r.Detail["streams_ok"])
		}
		if r.Format == "gzip" && asInt(r.Detail["wrapper_bytes"]) != 18 {
			t.Errorf("gzip wrapper bytes %v, want 18", r.Detail["wrapper_bytes"])
		}
	}
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	}
	return 0
}

func TestPreflateWrongReconstructionIsInvalid(t *testing.T) {
	rows := preflateRows(t, fakeOracle(t, true), []string{"preflate"}, "deflate-L6-insert-mid")
	for _, r := range rows {
		if r.Status != "invalid" || r.RoundtripExact != "NO" {
			t.Errorf("%s %s: status %s exact %s, want invalid/NO", r.Family, r.Revision, r.Status, r.RoundtripExact)
		}
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
}

// TestPreflateRealOracle runs the real preflate-rs wrapper when it has been built.
func TestPreflateRealOracle(t *testing.T) {
	bin, ok := PreflateBinary()
	if !ok {
		t.Skip("preflate oracle not built (oracle/preflate/setup.sh)")
	}
	rows := preflateRows(t, bin, []string{"preflate", "preflate-rcdc"},
		"deflate-L6-insert-mid", "deflate-L0-insert-mid", "deflate-fixed-insert-mid", "deflate-huffonly-insert-mid",
		"gzip-L9-insert-mid", "zip-deflate-change")
	supported := 0
	for _, r := range rows {
		if r.Status != "ok" || r.RoundtripExact != "yes" {
			t.Errorf("%s %s %s: %s %s %s", r.Family, r.Revision, r.Rep, r.Status, r.RoundtripExact, r.Error)
		}
		if r.Family == "deflate-huffonly-insert-mid" {
			if asInt(r.Detail["streams_ok"]) != 0 || r.Detail["fail_classes"] == nil {
				t.Errorf("huffman-only stream expected to be unsupported by preflate-rs: %v", r.Detail)
			}
			continue
		}
		if r.Detail["streams"] != r.Detail["streams_ok"] {
			t.Errorf("%s %s: unsupported stream: %v", r.Family, r.Revision, r.Detail["fail_classes"])
		}
		supported++
	}
	if supported == 0 {
		t.Fatal("no supported rows")
	}
}
