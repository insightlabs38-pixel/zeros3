package repz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// ScanIndex reads one record section in bounded pieces, verifying its length
// against the representation, its SHA-256 against its address and the strict
// ascending key order, and calls fn for every record. A section that fails any
// check yields an error before the caller can trust any record from it.
func ScanIndex(src Source, x Index, fn func(key []byte, target SHA) error) error {
	if x.Count < 1 || x.Count > maxIndexCount || x.KeyWidth < 1 || x.KeyWidth > maxKeyWidth {
		return corrupt("index geometry %d x %d", x.KeyWidth, x.Count)
	}
	size, err := src.Size(x.Ref)
	if err != nil {
		return err
	}
	if size != x.Bytes() {
		return fmt.Errorf("%w: index component is %d bytes, recipe says %d", ErrMismatch, size, x.Bytes())
	}
	w := int64(x.KeyWidth + 32)
	per := max(int64(1), (64<<10)/w)
	buf := make([]byte, per*w)
	h := sha256.New()
	var prev []byte
	for rec := int64(0); rec < x.Count; {
		n := min(per, x.Count-rec)
		b := buf[:n*w]
		if err := readFull(src, x.Ref, b, rec*w); err != nil {
			return err
		}
		h.Write(b)
		for i := int64(0); i < n; i++ {
			r := b[i*w : (i+1)*w]
			key := r[:x.KeyWidth]
			if prev != nil && bytes.Compare(prev, key) >= 0 {
				return corrupt("index %x: keys not strictly ascending at record %d", x.Ref[:6], rec+i)
			}
			prev = append(prev[:0], key...)
			var t SHA
			copy(t[:], r[x.KeyWidth:])
			if err := fn(key, t); err != nil {
				return err
			}
		}
		rec += n
	}
	var sum SHA
	h.Sum(sum[:0])
	if sum != x.Ref {
		return fmt.Errorf("%w: index %x bytes do not hash to their address", ErrMismatch, x.Ref[:6])
	}
	return nil
}
