package nativerescue

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

type Frozen struct {
	Root    [32]byte
	Objects map[[32]byte][]byte
}

// Freeze is an experimental storage envelope, not a repz3/production kind.
// Content-addressed index nodes name every component in order. The root contains
// only the recipe and node IDs; no analysis closure is required by Replay.
func Freeze(meta []byte, comps [][]byte) (Frozen, error) {
	f := Frozen{Objects: map[[32]byte][]byte{}}
	if len(meta) > 32<<20 || len(comps) > maxParts {
		return f, fmt.Errorf("native: freeze bounds")
	}
	put := func(b []byte) [32]byte { id := sha256.Sum256(b); f.Objects[id] = append([]byte(nil), b...); return id }
	var nodes [][32]byte
	var node []byte
	count := 0
	flush := func() {
		if len(node) > 0 {
			nodes = append(nodes, put(node))
			node = nil
			count = 0
		}
	}
	total := 0
	for _, c := range comps {
		if len(c) > 32<<20 || len(c) > 2*MaxNative-total {
			return f, fmt.Errorf("native: freeze component bound")
		}
		total += len(c)
		id := put(c)
		node = append(node, id[:]...)
		count++
		if (int(id[0])<<8|int(id[1]))%128 == 0 || count >= 512 {
			flush()
		}
	}
	flush()
	root := []byte("NBF1")
	root = uv(root, len(meta))
	root = append(root, meta...)
	root = uv(root, len(nodes))
	for _, id := range nodes {
		root = append(root, id[:]...)
	}
	if len(root) > 40<<20 {
		return f, fmt.Errorf("native: envelope root bound")
	}
	f.Root = put(root)
	return f, nil
}

// Replay fetches and verifies every physical object by address before Decode.
func Replay(root [32]byte, get func([32]byte) ([]byte, error)) ([]byte, error) {
	fetch := func(id [32]byte, max int) ([]byte, error) {
		b, e := get(id)
		if e != nil {
			return nil, e
		}
		if len(b) > max {
			return nil, fmt.Errorf("native: fetched object bound")
		}
		if sha256.Sum256(b) != id {
			return nil, fmt.Errorf("native: corrupt addressed object")
		}
		return b, nil
	}
	raw, e := fetch(root, 40<<20)
	if e != nil {
		return nil, e
	}
	if len(raw) < 4 || !bytes.Equal(raw[:4], []byte("NBF1")) {
		return nil, fmt.Errorf("native: envelope magic")
	}
	r := &reader{b: raw[4:]}
	n := r.uv()
	if n > 32<<20 || n > uint64(len(r.b)) {
		return nil, fmt.Errorf("native: envelope recipe bound")
	}
	meta := r.take(int(n))
	nn := r.uv()
	if nn > maxParts || nn*32 != uint64(len(r.b)) {
		return nil, fmt.Errorf("native: envelope node count")
	}
	var comps [][]byte
	total := 0
	for i := uint64(0); i < nn; i++ {
		var id [32]byte
		copy(id[:], r.take(32))
		node, e := fetch(id, 512*32)
		if e != nil {
			return nil, e
		}
		if len(node) == 0 || len(node)%32 != 0 {
			return nil, fmt.Errorf("native: envelope node shape")
		}
		for at := 0; at < len(node); at += 32 {
			if len(comps) >= maxParts {
				return nil, fmt.Errorf("native: envelope component count")
			}
			copy(id[:], node[at:at+32])
			b, e := fetch(id, 32<<20)
			if e != nil {
				return nil, e
			}
			if len(b) > 2*MaxNative-total {
				return nil, fmt.Errorf("native: envelope total budget")
			}
			total += len(b)
			comps = append(comps, b)
		}
	}
	if r.err != nil || len(r.b) != 0 {
		return nil, fmt.Errorf("native: envelope framing")
	}
	return Decode(meta, comps)
}
func (f Frozen) Get(id [32]byte) ([]byte, error) {
	b, ok := f.Objects[id]
	if !ok {
		return nil, fmt.Errorf("native: missing object")
	}
	return b, nil
}
func Proof(meta []byte, comps [][]byte, want []byte) error {
	f, e := Freeze(meta, comps)
	if e != nil {
		return e
	}
	out, e := Replay(f.Root, f.Get)
	if e != nil {
		return e
	}
	if len(out) != len(want) || sha256.Sum256(out) != sha256.Sum256(want) || !bytes.Equal(out, want) {
		return fmt.Errorf("native: closed replay mismatch")
	}
	return nil
}
