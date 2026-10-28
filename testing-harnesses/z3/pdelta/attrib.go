package pdelta

// Attr is the Z3-32 pairwise attribution (cmd/z3mb attribute) with the byte categories left to
// the caller: the literal bytes of cur against base, before and after the format's reference
// slots were masked in both files, split by the category of each byte of cur.
type Attr struct {
	Size      int
	Raw       int    // literal bytes of cur against base, unmasked
	Masked    int    // literal bytes after masking
	Remaining [8]int // masked-literal bytes by category (the change that is really there)
	Removed   [8]int // raw-literal bytes that masking removed, by category
}

// Attribute runs Runs on (base, cur) and on (baseM, curM) and attributes every literal byte of
// cur to cat[i].
func Attribute(base, cur, baseM, curM []byte, cat []uint8) Attr {
	mark := func(rs [][2]int) []bool {
		m := make([]bool, len(cur))
		for _, r := range rs {
			for i := r[0]; i < r[1] && i < len(m); i++ {
				m[i] = true
			}
		}
		return m
	}
	A := mark(Runs(base, cur, true).Runs)
	B := mark(Runs(baseM, curM, true).Runs)
	r := Attr{Size: len(cur)}
	for i := range cur {
		if A[i] {
			r.Raw++
		}
		if B[i] {
			r.Masked++
			r.Remaining[cat[i]]++
		} else if A[i] {
			r.Removed[cat[i]]++
		}
	}
	return r
}
