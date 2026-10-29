package dexz3

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"testing"
)

type measuredSource struct {
	b    []byte
	max  int
	fail bool
}

func (s *measuredSource) ReadAt(p []byte, off int64) (int, error) {
	if len(p) > s.max {
		s.max = len(p)
	}
	if s.fail {
		return 0, io.ErrClosedPipe
	}
	return bytes.NewReader(s.b).ReadAt(p, off)
}

func TestA4WalkAtLegacyDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(4103))
	for _, name := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		original := fixture(t, name)
		for i := 0; i < 700; i++ {
			data := append([]byte(nil), original...)
			if i > 0 {
				j := rng.Intn(len(data))
				data[j] ^= byte(1 + rng.Intn(255))
			}
			f, err := parse(data)
			if err != nil {
				continue
			}
			for _, opts := range []WalkOpts{{}, {Offsets: true, Debug: true}, {Offsets: true, Debug: true, Masked: true}} {
				old, oe := legacyWalk(f, data, opts)
				src := &measuredSource{b: data}
				got, ge := WalkAt(f, src, opts)
				if fmt.Sprint(oe) != fmt.Sprint(ge) || !reflect.DeepEqual(old, got) {
					t.Fatalf("%s mutation=%d opts=%+v error %v/%v", name, i, opts, oe, ge)
				}
				if src.max > 2<<20 {
					t.Fatalf("unbounded request %d", src.max)
				}
			}
		}
	}
}

func TestA4WalkAtInputFailure(t *testing.T) {
	data := fixture(t, "fixture-api28.dex")
	f, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = WalkAt(f, &measuredSource{b: data, fail: true}, WalkOpts{Offsets: true, Debug: true}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost input error %v", err)
	}
	if _, err = WalkAt(f, bytes.NewReader(data[:len(data)/2]), WalkOpts{Offsets: true, Debug: true}); err == nil {
		t.Fatal("truncated backing source accepted")
	}
}

func TestA4SourceWindowBoundary(t *testing.T) {
	b := make([]byte, 3<<20)
	for i := range b {
		b[i] = byte(i * 19)
	}
	r := &measuredSource{b: b}
	s := &walkSource{r: r, size: int64(len(b)), lo: -1}
	for _, off := range []int64{0, 65534, 65535, 65536, 131071, int64(len(b)) - 4, 3, 1 << 20} {
		if got := s.span(off, 4); !bytes.Equal(got, b[off:off+4]) {
			t.Fatalf("offset %d", off)
		}
	}
	if r.max > 64<<10 || len(s.buf) > 64<<10 || s.err != nil {
		t.Fatalf("window %d retained %d err %v", r.max, len(s.buf), s.err)
	}
	if got := s.span(300, 2<<20); !bytes.Equal(got, b[300:300+(2<<20)]) || len(s.buf) > 64<<10 {
		t.Fatal("bounded large instruction buffer")
	}
}
