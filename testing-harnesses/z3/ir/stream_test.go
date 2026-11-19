package ir

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"testing"
	"testing/iotest"

	"zeros3-z3/deflate"
)

type memSink struct {
	plain bytes.Buffer
	units [][]byte
}

func (m *memSink) Plain(p []byte) error { m.plain.Write(p); return nil }
func (m *memSink) Unit(i int, d []byte) error {
	if i != len(m.units) {
		return fmt.Errorf("unit %d out of order", i)
	}
	m.units = append(m.units, append([]byte(nil), d...))
	return nil
}

// The streaming encoder must produce the oracle's skeleton, units and
// plaintext, and the streaming reconstruction must rebuild the input.
func TestStreamMatchesOracle(t *testing.T) {
	streams := testStreams(t)
	for _, name := range []string{"mem1", "gzip-rsync", "L1", "L2", "L3"} {
		raw, err := os.ReadFile("testdata/zlibfast/" + name + ".deflate")
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream{name, raw})
	}
	for _, st := range streams {
		for _, prof := range []uint8{0, 1, 2, 3, 4, AutoProfile} {
			if testing.Short() && prof > 1 && prof != AutoProfile {
				continue
			}
			t.Run(fmt.Sprintf("%s/p%d", st.name, prof), func(t *testing.T) {
				s, err := deflate.Parse(st.raw, deflate.Options{})
				if err != nil {
					t.Fatal(err)
				}
				var sink memSink
				ss, rest, err := EncodeStream(iotest.HalfReader(bytes.NewReader(append(append([]byte(nil), st.raw...), 1, 2, 3))), StreamConfig{Target: 16 << 10, Profile: prof}, &sink)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(rest, []byte{1, 2, 3}) {
					t.Fatalf("bytes after the stream: %x", rest)
				}
				if !bytes.Equal(sink.plain.Bytes(), s.Plain) {
					t.Fatalf("streamed plaintext differs (%d vs %d bytes)", sink.plain.Len(), len(s.Plain))
				}
				if prof != AutoProfile {
					pk, err := Encode(s, Config{Codec: Pred, Policy: Policy{PTokRun, 16 << 10}, Profile: prof})
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(pk.Skeleton, ss.Skeleton) {
						t.Fatalf("skeleton differs: oracle %d B, stream %d B", len(pk.Skeleton), len(ss.Skeleton))
					}
					if len(pk.Units) != len(sink.units) {
						t.Fatalf("%d units, oracle %d", len(sink.units), len(pk.Units))
					}
					for i := range pk.Units {
						if !bytes.Equal(pk.Units[i], sink.units[i]) {
							t.Fatalf("unit %d differs", i)
						}
					}
				}
				var out bytes.Buffer
				err = ReconstructStream(&out, ss.Skeleton, func(i int) ([]byte, error) { return sink.units[i], nil },
					iotest.OneByteReader(bytes.NewReader(sink.plain.Bytes())), StreamDecodeOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out.Bytes(), st.raw) {
					t.Fatalf("streamed reconstruction differs (%d vs %d bytes)", out.Len(), len(st.raw))
				}
				if testing.Short() {
					return
				}
				// the whole-stream decoder accepts what the streaming encoder wrote
				var out2 bytes.Buffer
				if err := Reconstruct(&out2, ss.Skeleton, func(i int) ([]byte, error) { return sink.units[i], nil }, s.Plain, DecodeOptions{VerifyMatches: true}); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out2.Bytes(), st.raw) {
					t.Fatal("whole-stream reconstruction of streamed units differs")
				}
			})
		}
	}
}

