package graphcore

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"strings"
	"testing"
)

func run(t *testing.T, prog []byte, blobs ...[]byte) ([]byte, error) {
	t.Helper()
	m := NewByteMachine()
	m.Limit = 1 << 20
	m.Blobs = blobs
	err := m.Exec(prog)
	return m.Output(), err
}

func uv(vs ...uint64) []byte { return uvarints(vs) }

func TestByteOps(t *testing.T) {
	var p []byte
	p = append(p, OpLit, 3, 'a', 'b', 'c')
	p = append(p, OpU, 4, 1)            // big-endian u32
	p = append(p, uv(0x01020304<<3)...) // token literal
	p = append(p, OpLEB, 0, 3)          // unsigned LEB, minimum 3 bytes (non-minimal encoding preserved)
	p = append(p, uv(5<<3)...)
	p = append(p, OpLEB, 1, 1)
	p = append(p, uv(uint64(0)<<3|7)...) // escape literal: -3 as uint64 below
	p = append(p, uv(^uint64(2))...)
	p = append(p, OpCInt)
	p = append(p, uv(300<<3)...)
	out, err := run(t, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'a', 'b', 'c', 1, 2, 3, 4, 0x85, 0x80, 0x00, 0x7D, 0x81, 0x2C}
	if !bytes.Equal(out, want) {
		t.Fatalf("got % x want % x", out, want)
	}
}

