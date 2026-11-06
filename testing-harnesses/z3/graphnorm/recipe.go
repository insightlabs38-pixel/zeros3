package graphnorm

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"zeros3-z3/simdelta"
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

var errRecipe = errors.New("graphnorm: malformed recipe")

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
	return rc, nil
}

// Replay rebuilds the artifact from stored objects only (no parser, no LLVM semantics).
// get returns a stored object (a simdelta wire); depth and work are bounded by Resolve.
func (r *LLVMRecipe) Replay(get func([32]byte) ([]byte, error)) ([]byte, error) {
	budget := int64(1) << 30
	resolve := func(ref [32]byte) ([]byte, error) { return simdelta.Resolve(ref, get, 2, budget) }
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
		return nil, errors.New("graphnorm: replay digest/length mismatch")
	}
	return out, nil
}
