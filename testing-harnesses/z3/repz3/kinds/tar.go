package kinds

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"zeros3-z3/repz3"
)

// The compound outer is an uncompressed tar layer (an OCI layer media type that is
// a plain tar) expressed as a KindConcat representation: each 512-byte header is a
// STORED component, an ordinary member body (with its block padding) is a STORED
// component, and a member body that is a SQLite database is a DERIVED member whose
// child is the SQLite representation and whose frame is the zero padding. The
// end-of-archive blocks are one STORED component. No tar semantics are in the
// contract; this splitter is the adapter.
//
// A gzip layer is not migrated: its DEFLATE skeleton still lives in the Z3-17
// closures (see the report).

const tarBlock = 512

// TarStats describes a layer analysis.
type TarStats struct {
	Entries, Derived int
}

// EncodeTarLayer splits an uncompressed tar archive. Bodies of at least minDerived
// bytes that begin with the SQLite magic become DERIVED members.
func EncodeTarLayer(tar []byte, minDerived int64) (*Bundle, *TarStats, error) {
	out := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(tar))}
	var objs [][]byte
	var kids []*Bundle
	st := &TarStats{}
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
		if size >= minDerived && allZero(pad) {
			var child *Bundle
			var err error
			switch {
			case bytes.HasPrefix(data, []byte("SQLite format 3\x00")):
				child, _, err = EncodeSQLite(bytes.NewReader(data), size)
			case isCAR(data):
				child, _, err = EncodeCAR(bytes.NewReader(data), size)
			default:
				err = errNoInner
			}
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
	b, err := newBundle(out, objs, kids...)
	return b, st, err
}

var errNoInner = fmt.Errorf("kinds: no inner representation for this member")

// isCAR is the Z3-19 sniff of a CARv1 header (varint length, DAG-CBOR map of 2) or a
// CARv2 pragma (map of 1); EncodeCAR still has to parse the whole archive.
func isCAR(b []byte) bool { return len(b) >= 64 && b[0] < 0x80 && (b[1] == 0xa2 || b[1] == 0xa1) }

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
