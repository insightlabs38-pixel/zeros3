package graphcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Piece is one program fragment. Kind 'S' = skeleton (literal operands), 'F' = function template.
type Piece struct {
	Kind byte
	Data []byte
}

// LLVMEncoding is the exact, reversible factorization of one bitcode file.
type LLVMEncoding struct {
	Pieces []Piece
	IDTab  []byte
	Tail   []byte
	Size   int
	SHA    [32]byte
	Stats  TmplStats
	// diagnostics
	Funcs, Fallback int
	FuncNames       []string // aligned with the 'F' pieces
}

func (L *LLVM) idTab() []byte {
	var b []byte
	for t := 0; t < NumTabs; t++ {
		b = binary.AppendUvarint(b, uint64(len(L.Keys[t])))
		for _, k := range L.Keys[t] {
			b = append(b, k[:]...)
		}
	}
	return b
}

// ParseIDTab reads the identity tables written by idTab.
func ParseIDTab(b []byte) ([NumTabs]*Table, error) {
	var tabs [NumTabs]*Table
	pos := 0
	for t := 0; t < NumTabs; t++ {
		n, k := binary.Uvarint(b[pos:])
		if k <= 0 || n > uint64(len(b)) || pos+k+int(n)*KeyLen > len(b) {
			return tabs, errors.New("idtab: truncated")
		}
		pos += k
		keys := make([]Key, n)
		for i := range keys {
			copy(keys[i][:], b[pos:pos+KeyLen])
			pos += KeyLen
		}
		tabs[t] = NewTable(keys)
	}
	if pos != len(b) {
		return tabs, errors.New("idtab: trailing bytes")
	}
	return tabs, nil
}

// EncodeLLVM parses, analyzes and factors a bitcode file; every function template is proven
// by execution against the original block before it is accepted, otherwise it falls back
// to the literal template. The whole file is then replayed and hashed.
func EncodeLLVM(data []byte, pol Policy) (*LLVMEncoding, error) {
	bc, err := ParseBitstream(data)
	if err != nil {
		return nil, err
	}
	L, err := AnalyzeLLVM(bc)
	if err != nil {
		return nil, err
	}
	tabs, err := ParseIDTab(L.idTab())
	if err != nil {
		return nil, err
	}
	info := bc.BlockInfo()
	enc := &LLVMEncoding{IDTab: L.idTab(), Size: len(data), SHA: sha256.Sum256(data)}
	fi := 0
	var pieces []Piece
	sk := &tmplBuilder{L: L, extIdx: map[extEnt]int{}, info: map[uint64][]Abbrev{}, scopes: [][]Abbrev{nil}}
	sk.hook = func(blk *Block) {
		if len(sk.out) > 0 {
			pieces = append(pieces, Piece{'S', sk.out})
			sk.out = nil
		}
		f := L.Funcs[fi]
		fi++
		enc.Funcs++
		t, st, ok := L.BuildFuncTemplate(f, pol)
		if ok && L.verifyFunc(t, f, info, tabs) {
			enc.Stats.add(st)
		} else {
			t = L.LiteralTemplate(f)
			enc.Fallback++
			if !L.verifyFunc(t, f, info, tabs) {
				// cannot happen unless the parser/writer pair is inexact; fail closed
				sk.fail = true
			}
		}
		pieces = append(pieces, Piece{'F', t})
		enc.FuncNames = append(enc.FuncNames, L.Vals[f.val].name)
	}
	for _, b := range bc.Blocks {
		sk.block(b, 0)
	}
	if sk.fail {
		return nil, errors.New("graphcore: literal template does not reproduce function block")
	}
	if len(sk.out) > 0 {
		pieces = append(pieces, Piece{'S', sk.out})
	}
	enc.Pieces = pieces
	enc.Tail = bc.Tail
	out, err := enc.Decode()
	if err != nil {
		return nil, fmt.Errorf("graphcore: self-replay failed: %w", err)
	}
	if !bytes.Equal(out, data) {
		return nil, errors.New("graphcore: self-replay mismatch")
	}
	return enc, nil
}

func (L *LLVM) verifyFunc(t []byte, f lfunc, info map[uint64][]Abbrev, tabs [NumTabs]*Table) bool {
	m := NewByteMachine()
	m.info = map[uint64][]Abbrev{}
	for k, v := range info {
		m.info[k] = v
	}
	m.Tabs = tabs
	if err := m.Exec(t); err != nil {
		return false
	}
	if m.Finish() != nil {
		return false
	}
	var w BitWriter
	w.Bits(1, 2)
	writeBlock(&w, f.blk, cloneInfo(info))
	return bytes.Equal(m.Output(), w.Bytes())
}

func cloneInfo(info map[uint64][]Abbrev) map[uint64][]Abbrev {
	m := make(map[uint64][]Abbrev, len(info))
	for k, v := range info {
		m[k] = append([]Abbrev(nil), v...)
	}
	return m
}

// Decode replays pieces exactly (no LLVM semantics: only tables and the VM).
func (e *LLVMEncoding) Decode() ([]byte, error) {
	tabs, err := ParseIDTab(e.IDTab)
	if err != nil {
		return nil, err
	}
	m := NewMachine()
	m.Tabs = tabs
	for _, p := range e.Pieces {
		if err := m.Exec(p.Data); err != nil {
			return nil, err
		}
	}
	if len(e.Tail) > 0 {
		lit := append([]byte{OpLit}, binary.AppendUvarint(nil, uint64(len(e.Tail)))...)
		if err := m.Exec(append(lit, e.Tail...)); err != nil {
			return nil, err
		}
	}
	if err := m.Finish(); err != nil {
		return nil, err
	}
	out := m.Output()
	if len(out) != e.Size || sha256.Sum256(out) != e.SHA {
		return nil, errors.New("graphcore: replay digest/length mismatch")
	}
	return out, nil
}
