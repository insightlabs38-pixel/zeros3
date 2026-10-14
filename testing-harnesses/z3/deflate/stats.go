package deflate

// Stats are structural counters of a parsed stream, for sizing later IR work.
type Stats struct {
	CompressedBytes int
	PlainBytes      int

	Blocks, StoredBlocks, FixedBlocks, DynamicBlocks int
	EmptyBlocks                                      int // no plaintext produced

	Tokens, Literals, Matches int
	MatchBytes                int // plaintext bytes produced by matches
	MaxDistance, MaxLength    int
	NonCanonicalLengths       int // length 258 coded as symbol 284 + extra 31
	StoredBytes               int

	DynHeaderBits int64 // dynamic headers excluding the 3-bit block header
	TokenBits     int64 // Huffman-coded tokens including end-of-block
	StoredBits    int64 // whole stored blocks including padding and LEN/NLEN
	CLSyms        int   // code-length symbols across dynamic headers
	RepeatSyms    int   // of which 16/17/18

	// NaiveIRBytes is a deliberately crude size of an uncompressed explicit IR
	// (not a codec design): 6 bytes per block, a dynamic header as 3 bits per
	// transmitted code-length code length + 1 byte per code-length symbol
	// (+1 when it has extra bits), 2 bytes per literal run, 4 bytes per match.
	NaiveIRBytes int
}

// Stats computes structural counters.
func (s *Stream) Stats() Stats {
	st := Stats{CompressedBytes: s.Consumed, PlainBytes: len(s.Plain), Blocks: len(s.Blocks)}
	for i := range s.Blocks {
		b := &s.Blocks[i]
		if b.PlainLen == 0 {
			st.EmptyBlocks++
		}
		st.NaiveIRBytes += 6
		switch b.Kind {
		case Stored:
			st.StoredBlocks++
			st.StoredBytes += b.PlainLen
			st.StoredBits += b.EndBit - b.StartBit
			continue
		case Fixed:
			st.FixedBlocks++
		case Dynamic:
			st.DynamicBlocks++
			st.DynHeaderBits += int64(b.HeaderBits)
			st.CLSyms += len(b.CLSyms)
			st.NaiveIRBytes += (int(b.HCLEN)+4)*3/8 + 1
			for _, c := range b.CLSyms {
				st.NaiveIRBytes++
				if c.Sym >= 16 {
					st.RepeatSyms++
					st.NaiveIRBytes++
				}
			}
		}
		st.TokenBits += b.EndBit - b.StartBit - 3 - int64(b.HeaderBits)
		inRun := false
		for _, t := range b.Tokens {
			st.Tokens++
			if t.IsLiteral() {
				st.Literals++
				if !inRun {
					st.NaiveIRBytes += 2
					inRun = true
				}
				continue
			}
			inRun = false
			st.Matches++
			st.NaiveIRBytes += 4
			l, d := t.Length(), t.Distance()
			st.MatchBytes += l
			st.MaxLength, st.MaxDistance = max(st.MaxLength, l), max(st.MaxDistance, d)
			if t.Sym() == 284 && t.LenExtra() == 31 {
				st.NonCanonicalLengths++
			}
		}
	}
	return st
}
