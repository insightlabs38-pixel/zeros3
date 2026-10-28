package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

const tarBlock = 512

func allZero(b []byte) bool { return bytes.Count(b, []byte{0}) == len(b) }

// Mechanically extracted from kinds.EncodeTarLayer; only the child callback
// changes. Native producer snapshots are deterministic USTAR tables, not a new kind.
func encodeSnapshot(tar []byte, childOf func([]byte) (*kinds.Bundle, error)) (*kinds.Bundle, *kinds.TarStats, error) {
	out := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(tar))}
	var objs [][]byte
	var kids []*kinds.Bundle
	st := &kinds.TarStats{}
	stored := func(b []byte) {
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(b), Size: int64(len(b))})
		objs = append(objs, append([]byte(nil), b...))
	}
	pos := 0
	for {
		if pos+tarBlock > len(tar) {
			return nil, nil, fmt.Errorf("kinds: tar ends inside a header at %d", pos)
		}
		hdr := tar[pos : pos+tarBlock]
		if allZero(hdr) {
			break
		}
		field := strings.Trim(string(hdr[124:136]), "\x00 ")
		size, err := strconv.ParseInt(field, 8, 64)
		if err != nil || size < 0 || hdr[124]&0x80 != 0 {
			return nil, nil, fmt.Errorf("kinds: tar entry at %d has no octal size", pos)
		}
		padded := (size + tarBlock - 1) / tarBlock * tarBlock
		if int64(len(tar)-pos-tarBlock) < padded {
			return nil, nil, fmt.Errorf("kinds: tar entry at %d is truncated", pos)
		}
		stored(hdr)
		body := tar[pos+tarBlock : pos+tarBlock+int(padded)]
		pos += tarBlock + int(padded)
		st.Entries++
		if padded == 0 {
			continue
		}
		data, pad := body[:size], body[size:]
		if size > 0 && allZero(pad) {
			var child *kinds.Bundle
			var err error
			child, err = childOf(data)
			if err == nil {
				out.Members = append(out.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(body), Size: padded,
					Child: child.ID, Frame: repz3.Frame{Zeros: len(pad)}})
				kids = append(kids, child)
				st.Derived++
				continue
			}
		}
		stored(body)
	}
	if pos < len(tar) {
		stored(tar[pos:])
	}
	b, err := kinds.NewBundle(out, objs, kids...)
	return b, st, err
}
