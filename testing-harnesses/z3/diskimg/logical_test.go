package diskimg

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestFlattenLaterWins(t *testing.T) {
	d := &Disk{Size: 1 * MiB, Writes: []Extent{{0, 8192, KRandom, 1}, {4096, 4096, KZero, 0}, {6000, 100, KText, 2}}}
	var got bytes.Buffer
	if err := d.Stream(&got, d.Flatten()); err != nil {
		t.Fatal(err)
	}
	b := got.Bytes()
	if int64(len(b)) != d.Size {
		t.Fatal("size", len(b))
	}
	want := make([]byte, 8192)
	fill(want, KRandom, 1, 0)
	if !bytes.Equal(b[:4096], want[:4096]) {
		t.Fatal("head differs")
	}
	if !bytes.Equal(b[4096:6000], make([]byte, 1904)) || bytes.Equal(b[6000:6100], make([]byte, 100)) || !bytes.Equal(b[6100:8192], make([]byte, 2092)) {
		t.Fatal("overlay wrong")
	}
}

func TestRawEqualsOracleStream(t *testing.T) {
	d := &Disk{Size: 24 * MiB, Writes: []Extent{{0, MiB, KMeta, 1}, {3 * MiB, 2 * MiB, KText, 2}, {8 * MiB, 3 * MiB, KDup, 3}, {10 * MiB, 4096, KZero, 0}, {20 * MiB, 8192, KPage, 4}}}
	d.Writes = append(d.Writes, Extent{300 * 1024, 4096, KRandom, 99})
	p := filepath.Join(t.TempDir(), "d.raw")
	if err := d.WriteRaw(p); err != nil {
		t.Fatal(err)
	}
	f, _ := os.ReadFile(p)
	want, err := d.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(f) != want {
		t.Fatal("raw file differs from generator stream")
	}
}

func TestFillIsOffsetPure(t *testing.T) {
	for _, k := range []Kind{KRandom, KText, KMeta, KStruct, KDup, KPage} {
		whole := make([]byte, 3*MiB+777)
		fill(whole, k, 5, 12345)
		part := make([]byte, 5000)
		fill(part, k, 5, 12345+999999)
		if !bytes.Equal(part, whole[999999:999999+5000]) {
			t.Fatalf("kind %d not offset-pure", k)
		}
	}
}