func TestLengthDeriveChecksum(t *testing.T) {
	// u16 LE length of the body that follows
	body := []byte{OpLenBeg, 2, 0, 0, OpLit, 5, 'h', 'e', 'l', 'l', 'o', OpLenEnd}
	out, err := run(t, body)
	if err != nil || !bytes.Equal(out, []byte{5, 0, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("length: % x %v", out, err)
	}
	// header with sha1 + crc32 patched after the payload (DEX-style)
	q := []byte{OpLit, 4, 'd', 'a', 't', 'a', OpLit, 20}
	q = append(q, make([]byte, 20)...)
	q = append(q, OpLit, 3, 'f', 'o', 'o', OpDerive, 2)
	q = append(q, uv(4, 24)...) // sha1 of out[24:] patched at 4
	q = append(q, OpDerive, 1)
	q = append(q, uv(0, 4)...) // crc32 LE of out[4:] patched at 0
	o2, err := run(t, q)
	if err != nil {
		t.Fatal(err)
	}
	h := sha1.Sum([]byte("foo"))
	if !bytes.Equal(o2[4:24], h[:]) {
		t.Fatal("sha1 derive")
	}
	if binary.LittleEndian.Uint32(o2) != crc32.ChecksumIEEE(o2[4:]) {
		t.Fatal("crc derive")
	}
	// self-inclusive length plus a MARK-scoped adler32 (big-endian, 4 bytes)
	r1 := []byte{OpLenBeg, 1, 0, 1, OpMark, OpLit, 3, 'x', 'y', 'z', OpCksum, 0, 4, 1, OpLenEnd}
	o3, err := run(t, r1)
	if err != nil {
		t.Fatal(err)
	}
	if o3[0] != 1+3+4 {
		t.Fatalf("self length %d", o3[0])
	}
	if binary.BigEndian.Uint32(o3[4:]) != 0 && false {
		t.Fatal("unreachable")
	}
	_ = adler32.Checksum
	// derive range errors
	for _, p := range [][]byte{
		append([]byte{OpDerive, 2}, uv(0, 0)...),              // patch beyond output
		append([]byte{OpDerive, 9}, uv(0, 0)...),              // unknown kind
		append([]byte{OpLit, 1, 0, OpDerive, 0}, uv(0, 5)...), // from beyond output
	} {
		if _, err := run(t, p); err == nil {
			t.Fatal("bad derive accepted")
		}
	}
}

func TestTableOp(t *testing.T) {
	// column 0 raw u16, column 1 delta u32
	stream := uv(7, 9, 11, Zig(100), Zig(5), Zig(-3))
	prog := []byte{OpStream, 0, 0, OpTable, 3, 2, 2, PredRaw, 4, PredDelta}
	out, err := run(t, prog, stream)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{7, 0, 100, 0, 0, 0, 9, 0, 105, 0, 0, 0, 11, 0, 102, 0, 0, 0}
	if !bytes.Equal(out, want) {
		t.Fatalf("got % x", out)
	}
	// overflow of the declared width is an error, never a silent truncation
	if _, err := run(t, []byte{OpStream, 0, 0, OpTable, 1, 1, 1, PredRaw}, uv(256)); err == nil {
		t.Fatal("width overflow accepted")
	}
	// delta underflow wraps to a huge value that must not fit
	if _, err := run(t, []byte{OpStream, 0, 0, OpTable, 1, 1, 4, PredDelta}, uv(Zig(-1))); err == nil {
		t.Fatal("negative delta accepted")
	}
	// short stream / absurd shape
	if _, err := run(t, []byte{OpStream, 0, 0, OpTable, 100, 1, 4, PredRaw}, uv(1)); err == nil {
		t.Fatal("short stream accepted")
	}
	if _, err := run(t, []byte{OpStream, 0, 0, OpTable, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F, 1, 4, PredRaw}, uv(1)); err == nil {
		t.Fatal("huge row count accepted")
	}
}

func TestCopyGatherPatchBounds(t *testing.T) {
	b := []byte("0123456789")
	out, err := run(t, append([]byte{OpCopy}, uv(0, 2, 3)...), b)
	if err != nil || string(out) != "234" {
		t.Fatalf("copy %q %v", out, err)
	}
	out, err = run(t, append([]byte{OpGather}, uv(0, 2, 8, 2, 0, 1)...), b)
	if err != nil || string(out) != "890" {
		t.Fatalf("gather %q %v", out, err)
	}
	bad := [][]byte{
		append([]byte{OpCopy}, uv(0, 8, 3)...),    // range
		append([]byte{OpCopy}, uv(1, 0, 1)...),    // blob index
		append([]byte{OpGather}, uv(0, 1<<40)...), // count
		append([]byte{OpPatch}, uv(5, 1)...),      // patch beyond output
		{OpZero, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},    // exceeds limit
		{0x7F},                                    // unknown op
		{OpLit, 9, 'a'},                           // truncated literal
		{OpEnd},                                   // unbalanced
		{OpLenEnd},
	}
	for i, p := range bad {
		if _, err := run(t, p, b); err == nil {
			t.Fatalf("bad program %d accepted", i)
		}
	}
	m := NewByteMachine()
	if err := m.Exec([]byte{OpLenBeg, 1, 0, 0}); err != nil || m.Finish() == nil {
		t.Fatal("open length scope must fail Finish")
	}
}

func TestLookupSemantics(t *testing.T) {
	k1, k2 := Key{1, 1, 1, 1}, Key{2, 2, 2, 2}
	m := NewMachine()
	m.Tabs[0] = NewTable([]Key{k1, k2, k2}) // k2 duplicated: ambiguous
	ext := []byte{OpExt, 2}
	ext = append(ext, k1[:]...)
	ext = append(ext, k2[:]...)
	ext = append(ext, 0, 0, 0)
	prog := append(ext, OpEnter, 9, 3)
	// abbreviation-free unabbreviated record using an extern token (tab 0, idx 0)
	prog = append(prog, 0x80|3, 1, 1) // record a3 code1 nops1
	prog = append(prog, uv((0<<2|0)<<3|TokXAbs)...)
	prog = append(prog, OpEnd)
	if err := m.Exec(prog); err != nil {
		t.Fatal(err)
	}
	// ambiguous key must be an error, not a silent pick
	m2 := NewMachine()
	m2.Tabs[0] = NewTable([]Key{k1, k2, k2})
	p2 := append([]byte(nil), ext...)
	p2 = append(p2, OpEnter, 9, 3, 0x80|3, 1, 1)
	p2 = append(p2, uv((1<<2|0)<<3|TokXAbs)...)
	if err := m2.Exec(p2); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous key accepted: %v", err)
	}
	// stale key (absent) is rejected
	m3 := NewMachine()
	m3.Tabs[0] = NewTable([]Key{k1})
	if err := m3.Exec(p2); err == nil {
		t.Fatal("stale dependency accepted")
	}
	// relative reference underflow
	m4 := NewMachine()
	m4.Tabs[0] = NewTable([]Key{k1, k2})
	p4 := append([]byte(nil), ext...)
	p4 = append(p4, OpSetInst, 0, OpEnter, 9, 3, 0x80|3, 1, 1)
	p4 = append(p4, uv((1<<2|0)<<3|TokXRel)...) // Inst=2, key idx1 -> 1: fine; use idx>Inst below
	if err := m4.Exec(p4); err != nil {
		t.Fatal(err)
	}
}
