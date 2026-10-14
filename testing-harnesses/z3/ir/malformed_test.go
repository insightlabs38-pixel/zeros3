package ir

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"

	"zeros3-z3/deflate"
)

func hasPred() bool {
	for _, c := range testCodecs {
		if c == Pred {
			return true
		}
	}
	return false
}

type malformedCase struct {
	codec Codec
	prof  uint8
}

// malformedCases are the (codec, profile) pairs the corruption tests cover.
func malformedCases() []malformedCase {
	var out []malformedCase
	for _, c := range testCodecs {
		out = append(out, malformedCase{c, 0})
		if c == Pred {
			out = append(out, malformedCase{c, 2})
		}
	}
	return out
}

func smallPackage(t testing.TB, codec Codec, pol Policy, profile uint8) (raw []byte, s *deflate.Stream, pk *Package) {
	plain := sample(40<<10, 7)
	raw = goDeflate(plain, 6)
	var err error
	if s, err = deflate.Parse(raw, deflate.Options{}); err != nil {
		t.Fatal(err)
	}
	if pk, err = Encode(s, Config{Codec: codec, Policy: pol, Profile: profile}); err != nil {
		t.Fatal(err)
	}
	return
}

func reconstruct(skel []byte, units [][]byte, plain []byte) ([]byte, error) {
	var out bytes.Buffer
	err := Reconstruct(&out, skel, func(i int) ([]byte, error) {
		if i >= len(units) {
			return nil, errors.New("missing unit")
		}
		return units[i], nil
	}, plain, DecodeOptions{})
	return out.Bytes(), err
}

// Every strict prefix of the skeleton and of every unit must fail cleanly, and
// no single-byte mutation may yield a different successfully "exact" stream.
func TestMalformedTruncationAndMutation(t *testing.T) {
	for _, c := range malformedCases() {
		raw, s, pk := smallPackage(t, c.codec, Policy{PTokRun, 4 << 10}, c.prof)
		if len(pk.Units) < 3 {
			t.Fatalf("%v: only %d units", c.codec, len(pk.Units))
		}
		for n := 0; n < len(pk.Skeleton); n++ {
			if out, err := reconstruct(pk.Skeleton[:n], pk.Units, s.Plain); err == nil {
				t.Fatalf("%v: truncated skeleton (%d of %d) accepted (%d bytes)", c.codec, n, len(pk.Skeleton), len(out))
			}
		}
		r := rand.New(rand.NewSource(int64(c.codec)))
		for trial := 0; trial < 400; trial++ {
			// mutate the skeleton
			sk := append([]byte(nil), pk.Skeleton...)
			sk[r.Intn(len(sk))] ^= byte(1 + r.Intn(255))
			if out, err := reconstruct(sk, pk.Units, s.Plain); err == nil && !bytes.Equal(out, raw) {
				t.Fatalf("%v: mutated skeleton produced a different stream", c.codec)
			}
			// mutate one unit
			us := append([][]byte(nil), pk.Units...)
			k := r.Intn(len(us))
			u := append([]byte(nil), us[k]...)
			if len(u) > 0 {
				u[r.Intn(len(u))] ^= byte(1 + r.Intn(255))
			}
			us[k] = u
			if out, err := reconstruct(pk.Skeleton, us, s.Plain); err == nil && !bytes.Equal(out, raw) {
				t.Fatalf("%v: mutated unit %d produced a different stream", c.codec, k)
			}
			// truncated unit
			us[k] = pk.Units[k][:r.Intn(len(pk.Units[k]))]
			if out, err := reconstruct(pk.Skeleton, us, s.Plain); err == nil && !bytes.Equal(out, raw) {
				t.Fatalf("%v: truncated unit %d produced a different stream", c.codec, k)
			}
			// wrong plaintext
			pl := append([]byte(nil), s.Plain...)
			pl[r.Intn(len(pl))] ^= 0x55
			if out, err := reconstruct(pk.Skeleton, pk.Units, pl); err == nil && !bytes.Equal(out, raw) {
				t.Fatalf("%v: corrupted plaintext produced a different stream", c.codec)
			}
		}
		// wrong plaintext length and a dropped unit
		if _, err := reconstruct(pk.Skeleton, pk.Units, s.Plain[:len(s.Plain)-1]); err == nil {
			t.Fatalf("%v: short plaintext accepted", c.codec)
		}
		if _, err := reconstruct(pk.Skeleton, pk.Units[:len(pk.Units)-1], s.Plain); err == nil {
			t.Fatalf("%v: missing unit accepted", c.codec)
		}
	}
}

