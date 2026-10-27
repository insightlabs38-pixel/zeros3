package jvmz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"zeros3-z3/idxref"
)

// ---- a small class-file writer for hand-built cases -------------------------------------------

type tcp struct {
	b []byte
	n int // next index
}

func newCP() *tcp { return &tcp{n: 1} }

func (c *tcp) add(tag byte, body []byte, slots int) int {
	i := c.n
	c.b = append(c.b, tag)
	c.b = append(c.b, body...)
	c.n += slots
	return i
}
func u2(v int) []byte             { return []byte{byte(v >> 8), byte(v)} }
func u4(v int) []byte             { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
func (c *tcp) utf8(s string) int  { return c.add(TagUtf8, append(u2(len(s)), s...), 1) }
func (c *tcp) integer(v int) int  { return c.add(TagInteger, u4(v), 1) }
func (c *tcp) float(v uint32) int { return c.add(TagFloat, u4(int(v)), 1) }
func (c *tcp) long(v uint64) int {
	return c.add(TagLong, append(u4(int(v>>32)), u4(int(v&0xffffffff))...), 2)
}
func (c *tcp) double(v uint64) int {
	return c.add(TagDouble, append(u4(int(v>>32)), u4(int(v&0xffffffff))...), 2)
}
func (c *tcp) class(n int) int      { return c.add(TagClass, u2(n), 1) }
func (c *tcp) str(n int) int        { return c.add(TagString, u2(n), 1) }
func (c *tcp) nat(n, d int) int     { return c.add(TagNameAndType, append(u2(n), u2(d)...), 1) }
func (c *tcp) field(k, nt int) int  { return c.add(TagFieldref, append(u2(k), u2(nt)...), 1) }
func (c *tcp) method(k, nt int) int { return c.add(TagMethodref, append(u2(k), u2(nt)...), 1) }
func (c *tcp) imethod(k, nt int) int {
	return c.add(TagInterfaceMethodref, append(u2(k), u2(nt)...), 1)
}
func (c *tcp) handle(kind, ref int) int {
	return c.add(TagMethodHandle, append([]byte{byte(kind)}, u2(ref)...), 1)
}
func (c *tcp) mtype(d int) int       { return c.add(TagMethodType, u2(d), 1) }
func (c *tcp) dynamic(b, nt int) int { return c.add(TagDynamic, append(u2(b), u2(nt)...), 1) }
func (c *tcp) indy(b, nt int) int    { return c.add(TagInvokeDynamic, append(u2(b), u2(nt)...), 1) }
func (c *tcp) module(n int) int      { return c.add(TagModule, u2(n), 1) }
func (c *tcp) pkg(n int) int         { return c.add(TagPackage, u2(n), 1) }

func attr(name int, info []byte) []byte {
	return append(append(u2(name), u4(len(info))...), info...)
}

func codeAttr(name, maxStack, maxLocals int, code []byte, exc [][4]int, sub []byte, nsub int) []byte {
	b := append(u2(maxStack), u2(maxLocals)...)
	b = append(b, u4(len(code))...)
	b = append(b, code...)
	b = append(b, u2(len(exc))...)
	for _, e := range exc {
		for _, v := range e {
			b = append(b, u2(v)...)
		}
	}
	b = append(b, u2(nsub)...)
	b = append(b, sub...)
	return attr(name, b)
}

type tmethod struct {
	flags      int
	name, desc int
	attrs      []byte
	nattr      int
}

func (c *tcp) class_(major int, this, super int, ifaces []int, methods []tmethod, classAttrs []byte, nClassAttrs int) []byte {
	b := u4(0xCAFEBABE)
	b = append(b, u2(0)...)
	b = append(b, u2(major)...)
	b = append(b, u2(c.n)...)
	b = append(b, c.b...)
	b = append(b, u2(0x21)...)
	b = append(b, u2(this)...)
	b = append(b, u2(super)...)
	b = append(b, u2(len(ifaces))...)
	for _, i := range ifaces {
		b = append(b, u2(i)...)
	}
	b = append(b, u2(0)...) // fields
	b = append(b, u2(len(methods))...)
	for _, m := range methods {
		b = append(b, u2(m.flags)...)
		b = append(b, u2(m.name)...)
		b = append(b, u2(m.desc)...)
		b = append(b, u2(m.nattr)...)
		b = append(b, m.attrs...)
	}
	b = append(b, u2(nClassAttrs)...)
	b = append(b, classAttrs...)
	return b
}

// base builds a valid class "A extends Object" with one method m()V whose bytecode is code; the
// builder is returned so a test can add entries before assembling.
func base(code []byte, setup func(c *tcp, idx map[string]int)) func() []byte {
	return func() []byte {
		c := newCP()
		idx := map[string]int{}
		idx["A"] = c.utf8("A")
		idx["cA"] = c.class(idx["A"])
		idx["Object"] = c.utf8("java/lang/Object")
		idx["cObject"] = c.class(idx["Object"])
		idx["Code"] = c.utf8("Code")
		idx["m"] = c.utf8("m")
		idx["()V"] = c.utf8("()V")
		if setup != nil {
			setup(c, idx)
		}
		m := tmethod{flags: 1, name: idx["m"], desc: idx["()V"], attrs: codeAttr(idx["Code"], 4, 4, code, nil, nil, 0), nattr: 1}
		return c.class_(65, idx["cA"], idx["cObject"], nil, []tmethod{m}, nil, 0)
	}
}

// ---- fixtures ---------------------------------------------------------------------------------

func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(filepath.Join("testdata", "classes"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".class" {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			out[filepath.ToSlash(p[len("testdata/classes/"):])] = b
		}
		return nil
	})
	if err != nil || len(out) < 10 {
		t.Fatalf("fixtures: %v (%d)", err, len(out))
	}
	return out
}

