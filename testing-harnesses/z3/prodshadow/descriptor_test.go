package prodshadow

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"
)

func sampleDesc(n int) *Descriptor {
	d := &Descriptor{}
	for i := 0; i < n; i++ {
		l := uint32(1000 + i*37)
		d.Refs = append(d.Refs, Ref{sha256.Sum256([]byte{byte(i), byte(i >> 8)}), l})
		d.Size += int64(l)
	}
	return d
}

func TestDescriptorRoundTripAndCanonical(t *testing.T) {
	d := sampleDesc(40)
	b := d.Append(nil)
	got, n, err := ParseDescriptor(b)
	if err != nil || n != len(b) || got.Size != d.Size || len(got.Refs) != 40 || !bytes.Equal(got.Append(nil), b) {
		t.Fatalf("round trip: %v", err)
	}
	// 40 chunks ~ 40*(32+3) bytes + header: the per-component overhead is small and linear
	if len(b) > 40*36+8 {
		t.Fatalf("descriptor %d bytes for 40 refs", len(b))
	}
}

func TestDescriptorMalformed(t *testing.T) {
	d := sampleDesc(3)
	good := d.Append(nil)
	cases := map[string][]byte{
		"empty":            nil,
		"truncated":        good[:len(good)-1],
		"zero size":        append([]byte{0, 1}, good[2:]...),
		"count bomb":       binary.AppendUvarint(binary.AppendUvarint(nil, 1<<20), 1<<40),
		"count > refs cap": binary.AppendUvarint(binary.AppendUvarint(nil, 5), MaxRefs+1),
		"size > cap":       binary.AppendUvarint(binary.AppendUvarint(nil, uint64(MaxSize)+1), 1),
		"non-minimal":      append([]byte{0x80 | byte(d.Size&0x7f), byte(d.Size >> 7), 3}, good[2:]...),
	}
	// sum mismatch
	bad := *d
	bad.Size++
	cases["sum mismatch"] = bad.Append(nil)
	// zero-length / oversized chunk
	z := *sampleDesc(2)
	z.Refs[0].Len = 0
	cases["zero chunk"] = z.Append(nil)
	o := *sampleDesc(2)
	o.Refs[0].Len = MaxChunk + 1
	cases["oversized chunk"] = o.Append(nil)
	for name, b := range cases {
		if _, _, err := ParseDescriptor(b); !errors.Is(err, ErrDescriptor) {
			t.Errorf("%s: want ErrDescriptor, got %v", name, err)
		}
	}
	// trailing bytes are the caller's to reject: ParseDescriptor reports what it consumed
	if _, n, err := ParseDescriptor(append(append([]byte(nil), good...), 9)); err != nil || n != len(good) {
		t.Errorf("consumed %d err %v", n, err)
	}
}

func TestRootMalformedAndTrailing(t *testing.T) {
	r := &Root{Key: "k", ObjSHA: sha256.Sum256([]byte("o")), ObjSize: 10, Rep: sha256.Sum256([]byte("r")),
		Descs: map[[32]byte]*Descriptor{sha256.Sum256([]byte("a")): sampleDesc(3), sha256.Sum256([]byte("b")): sampleDesc(5)}}
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := r.Marshal()
	if !bytes.Equal(b, b2) {
		t.Fatal("marshal not deterministic")
	}
	if g, err := UnmarshalRoot(b); err != nil || len(g.Descs) != 2 || g.Key != "k" {
		t.Fatalf("round trip: %v", err)
	}
	// every single-byte truncation and bit flip is rejected, never a panic
	for i := 0; i < len(b); i++ {
		if _, err := UnmarshalRoot(b[:i]); err == nil {
			t.Fatalf("truncation at %d accepted", i)
		}
	}
	for i := 0; i < len(b); i++ {
		c := append([]byte(nil), b...)
		c[i] ^= 1 << (i % 8)
		if _, err := UnmarshalRoot(c); err == nil {
			t.Fatalf("flip at %d accepted", i)
		}
	}
	if _, err := UnmarshalRoot(append(append([]byte(nil), b...), 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
	// a correctly checksummed root with trailing bytes inside the body is rejected too
	body := append(append([]byte(nil), b[:len(b)-4]...), 0xAA)
	body = binary.LittleEndian.AppendUint32(body, crcOf(body))
	if _, err := UnmarshalRoot(body); err == nil {
		t.Fatal("trailing body byte accepted")
	}
}

func FuzzParseRoot(f *testing.F) {
	r := &Root{Key: "k", Descs: map[[32]byte]*Descriptor{sha256.Sum256([]byte("a")): sampleDesc(3)}}
	b, _ := r.Marshal()
	f.Add(b)
	f.Add(sampleDesc(4).Append(nil))
	rng := rand.New(rand.NewSource(1))
	junk := make([]byte, 200)
	rng.Read(junk)
	f.Add(junk)
	f.Fuzz(func(t *testing.T, in []byte) {
		if r, err := UnmarshalRoot(in); err == nil {
			// anything accepted must be canonical: it re-marshals to the same bytes
			out, err := r.Marshal()
			if err != nil || !bytes.Equal(out, in) {
				t.Fatalf("accepted a non-canonical root")
			}
		}
		if d, n, err := ParseDescriptor(in); err == nil {
			if !bytes.Equal(d.Append(nil), in[:n]) {
				t.Fatalf("accepted a non-canonical descriptor")
			}
		}
	})
}
