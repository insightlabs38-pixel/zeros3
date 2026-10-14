package eraez3

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestSnappyRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 14, 15, 16, 100, 700, 5000, 65535, 65536, 70000, 200000} {
		for kind := 0; kind < 3; kind++ {
			b := make([]byte, n)
			for i := range b {
				switch kind {
				case 0:
					b[i] = byte(rng.Uint32())
				case 1:
					b[i] = byte(i % 7)
				default:
					b[i] = byte(rng.Uint32() & 3)
				}
			}
			for name, enc := range map[string][]byte{"ref": FrameEncodeRef(b), "raw": FrameEncodeRaw(b)} {
				got, err := FrameDecode(enc)
				if err != nil || !bytes.Equal(got, b) {
					t.Fatalf("%s n=%d kind=%d: %v", name, n, kind, err)
				}
			}
		}
	}
}

func TestSnappyRejects(t *testing.T) {
	good := FrameEncodeRef(bytes.Repeat([]byte("abcdefgh"), 100))
	for name, b := range map[string][]byte{
		"empty":    nil,
		"no-id":    good[10:],
		"trunc":    good[:len(good)-3],
		"badcrc":   append(append([]byte(nil), good[:14]...), append([]byte{good[14] ^ 1}, good[15:]...)...),
		"badchunk": append(append([]byte(nil), good...), 0x42, 4, 0, 0, 0, 0, 0, 0),
	} {
		if _, err := FrameDecode(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestRealSnappyReproduced: on a real EraE file every framed entry must be
// reproduced by the reference encoder port.
func TestRealSnappyReproduced(t *testing.T) {
	f := realFile(t, "sepolia-00000-8e3e7dc9.erae")
	ents, err := Parse(bytes.NewReader(f), int64(len(f)), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if e.Type != TypeHeader && e.Type != TypeBody && e.Type != TypeSlimReceipts {
			continue
		}
		d := f[e.Off+8 : e.Off+8+int64(e.Len)]
		plain, err := FrameDecode(d)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(FrameEncodeRef(plain), d) {
			t.Fatalf("entry at %d (type %d) not reproduced", e.Off, e.Type)
		}
		n++
	}
	t.Logf("%d framed entries reproduced byte for byte", n)
}

func cacheDir() string {
	if d := os.Getenv("Z3_ERAE_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "erae")
}

func realFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cacheDir(), name))
	if err != nil {
		t.Skipf("real sample %s not fetched: %v", name, err)
	}
	return b
}
