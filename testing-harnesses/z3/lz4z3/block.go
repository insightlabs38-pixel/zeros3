package lz4z3

import "bytes"

// Seq is one LZ4 sequence: Lit literal bytes, then a match of Match bytes (>= 4) at Off.
// The final sequence of a block has Match == 0 and Off == 0.
type Seq struct {
	Lit, Match, Off int
}

const (
	minMatch = 4
	maxOff   = 65535
)

// Decode parses and decodes one raw LZ4 block. want < 0 means the decoded size is unknown;
// otherwise decoding must end at exactly want bytes. It returns the payload and the exact
// sequence list. Offsets must lie inside already-produced output (no dictionary).
func Decode(block []byte, want int, lim Limits) ([]byte, []Seq, error) {
	return DecodeH(block, nil, want, lim)
}

// DecodeH is Decode for a block that may reference up to 64 KiB of preceding history
// (linked frame blocks). The returned payload excludes the history.
func DecodeH(block, hist []byte, want int, lim Limits) ([]byte, []Seq, error) {
	lim = lim.norm()
	if len(hist) > maxOff {
		hist = hist[len(hist)-maxOff:]
	}
	nh := len(hist)
	if want > lim.MaxDecoded {
		return nil, nil, ErrLimit
	}
	out := make([]byte, nh, nh+max(want, 0))
	copy(out, hist)
	var seqs []Seq
	ip := 0
	for {
		if ip >= len(block) {
			return nil, nil, corrupt("block ends before a token")
		}
		tok := block[ip]
		ip++
		lit := int(tok >> 4)
		if lit == 15 {
			n, k, err := extLen(block[ip:])
			if err != nil {
				return nil, nil, err
			}
			ip += k
			lit += n
		}
		if lit < 0 || lit > len(block)-ip {
			return nil, nil, corrupt("literal run %d exceeds block", lit)
		}
		if len(out)-nh+lit > lim.MaxDecoded {
			return nil, nil, ErrLimit
		}
		out = append(out, block[ip:ip+lit]...)
		ip += lit
		if len(seqs) >= lim.MaxSeqs {
			return nil, nil, ErrLimit
		}
		if ip == len(block) { // final literal-only sequence
			if tok&15 != 0 {
				return nil, nil, corrupt("final sequence carries a match nibble")
			}
			seqs = append(seqs, Seq{Lit: lit})
			break
		}
		if len(block)-ip < 2 {
			return nil, nil, corrupt("truncated offset")
		}
		off := int(block[ip]) | int(block[ip+1])<<8
		ip += 2
		if off == 0 || off > len(out) {
			return nil, nil, corrupt("offset %d with %d bytes of history", off, len(out))
		}
		ml := int(tok & 15)
		if ml == 15 {
			n, k, err := extLen(block[ip:])
			if err != nil {
				return nil, nil, err
			}
			ip += k
			ml += n
		}
		ml += minMatch
		if ml < 0 || len(out)-nh+ml > lim.MaxDecoded {
			return nil, nil, ErrLimit
		}
		if off >= ml {
			out = append(out, out[len(out)-off:len(out)-off+ml]...)
		} else {
			for i := 0; i < ml; i++ { // overlapping copy repeats the last 'off' bytes
				out = append(out, out[len(out)-off])
			}
		}
		seqs = append(seqs, Seq{Lit: lit, Match: ml, Off: off})
	}
	if want >= 0 && len(out)-nh != want {
		return nil, nil, corrupt("decoded %d bytes, expected %d", len(out)-nh, want)
	}
	return out[nh:], seqs, nil
}

// extLen reads a 255-extension length. It rejects values that overflow an int32.
func extLen(b []byte) (n, k int, err error) {
	for {
		if k >= len(b) {
			return 0, 0, corrupt("truncated length extension")
		}
		c := int(b[k])
		k++
		n += c
		if n > 1<<31-1 {
			return 0, 0, ErrLimit
		}
		if c != 255 {
			return n, k, nil
		}
	}
}

func appendExt(dst []byte, n int) []byte {
	for ; n >= 255; n -= 255 {
		dst = append(dst, 255)
	}
	return append(dst, byte(n))
}

// Build rebuilds the exact block from the payload and its sequence list.
func Build(plain []byte, seqs []Seq) ([]byte, error) { return BuildH(nil, plain, seqs) }

// BuildH is Build for a block whose matches may reach into hist (the preceding payload).
func BuildH(hist, plain []byte, seqs []Seq) ([]byte, error) {
	if len(hist) > maxOff {
		hist = hist[len(hist)-maxOff:]
	}
	nh := len(hist)
	out := make([]byte, 0, len(plain)/2+16)
	pos := 0
	for i, s := range seqs {
		last := i == len(seqs)-1
		if s.Lit < 0 || s.Lit > len(plain)-pos {
			return nil, recipeErr("literal run %d at %d of %d", s.Lit, pos, len(plain))
		}
		if last != (s.Match == 0) || (s.Match == 0 && s.Off != 0) {
			return nil, recipeErr("sequence %d: only the final sequence may be literal-only", i)
		}
		if !last && (s.Match < minMatch || s.Off < 1 || s.Off > maxOff) {
			return nil, recipeErr("sequence %d: match %d offset %d", i, s.Match, s.Off)
		}
		var tok byte
		if s.Lit >= 15 {
			tok = 15 << 4
		} else {
			tok = byte(s.Lit) << 4
		}
		ml := s.Match - minMatch
		if s.Match > 0 {
			if ml >= 15 {
				tok |= 15
			} else {
				tok |= byte(ml)
			}
		}
		out = append(out, tok)
		if s.Lit >= 15 {
			out = appendExt(out, s.Lit-15)
		}
		out = append(out, plain[pos:pos+s.Lit]...)
		pos += s.Lit
		if s.Match > 0 {
			if s.Off > pos+nh {
				return nil, recipeErr("sequence %d: offset %d beyond %d bytes of history", i, s.Off, pos+nh)
			}
			if s.Match > len(plain)-pos {
				return nil, recipeErr("sequence %d: match runs past the payload", i)
			}
			if pos >= s.Off && s.Off >= s.Match {
				if !bytes.Equal(plain[pos:pos+s.Match], plain[pos-s.Off:pos-s.Off+s.Match]) {
					return nil, recipeErr("sequence %d: payload differs from the match source", i)
				}
			} else {
				for j := 0; j < s.Match; j++ { // the payload must contain the copy the block implies
					k := pos + j - s.Off
					src := byte(0)
					if k >= 0 {
						src = plain[k]
					} else {
						src = hist[nh+k]
					}
					if plain[pos+j] != src {
						return nil, recipeErr("sequence %d: payload differs from the match source", i)
					}
				}
			}
			out = append(out, byte(s.Off), byte(s.Off>>8))
			if ml >= 15 {
				out = appendExt(out, ml-15)
			}
			pos += s.Match
		}
	}
	if len(seqs) == 0 || pos != len(plain) {
		return nil, recipeErr("sequences cover %d of %d bytes", pos, len(plain))
	}
	return out, nil
}