// Truncated or corrupted inputs fail without panics; the ring never grows.
func TestStreamMalformed(t *testing.T) {
	raw := goDeflate(sample(300<<10, 3), 6)
	for n := 0; n < len(raw); n += len(raw)/37 + 1 {
		var sink memSink
		if _, _, err := EncodeStream(bytes.NewReader(raw[:n]), StreamConfig{Profile: 0}, &sink); err == nil {
			t.Fatalf("truncated stream (%d of %d) accepted", n, len(raw))
		}
	}
	var sink memSink
	ss, _, err := EncodeStream(bytes.NewReader(raw), StreamConfig{Profile: 1}, &sink)
	if err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(){
		func() { sink.units[3][len(sink.units[3])/2] ^= 0x40 },
		func() { sink.units = sink.units[:len(sink.units)-1] },
	} {
		us := make([][]byte, len(sink.units))
		for i := range us {
			us[i] = append([]byte(nil), sink.units[i]...)
		}
		saved := sink.units
		sink.units = us
		mut()
		var out bytes.Buffer
		err := ReconstructStream(&out, ss.Skeleton, func(i int) ([]byte, error) {
			if i >= len(sink.units) {
				return nil, io.ErrUnexpectedEOF
			}
			return sink.units[i], nil
		}, bytes.NewReader(sink.plain.Bytes()), StreamDecodeOptions{})
		if err == nil && bytes.Equal(out.Bytes(), raw) {
			t.Fatal("corruption went unnoticed")
		}
		sink.units = saved
	}
	if err := ReconstructStream(io.Discard, ss.Skeleton, func(i int) ([]byte, error) { return sink.units[i], nil }, bytes.NewReader(sink.plain.Bytes()[:100]), StreamDecodeOptions{}); err == nil {
		t.Fatal("short plaintext accepted")
	}
}

// Any byte range of the compressed stream is rebuilt from the index, a few
// units and a plaintext window, for every starting-unit sparsity.
func TestMaterializeRange(t *testing.T) {
	streams := testStreams(t)
	for _, name := range []string{"mem1", "gzip-rsync", "L1"} {
		raw, err := os.ReadFile("testdata/zlibfast/" + name + ".deflate")
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream{name, raw})
	}
	for _, st := range streams {
		if len(st.raw) < 8 {
			continue
		}
		t.Run(st.name, func(t *testing.T) {
			var sink memSink
			ss, _, err := EncodeStream(bytes.NewReader(st.raw), StreamConfig{Target: 4 << 10, Profile: AutoProfile}, &sink)
			if err != nil {
				t.Fatal(err)
			}
			ix, err := ss.Index()
			if err != nil {
				t.Fatalf("index: %v", err)
			}
			if !bytes.Equal(ix.Marshal(), ss.RangeIndexData) {
				t.Fatal("index does not re-marshal identically")
			}
			plain := sink.plain.Bytes()
			plainAt := func(from, to int64) ([]byte, error) { return plain[from:to], nil }
			unit := func(i int) ([]byte, error) { return sink.units[i], nil }
			r := rand.New(rand.NewSource(5))
			n := int64(len(st.raw))
			for _, step := range []int{1, 3} {
				sp := ix.Checkpoints(step)
				for trial := 0; trial < 60; trial++ {
					a := r.Int63n(n)
					l := int64(1 + r.Intn(3000))
					if trial%10 == 0 {
						a = 0
					}
					if trial%10 == 1 {
						a = max(0, n-l)
					}
					b := min(n, a+l)
					got, need, err := MaterializeRange(ss.Skeleton, sp, step, unit, plainAt, a, b)
					if err != nil {
						t.Fatalf("step %d range [%d,%d): %v", step, a, b, err)
					}
					if !bytes.Equal(got, st.raw[a:b]) {
						t.Fatalf("step %d range [%d,%d) differs (%+v)", step, a, b, need)
					}
					if need.UnitsDecoded > len(sink.units)+1 {
						t.Fatalf("decoded %d units of %d", need.UnitsDecoded, len(sink.units))
					}
				}
			}
		})
	}
}

