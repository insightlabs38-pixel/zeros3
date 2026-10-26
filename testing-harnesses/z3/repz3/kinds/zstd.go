package kinds

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"zeros3-z3/repz3"
	"zeros3-z3/zstdz3"
)

// Zstd is the exact Zstandard outer on the contract (Z3-25 lab kind): the stream's bytes
// are rebuilt by zstdz3.Replay from
//
//	the skeleton (frame headers, block records, verbatim table descriptions, residual
//	state) | one unit per block with sequences (match-model coded decisions) or per
//	fallback block (its original bytes) | the semantic plaintext
//
// The plaintext is the last member: a DERIVED member whose child is an ordinary
// representation of the decoded bytes (a tar split, or plain CDC), so the same child
// serves a compressed and an uncompressed layer. The earlier members are the CDC-cut
// decoder table
//
//	uvarint(len skeleton) | skeleton | uvarint(n units) | n x ( uvarint(len) |
//	    len < 64 ? the unit bytes : SHA-256 of the unit )
//
// whose large units are VIA. The recipe is uvarint(table members). Nothing is a
// closure: the decoder dispatches on the serialized kind, streams the child's output
// into zstdz3.Replay through a pipe and fetches units lazily, so memory is the
// replay window and one unit, not the layer. The compressed bytes' length and
// SHA-256 are checked by the caller against the object identity; the content
// checksum inside the stream is recomputed over the plaintext by the replay.

const (
	zsUnitInline = 64
	maxZsTable   = 256 << 20
	// MaxZstdPlain bounds analysis memory (bytes decoded), MaxZstdRatio the expansion.
	MaxZstdPlain = 1 << 30
	MaxZstdRatio = 1 << 12
)

// ZstdStats describes a zstd analysis.
type ZstdStats struct {
	SkeletonBytes, Units, InlineUnits int
	UnitBytes, FallbackBytes          int
	TableBytes                        int
	Stats                             zstdz3.Stats
	Plain                             int64
	Analyze                           time.Duration
}

var errZstd = errors.New("kinds: zstd stream the contract does not express")

// EncodeZstd analyses one Zstandard stream. child builds the representation of the
// decoded bytes (EncodeTarLayer, or CDC for a plaintext that is not a tar).
func EncodeZstd(zst []byte, child func(plain []byte) (*Bundle, error)) (*Bundle, *ZstdStats, error) {
	t0 := time.Now()
	var plain, units bytes.Buffer
	res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{Limits: zstdz3.Limits{
		MaxTotalDecoded: min(MaxZstdPlain, int64(len(zst))*MaxZstdRatio+1<<20)}})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errZstd, err)
	}
	kid, err := child(plain.Bytes())
	if err != nil {
		return nil, nil, err
	}
	var table []byte
	table = binary.AppendUvarint(table, uint64(len(res.Raw)))
	table = append(table, res.Raw...)
	table = binary.AppendUvarint(table, uint64(len(res.UnitSizes)))
	st := &ZstdStats{SkeletonBytes: len(res.Raw), Units: len(res.UnitSizes), Stats: res.Stats, Plain: int64(plain.Len())}
	var objs [][]byte
	ub := units.Bytes()
	for _, n := range res.UnitSizes {
		u := ub[:n]
		ub = ub[n:]
		table = binary.AppendUvarint(table, uint64(n))
		st.UnitBytes += int(n)
		if n < zsUnitInline {
			table = append(table, u...)
			st.InlineUnits++
			continue
		}
		h := sha256.Sum256(u)
		table = append(table, h[:]...)
		objs = append(objs, append([]byte(nil), u...))
	}
	st.TableBytes = len(table)
	ms, tobjs, err := chunkTable(table)
	if err != nil {
		return nil, nil, err
	}
	out := &repz3.Rep{Kind: repz3.KindZstd, Size: int64(len(zst)), Members: ms}
	out.Members = append(out.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(plain.Bytes()), Size: int64(plain.Len()), Child: kid.ID})
	out.Recipe = binary.AppendUvarint(nil, uint64(len(ms)))
	st.Analyze = time.Since(t0)
	b, err := newBundle(out, append(objs, tobjs...), kid)
	return b, st, err
}

