package simdelta

import (
	"sync/atomic"

	"zeros3-z3/dcore"
)

// matcherMode selects the COPY/ADD matcher used by the rolling, git and
// adaptive encoders. The wire format and decoder are identical for both: the
// matcher only chooses which (verified) COPY/ADD program is emitted.
var matcherMode atomic.Int32 // 0 = dcore (default), 1 = B1 reference

// SetReferenceMatcher switches the encoders back to the unmodified B1 matcher
// (for reproducing B1 evidence and for differential comparison).
func SetReferenceMatcher(ref bool) {
	if ref {
		matcherMode.Store(1)
	} else {
		matcherMode.Store(0)
	}
}

func dcoreParams(algorithm string) dcore.Params {
	p := dcore.DefaultParams()
	switch algorithm {
	case "git":
		p.Window, p.MinCopy, p.Slots = 16, 16, 2
	case "adaptive":
		p.Window, p.MinCopy, p.Slots = 8, 8, 4
	default: // rolling
		p.Window, p.MinCopy, p.Slots = 24, 24, 1
	}
	return p
}

func matchOps(algorithm string, bases [][]byte, target []byte, anchor, stride, slots int) []op {
	if matcherMode.Load() == 1 {
		return match(bases, target, anchor, stride, slots)
	}
	ops, err := dcore.Match(bases, target, dcoreParams(algorithm))
	if err != nil { // unsupported size: the caller's Encode already bounds inputs
		return []op{{base: -1, lit: target, n: len(target)}}
	}
	out := make([]op, 0, len(ops))
	for _, o := range ops {
		if o.Base < 0 {
			out = append(out, op{lit: o.Lit, n: len(o.Lit)})
		} else {
			out = append(out, op{base: o.Base, off: int(o.Off), n: int(o.N)})
		}
	}
	return out
}
