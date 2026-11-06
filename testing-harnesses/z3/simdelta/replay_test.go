package simdelta

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestPinnedRealCLRSerializedReplay(t *testing.T) {
	f, e := os.Open("../../z4/b1/clr-replay.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	var fixture struct {
		Roots     []string
		SourceSHA string `json:"source_sha"`
		Size      int
		Objects   map[string][]byte
	}
	if e = json.NewDecoder(gz).Decode(&fixture); e != nil {
		t.Fatal(e)
	}
	// Independently pinned Newtonsoft.Json 13.0.4 from the A4/public inventory.
	const expected = "12246aa5e14245cbd1a0b8794851defd46e6d782c60eb650ba5aa90ef8447eee"
	if fixture.SourceSHA != expected {
		t.Fatal("source pin drift")
	}
	get := func(h [32]byte) ([]byte, error) {
		w, ok := fixture.Objects[hex.EncodeToString(h[:])]
		if !ok {
			return nil, fmt.Errorf("missing")
		}
		return w, nil
	}
	hs := sha256.New()
	n := 0
	for _, s := range fixture.Roots {
		raw, e := hex.DecodeString(s)
		if e != nil || len(raw) != 32 {
			t.Fatal("root")
		}
		var h [32]byte
		copy(h[:], raw)
		b, e := Resolve(h, get, 4, 1<<30)
		if e != nil {
			t.Fatal(e)
		}
		hs.Write(b)
		n += len(b)
	}
	if hex.EncodeToString(hs.Sum(nil)) != expected || n != fixture.Size {
		t.Fatal("native exact reconstruction")
	}
}
