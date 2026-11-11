package graphnorm

import (
	"zeros3-z3/graphcore"
	"zeros3-z3/simdelta"
)

// resolver adapts a store of simdelta wires (full objects or delta chains of depth <= 2) to the
// logical-bytes resolver the portable core expects.
func resolver(get func([32]byte) ([]byte, error)) func([32]byte) ([]byte, error) {
	budget := int64(1) << 30
	return func(ref [32]byte) ([]byte, error) { return simdelta.Resolve(ref, get, 2, budget) }
}

// ReplayLLVM rebuilds a bitcode artifact from stored objects (research adapter around the core).
func ReplayLLVM(r *graphcore.LLVMRecipe, get func([32]byte) ([]byte, error)) ([]byte, error) {
	return r.Replay(resolver(get))
}

// ReplayProg rebuilds a program-style artifact (PDB, DEX) from stored objects.
func ReplayProg(r *graphcore.ProgRecipe, get func([32]byte) ([]byte, error)) ([]byte, error) {
	return r.Replay(resolver(get))
}
