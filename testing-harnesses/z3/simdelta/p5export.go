package simdelta

// Z4-P5 measurement hooks. These expose the unchanged B1 matcher as the
// reference for the optimized dcore matcher and do not alter any B1 behaviour.

// RefOp is one COPY (Base >= 0) or ADD (Base < 0) of the reference matcher.
type RefOp struct {
	Base, Off, N int
	Lit          []byte
}

// MatchRef runs the unmodified B1 matcher with one of its named algorithm
// parameter sets ("rolling", "git", "adaptive") and returns its program.
func MatchRef(algorithm string, bases [][]byte, target []byte) []RefOp {
	anchor, stride, slots := 24, 8, 1
	switch algorithm {
	case "git":
		anchor, stride, slots = 16, 4, 2
	case "adaptive":
		anchor, stride, slots = 8, 4, 4
	}
	var out []RefOp
	for _, o := range match(bases, target, anchor, stride, slots) {
		out = append(out, RefOp{o.base, o.off, o.n, o.lit})
	}
	return out
}
