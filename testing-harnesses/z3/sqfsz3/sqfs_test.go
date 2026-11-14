package sqfsz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func load(t testing.TB, n string) []byte {
	b, err := os.ReadFile("testdata/" + n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func roundTrip(t testing.TB, img []byte) (Stats, []byte, []byte) {
	rec, plain, st, err := Encode(img, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Decode(&out, rec, plain, Limits{}); err != nil || !bytes.Equal(out.Bytes(), img) {
		t.Fatalf("decode differs: %v", err)
	}
	return st, rec, plain
}

func TestRealImages(t *testing.T) {
	for _, n := range []string{"small_lz4_b4k.sqsh", "small_lz4_b16k.sqsh"} {
		st, _, plain := roundTrip(t, load(t, n))
		if st.Blocks == 0 || st.Predicted != st.Blocks-st.Stored || st.Stored == 0 || len(plain) == 0 {
			t.Errorf("%s: %+v", n, st)
		}
	}
	// LZ4HC image (mksquashfs -Xhc = level 12): Z4-P7 ported the optimal parser, so every
	// compressed block is now a predictor recipe (B6 needed explicit/hybrid recipes here).
	st, _, _ := roundTrip(t, load(t, "small_lz4hc.sqsh"))
	if st.Explicit+st.Hybrid != 0 || st.Predicted != st.Blocks-st.Stored {
		t.Errorf("hc image should be fully predicted: %+v", st)
	}
	if _, _, _, err := Encode(load(t, "small_xz.sqsh"), Limits{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("xz image: %v", err)
	}
}

func TestHostileImages(t *testing.T) {
	img := load(t, "small_lz4_b4k.sqsh")
	for _, cut := range []int{0, 5, 95, 96, 200, 1000, len(img) / 2, len(img) - 4096, len(img) - 1} {
		if _, _, _, err := Encode(img[:cut], Limits{}); err == nil {
			// a truncation that still parses must round trip (the tail is verbatim)
			roundTrip(t, img[:cut])
		}
	}
	// every single-byte corruption of the superblock and table pointers: no panic, no wrong output
	for i := 0; i < 96; i++ {
		c := append([]byte(nil), img...)
		c[i] ^= 0xFF
		if rec, plain, _, err := Encode(c, Limits{}); err == nil {
			var out bytes.Buffer
			if err := Decode(&out, rec, plain, Limits{}); err != nil || !bytes.Equal(out.Bytes(), c) {
				t.Fatalf("superblock byte %d: accepted but inexact", i)
			}
		}
	}
	// corruption anywhere else
	for i := 96; i < len(img); i += 61 {
		c := append([]byte(nil), img...)
		c[i] ^= 0x40
		if rec, plain, _, err := Encode(c, Limits{}); err == nil {
			var out bytes.Buffer
			if err := Decode(&out, rec, plain, Limits{}); err != nil || !bytes.Equal(out.Bytes(), c) {
				t.Fatalf("byte %d: accepted but inexact", i)
			}
		}
	}
	// limits
	if _, _, _, err := Encode(img, Limits{MaxImage: 1000}); !errors.Is(err, ErrLimit) {
		t.Errorf("image limit: %v", err)
	}
	if _, _, _, err := Encode(img, Limits{MaxBlocks: 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("block limit: %v", err)
	}
	if _, _, _, err := Encode(img, Limits{MaxDecoded: 100}); !errors.Is(err, ErrLimit) {
		t.Errorf("decoded limit: %v", err)
	}
	// bad inode table pointer
	c := append([]byte(nil), img...)
	binary.LittleEndian.PutUint64(c[64:], 1<<40)
	if _, _, _, err := Encode(c, Limits{}); err == nil {
		t.Error("inode table beyond the image accepted")
	}
}

func TestHostileRecipes(t *testing.T) {
	img := load(t, "small_lz4_b4k.sqsh")
	rec, plain, _, err := Encode(img, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rec {
		c := append([]byte(nil), rec...)
		c[i] ^= 0xA5
		var out bytes.Buffer
		_ = Decode(&out, c, plain, Limits{MaxImage: 1 << 24}) // must not panic or allocate unboundedly
	}
	var out bytes.Buffer
	if err := Decode(&out, rec, plain[:len(plain)-1], Limits{}); err == nil {
		t.Error("short payload accepted")
	}
	if err := Decode(&out, rec, append(plain, 0), Limits{}); err == nil {
		t.Error("long payload accepted")
	}
	if err := Decode(&out, rec[:len(rec)-1], plain, Limits{}); err == nil {
		t.Error("truncated verbatim section accepted")
	}
	if err := Decode(shortWriter{}, rec, plain, Limits{}); err == nil {
		t.Error("short writer accepted")
	}
	// payload that no longer contains the copies the recipe implies
	bad := append([]byte(nil), plain...)
	for i := range bad {
		bad[i] ^= 0x01
	}
	if err := Decode(&out, rec, bad, Limits{}); err == nil {
		// stored (random) blocks have no copies to violate; compressed blocks must notice
		t.Error("damaged payload accepted")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func FuzzEncode(f *testing.F) {
	for _, n := range []string{"small_lz4_b4k.sqsh", "small_lz4_b16k.sqsh", "small_lz4hc.sqsh"} {
		f.Add(load(f, n))
	}
	f.Fuzz(func(t *testing.T, img []byte) {
		lim := Limits{MaxImage: 1 << 22, MaxBlocks: 4096, MaxInodes: 1 << 16, MaxDecoded: 1 << 24}
		rec, plain, _, err := Encode(img, lim)
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := Decode(&out, rec, plain, lim); err != nil || !bytes.Equal(out.Bytes(), img) {
			t.Fatalf("accepted image does not rebuild: %v", err)
		}
	})
}
