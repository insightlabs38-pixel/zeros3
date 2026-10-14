package main

import (
	"encoding/binary"
	"testing"
)

func rec(pub byte, dataLen int, exe byte) []byte {
	b := make([]byte, avHeader+(dataLen+7)&^7)
	binary.LittleEndian.PutUint64(b[0:], 7)
	binary.LittleEndian.PutUint64(b[8:], uint64(dataLen))
	b[16] = pub
	b[48+16+32] = exe
	return b
}

func TestParseAppendVec(t *testing.T) {
	var f []byte
	f = append(f, rec(1, 0, 0)...)
	f = append(f, rec(2, 13, 1)...)
	f = append(f, rec(3, 100, 0)...)
	n, used, ok := parseAppendVec(f, nil)
	if !ok || n != 3 || used != int64(len(f)) {
		t.Fatalf("n=%d used=%d/%d ok=%v", n, used, len(f), ok)
	}
	// trailing zero padding stops the walk
	f = append(f, make([]byte, 4096)...)
	if n, _, ok := parseAppendVec(f, nil); !ok || n != 3 {
		t.Fatalf("padding: n=%d ok=%v", n, ok)
	}
	// a record that claims more data than the file has is not a valid AppendVec
	bad := rec(1, 64, 0)
	binary.LittleEndian.PutUint64(bad[8:], 1<<20)
	if _, _, ok := parseAppendVec(bad, nil); ok {
		t.Fatal("oversized record accepted")
	}
	// executable byte must be 0 or 1
	bad = rec(1, 8, 2)
	if _, _, ok := parseAppendVec(bad, nil); ok {
		t.Fatal("bad executable flag accepted")
	}
}
