package kinds

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"zeros3-z3/repz3"
)

func zstdFixtures(t *testing.T) map[string][]byte {
	man, err := os.ReadFile("../../zstdz3/testdata/MANIFEST")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, line := range strings.Split(string(man), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		name := strings.Split(line, "\t")[0]
		b, err := os.ReadFile(filepath.Join("../../zstdz3/testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = b
	}
	return out
}

// TestZstdKind proves each committed stream through the serialized representation only,
// with a plain CDC child: exact length, SHA-256 and bytes, and the GC gate.
func TestZstdKind(t *testing.T) {
	for name, zst := range zstdFixtures(t) {
		b, st, err := EncodeZstd(zst, CDC)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := Prove(nil, b, zst); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Stats.Blocks[3] != 0 {
			t.Errorf("%s: %d fallback blocks", name, st.Stats.Blocks[3])
		}
	}
}

// TestZstdKindRejects: a missing or tampered unit, skeleton component or plaintext chunk
// fails reconstruction rather than producing other bytes.
func TestZstdKindRejects(t *testing.T) {
	zst := zstdFixtures(t)["ref157_json.zst"]
	b, _, err := EncodeZstd(zst, CDC)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, err := b.Persist(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rebuild(st, id); err != nil {
		t.Fatal(err)
	}
	for _, victim := range st.IDs() {
		if victim == id {
			continue
		}
		missing := st.Clone()
		missing.Delete(victim)
		if _, err := rebuild(missing, id); err == nil {
			t.Errorf("reconstruction succeeded without component %x", victim[:6])
		}
		tampered := st.Clone()
		data := append([]byte(nil), tampered.Bytes(victim)...)
		data[len(data)/2] ^= 0x20
		tampered.Set(victim, data)
		if got, err := rebuild(tampered, id); err == nil && bytes.Equal(got, zst) {
			t.Errorf("tampered component %x went unnoticed", victim[:6])
		} else if err == nil {
			t.Errorf("tampered component %x reconstructed different bytes without error", victim[:6])
		} else if errors.Is(err, errZstd) {
			t.Errorf("unexpected error class: %v", err)
		}
	}
	reach, err := NewEnv(st).Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range st.IDs() {
		if _, ok := reach.Role[other]; !ok {
			t.Errorf("object %x not reachable", other[:6])
		}
	}
	_ = sha256.Sum256
}

// TestZstdOverTar composes zstd -> tar layer (DERIVED child) when a reference zstd is
// installed: exact compression normalization plus container structure.
func TestZstdOverTar(t *testing.T) {
	bin := "/root/z3cache/tools/zstd-1.5.7/zstd"
	if v := os.Getenv("Z3_TOOLS"); v != "" {
		bin = v + "/tools/zstd-1.5.7/zstd"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("reference zstd not installed")
	}
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for i := 0; i < 40; i++ {
		body := bytes.Repeat([]byte("layer file content line\n"), 30+i)
		tw.WriteHeader(&tar.Header{Name: "f" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Size: int64(len(body)), Mode: 0o644, Format: tar.FormatPAX})
		tw.Write(body)
	}
	tw.Close()
	cmd := exec.Command(bin, "-3", "-T1", "-q", "-c")
	cmd.Stdin = bytes.NewReader(tb.Bytes())
	zst, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := EncodeZstd(zst, func(plain []byte) (*Bundle, error) {
		bb, _, err := EncodeTarLayer(plain, 1<<62)
		return bb, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Children) != 1 || b.Children[0].Rep.Kind != repz3.KindConcat {
		t.Fatalf("zstd -> tar chain not formed")
	}
	if err := Prove(nil, b, zst); err != nil {
		t.Fatal(err)
	}
}
