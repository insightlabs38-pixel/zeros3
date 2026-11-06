package semcas

import (
	"bytes"
	"encoding/binary"
	"errors"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
)

// EncodeDeflate analyses a raw DEFLATE stream and returns the exact residual (skeleton and
// units) that, with the stream's plaintext, regenerates the original compressed bytes.
// The residual is verified by replay; ok is false if exact replay is unavailable.
func EncodeDeflate(raw []byte) (plain, residual []byte, ok bool) {
	st, err := deflate.Parse(raw, deflate.Options{MaxOutput: 1 << 30})
	if err != nil || st.Consumed != len(raw) {
		return nil, nil, false
	}
	return encodeStream(st, raw)
}

func encodeStream(st *deflate.Stream, raw []byte) ([]byte, []byte, bool) {
	pol := ir.Policy{Kind: ir.PStream}
	if len(st.Plain) > 64<<10 {
		pol = ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}
	}
	pk, err := ir.Encode(st, ir.Config{Codec: ir.Pred, Policy: pol, Profile: ir.AutoProfile})
	if err != nil {
		return nil, nil, false
	}
	res := binary.AppendUvarint(nil, uint64(len(pk.Skeleton)))
	res = append(res, pk.Skeleton...)
	res = binary.AppendUvarint(res, uint64(len(pk.Units)))
	for _, u := range pk.Units {
		res = binary.AppendUvarint(res, uint64(len(u)))
		res = append(res, u...)
	}
	out, err := inflateReplay(st.Plain, res)
	if err != nil || !bytes.Equal(out, raw) {
		return nil, nil, false
	}
	return st.Plain, res, true
}

// inflateReplay regenerates the compressed bytes from plaintext and residual.
func inflateReplay(plain, res []byte) ([]byte, error) {
	pos := 0
	u := func() (int, error) {
		v, n := binary.Uvarint(res[pos:])
		if n <= 0 || v > uint64(len(res)) {
			return 0, errors.New("semcas: bad deflate residual")
		}
		pos += n
		return int(v), nil
	}
	sl, err := u()
	if err != nil || sl > len(res)-pos {
		return nil, errors.New("semcas: bad deflate residual")
	}
	skel := res[pos : pos+sl]
	pos += sl
	nu, err := u()
	if err != nil || nu > len(res) {
		return nil, errors.New("semcas: bad deflate residual")
	}
	units := make([][]byte, nu)
	for i := range units {
		l, err := u()
		if err != nil || l > len(res)-pos {
			return nil, errors.New("semcas: bad deflate residual")
		}
		units[i] = res[pos : pos+l]
		pos += l
	}
	var out bytes.Buffer
	err = ir.Reconstruct(&out, skel, func(k int) ([]byte, error) {
		if k >= len(units) {
			return nil, errors.New("semcas: missing deflate unit")
		}
		return units[k], nil
	}, plain, ir.DecodeOptions{})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
