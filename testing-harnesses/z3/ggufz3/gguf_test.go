package ggufz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

type tspec struct {
	name string
	dims []uint64
	typ  uint32
	fill byte
}

func build(version uint32, align int, ts []tspec, _ func([]byte) []byte) []byte {
	var bt []BuildTensor
	for _, t := range ts {
		n, err := NBytes(t.typ, t.dims)
		if err != nil {
			panic(err)
		}
		bt = append(bt, BuildTensor{t.name, t.dims, t.typ, bytes.Repeat([]byte{t.fill}, int(n))})
	}
	return Build(version, align, bt)
}

var sample = []tspec{
	{"token_embd.weight", []uint64{256, 3}, 14, 1},   // Q6_K: 3 blocks of 210
	{"blk.0.attn_norm.weight", []uint64{64}, 0, 2},   // F32
	{"blk.0.ffn_down.weight", []uint64{64, 2}, 8, 3}, // Q8_0: 4 blocks of 34
	{"output.weight", []uint64{256, 1}, 12, 4},       // Q4_K: 144 bytes
}

func TestParseSpansAndTiling(t *testing.T) {
	for _, align := range []int{32, 64, 16} {
		b := build(3, align, sample, nil)
		f, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{})
		if err != nil {
			t.Fatalf("align %d: %v", align, err)
		}
		if f.Version != 3 || f.Alignment != int64(align) || len(f.Tensors) != 4 || len(f.KVs) < 8 {
			t.Fatalf("align %d: header %+v", align, f)
		}
		if kv, ok := f.Lookup("tokenizer.tokens"); !ok || kv.Count != 3 || kv.ElemType != TString {
			t.Fatalf("array kv %+v", kv)
		}
		if kv, _ := f.Lookup("general.architecture"); kv.Str != "llama" {
			t.Fatalf("string kv %+v", kv)
		}
		want := []int64{630, 256, 136, 144}
		for i, tn := range f.Tensors {
			if tn.Size() != want[i] {
				t.Fatalf("tensor %s: size %d want %d", tn.Name, tn.Size(), want[i])
			}
			if !bytes.Equal(b[tn.Begin:tn.End], bytes.Repeat([]byte{byte(i + 1)}, int(tn.Size()))) {
				t.Fatalf("tensor %s: span does not hold its bytes", tn.Name)
			}
			if (tn.Begin-f.DataStart)%int64(align) != 0 {
				t.Fatalf("tensor %s unaligned", tn.Name)
			}
		}
		// the segments tile the file exactly and rebuild it byte for byte
		var out []byte
		for _, s := range f.Segments() {
			if s.Begin != int64(len(out)) {
				t.Fatalf("segment gap at %d", len(out))
			}
			out = append(out, b[s.Begin:s.End]...)
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("align %d: segments do not rebuild the file", align)
		}
	}
}

func TestParseRefusals(t *testing.T) {
	good := build(3, 32, sample, nil)
	parse := func(b []byte) error { _, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{}); return err }
	if err := parse(build(1, 32, sample, nil)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("version 1: %v", err)
	}
	if err := parse(good[:3]); err == nil {
		t.Error("3-byte file accepted")
	}
	bad := append([]byte(nil), good...)
	bad[0] = 'X'
	if err := parse(bad); !errors.Is(err, ErrFormat) {
		t.Errorf("magic: %v", err)
	}
}

func TestUnsupportedTypeAndOverflow(t *testing.T) {
	if _, err := NBytes(31, []uint64{32}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("type 31: %v", err)
	}
	if _, err := NBytes(0, []uint64{1 << 40, 1 << 40}); !errors.Is(err, ErrFormat) {
		t.Errorf("element overflow: %v", err)
	}
	if _, err := NBytes(14, []uint64{255}); !errors.Is(err, ErrFormat) {
		t.Errorf("partial block: %v", err)
	}
	if _, err := NBytes(0, []uint64{1 << 62}); !errors.Is(err, ErrFormat) {
		t.Errorf("byte-size overflow: %v", err)
	}
	// a tensor type outside the table inside a real header
	b := build(3, 32, []tspec{{"x", []uint64{32}, 0, 0}}, nil)
	i := bytes.LastIndex(b, []byte("x"))
	// name(8+1) nd(4) dims(8) -> type at i+1+4+8
	binary.LittleEndian.PutUint32(b[i+1+4+8:], 31)
	if _, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unknown type in header: %v", err)
	}
}

func TestTruncationAndHostileCounts(t *testing.T) {
	b := build(3, 32, sample, nil)
	for n := 0; n < len(b); n++ {
		f, err := Parse(bytes.NewReader(b[:n]), int64(n), Limits{})
		// a prefix can only parse if the data area it declares still fits
		if err == nil {
			if f.DataEnd > int64(n) {
				t.Fatalf("prefix %d accepted with data end %d", n, f.DataEnd)
			}
		}
	}
	// hostile counts: 2^60 tensors / kvs / array elements must fail without allocating
	for _, off := range []int{8, 16} {
		c := append([]byte(nil), b...)
		binary.LittleEndian.PutUint64(c[off:], 1<<60)
		if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
			t.Errorf("count at %d accepted", off)
		}
		binary.LittleEndian.PutUint64(c[off:], 1<<30) // within MaxTensors but far past the header
		if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
			t.Errorf("count 2^30 at %d accepted", off)
		}
	}
	// hostile array count inside a key/value
	c := append([]byte(nil), b...)
	i := bytes.Index(c, []byte("tokenizer.tokens")) + len("tokenizer.tokens") + 4 + 4
	binary.LittleEndian.PutUint64(c[i:], 1<<62)
	if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
		t.Error("array count 2^62 accepted")
	}
	// a metadata bound smaller than the header refuses
	if _, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{MaxMeta: 200}); err == nil {
		t.Error("MaxMeta ignored")
	}
}

func TestOverlapAndRange(t *testing.T) {
	b := build(3, 32, sample, nil)
	f, _ := Parse(bytes.NewReader(b), int64(len(b)), Limits{})
	// move tensor 3's offset onto tensor 2
	c := append([]byte(nil), b...)
	i := bytes.Index(c, []byte("output.weight")) + len("output.weight") + 4 + 16 + 4
	binary.LittleEndian.PutUint64(c[i:], uint64(f.Tensors[2].Begin-f.DataStart))
	if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
		t.Error("overlap accepted")
	}
	// past the end of file
	binary.LittleEndian.PutUint64(c[i:], uint64(len(c)))
	if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
		t.Error("out-of-file tensor accepted")
	}
	// unaligned offset
	binary.LittleEndian.PutUint64(c[i:], uint64(f.Tensors[3].Begin-f.DataStart+1))
	if _, err := Parse(bytes.NewReader(c), int64(len(c)), Limits{}); err == nil {
		t.Error("unaligned tensor accepted")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(build(3, 32, sample, nil))
	f.Add(build(2, 64, sample[:2], nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		g, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{MaxMeta: 1 << 20})
		if err != nil {
			return
		}
		var n int64
		for _, s := range g.Segments() {
			if s.Begin != n || s.End < s.Begin || s.End > int64(len(b)) {
				t.Fatalf("segments do not tile: %+v", s)
			}
			n = s.End
		}
		if n != int64(len(b)) {
			t.Fatalf("segments end at %d of %d", n, len(b))
		}
	})
}
