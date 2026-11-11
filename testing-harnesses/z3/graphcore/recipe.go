package graphcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const recipeMagic = "ZB4R"

// RecipeStep refers to one stored component by logical SHA-256.
type RecipeStep struct {
	Kind byte // 'S' skeleton, 'F' function template
	Ref  [32]byte
}

// LLVMRecipe is the per-artifact root: everything else lives in content-addressed objects.
type LLVMRecipe struct {
	Size  uint64
	SHA   [32]byte
	Tail  []byte
	IDTab [32]byte
	Steps []RecipeStep
}

func (r *LLVMRecipe) Marshal() []byte {
	b := []byte(recipeMagic)
	b = binary.AppendUvarint(b, r.Size)
	b = append(b, r.SHA[:]...)
	b = binary.AppendUvarint(b, uint64(len(r.Tail)))
	b = append(b, r.Tail...)
	b = append(b, r.IDTab[:]...)
	b = binary.AppendUvarint(b, uint64(len(r.Steps)))
	for _, s := range r.Steps {
		b = append(b, s.Kind)
		b = append(b, s.Ref[:]...)
	}
	return b
}

var errRecipe = errors.New("graphcore: malformed recipe")

func ParseLLVMRecipe(b []byte) (*LLVMRecipe, error) {
	if len(b) < 4 || string(b[:4]) != recipeMagic {
		return nil, errRecipe
	}
	r := &opReader{b: b, pos: 4}
	rc := &LLVMRecipe{Size: r.uv()}
	copy(rc.SHA[:], r.bytes(32))
	rc.Tail = append([]byte(nil), r.bytes(r.uv())...)
	copy(rc.IDTab[:], r.bytes(32))
	n := r.uv()
	if r.err != nil || n > uint64(len(b))/33 {
		return nil, errRecipe
	}
	for i := uint64(0); i < n; i++ {
		var s RecipeStep
		s.Kind = r.byte()
		copy(s.Ref[:], r.bytes(32))
		if s.Kind != 'S' && s.Kind != 'F' {
			return nil, errRecipe
		}
		rc.Steps = append(rc.Steps, s)
	}
	if r.err != nil || r.pos != len(b) || rc.Size > MaxOut {
		return nil, errRecipe
	}
	if !bytes.Equal(rc.Marshal(), b) { // reject non-minimal varints: one canonical wire form
		return nil, errRecipe
	}
	return rc, nil
}

// Replay rebuilds the artifact from stored objects only (no parser, no LLVM semantics).
// resolve returns the logical bytes of a stored object by digest (the adapter owns storage,
// delta resolution and its depth/work bounds); a missing or wrong object must return an error.
func (r *LLVMRecipe) Replay(resolve func([32]byte) ([]byte, error)) ([]byte, error) {
	idt, err := resolve(r.IDTab)
	if err != nil {
		return nil, fmt.Errorf("idtab: %w", err)
	}
	tabs, err := ParseIDTab(idt)
	if err != nil {
		return nil, err
	}
	m := NewMachine()
	m.Tabs = tabs
	for i, s := range r.Steps {
		data, err := resolve(s.Ref)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
		if err := m.Exec(data); err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
	}
	if len(r.Tail) > 0 {
		if err := m.Exec(append(append([]byte{OpLit}, binary.AppendUvarint(nil, uint64(len(r.Tail)))...), r.Tail...)); err != nil {
			return nil, err
		}
	}
	if err := m.Finish(); err != nil {
		return nil, err
	}
	out := m.Output()
	if uint64(len(out)) != r.Size || sha256.Sum256(out) != r.SHA {
		return nil, errors.New("graphcore: replay digest/length mismatch")
	}
	return out, nil
}

// ProgRecipe is the root for program-style artifacts (PDB, DEX): one program plus the blobs
// its COPY/STREAM ops reference, all stored as content-addressed objects.
type ProgRecipe struct {
	Size  uint64
	SHA   [32]byte
	Prog  [32]byte
	Blobs [][32]byte
}

const progMagic = "ZB4P"

func (r *ProgRecipe) Marshal() []byte {
	b := []byte(progMagic)
	b = binary.AppendUvarint(b, r.Size)
	b = append(b, r.SHA[:]...)
	b = append(b, r.Prog[:]...)
	b = binary.AppendUvarint(b, uint64(len(r.Blobs)))
	for _, x := range r.Blobs {
		b = append(b, x[:]...)
	}
	return b
}

func ParseProgRecipe(b []byte) (*ProgRecipe, error) {
	if len(b) < 4 || string(b[:4]) != progMagic {
		return nil, errRecipe
	}
	r := &opReader{b: b, pos: 4}
	rc := &ProgRecipe{Size: r.uv()}
	copy(rc.SHA[:], r.bytes(32))
	copy(rc.Prog[:], r.bytes(32))
	n := r.uv()
	if r.err != nil || n > 64 {
		return nil, errRecipe
	}
	for i := uint64(0); i < n; i++ {
		var x [32]byte
		copy(x[:], r.bytes(32))
		rc.Blobs = append(rc.Blobs, x)
	}
	if r.err != nil || r.pos != len(b) || rc.Size > MaxOut || !bytes.Equal(rc.Marshal(), b) {
		return nil, errRecipe
	}
	return rc, nil
}

func (r *ProgRecipe) Replay(resolve func([32]byte) ([]byte, error)) ([]byte, error) {
	prog, err := resolve(r.Prog)
	if err != nil {
		return nil, err
	}
	m := NewByteMachine()
	m.Limit = int(min(r.Size, MaxOut))
	for _, x := range r.Blobs {
		b, err := resolve(x)
		if err != nil {
			return nil, err
		}
		m.Blobs = append(m.Blobs, b)
	}
	if err := m.Exec(prog); err != nil {
		return nil, err
	}
	if err := m.Finish(); err != nil {
		return nil, err
	}
	out := m.Output()
	if uint64(len(out)) != r.Size || sha256.Sum256(out) != r.SHA {
		return nil, errors.New("graphcore: replay digest/length mismatch")
	}
	return out, nil
}
