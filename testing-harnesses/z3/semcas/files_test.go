package semcas

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fileCase struct {
	name, kind string
}

func ingestFile(t *testing.T, fp *FilePlanner, kind, name string) []byte {
	t.Helper()
	data := fixture(t, name)
	var rc *Recipe
	var err error
	switch kind {
	case "tar":
		rc, err = fp.IngestTar(name, data)
	case "pack":
		rc, err = fp.IngestGitPack(name, data)
	case "arrow":
		rc, _, err = fp.IngestArrow(name, data)
	case "parquet":
		rc, _, err = fp.IngestParquet(name, data)
	case "pqx":
		rc, _, err = fp.IngestParquetExact(name, data)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := fp.S.PutRecipe(rc); err != nil {
		t.Fatal(err)
	}
	return data
}

// tar, gzip layer and Git pack share file contents; a fresh open replays every original.
func TestContainersShareFileContent(t *testing.T) {
	s := newStore(t)
	fp := NewFilePlanner(s)
	files := map[string][]byte{}
	for _, c := range []fileCase{{"tiny.tar", "tar"}, {"tiny.tar.gz", "tar"}, {"tiny.pack", "pack"}} {
		files[c.name] = ingestFile(t, fp, c.kind, c.name)
	}
	if fp.Exact["gzip-replay"] != 1 || fp.Exact["git-entry-replay"] == 0 {
		t.Fatalf("exact codec replay not used: %v", fp.Exact)
	}
	// every tar member content is also a Git blob: its content comp exists exactly once
	n := 0
	for _, r := range s.All() {
		if r.Desc.Kind == KindFile {
			n++
		}
	}
	if n != 6 {
		t.Fatalf("expected 6 shared file-content components, got %d", n)
	}
	replayAll(t, dirOfM.get(s), files)
}

func TestColumnarFormatsShareColumns(t *testing.T) {
	s := newStore(t)
	fp := NewFilePlanner(s)
	files := map[string][]byte{}
	files["tiny.arrow"] = ingestFile(t, fp, "arrow", "tiny.arrow")
	files["tiny_plain.parquet"] = ingestFile(t, fp, "parquet", "tiny_plain.parquet")
	files["tiny_snappy.parquet"] = ingestFile(t, fp, "pqx", "tiny_snappy.parquet")
	cols := 0
	for _, r := range s.All() {
		if r.Desc.Kind == KindColumn && !r.Derived() {
			cols++
		}
	}
	if cols != 3 {
		t.Fatalf("three canonical columns expected (int64, float64, string stream), got %d", cols)
	}
	replayAll(t, dirOfM.get(s), files)
	// signedness: an unsigned view of the same bytes is a different semantic component
	a := s.All()
	var i64 *Record
	for _, r := range a {
		if r.Desc.Kind == KindColumn && r.Desc.DType == DI64 {
			i64 = r
		}
	}
	dU := colDesc(DU64, int(i64.PayloadLen/8))
	u, err := s.PutRaw(dU, mustPayload(t, s, i64.CID))
	if err != nil || u.CID == i64.CID || u.Payload != i64.Payload {
		t.Fatal("u64 view must be distinct semantically yet share bytes")
	}
}

func mustPayload(t *testing.T, s *Store, c ID) []byte {
	t.Helper()
	b, err := s.Payload(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Corrupt or missing codec residuals and components fail closed, never yield wrong bytes.
func TestContainerResidualCorruptionFailsClosed(t *testing.T) {
	s := newStore(t)
	fp := NewFilePlanner(s)
	data := ingestFile(t, fp, "tar", "tiny.tar.gz")
	rc, _ := s.GetRecipe(PayloadSHA(data))
	var victim *Record
	for _, r := range s.All() {
		if r.Derived() && len(r.Ops) == 1 && r.Ops[0].Kind == OpDeflate {
			victim = r
		}
	}
	if victim == nil {
		t.Fatal("no deflate derivation")
	}
	path := filepath.Join(dirOfM.get(s), "comps", victim.CID.String())
	orig, _ := os.ReadFile(path)
	failed := 0
	for i := 8; i < len(orig); i += 3 {
		m := append([]byte(nil), orig...)
		m[i] ^= 0x55
		os.WriteFile(path, m, 0o644)
		s2, err := Open(dirOfM.get(s))
		if err != nil {
			failed++
			continue
		}
		var out bytes.Buffer
		if err := s2.Replay(rc, &out, 1<<20); err == nil && !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("flip at %d produced wrong bytes without an error", i)
		} else if err != nil {
			failed++
		}
	}
	os.WriteFile(path, orig, 0o644)
	if failed == 0 {
		t.Fatal("no corruption was detected at all")
	}
	// missing parent component
	s3, _ := Open(dirOfM.get(s))
	for _, r := range s3.All() {
		if r.Desc.Kind == KindFile {
			os.Remove(filepath.Join(dirOfM.get(s), "blobs", r.Payload.String()))
			break
		}
	}
	s4, _ := Open(dirOfM.get(s))
	if err := s4.Replay(rc, &bytes.Buffer{}, 1<<20); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing component: %v", err)
	}
}

// Unsupported inputs take the accounted opaque fallback and still replay exactly.
func TestUnsupportedFallbacks(t *testing.T) {
	s := newStore(t)
	fp := NewFilePlanner(s)
	garbage := rnd(70000, 9)
	rc, err := fp.IngestTar("junk.tar", garbage)
	if err != nil {
		t.Fatal(err)
	}
	s.PutRecipe(rc)
	if fp.Exact["tar-unsupported"] != 1 {
		t.Fatalf("fallback not counted: %v", fp.Exact)
	}
	if _, err := ParseArrowFile(garbage); err == nil {
		t.Fatal("garbage parsed as arrow")
	}
	replayAll(t, dirOfM.get(s), map[string][]byte{"junk": garbage})
}

func FuzzParseArrowFile(f *testing.F) {
	f.Add(fixture(f, "tiny.arrow"))
	f.Fuzz(func(t *testing.T, b []byte) { ParseArrowFile(b) })
}
