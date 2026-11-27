package lzl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"testing"
)

const goldenBody = "ZeroS3 range record\x00zero zero zero zero\n"

func golden(t testing.TB) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/record-v1.hex")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecordV1Golden(t *testing.T) {
	data := []byte(goldenBody)
	digest := sha256.Sum256(data)
	record := golden(t)
	encoder := NewEncoder(Params{Mode: ModeFast})
	coded, err := encoder.EncodeRecord(nil, data)
	if err != nil || !bytes.Equal(coded, record) {
		t.Fatalf("golden mismatch: %x %v", coded, err)
	}
	var decoder Decoder
	got, err := decoder.DecodeRecord(nil, record, len(data), digest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("independent decoder fixture failed: %q %v", got, err)
	}
	for i := range len(record) {
		if _, err := decoder.DecodeRecord(nil, record[:i], len(data), digest); err == nil {
			t.Fatalf("accepted truncation at byte %d", i)
		}
		bad := bytes.Clone(record)
		bad[i] ^= 0x20
		if _, err := decoder.DecodeRecord(nil, bad, len(data), digest); err == nil {
			t.Fatalf("accepted corruption at byte %d", i)
		}
	}
	for _, size := range []int{-1, 0, MaxRecordBytes + 1, len(data) - 1, len(data) + 1} {
		if _, err := decoder.DecodeRecord(nil, record, size, digest); err == nil {
			t.Fatalf("accepted wrong size %d", size)
		}
	}
	if _, err := decoder.DecodeRecord(nil, record, len(data), [32]byte{}); err == nil {
		t.Fatal("accepted an unverified record digest")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(811, 29))
	data := make([]byte, MaxRecordBytes)
	for i := range data {
		if i > 1024 && rng.IntN(4) != 0 {
			data[i] = data[i-1-rng.IntN(1024)]
		} else {
			data[i] = byte(rng.IntN(19))
		}
	}
	for _, p := range []Params{{Mode: ModeFast}, {Mode: ModePrice}, {Mode: ModeOpt}} {
		encoder := NewEncoder(p)
		var decoder Decoder
		var dst []byte
		for _, sample := range [][]byte{[]byte("x"), {0, 0, 0}, bytes.Repeat([]byte("overlapping copy"), 10000), data} {
			sum := sha256.Sum256(sample)
			coded, err := encoder.EncodeRecord(nil, sample)
			if err != nil {
				t.Fatal(err)
			}
			dst, err = decoder.DecodeRecord(dst[:0], coded, len(sample), sum)
			if err != nil || !bytes.Equal(dst, sample) {
				t.Fatalf("mode %d length %d failed: %v", p.Mode, len(sample), err)
			}
		}
	}
}

func FuzzRangePayload(f *testing.F) {
	f.Add(golden(f)[5:], uint16(len(goldenBody)))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, uint16(128))
	f.Add([]byte{}, uint16(1))
	f.Fuzz(func(t *testing.T, body []byte, logical uint16) {
		n := int(logical) % 4096
		var decoder Decoder
		out, err := decoder.Decode(nil, body, n)
		if err == nil && len(out) != n {
			t.Fatal("decoder crossed its output bounds")
		}
	})
}