func TestSkeletonLimits(t *testing.T) {
	_, s, pk := smallPackage(t, E2, Policy{PTokRun, 4 << 10}, 0)
	base := pk.Skeleton
	// block count over the limit
	bad := append([]byte{skelVersion, byte(E2), 0}, binary.AppendUvarint(nil, maxBlocks+1)...)
	if _, err := ParseSkeleton(bad, len(s.Plain)); !errors.Is(err, ErrLimit) {
		t.Errorf("block-count overflow: %v", err)
	}
	// a huge varint
	bad = append([]byte{skelVersion, byte(E2), 0}, bytes.Repeat([]byte{0xff}, 11)...)
	if _, err := ParseSkeleton(bad, len(s.Plain)); err == nil {
		t.Error("varint overflow accepted")
	}
	// the maximum plaintext bound is enforced on token counts
	if _, err := ParseSkeleton(base, 10); err == nil {
		t.Error("skeleton accepted with a 10-byte plaintext limit")
	}
	if _, err := ParseSkeleton(append(append([]byte(nil), base...), 0), len(s.Plain)); err == nil {
		t.Error("trailing byte accepted")
	}
	if _, err := ParseSkeleton(nil, 10); err == nil {
		t.Error("empty skeleton accepted")
	}
}

// Random bytes must never panic any decoder, and any success must account for
// exactly the declared plaintext.
func TestUnitDecodersOnGarbage(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	plain := sample(20<<10, 3)
	for i := 0; i < 3000; i++ {
		n := r.Intn(200)
		data := make([]byte, n)
		r.Read(data)
		if n > 0 {
			data[0] = byte(testCodecs[r.Intn(len(testCodecs))])
		}
		pl := 1 + r.Intn(5000)
		start := r.Intn(len(plain) - pl)
		for _, dec := range []func() ([]Run, error){
			func() ([]Run, error) { return decodeE1(data, pl) },
			func() ([]Run, error) { return decodeE1Flate(data, pl) },
			func() ([]Run, error) { return decodeE2(data, pl) },
			func() ([]Run, error) {
				if !hasPred() {
					return nil, errors.New("no predictor")
				}
				return newPredictor(plain, uint8(i%5)).decodeUnit(data, start, pl)
			},
		} {
			runs, err := dec()
			if err != nil {
				continue
			}
			tot := 0
			for _, run := range runs {
				tot += run.plainLen()
			}
			if tot != pl {
				t.Fatalf("decoder accepted garbage with %d plaintext bytes, declared %d", tot, pl)
			}
		}
	}
}

func FuzzUnitDecode(f *testing.F) {
	_, _, pk := smallPackage(f, E2, Policy{PTokRun, 4 << 10}, 0)
	for _, c := range testCodecs {
		_, _, p := smallPackage(f, c, Policy{PTokRun, 4 << 10}, 0)
		f.Add(p.Units[1], uint16(p.Skel.Units[1].PlainLen), byte(c))
	}
	_ = pk
	plain := sample(64<<10, 5)
	f.Fuzz(func(t *testing.T, data []byte, plen uint16, codec byte) {
		pl := int(plen)%8192 + 1
		which := testCodecs[int(codec)%len(testCodecs)]
		if len(data) > 0 {
			data[0] = byte(which)
		}
		var runs []Run
		var err error
		switch which {
		case E1:
			runs, err = decodeE1(data, pl)
		case E1Flate:
			runs, err = decodeE1Flate(data, pl)
		case E2:
			runs, err = decodeE2(data, pl)
		default:
			runs, err = newPredictor(plain, codec).decodeUnit(data, int(codec)*13, pl)
		}
		if err == nil {
			tot := 0
			for _, r := range runs {
				tot += r.plainLen()
			}
			if tot != pl {
				t.Fatalf("accepted unit covers %d of %d bytes", tot, pl)
			}
		}
	})
}

