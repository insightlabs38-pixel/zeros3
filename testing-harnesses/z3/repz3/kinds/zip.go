package kinds

import (
	"crypto/sha256"
	"fmt"
	"io"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/torchz3"
)

// ZIPStats distinguishes structural payload opportunity from opaque compression.
type ZIPStats struct {
	Members, Stored, Deflated, Derived int
	Framing, Payload                   int64
}

// EncodeZIPAt preserves exact ZIP framing with existing Concat/DERIVED bytes.
// Method-8 streams and signing material remain opaque. A bounded stored member
// may use child; decode uses only serialized bundles, never that callback.
// Bundle object collection is lab-owned; a production port would stream put
// callbacks to CAS exactly as the I1B codec adapters do.
func EncodeZIPAt(src io.ReaderAt, size int64, child func([]byte) (*Bundle, error)) (*Bundle, ZIPStats, error) {
	f, err := torchz3.Parse(src, size, torchz3.Limits{MaxEntries: 1 << 16, MaxCD: 32 << 20, MaxName: 4096, AllowDeflate: true, AllowOpaqueTail: true})
	var st ZIPStats
	if err != nil {
		return nil, st, err
	}
	st.Members = len(f.Members)
	members := make(map[int64]torchz3.Member, len(f.Members))
	for _, m := range f.Members {
		members[m.DataBegin] = m
		if m.Method == 0 {
			st.Stored++
		} else {
			st.Deflated++
		}
	}
	out := &repz3.Rep{Kind: repz3.KindConcat, Size: size}
	var objs [][]byte
	var children []*Bundle
	for _, segment := range f.Segments() {
		n := segment.End - segment.Begin
		if n < 0 {
			return nil, st, fmt.Errorf("ZIP segment order")
		}
		if n == 0 {
			continue
		}
		if m, ok := members[segment.Begin]; ok && segment.End == m.DataEnd {
			st.Payload += n
			if child != nil && m.Method == 0 && m.Flags&1 == 0 && n <= 1<<20 {
				data := make([]byte, n)
				if _, err := src.ReadAt(data, segment.Begin); err != nil {
					return nil, st, err
				}
				kid, err := child(data)
				if err == nil && kid != nil {
					out.Members = append(out.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(data), Size: n, Child: kid.ID})
					children = append(children, kid)
					st.Derived++
					continue
				}
			}
		} else {
			st.Framing += n
		}
		err := lab.ChunkCDC(io.NewSectionReader(src, segment.Begin, n), func(_ int64, data []byte) error {
			copyData := append([]byte(nil), data...)
			out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(copyData), Size: int64(len(copyData))})
			objs = append(objs, copyData)
			return nil
		})
		if err != nil {
			return nil, st, err
		}
	}
	b, err := newBundle(out, objs, children...)
	return b, st, err
}