func parse(b []byte) (*File, error) {
	c, _, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{})
	return c, err
}

func rt(t testing.TB, b []byte, cfg Config) *Rep {
	t.Helper()
	c, err := parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep, err := Encode(c, b, cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	meta := append([]byte(nil), rep.Meta...)
	comps := make([][]byte, len(rep.Comps))
	for i, c := range rep.Comps {
		comps[i] = append([]byte(nil), c...)
	}
	out, err := Decode(meta, comps)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(out, b) {
		t.Fatalf("decode differs (%d vs %d bytes)", len(out), len(b))
	}
	return &Rep{Meta: meta, Comps: comps, Stats: rep.Stats}
}

func fixedCut(n int) func([]byte, []idxref.Sec) []int {
	return func(p []byte, _ []idxref.Sec) []int {
		var out []int
		for e := n; e < len(p); e += n {
			out = append(out, e)
		}
		return out
	}
}

func cpBody(p []byte, secs []idxref.Sec) []int { return []int{secs[0].End} }

func TestFixtureClassesRoundTrip(t *testing.T) {
	cuts := map[string]func([]byte, []idxref.Sec) []int{"one": nil, "cp+body": cpBody, "fixed 200": fixedCut(200)}
	tags := map[byte]bool{}
	attrs := map[string]bool{}
	ops := map[byte]int{}
	spaces := map[Space]int{}
	var names []string
	fx := fixtures(t)
	for n := range fx {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := fx[n]
		c, err := parse(b)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		for i := 1; i < c.CPCount; i++ {
			if c.CP[i].Tag != 0 {
				tags[c.CP[i].Tag] = true
			}
		}
		for a := range c.Attrs {
			attrs[a] = true
		}
		for op, k := range c.Ops {
			ops[op] += k
		}
		for _, s := range c.Slots {
			spaces[s.Space]++
		}
		for cn, cut := range cuts {
			for _, k0 := range []int{1, 2, 3, 4} {
				k0, cut := k0, cut
				t.Run(fmt.Sprintf("%s/%s/K%d", n, cn, k0), func(t *testing.T) { rt(t, b, Config{K0: k0, Cut: cut}) })
			}
		}
	}
	for _, tag := range []byte{TagUtf8, TagInteger, TagFloat, TagLong, TagDouble, TagClass, TagString, TagFieldref, TagMethodref, TagInterfaceMethodref, TagNameAndType, TagMethodHandle, TagMethodType, TagInvokeDynamic, TagModule, TagPackage} {
		if !tags[tag] {
			t.Errorf("no fixture class has constant-pool tag %d", tag)
		}
	}
	for _, a := range []string{"Code", "LineNumberTable", "LocalVariableTable", "LocalVariableTypeTable", "StackMapTable", "Signature", "Exceptions", "InnerClasses", "EnclosingMethod", "NestHost", "NestMembers", "PermittedSubclasses", "Record", "BootstrapMethods", "ConstantValue", "MethodParameters", "RuntimeVisibleAnnotations", "RuntimeVisibleParameterAnnotations", "AnnotationDefault", "SourceFile", "Deprecated", "Module"} {
		if !attrs[a] {
			t.Errorf("no fixture class has attribute %s", a)
		}
	}
	// the finite decoder met the forms the parser must handle
	for _, op := range []byte{0x12, 0x13, 0x14, 0xaa, 0xab, 0xc4, 0xba, 0xb9, 0xc5, 0xb6, 0xb7, 0xb8, 0xb2, 0xb4, 0xbb, 0xc0, 0xc1} {
		if ops[op] == 0 {
			t.Errorf("no fixture method uses opcode %#x", op)
		}
	}
	for sp := SpUtf8; sp < numSpaces; sp++ {
		if sp != SpAttr && sp != SpInt && sp != SpDouble && sp != SpFloat && sp != SpLong && sp != SpMember && sp != SpModule && sp != SpPackage && spaces[sp] == 0 {
			t.Errorf("no slot in space %s", sp)
		}
	}
}

func TestHandBuiltConstantsTwoSlotsDynamicAndDuplicates(t *testing.T) {
	// ldc2_w of a Long and a Double (two-slot entries shift the next index), ldc_w, condy via ldc,
	// invokedynamic, and two identical Class entries (a duplicate identity)
	build := base(nil, nil)
	_ = build
	var code []byte
	mk := func() []byte {
		c := newCP()
		idx := map[string]int{}
		idx["A"] = c.utf8("A")
		idx["cA"] = c.class(idx["A"])
		idx["Object"] = c.utf8("java/lang/Object")
		idx["cObject"] = c.class(idx["Object"])
		idx["Code"] = c.utf8("Code")
		idx["m"] = c.utf8("m")
		idx["()V"] = c.utf8("()V")
		l := c.long(0x1122334455667788)
		d := c.double(0x4010000000000000)
		after := c.integer(7) // index right after a two-slot entry
		if after != d+2 {
			t.Fatalf("two-slot accounting: %d after %d", after, d)
		}
		nt := c.nat(c.utf8("run"), c.utf8("()I"))
		dyn := c.dynamic(0, nt)
		ind := c.indy(0, nt)
		c.class(idx["A"]) // duplicate Class entry (same name)
		c.class(idx["A"])
		mh := c.handle(6, c.method(idx["cA"], nt))
		c.mtype(idx["()V"])
		code = []byte{0x14, byte(l >> 8), byte(l), 0x14, byte(d >> 8), byte(d), 0x12, byte(after), 0x12, byte(dyn), 0xba, byte(ind >> 8), byte(ind), 0, 0, 0xbb, byte(idx["cA"] >> 8), byte(idx["cA"]), 0x57, 0xb1}
		_ = mh
		m := tmethod{flags: 1, name: idx["m"], desc: idx["()V"], attrs: codeAttr(idx["Code"], 4, 4, code, nil, nil, 0), nattr: 1}
		// BootstrapMethods so the dynamic entries have a table (index 0 names handle mh)
		bsm := append(u2(1), append(u2(mh), u2(0)...)...)
		attrs := attr(c.utf8("BootstrapMethods"), bsm)
		return c.class_(65, idx["cA"], idx["cObject"], nil, []tmethod{m}, attrs, 1)
	}
	b := mk()
	cl, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[byte]bool{}
	for i := 1; i < cl.CPCount; i++ {
		seen[cl.CP[i].Tag] = true
	}
	if !seen[TagDynamic] || !seen[TagLong] || !seen[TagDouble] {
		t.Fatalf("hand-built class lacks Dynamic/Long/Double: %v", seen)
	}
	for _, k0 := range []int{1, 2, 3, 4} {
		r := rt(t, b, Config{K0: k0, Cut: fixedCut(64)})
		if r.Stats.Duplicates < 1 {
			t.Fatalf("duplicates not seen: %+v", r.Stats)
		}
	}
	// a reference to a Long through ldc (the 32-bit form) is not allowed to name a Double slot index
	bad := append([]byte(nil), b...)
	cl, _ = parse(bad)
	for _, s := range cl.Slots {
		if s.Cls == ClCode && s.Len == 1 {
			bad[s.Off] = 0 // ldc of index 0
		}
	}
	if _, err := parse(bad); !errors.Is(err, ErrFormat) {
		t.Fatalf("ldc of index 0 accepted: %v", err)
	}
}

func TestMalformedClassFilesAreRefused(t *testing.T) {
	good := base([]byte{0xb1}, nil)()
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"bad magic", mut(func(b []byte) []byte { b[0] = 0; return b }), ErrFormat},
		{"future version", mut(func(b []byte) []byte { b[7] = 99; return b }), ErrUnsupported},
		{"ancient version", mut(func(b []byte) []byte { b[7] = 40; return b }), ErrUnsupported},
		{"constant_pool_count 0", mut(func(b []byte) []byte { b[8], b[9] = 0, 0; return b }), ErrFormat},
		{"unknown pool tag", mut(func(b []byte) []byte { b[10] = 2; return b }), ErrUnsupported},
		{"tag 13", mut(func(b []byte) []byte { b[10] = 13; return b }), ErrUnsupported},
		{"truncated mid pool", good[:20], ErrFormat},
		{"trailing bytes", append(append([]byte(nil), good...), 0), ErrFormat},
		{"empty", nil, ErrFormat},
	}
	// class name index naming a Utf8 instead of a Class
	cn := base([]byte{0xb1}, nil)()
	c, _ := parse(cn)
	for _, s := range c.Slots {
		if s.Cls == ClHdr && s.Space == SpClass {
			x := append([]byte(nil), cn...)
			binary.BigEndian.PutUint16(x[s.Off:], 1) // index 1 is a Utf8
			cases = append(cases, struct {
				name string
				b    []byte
				want error
			}{"this_class names a Utf8", x, ErrFormat})
			x = append([]byte(nil), cn...)
			binary.BigEndian.PutUint16(x[s.Off:], 9999)
			cases = append(cases, struct {
				name string
				b    []byte
				want error
			}{"this_class out of range", x, ErrFormat})
			break
		}
	}
	// bytecode and attribute structure
	codeCases := []struct {
		name string
		code []byte
	}{
		{"invalid opcode 0xcb", []byte{0xcb, 0xb1}},
		{"unused opcode 0xd0", []byte{0xd0}},
		{"instruction overruns the code", []byte{0x11, 0x00}},
		{"tableswitch truncated", []byte{0xaa, 0, 0, 0}},
		{"tableswitch range", []byte{0xaa, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}},
		{"lookupswitch pairs overrun", []byte{0xab, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}},
		{"wide of a bad opcode", []byte{0xc4, 0x00, 0, 0}},
		{"wide at the end", []byte{0xc4}},
		{"ldc with a non-loadable operand", []byte{0x12, 0x01}}, // index 1 is Utf8
	}
	for _, c := range codeCases {
		cases = append(cases, struct {
			name string
			b    []byte
			want error
		}{c.name, base(c.code, nil)(), ErrFormat})
	}
	for _, c := range cases {
		_, err := parse(c.b)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// attribute extents
	extra := func(f func(c *tcp, idx map[string]int) []byte) []byte {
		c := newCP()
		idx := map[string]int{}
		idx["A"] = c.utf8("A")
		idx["cA"] = c.class(idx["A"])
		idx["Object"] = c.utf8("java/lang/Object")
		idx["cObject"] = c.class(idx["Object"])
		attrs := f(c, idx)
		return c.class_(65, idx["cA"], idx["cObject"], nil, nil, attrs, 1)
	}
	attrCases := map[string]func(c *tcp, idx map[string]int) []byte{
		"length overruns the file": func(c *tcp, idx map[string]int) []byte { return append(u2(c.utf8("X")), u4(1<<20)...) },
		"Signature shorter than 2": func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("Signature"), []byte{0}) },
		"Signature longer than 2":  func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("Signature"), []byte{0, 0, 0, 0}) },
		"SourceFile names a Class": func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("SourceFile"), u2(idx["cA"])) },
		"annotation tag":           func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("AnnotationDefault"), []byte{'?', 0, 0}) },
		"StackMapTable frame type": func(c *tcp, idx map[string]int) []byte {
			return attr(c.utf8("RuntimeVisibleAnnotations"), []byte{0, 1, 0, 1, 0, 1, 0, 1, '[', 0xff, 0xff})
		},
		"InnerClasses count": func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("InnerClasses"), append(u2(500), 0, 0)) },
		"BootstrapMethods arguments": func(c *tcp, idx map[string]int) []byte {
			return attr(c.utf8("BootstrapMethods"), append(u2(1), 0, 1, 0xff, 0xff))
		},
	}
	for name, f := range attrCases {
		b := extra(f)
		if _, err := parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// nesting depth: an annotation default of 60 nested arrays
	deep := func(c *tcp, idx map[string]int) []byte {
		var info []byte
		for i := 0; i < 60; i++ {
			info = append(info, '[', 0, 1)
		}
		info = append(info, 'I', 0, 1)
		return attr(c.utf8("AnnotationDefault"), info)
	}
	if _, err := parse(extra(deep)); err == nil {
		t.Error("element_value nesting beyond the limit accepted")
	}
	// an unknown attribute is carried as exact bytes
	unk := extra(func(c *tcp, idx map[string]int) []byte { return attr(c.utf8("VendorBlob"), []byte{1, 2, 3, 4, 5}) })
	r := rt(t, unk, Config{})
	_ = r
	c2, _ := parse(unk)
	if a := c2.Attrs["VendorBlob"]; a == nil || a.Deep {
		t.Fatalf("unknown attribute not recorded as opaque: %+v", a)
	}
}

