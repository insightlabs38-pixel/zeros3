package thriftcompactz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// enc is a tiny Compact Protocol writer for building test input.
type enc struct{ b []byte }

func (e *enc) field(last *int16, id int16, t Type) {
	if d := id - *last; d > 0 && d <= 15 {
		e.b = append(e.b, byte(d)<<4|byte(t))
	} else {
		e.b = append(e.b, byte(t))
		e.b = AppendI64(e.b, int64(id))
	}
	*last = id
}
func (e *enc) i32(v int32)  { e.b = AppendI64(e.b, int64(v)) }
func (e *enc) bin(s string) { e.b = append(binary.AppendUvarint(e.b, uint64(len(s))), s...) }
func (e *enc) stop()        { e.b = append(e.b, 0) }
func (e *enc) list(t Type, n int) {
	if n < 15 {
		e.b = append(e.b, byte(n)<<4|byte(t))
	} else {
		e.b = binary.AppendUvarint(append(e.b, 0xf0|byte(t)), uint64(n))
	}
}

// sample: struct{1:i32 -7, 2:binary "abc", 3:list<i32>[1,2,3], 5:true, 40:struct{1:i64 1<<40}}
// (field 40 uses the long form of the field header).
func sample() []byte {
	var e enc
	var last int16
	e.field(&last, 1, I32)
	e.i32(-7)
	e.field(&last, 2, Binary)
	e.bin("abc")
	e.field(&last, 3, List)
	e.list(I32, 3)
	e.i32(1)
	e.i32(2)
	e.i32(3)
	e.field(&last, 5, BoolTrue)
	e.field(&last, 40, Struct)
	var l2 int16
	e.field(&l2, 1, I64)
	e.b = AppendI64(e.b, 1<<40)
	e.stop()
	e.stop()
	return e.b
}

func TestStructReadAndSkip(t *testing.T) {
	b := sample()
	r := NewReader(b, Limits{})
	var got struct {
		a    int32
		s    string
		l    []int32
		flag bool
		big  int64
	}
	err := r.Struct(func(id int16, ft Type) error {
		switch id {
		case 1:
			v, err := r.I32()
			got.a = v
			return err
		case 2:
			v, err := r.Binary()
			got.s = string(v)
			return err
		case 3:
			_, n, err := r.ListHeader()
			for i := 0; i < n && err == nil; i++ {
				var v int32
				v, err = r.I32()
				got.l = append(got.l, v)
			}
			return err
		case 5:
			got.flag = ft == BoolTrue
			return nil
		case 40:
			return r.Struct(func(id int16, ft Type) error {
				v, err := r.I64()
				got.big = v
				return err
			})
		}
		return r.Skip(ft)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.a != -7 || got.s != "abc" || len(got.l) != 3 || got.l[2] != 3 || !got.flag || got.big != 1<<40 || r.Remaining() != 0 {
		t.Fatalf("got %+v remaining %d", got, r.Remaining())
	}
	// skipping everything consumes the same bytes
	r2 := NewReader(b, Limits{})
	if err := r2.Skip(Struct); err != nil || r2.Pos() != len(b) {
		t.Fatalf("skip: %v pos %d of %d", err, r2.Pos(), len(b))
	}
}

func TestMalformed(t *testing.T) {
	deep := bytes.Repeat([]byte{byte(1)<<4 | byte(Struct)}, 40)
	for _, tc := range []struct {
		name string
		in   []byte
		lim  Limits
		want error
	}{
		{"empty", nil, Limits{}, ErrShort},
		{"truncated binary", []byte{byte(1)<<4 | byte(Binary), 5, 'a', 'b'}, Limits{}, ErrShort},
		{"truncated varint", []byte{byte(1)<<4 | byte(I64), 0x80}, Limits{}, ErrShort},
		{"overlong varint", append([]byte{byte(1)<<4 | byte(I64)}, bytes.Repeat([]byte{0xff}, 11)...), Limits{}, ErrCorrupt},
		{"hostile list count", []byte{byte(1)<<4 | byte(List), 0xf5, 0xff, 0xff, 0xff, 0x7f}, Limits{}, ErrLimit},
		{"list count beyond bytes", []byte{byte(1)<<4 | byte(List), 0x95, 1, 2}, Limits{}, ErrShort},
		{"hostile binary length", []byte{byte(1)<<4 | byte(Binary), 0xff, 0xff, 0xff, 0xff, 0x0f}, Limits{}, ErrLimit},
		{"hostile map count", []byte{byte(1)<<4 | byte(Map), 0xff, 0xff, 0xff, 0x7f, 0x55}, Limits{}, ErrLimit},
		{"bad element type", []byte{byte(1)<<4 | byte(List), 0x1e, 1}, Limits{}, ErrCorrupt},
		{"nesting", deep, Limits{MaxDepth: 8}, ErrLimit},
		{"too many fields", bytes.Repeat([]byte{byte(1)<<4 | byte(BoolTrue)}, 5000), Limits{MaxFields: 100}, ErrLimit},
		{"value budget", append(bytes.Repeat([]byte{byte(1)<<4 | byte(Byte), 0}, 600), 0), Limits{MaxValues: 100, MaxFields: 1000}, ErrLimit},
		{"unconsumed field", []byte{byte(1)<<4 | byte(I32), 0, 0}, Limits{}, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(tc.in, tc.lim)
			var err error
			if tc.name == "unconsumed field" {
				err = r.Struct(func(id int16, ft Type) error { return nil })
			} else {
				err = r.Skip(Struct)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// Every prefix of a valid struct is rejected without a panic and never read past its end.
func TestEveryPrefix(t *testing.T) {
	b := sample()
	for n := 0; n < len(b); n++ {
		r := NewReader(b[:n], Limits{})
		if err := r.Skip(Struct); err == nil {
			t.Fatalf("prefix of %d/%d bytes parsed", n, len(b))
		} else if !errors.Is(err, ErrShort) {
			t.Fatalf("prefix %d: %v", n, err)
		}
	}
}

func FuzzSkip(f *testing.F) {
	f.Add(sample())
	f.Add([]byte{0x19, 0x35, 1, 2, 3, 0})
	f.Add([]byte(strings.Repeat("\x1c", 20)))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := NewReader(b, Limits{MaxBinary: 1 << 20})
		if err := r.Skip(Struct); err == nil && r.Pos() > len(b) {
			t.Fatal("read past the buffer")
		}
	})
}

func TestIntegerRanges(t *testing.T) {
	if _, err := NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0x1f}, Limits{}).I32(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("i32 above 32 bits: %v", err)
	}
	if _, err := NewReader([]byte{0xff, 0xff, 0xff, 0x7f}, Limits{}).I16(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("i16 out of range: %v", err)
	}
	for _, v := range []int64{0, 1, -1, 1<<31 - 1, -1 << 31, 1<<62 + 5, -1 << 63} {
		got, err := NewReader(AppendI64(nil, v), Limits{}).I64()
		if err != nil || got != v {
			t.Fatalf("i64 %d: %d %v", v, got, err)
		}
	}
}
