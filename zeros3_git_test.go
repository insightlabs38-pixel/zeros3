package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func repGitFixtures(t testing.TB) []repLabCodecFixture {
	t.Helper()
	raw, err := os.ReadFile("testing-harnesses/testdata/i1c-git/lab-git.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestRepGitLabWireAndReaderAt(t *testing.T) {
	fixtures := repGitFixtures(t)
	if len(fixtures) != 8 {
		t.Fatalf("fixture count %d", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			// Pack bundles have no derived children, so their entire body is identical
			// after the pre-existing five-byte lab/production envelope difference.
			if !bytes.Equal(f.Root[5:], f.LabRoot[5:]) {
				t.Fatal("lab pack representation body changed")
			}
			src := newRepMem()
			for text, object := range f.Objects {
				want, err := decodeHexSHA256(text)
				if err != nil {
					t.Fatal(err)
				}
				if src.put(object) != want {
					t.Fatal("fixture component digest differs")
				}
			}
			root := src.put(f.Root)
			env := &repEnv{Src: src, Limits: repProdLimits}
			var out bytes.Buffer
			n, sum, err := env.reconstruct(root, &out)
			if err != nil || n != int64(len(f.Input)) || sum != sha256.Sum256(f.Input) || !bytes.Equal(out.Bytes(), f.Input) {
				t.Fatalf("exact pack replay: %v", err)
			}
			algo := repGTSHA1
			if strings.HasPrefix(f.Name, "sha256") {
				algo = repGTSHA256
			}
			p, err := repGTParsePackAt(bytes.NewReader(f.Input), int64(len(f.Input)), algo, repGTLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Resolve(); err != nil {
				t.Fatal(err)
			}
			if p.Version != binary.BigEndian.Uint32(f.Input[4:8]) {
				t.Fatal("pack version changed")
			}
			call, err := env.open(root)
			if err != nil {
				t.Fatal(err)
			}
			if call.Rep.Kind != repKindGit {
				t.Fatal("Git number changed")
			}
			index := call.Rep.Indexes[0]
			if index.KeyWidth != algo.Size() || int(index.Count) != len(p.Entries) {
				t.Fatal("native index shape differs")
			}
			for _, entry := range p.Entries {
				if _, found, err := call.resolve(entry.ID); err != nil || !found {
					t.Fatalf("native lookup failed: %v", err)
				}
			}
			reach, err := env.reachable(root)
			if err != nil {
				t.Fatal(err)
			}
			for id := range src.seen {
				if _, ok := reach.Role[id]; !ok {
					t.Fatalf("unprotected read %x", id)
				}
			}
		})
	}
}