// BenchmarkEncodeStreamGz measures streaming analysis of a real gzip file (P8_GZ=path, first P8_GZ_MIB MiB of
// the compressed stream, Workers from P8_WORKERS): go test -bench EncodeStreamGz -run x ./ir
func BenchmarkEncodeStreamGz(b *testing.B) {
	path := os.Getenv("P8_GZ")
	if path == "" {
		b.Skip("set P8_GZ")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	mib := 4
	fmt.Sscan(os.Getenv("P8_GZ_MIB"), &mib)
	workers := 1
	fmt.Sscan(os.Getenv("P8_WORKERS"), &workers)
	hdr, err := deflate.GzipHeader(bytes.NewReader(raw))
	if err != nil {
		b.Fatal(err)
	}
	body := raw[len(hdr):]
	if len(body) > mib<<20 {
		body = body[:mib<<20] // a truncated stream: analysis stops at the cut, which is all the timing needs
	}
	b.SetBytes(int64(len(body)))
	for i := 0; i < b.N; i++ {
		var sink memSink
		_, _, err := EncodeStream(bytes.NewReader(body), StreamConfig{Target: 16 << 10, Profile: AutoProfile, Workers: workers}, &sink)
		if err != nil && len(body) == len(raw)-len(hdr) {
			b.Fatal(err)
		}
	}
}

// Unit workers never change the output: streaming encode, whole-stream encode and both decoders agree for one and
// several workers, on every producer stream.
func TestUnitWorkersAreByteIdentical(t *testing.T) {
	for _, st := range testStreams(t) {
		t.Run(st.name, func(t *testing.T) {
			var ref memSink
			rs, _, err := EncodeStream(bytes.NewReader(st.raw), StreamConfig{Target: 16 << 10, Profile: AutoProfile, Workers: 1}, &ref)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range []int{2, 5} {
				var got memSink
				gs, _, err := EncodeStream(bytes.NewReader(st.raw), StreamConfig{Target: 16 << 10, Profile: AutoProfile, Workers: w}, &got)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(rs.Skeleton, gs.Skeleton) || rs.UnitBytes != gs.UnitBytes || rs.Pred != gs.Pred || len(ref.units) != len(got.units) || !bytes.Equal(ref.plain.Bytes(), got.plain.Bytes()) {
					t.Fatalf("workers=%d: stream encode differs", w)
				}
				for i := range ref.units {
					if !bytes.Equal(ref.units[i], got.units[i]) {
						t.Fatalf("workers=%d: unit %d differs", w, i)
					}
				}
				var out bytes.Buffer
				err = ReconstructStream(&out, gs.Skeleton, func(i int) ([]byte, error) { return got.units[i], nil }, bytes.NewReader(got.plain.Bytes()), StreamDecodeOptions{Workers: w})
				if err != nil || !bytes.Equal(out.Bytes(), st.raw) {
					t.Fatalf("workers=%d: reconstruction differs (%v)", w, err)
				}
			}
			s, err := deflate.Parse(st.raw, deflate.Options{})
			if err != nil {
				t.Fatal(err)
			}
			p1, err := Encode(s, Config{Codec: Pred, Policy: Policy{Kind: PTokRun, Target: 16 << 10}, Profile: AutoProfile, Workers: 1})
			if err != nil {
				t.Fatal(err)
			}
			p4, err := Encode(s, Config{Codec: Pred, Policy: Policy{Kind: PTokRun, Target: 16 << 10}, Profile: AutoProfile, Workers: 4})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(p1.Skeleton, p4.Skeleton) || len(p1.Units) != len(p4.Units) || p1.Pred != p4.Pred {
				t.Fatal("whole-stream encode differs across worker counts")
			}
			for i := range p1.Units {
				if !bytes.Equal(p1.Units[i], p4.Units[i]) {
					t.Fatalf("whole-stream unit %d differs", i)
				}
			}
			var a, b bytes.Buffer
			unit := func(i int) ([]byte, error) { return p1.Units[i], nil }
			if err := Reconstruct(&a, p1.Skeleton, unit, s.Plain, DecodeOptions{Workers: 1}); err != nil {
				t.Fatal(err)
			}
			if err := Reconstruct(&b, p1.Skeleton, unit, s.Plain, DecodeOptions{Workers: 4}); err != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
				t.Fatalf("whole-stream decode differs across worker counts: %v", err)
			}
		})
	}
}
