package sstz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"zeros3-z3/zstdz3"
)

// Frame analysis/replay mechanically extracted from parquetz3/codec.go. Only
// error/return types and the tighter per-SST-block budgets differ.
func zstdLimits(lim Limits) zstdz3.Limits {
	lim = lim.norm()
	return zstdz3.Limits{MaxFrames: 1, MaxBlocks: 1 << 16, MaxWindow: lim.MaxDecoded, MaxFrameDecoded: lim.MaxDecoded, MaxTotalDecoded: lim.MaxDecoded, MaxLiterals: lim.MaxDecoded, MaxSequences: lim.MaxDecoded, MaxIRBytes: lim.MaxRecipe, MaxSkippable: lim.MaxBlockBytes}
}
func analyzeZstdFrame(payload []byte, want int64, lim Limits) ([]byte, []byte, error) {
	var plain, units bytes.Buffer
	res, err := zstdz3.Parse(bytes.NewReader(payload), &plain, &units, zstdz3.Options{Limits: zstdLimits(lim)})
	if err != nil {
		return nil, nil, corrupt("zstd block: %v", err)
	}
	if int64(plain.Len()) != want {
		return nil, nil, corrupt("zstd block decodes to %d bytes, header says %d", plain.Len(), want)
	}
	z := appendUv(nil, uint64(len(res.Raw)))
	z = append(append(z, res.Raw...), units.Bytes()...)
	recipe := z
	var out bytes.Buffer
	if err := replayZstdFrame(&out, recipe, plain.Bytes(), lim); err != nil || !bytes.Equal(out.Bytes(), payload) {
		return nil, nil, fmt.Errorf("%w: zstd replay differs from the block (%v)", ErrUnsupported, err)
	}
	return plain.Bytes(), recipe, nil
}

func replayZstdFrame(w io.Writer, recipe []byte, body []byte, lim Limits) error {
	n, k := binary.Uvarint(recipe)
	if k <= 0 || n > uint64(len(recipe)-k) {
		return recipeErr("zstd recipe")
	}
	skel := recipe[k : k+int(n)]
	units := recipe[k+int(n):]
	_, err := zstdz3.Replay(w, skel, bytes.NewReader(units), bytes.NewReader(body), zstdLimits(lim))
	return err
}

// RocksDB/Pebble format-2 compression adds a varint decoded length before the
// standard frame. Its original bytes are retained, including varint spelling.
func analyzeZstdBlock(payload []byte, lim Limits) ([]byte, []byte, error) {
	lim = lim.norm()
	n, k := binary.Uvarint(payload)
	if k <= 0 || n == 0 || n > uint64(lim.MaxDecoded) {
		return nil, nil, unsupported("zstd block decoded-size prefix")
	}
	plain, frame, e := analyzeZstdFrame(payload[k:], int64(n), lim)
	if e != nil {
		return nil, nil, e
	}
	recipe := appendUv(nil, uint64(k))
	recipe = append(recipe, payload[:k]...)
	recipe = append(recipe, frame...)
	return plain, recipe, nil
}
func replayZstdBlock(w io.Writer, recipe, plain []byte, lim Limits) error {
	n, k := binary.Uvarint(recipe)
	if k <= 0 || n == 0 || n > 10 || n > uint64(len(recipe)-k) {
		return recipeErr("zstd prefix recipe")
	}
	prefix := recipe[k : k+int(n)]
	want, l := binary.Uvarint(prefix)
	if l != len(prefix) || want != uint64(len(plain)) {
		return recipeErr("zstd decoded size")
	}
	if n, e := w.Write(prefix); e != nil {
		return e
	} else if n != len(prefix) {
		return io.ErrShortWrite
	}
	return replayZstdFrame(w, recipe[k+int(n):], plain, lim)
}
