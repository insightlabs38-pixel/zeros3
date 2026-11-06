package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func TestStructuralIndependentProof(t *testing.T) {
	data := bytes.Repeat([]byte("native exact source"), 100)
	h := sha256.Sum256(data)
	rep := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data)), Members: []repz3.Member{{Role: repz3.Stored, SHA: h, Size: int64(len(data))}}}
	bundle, e := kinds.NewBundle(rep, [][]byte{data})
	if e != nil {
		t.Fatal(e)
	}
	store := repz3.NewMemStore()
	root, _, e := bundle.Persist(store)
	if e != nil {
		t.Fatal(e)
	}
	reach, e := structEnv(store).Reachable(root)
	if e != nil {
		t.Fatal(e)
	}
	stream := append([]byte("B1S1"), root[:]...)
	stream = append(stream, h[:]...)
	stream = binary.LittleEndian.AppendUint64(stream, uint64(len(data)))
	for _, id := range reach.Order {
		b := store.Bytes(id)
		stream = binary.LittleEndian.AppendUint32(stream, uint32(len(b)))
		stream = append(stream, b...)
	}
	if e := verifyStructural(stream); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(bytes.Join(structuralChunks(stream), nil), stream) {
		t.Fatal("component boundaries changed bytes")
	}
	stream[len(stream)-1] ^= 1
	if e := verifyStructural(stream); e == nil {
		t.Fatal("corrupt component")
	}
}

func TestExportReplacementPreservesPrevious(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	a, b := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	if e := exportStore(dir, map[[32]byte][]byte{a: []byte("old")}, manifest{}); e != nil {
		t.Fatal(e)
	}
	if e := exportStore(dir, map[[32]byte][]byte{b: []byte("new")}, manifest{}); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 2 {
		t.Fatal("stale object in replacement", e)
	}
	old, e := filepath.Glob(dir + "-previous-*/" + hex.EncodeToString(a[:]) + ".delta")
	if e != nil || len(old) != 1 {
		t.Fatal("previous export lost", e)
	}
	body, e := os.ReadFile(old[0])
	if e != nil || string(body) != "old" {
		t.Fatal("previous bytes", e)
	}
}