func FuzzSkeleton(f *testing.F) {
	for _, c := range testCodecs {
		_, _, p := smallPackage(f, c, Policy{PTokRun, 4 << 10}, 0)
		f.Add(p.Skeleton)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		sk, err := ParseSkeleton(b, 1<<20)
		if err != nil {
			return
		}
		// a parsed skeleton must re-marshal to the same bytes (canonical form)
		if !bytes.Equal(sk.Marshal(), b) {
			t.Fatalf("skeleton not canonical:\n in  %x\n out %x", b, sk.Marshal())
		}
	})
}

// FuzzReconstruct mutates a valid (skeleton, unit, plaintext) triple.
func FuzzReconstruct(f *testing.F) {
	for _, c := range testCodecs {
		raw, s, p := smallPackage(f, c, Policy{PTokRun, 4 << 10}, 0)
		_ = raw
		f.Add(p.Skeleton, p.Units[0], p.Units[1], s.Plain[:len(s.Plain)/2])
	}
	f.Fuzz(func(t *testing.T, skel, u0, u1, plain []byte) {
		if len(plain) > 1<<16 {
			return
		}
		units := [][]byte{u0, u1, u1, u0}
		var out bytes.Buffer
		err := Reconstruct(&out, skel, func(i int) ([]byte, error) { return units[i%len(units)], nil }, plain, DecodeOptions{})
		if err == nil {
			// success implies a valid DEFLATE stream whose plaintext is what we supplied
			s2, perr := deflate.Parse(out.Bytes(), deflate.Options{})
			if perr != nil || !bytes.Equal(s2.Plain, plain) {
				t.Fatalf("reconstruct succeeded with an invalid stream: %v", perr)
			}
		}
	})
}

// FuzzEncodeDecode compresses arbitrary data and checks the IR round trip.
func FuzzEncodeDecode(f *testing.F) {
	f.Add([]byte("hello hello hello hello world"), byte(6), byte(0))
	f.Add(sample(5000, 1), byte(1), byte(7))
	f.Add(bytes.Repeat([]byte{0}, 3000), byte(9), byte(3))
	f.Fuzz(func(t *testing.T, data []byte, level, mode byte) {
		if len(data) > 1<<17 {
			return
		}
		levels := []int{0, 1, 2, 4, 6, 9, -2}
		raw := goDeflate(data, levels[int(level)%len(levels)])
		s, err := deflate.Parse(raw, deflate.Options{})
		if err != nil {
			t.Fatal(err)
		}
		codec := testCodecs[int(mode)%len(testCodecs)]
		pol := policies[int(mode/4)%len(policies)]
		pk, err := Encode(s, Config{Codec: codec, Policy: pol, Profile: mode % 4})
		if err != nil {
			return // an encoder may decline; the explicit form is the fallback
		}
		var out bytes.Buffer
		err = Reconstruct(&out, pk.Skeleton, func(i int) ([]byte, error) { return pk.Units[i], nil }, s.Plain, DecodeOptions{VerifyMatches: true})
		if err != nil || !bytes.Equal(out.Bytes(), raw) {
			t.Fatalf("%v/%v: %v (equal=%v)", codec, pol, err, bytes.Equal(out.Bytes(), raw))
		}
	})
}