type zstdDecoder struct{}

type zsUnit struct {
	inline []byte
	sha    repz3.SHA
	size   int64
}

func zstdRecipe(c *repz3.Call) (nTable int, err error) {
	nt, k := binary.Uvarint(c.Rep.Recipe)
	if k <= 0 || k != len(c.Rep.Recipe) || nt < 1 || nt+1 != uint64(len(c.Rep.Members)) || c.Rep.Members[nt].Role != repz3.Derived {
		return 0, fmt.Errorf("%w: zstd recipe members", repz3.ErrCorrupt)
	}
	return int(nt), nil
}

func zstdTable(c *repz3.Call, nTable int) (skel []byte, units []zsUnit, err error) {
	t, err := readTable(c, nTable, maxZsTable)
	if err != nil {
		return nil, nil, err
	}
	bad := func(what string) error { return fmt.Errorf("%w: zstd table %s", repz3.ErrCorrupt, what) }
	sl, k := binary.Uvarint(t)
	if k <= 0 || sl > uint64(len(t)-k) {
		return nil, nil, bad("skeleton")
	}
	skel, t = t[k:k+int(sl)], t[k+int(sl):]
	n, k := binary.Uvarint(t)
	if k <= 0 || n > uint64(len(t)-k) {
		return nil, nil, bad("unit count")
	}
	t = t[k:]
	units = make([]zsUnit, 0, n)
	for i := uint64(0); i < n; i++ {
		l, k := binary.Uvarint(t)
		if k <= 0 || l > 1<<30 {
			return nil, nil, bad("unit length")
		}
		t = t[k:]
		u := zsUnit{size: int64(l)}
		if l < zsUnitInline {
			if uint64(len(t)) < l {
				return nil, nil, bad("inline unit")
			}
			u.inline, t = t[:l], t[l:]
		} else {
			if len(t) < 32 {
				return nil, nil, bad("unit digest")
			}
			copy(u.sha[:], t)
			t = t[32:]
		}
		units = append(units, u)
	}
	if len(t) != 0 {
		return nil, nil, bad("trailing bytes")
	}
	return skel, units, nil
}

// unitReader streams the units in order, fetching each large one from the source when
// the replay first needs it.
type unitReader struct {
	c     *repz3.Call
	units []zsUnit
	next  int
	cur   []byte
}

func (u *unitReader) Read(p []byte) (int, error) {
	for len(u.cur) == 0 {
		if u.next >= len(u.units) {
			return 0, io.EOF
		}
		x := u.units[u.next]
		u.next++
		if x.size < zsUnitInline {
			u.cur = x.inline
			continue
		}
		b, err := u.c.GetExact(x.sha, x.size)
		if err != nil {
			return 0, err
		}
		u.cur = b
	}
	n := copy(p, u.cur)
	u.cur = u.cur[n:]
	return n, nil
}

func (zstdDecoder) Decode(c *repz3.Call) error {
	nTable, err := zstdRecipe(c)
	if err != nil {
		return err
	}
	skel, units, err := zstdTable(c, nTable)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { // the child's progressive output is the semantic plaintext
		err := c.WriteMember(nTable, pw)
		pw.CloseWithError(err)
		done <- err
	}()
	_, err = zstdz3.Replay(c.W, skel, bufio.NewReaderSize(&unitReader{c: c, units: units}, 1<<16), pr, zstdz3.Limits{})
	pr.CloseWithError(errAbort)
	werr := <-done
	if err != nil {
		return err
	}
	return werr
}

// Expand: the large units are named by the stored table.
func (zstdDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	nTable, err := zstdRecipe(c)
	if err != nil {
		return err
	}
	_, units, err := zstdTable(c, nTable)
	if err != nil {
		return err
	}
	for _, u := range units {
		if u.size >= zsUnitInline {
			emit(u.sha)
		}
	}
	return nil
}
