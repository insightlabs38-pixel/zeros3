package deflate

// Small exported helpers for the Z3 reconstruction-IR package, so that it
// applies exactly the parser's acceptance rules instead of re-deriving them.

// ValidLengths reports whether lens form a Huffman code the parser accepts:
// complete, or a single length-1 code, or (if allowEmpty) no codes at all.
func ValidLengths(lens []uint8, allowEmpty bool) bool {
	d, ok := buildDecoder(lens)
	return ok && (allowEmpty || !d.empty)
}

// CLOrder is the transmission order of the 19 code-length code lengths.
func CLOrder() [19]uint8 { return clOrder }

// LengthExtraBits is the number of extra bits of length symbol sym (257..285).
func LengthExtraBits(sym int) int { return int(lenExtra[sym-257]) }

// DistExtraBits is the number of extra bits of distance code dc (0..29).
func DistExtraBits(dc int) int { return int(distExtr[dc]) }

// LengthBase and DistBase give the smallest length/distance of a code.
func LengthBase(sym int) int { return lenBase[sym-257] }
func DistBase(dc int) int    { return distBase[dc] }