func TestTruncationAndByteFlipsNeverPanic(t *testing.T) {
	fx := fixtures(t)
	for _, n := range []string{"p/Managed.class", "module-info.class", "p/Rec.class"} {
		b := fx[n]
		for k := 0; k < len(b); k += 1 + len(b)/600 {
			if c, err := parse(b[:k]); err == nil && c != nil {
				t.Fatalf("%s truncated to %d parsed", n, k)
			}
		}
		for i := 0; i < len(b); i += 1 + len(b)/900 {
			x := append([]byte(nil), b...)
			x[i] ^= 0xa5
			c, err := parse(x)
			if err != nil {
				continue
			}
			rep, err := Encode(c, x, Config{})
			if err != nil {
				continue
			}
			if out, err := Decode(rep.Meta, rep.Comps); err != nil || !bytes.Equal(out, x) {
				t.Fatalf("%s byte %d: encode succeeded but the round trip failed", n, i)
			}
		}
	}
}

func TestTamperedRepresentationNeverPanics(t *testing.T) {
	b := fixtures(t)["p/Managed.class"]
	rep := rt(t, b, Config{Cut: fixedCut(700)})
	refused, differ := 0, 0
	for ci := range rep.Comps {
		for _, at := range []int{0, 1, len(rep.Comps[ci]) / 2, len(rep.Comps[ci]) - 1} {
			cs := make([][]byte, len(rep.Comps))
			for i, c := range rep.Comps {
				cs[i] = append([]byte(nil), c...)
			}
			cs[ci][at] ^= 0x44
			out, err := Decode(rep.Meta, cs)
			switch {
			case err != nil:
				refused++
			case !bytes.Equal(out, b):
				differ++ // no checksum inside a class file: the object digest (repz3) is the guard
			}
		}
	}
	if refused+differ == 0 {
		t.Fatal("no tamper had any effect")
	}
	for _, m := range [][]byte{rep.Meta[:3], append([]byte("JVN1"), 9), []byte("XXXX\x02")} {
		if _, err := Decode(m, rep.Comps); err == nil {
			t.Errorf("bad meta %q accepted", m)
		}
	}
	if _, err := Decode(rep.Meta, rep.Comps[1:]); err == nil {
		if out, _ := Decode(rep.Meta, rep.Comps[1:]); bytes.Equal(out, b) {
			t.Error("dropped component went unnoticed")
		}
	}
}

func FuzzClass(f *testing.F) {
	for _, b := range fixtures(f) {
		f.Add(b)
	}
	f.Add(base([]byte{0xb1}, nil)())
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		c, err := parse(b)
		if err != nil {
			return
		}
		rep, err := Encode(c, b, Config{Cut: fixedCut(300)})
		if err != nil {
			return
		}
		out, err := Decode(rep.Meta, rep.Comps)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("encode succeeded but decode failed: %v", err)
		}
	})
}
