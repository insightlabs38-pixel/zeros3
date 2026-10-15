package kinds

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/repz3"
	"zeros3-z3/sqlitez3"
)

// Partial reads through the contract: each opens a representation (the
// representation object is read and its digest checked), then touches only the
// components the one logical object needs. They exist to show what the contract
// preserves; they are not a production read path.

// ReadSQLitePage returns page n (0-based) of an opened SQLite representation: the
// representation object, one node, one page component.
func ReadSQLitePage(c *repz3.Call, n int64) ([]byte, error) {
	if c.Rep.Kind != repz3.KindSQLite {
		return nil, errors.New("kinds: not a SQLite representation")
	}
	rec, err := sqliteRecipe(c)
	if err != nil {
		return nil, err
	}
	return sqlitez3.ReadPage(rec, n, func(id sqlitez3.ID) ([]byte, error) { return c.Get(id) }, sqlitez3.Limits{})
}

// ReadCARBlock returns the block with the given CID from an opened CAR
// representation. The block table and order (the whole recipe) are needed to
// locate it; only the one block is fetched.
func ReadCARBlock(c *repz3.Call, cid []byte) ([]byte, error) {
	if c.Rep.Kind != repz3.KindCAR {
		return nil, errors.New("kinds: not a CAR representation")
	}
	rep, err := carRep(c)
	if err != nil {
		return nil, err
	}
	for i := range rep.Refs {
		if bytes.Equal(rep.CIDBytes(i), cid) {
			if rep.Refs[i].Inline != nil {
				return rep.Refs[i].Inline, nil
			}
			return c.Get(rep.Refs[i].Sum)
		}
	}
	return nil, fmt.Errorf("%w: CID not in the archive", repz3.ErrMissing)
}

// ErrDeltaEntry is returned by ReadGitObject for an object stored as a delta: the
// lab read does not follow base chains.
var ErrDeltaEntry = errors.New("kinds: object is stored as a delta; base-chain reads are not implemented")

// ReadGitObject returns the type and payload of a non-delta object by native id:
// an index lookup (O(log n) bounded reads), the entry's first stream component and
// one inflate. An entry whose zlib stream was cut into several components needs
// the stored stream table to find the continuation (the pieces that follow its
// first one); a single-component entry never touches the table. The id itself
// verifies the (type, payload) pair.
func ReadGitObject(c *repz3.Call, id []byte) (typ string, payload []byte, err error) {
	if c.Rep.Kind != repz3.KindGit {
		return "", nil, errors.New("kinds: not a Git representation")
	}
	comp, found, err := c.Resolve(id)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, fmt.Errorf("%w: object id", repz3.ErrMissing)
	}
	first, err := c.GetAny(comp)
	if err != nil {
		return "", nil, err
	}
	pr := &pieceReader{cur: first}
	pr.next = func() ([]byte, error) { // the table is read only when the first piece ends inside the stream
		if pr.tab == nil {
			_, t, err := gitTable(c)
			if err != nil {
				return nil, err
			}
			pr.tab = t
			for i, e := range t {
				if e.sha == comp {
					pr.pos = i + 1
					break
				}
			}
			if pr.pos == 0 {
				return nil, fmt.Errorf("%w: stream not in the table", repz3.ErrCorrupt)
			}
		}
		if pr.pos >= len(pr.tab) {
			return nil, io.ErrUnexpectedEOF
		}
		e := pr.tab[pr.pos]
		pr.pos++
		return c.GetExact(e.sha, e.size)
	}
	zr, err := zlib.NewReader(pr)
	if err != nil {
		return "", nil, err
	}
	payload, err = io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return "", nil, err
	}
	for _, t := range []string{"blob", "tree", "commit", "tag"} {
		hdr := fmt.Sprintf("%s %d\x00", t, len(payload))
		var sum []byte
		if len(id) == 32 {
			h := sha256.Sum256(append([]byte(hdr), payload...))
			sum = h[:]
		} else {
			h := sha1.Sum(append([]byte(hdr), payload...))
			sum = h[:]
		}
		if bytes.Equal(sum, id) {
			return t, payload, nil
		}
	}
	return "", nil, ErrDeltaEntry
}

// pieceReader presents consecutive stream components as one byte stream, fetching
// the next only when the inflater needs a byte beyond the current one.
type pieceReader struct {
	cur  []byte
	off  int
	next func() ([]byte, error)
	tab  []gitEntry
	pos  int
}

func (p *pieceReader) ReadByte() (byte, error) {
	for p.off >= len(p.cur) {
		b, err := p.next()
		if err != nil {
			return 0, err
		}
		p.cur, p.off = b, 0
	}
	p.off++
	return p.cur[p.off-1], nil
}

func (p *pieceReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c, err := p.ReadByte()
	if err != nil {
		return 0, err
	}
	b[0] = c
	n := 1 + copy(b[1:], p.cur[p.off:])
	p.off += n - 1
	return n, nil
}
