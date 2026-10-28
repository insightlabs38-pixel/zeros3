package idxref

import "encoding/binary"

// Repeated-reference coders (Z3-33). A reference is a self-delimiting byte string (a PrefixIndex /
// ClassIndex name); the same target is named many times inside one component. These coders turn
// the sequence of reference byte strings of one component stream into a shorter byte string using
// only state that lives and dies with that component: no store-wide ids, no persistent table. An
// unchanged component therefore stays byte-identical whatever happens elsewhere.

// RefRec is one emitted reference (analysis): the component it landed in, its stream and class,
// and its bytes.
type RefRec struct {
	Comp   int
	Stream uint8
	Class  uint8
	B      []byte
}

// DictEncode codes refs as an adaptive component-local dictionary: the first occurrence of a
// reference is the code len(dict) followed by its bytes, a repeat is the dictionary ordinal.
// Ordinals are uvarints (one byte below 128 distinct targets).
func DictEncode(refs [][]byte) []byte {
	var out []byte
	idx := map[string]uint64{}
	for _, r := range refs {
		if i, ok := idx[string(r)]; ok {
			out = binary.AppendUvarint(out, i)
			continue
		}
		n := uint64(len(idx))
		idx[string(r)] = n
		out = binary.AppendUvarint(out, n)
		out = append(out, r...)
	}
	return out
}

// DictDecode inverts DictEncode; lenOf reports the length of the reference that starts at b
// (the PrefixIndex tells it: a reference is self-delimiting).
func DictDecode(code []byte, lenOf func(b []byte) int) ([][]byte, error) {
	var out [][]byte
	var dict [][]byte
	for len(code) > 0 {
		v, n := binary.Uvarint(code)
		if n <= 0 || v > uint64(len(dict)) {
			return nil, bad("dictionary ordinal")
		}
		code = code[n:]
		if v < uint64(len(dict)) {
			out = append(out, dict[v])
			continue
		}
		l := lenOf(code)
		if l <= 0 || l > len(code) {
			return nil, bad("dictionary entry")
		}
		dict = append(dict, code[:l])
		out = append(out, code[:l])
		code = code[l:]
	}
	return out, nil
}

// MTFEncode codes refs with a move-to-front cache of size n (n <= 250): a hit is the one-byte
// recency rank, a miss is the byte 255 followed by the reference bytes.
func MTFEncode(refs [][]byte, n int) []byte {
	var out []byte
	var cache [][]byte
	for _, r := range refs {
		hit := -1
		for i, c := range cache {
			if string(c) == string(r) {
				hit = i
				break
			}
		}
		if hit >= 0 {
			out = append(out, byte(hit))
			copy(cache[1:hit+1], cache[:hit])
			cache[0] = r
			continue
		}
		out = append(out, 255)
		out = append(out, r...)
		if len(cache) < n {
			cache = append(cache, nil)
		}
		copy(cache[1:], cache[:len(cache)-1])
		cache[0] = r
	}
	return out
}

// AutoK0 is the identity prefix length for a table of n candidates: the smallest k (1..3) for
// which a K-byte prefix leaves at most `pct` percent of the candidates sharing it with another
// (the decoder knows n, so the length is implied).
func AutoK0(n, pct int) int {
	cap := uint64(1)
	for k := 1; k <= 3; k++ {
		cap <<= 8
		if uint64(n)*100 <= uint64(pct)*cap {
			return k
		}
	}
	return 3
}

// DictSplitEncode codes refs as the list of distinct references (first-occurrence order)
// followed by one uvarint ordinal per reference: the hashes and the small integers sit in two
// runs, so the pack stage can compress them separately.
func DictSplitEncode(refs [][]byte) []byte {
	idx := map[string]uint64{}
	var uniq, ord []byte
	nu := 0
	for _, r := range refs {
		i, ok := idx[string(r)]
		if !ok {
			i = uint64(nu)
			nu++
			idx[string(r)] = i
			uniq = append(uniq, r...)
		}
		ord = binary.AppendUvarint(ord, i)
	}
	out := binary.AppendUvarint(nil, uint64(nu))
	out = append(out, uniq...)
	return append(out, ord...)
}
