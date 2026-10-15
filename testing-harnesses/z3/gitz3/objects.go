package gitz3

import (
	"strconv"

	"zeros3-z3/deflate"
)

// CanonHeader returns "<type> <size>\x00".
func CanonHeader(t ObjType, size int64) []byte {
	b := make([]byte, 0, 24)
	b = append(b, t.String()...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, size, 10)
	return append(b, 0)
}

// Canonical returns the exact object bytes Git hashes: header || payload.
func Canonical(t ObjType, payload []byte) []byte {
	return append(CanonHeader(t, int64(len(payload))), payload...)
}

// ObjectID is the native identity of an object.
func ObjectID(a Algo, t ObjType, payload []byte) []byte {
	h := a.New()
	h.Write(CanonHeader(t, int64(len(payload))))
	h.Write(payload)
	return h.Sum(nil)
}

// ParseCanonHeader parses a canonical header at the start of b and returns the
// type, the declared size and the header length (through the NUL). The size must
// be canonical decimal (no sign, no leading zeros, at most 18 digits).
func ParseCanonHeader(b []byte) (ObjType, int64, int, error) {
	var t ObjType
	i := 0
	switch {
	case hasPrefix(b, "commit "):
		t, i = Commit, 7
	case hasPrefix(b, "tree "):
		t, i = Tree, 5
	case hasPrefix(b, "blob "):
		t, i = Blob, 5
	case hasPrefix(b, "tag "):
		t, i = Tag, 4
	default:
		return 0, 0, 0, corrupt("object type")
	}
	j := i
	var size int64
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		if j-i >= 18 {
			return 0, 0, 0, corrupt("object size too long")
		}
		size = size*10 + int64(b[j]-'0')
		j++
	}
	if j == i || (b[i] == '0' && j-i > 1) {
		return 0, 0, 0, corrupt("object size digits")
	}
	if j >= len(b) || b[j] != 0 {
		return 0, 0, 0, corrupt("object header terminator")
	}
	return t, size, j + 1, nil
}

func hasPrefix(b []byte, s string) bool { return len(b) >= len(s) && string(b[:len(s)]) == s }

// Loose is a decoded loose object file.
type Loose struct {
	Type    ObjType
	Size    int64
	Z       *deflate.Zlib // exact DEFLATE parse of the file (plaintext = Canon)
	Canon   []byte        // header || payload
	HdrLen  int
	Payload []byte
	ID      []byte
}

// ParseLoose decodes a loose object file: a zlib stream over the canonical
// object bytes. The declared size must equal the payload length. The caller
// compares ID with the file name when it has one (VerifyID).
func ParseLoose(a Algo, file []byte, lim Limits) (*Loose, error) {
	lim = lim.norm()
	if int64(len(file)) > lim.MaxObjectSize+1<<20 {
		return nil, limit("loose file %d bytes", len(file))
	}
	z, err := deflate.ParseZlib(file, deflate.Options{MaxOutput: int(min(lim.MaxObjectSize+64, 1<<31-1))})
	if err != nil {
		return nil, err
	}
	canon := z.Stream.Plain
	t, size, hl, err := ParseCanonHeader(canon)
	if err != nil {
		return nil, err
	}
	if size != int64(len(canon)-hl) {
		return nil, corrupt("declared size %d, payload %d", size, len(canon)-hl)
	}
	if size > lim.MaxObjectSize {
		return nil, limit("object size %d", size)
	}
	return &Loose{Type: t, Size: size, Z: z, Canon: canon, HdrLen: hl, Payload: canon[hl:], ID: a.Sum(canon)}, nil
}

// VerifyID checks the native ID against an expected one (e.g. the loose file name).
func (l *Loose) VerifyID(want []byte) error {
	if string(l.ID) != string(want) {
		return ErrMismatch
	}
	return nil
}
