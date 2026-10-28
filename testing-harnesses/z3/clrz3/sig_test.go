package clrz3

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseSig(t *testing.T) {
	deep := bytes.Repeat([]byte{0x0f}, 100)
	cases := []struct {
		name string
		kind byte
		b    []byte
		want []SigSlot // nil with ok=false means rejected
		ok   bool
	}{
		{"void()", BlobMethod, []byte{0x00, 0x00, 0x01}, nil, true},
		{"int32(string, class TypeRef 5)", BlobMethod, []byte{0x20, 0x02, 0x08, 0x0e, 0x12, 0x15}, []SigSlot{{5, 1, 0x15}}, true},
		{"generic method with generic arg", BlobMethod, []byte{0x30, 0x01, 0x01, 0x01, 0x1e, 0x00}, nil, true},
		{"vararg with sentinel", BlobMethod, []byte{0x05, 0x02, 0x01, 0x08, 0x41, 0x0e}, nil, true},
		{"two-byte coded index", BlobMethod, []byte{0x20, 0x01, 0x12, 0x80, 0x40, 0x08}, []SigSlot{{3, 2, 0x40}}, true},
		{"four-byte coded index", BlobMethod, []byte{0x00, 0x01, 0x01, 0x12, 0xc0, 0x00, 0x40, 0x04}, []SigSlot{{4, 4, 0x4004}}, true},
		{"custom modifier on return", BlobMethod, []byte{0x00, 0x00, 0x1f, 0x09, 0x01}, []SigSlot{{3, 1, 0x09}}, true},
		{"field valuetype TypeDef 2", BlobField, []byte{0x06, 0x11, 0x08}, []SigSlot{{2, 1, 0x08}}, true},
		{"field byref is not allowed to dangle", BlobField, []byte{0x06}, nil, false},
		{"local sig with pinned and byref", BlobLocal, []byte{0x07, 0x03, 0x08, 0x45, 0x0f, 0x08, 0x10, 0x12, 0x15}, []SigSlot{{8, 1, 0x15}}, true},
		{"property", BlobProp, []byte{0x28, 0x01, 0x08, 0x08}, nil, true},
		{"typespec generic instance", BlobType, []byte{0x15, 0x12, 0x15, 0x02, 0x08, 0x0e}, []SigSlot{{2, 1, 0x15}}, true},
		{"typespec szarray of class", BlobType, []byte{0x1d, 0x12, 0x15}, []SigSlot{{2, 1, 0x15}}, true},
		{"typespec array rank 2", BlobType, []byte{0x14, 0x08, 0x02, 0x00, 0x00}, nil, true},
		{"typespec function pointer", BlobType, []byte{0x1b, 0x00, 0x01, 0x08, 0x12, 0x15}, []SigSlot{{5, 1, 0x15}}, true},
		{"methodspec", BlobInst, []byte{0x0a, 0x02, 0x08, 0x0e}, nil, true},
		{"truncated params", BlobMethod, []byte{0x20, 0x02, 0x08, 0x0e}, nil, false},
		{"trailing byte", BlobMethod, []byte{0x00, 0x00, 0x01, 0x00}, nil, false},
		{"TypeDefOrRef tag 3", BlobField, []byte{0x06, 0x12, 0x0f}, nil, false},
		{"null type reference", BlobField, []byte{0x06, 0x12, 0x00}, nil, false},
		{"recursion bomb", BlobType, deep, nil, false},
		{"generic instance without arguments", BlobType, []byte{0x15, 0x12, 0x15, 0x00}, nil, false},
		{"unknown element type", BlobField, []byte{0x06, 0x17}, nil, false},
		{"huge count", BlobMethod, []byte{0x00, 0xc0, 0xff, 0xff, 0xff, 0x01}, nil, false},
		{"empty", BlobMethod, nil, nil, false},
		{"wrong leading byte for kind", BlobField, []byte{0x07, 0x00}, nil, false},
	}
	for _, c := range cases {
		got, ok := ParseSig(c.kind, c.b, true)
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.ok)
			continue
		}
		if ok && len(got)+len(c.want) > 0 && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: slots %v want %v", c.name, got, c.want)
		}
	}
	// a masked blob (reference values zeroed, widths kept) has the same shape
	for _, c := range cases {
		if !c.ok || len(c.want) == 0 {
			continue
		}
		m := append([]byte(nil), c.b...)
		for _, s := range c.want {
			maskCompressed(m[s.Off:s.Off+s.Width], s.Width)
		}
		got, ok := ParseSig(c.kind, m, false)
		if !ok || len(got) != len(c.want) {
			t.Errorf("%s: masked blob parses to %v ok=%v", c.name, got, ok)
			continue
		}
		for i := range got {
			if got[i].Off != c.want[i].Off || got[i].Width != c.want[i].Width {
				t.Errorf("%s: masked slot %d moved: %v want %v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestCompressed(t *testing.T) {
	for _, w := range []int{1, 2, 4} {
		for _, v := range []uint32{0, 1, 0x3f, 0x7f, 0x3fff, 0x1fffffff} {
			b := make([]byte, w)
			if !putCompressed(b, w, v) {
				continue // does not fit this width
			}
			s := &sigp{b: b}
			got, gw := s.cu()
			if s.bad || got != v || gw != w {
				t.Errorf("width %d value %#x: read %#x width %d (bad=%v)", w, v, got, gw, s.bad)
			}
		}
	}
	if putCompressed(make([]byte, 1), 1, 0x80) || putCompressed(make([]byte, 2), 2, 0x4000) || putCompressed(make([]byte, 4), 4, 0x20000000) {
		t.Error("an oversize value was written")
	}
}

func TestSniffAndKind(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		want byte
	}{{[]byte{0x06, 0x08}, BlobField}, {[]byte{0x07, 0x00}, BlobLocal}, {[]byte{0x28, 0x00}, BlobProp}, {[]byte{0x0a, 0x01}, BlobInst}, {[]byte{0x20, 0x00}, BlobMethod}, {[]byte{0x1d}, BlobOpaque}, {nil, BlobOpaque}} {
		if got := SniffKind(c.b); got != c.want {
			t.Errorf("% x: kind %d want %d", c.b, got, c.want)
		}
	}
	if KindOf(SigField, []byte{1}) != BlobField || KindOf(SigNone, []byte{6}) != BlobOpaque || KindOf(SigAny, []byte{7}) != BlobLocal {
		t.Error("KindOf")
	}
}
