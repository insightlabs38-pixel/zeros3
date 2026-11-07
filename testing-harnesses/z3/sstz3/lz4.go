package sstz3

import (
	"encoding/binary"
	"io"
	"zeros3-z3/lz4z3"
)

// Z4-B6: RocksDB LZ4 / LZ4HC data and index blocks (compress_format_version 2): a varint32
// decoded size followed by one raw LZ4 block. The prefix spelling is kept verbatim; the block
// is described by an lz4z3.Recipe (predictor id or explicit sequences), verified at analysis.
func lz4Limits(lim Limits) lz4z3.Limits {
	lim = lim.norm()
	return lz4z3.Limits{MaxDecoded: int(lim.MaxDecoded), MaxSeqs: int(lim.MaxDecoded)/4 + 1}
}

// analyzeLZ4Block returns the decoded block and the serialized recipe
// (uvarint prefix length | prefix bytes | lz4z3 recipe).
func analyzeLZ4Block(payload []byte, lim Limits) ([]byte, []byte, error) {
	lim = lim.norm()
	n, k := binary.Uvarint(payload)
	if k <= 0 || n == 0 || n > uint64(lim.MaxDecoded) {
		return nil, nil, unsupported("lz4 block decoded-size prefix")
	}
	ll := lz4Limits(lim)
	plain, seqs, err := lz4z3.Decode(payload[k:], int(n), ll)
	if err != nil {
		return nil, nil, corrupt("lz4 block: %v", err)
	}
	rc := lz4z3.Analyze(plain, seqs, true)
	rep, err := rc.Replay(nil, plain, ll)
	if err != nil || string(rep) != string(payload[k:]) {
		return nil, nil, unsupported("lz4 recipe does not reproduce the block (%v)", err)
	}
	recipe := appendUv(nil, uint64(k))
	recipe = append(recipe, payload[:k]...)
	recipe = append(recipe, rc.Marshal()...)
	return plain, recipe, nil
}

func replayLZ4Block(w io.Writer, recipe, plain []byte, lim Limits) error {
	lim = lim.norm()
	n, k := binary.Uvarint(recipe)
	if k <= 0 || n == 0 || n > 10 || n > uint64(len(recipe)-k) {
		return recipeErr("lz4 prefix recipe")
	}
	prefix := recipe[k : k+int(n)]
	want, l := binary.Uvarint(prefix)
	if l != len(prefix) || want != uint64(len(plain)) {
		return recipeErr("lz4 decoded size")
	}
	rc, used, err := lz4z3.Unmarshal(recipe[k+int(n):], lz4Limits(lim))
	if err != nil {
		return recipeErr("lz4: %v", err)
	}
	if used != len(recipe)-k-int(n) {
		return recipeErr("lz4 recipe has %d trailing bytes", len(recipe)-k-int(n)-used)
	}
	blk, err := rc.Replay(nil, plain, lz4Limits(lim))
	if err != nil {
		return recipeErr("lz4: %v", err)
	}
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	nw, err := w.Write(blk)
	if err == nil && nw != len(blk) {
		err = io.ErrShortWrite
	}
	return err
}

// lz4Kind returns the lz4z3 recipe kind (0 explicit, 1 predicted, 2 hybrid) of an analyzed recipe.
func lz4Kind(recipe []byte) int {
	return int(recipe[1+int(recipe[0])]) % 3
}
