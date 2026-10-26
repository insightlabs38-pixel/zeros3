package kinds

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	"zeros3-z3/ir"
	"zeros3-z3/repz3"
)

// Gzip is the exact compressed outer on the contract: one gzip member whose bytes
// are rebuilt from
//
//	the verbatim RFC 1952 header | the Z3-08G predictive IR (skeleton + token-run
//	units, no literal values) replayed over the semantic plaintext | the verbatim
//	8-byte trailer
//
// The semantic plaintext is the last member: a DERIVED member whose child is an
// ordinary representation of the inflated bytes (a tar split with SQLite/CAR
// children, or plain CDC), so the same child representation serves a compressed
// and an uncompressed layer and gzip -> tar -> database is depth 3. The earlier
// members are the CDC-cut decoder table
//
//	uvarint(len skeleton) | skeleton | uvarint(n units) | n x ( uvarint(len) |
//	    len < 64 ? the unit bytes : SHA-256 of the unit )
//
// whose large units are VIA. The recipe is
//
//	uvarint(len header) | header | trailer (8 B) | uvarint(table members)
//
// Nothing in a closure: the decoder dispatches on the serialized kind, reads the
// table and the units through the bounded source and streams the child's output
// into ir.ReconstructStream through a pipe, so memory is the IR window plus one
// unit, not the layer. The gzip CRC-32 and ISIZE are recomputed over the plaintext
// and compared with the trailer before the trailer is written; the DEFLATE
// stream's own length and CRC-32 are checked by the IR. Exactness beyond what the
// IR proved is not claimed: analysis rejects (falls back to CDC) a member the IR
// cannot replay byte for byte, a trailer that disagrees with the plaintext, extra
// members or trailing bytes.

const (
	gzUnitInline = 64
	maxGzTable   = 256 << 20
	maxGzHeader  = 1<<16 + 1024
	// Analysis admission, as ociz3.Guard: inflated bytes and expansion ratio.
	MaxGzipInflated = 256 << 20
	MaxGzipRatio    = 128
)

// GzipStats describes a gzip analysis.
type GzipStats struct {
	HeaderBytes, SkeletonBytes, Blocks int
	Units, InlineUnits                 int
	UnitBytes, TableBytes              int
	Plain                              int64
	Analyze                            time.Duration
}

var errGzip = errors.New("kinds: gzip member the contract does not express")

// gzipHeaderLen returns the length of the RFC 1952 header at the start of b.
func gzipHeaderLen(b []byte) (int, error) {
	if len(b) < 10 || b[0] != 0x1f || b[1] != 0x8b || b[2] != 8 {
		return 0, fmt.Errorf("%w: not a gzip deflate member", errGzip)
	}
	flg := b[3]
	if flg&0xe0 != 0 {
		return 0, fmt.Errorf("%w: reserved flags", errGzip)
	}
	pos := 10
	if flg&4 != 0 { // FEXTRA
		if len(b) < pos+2 {
			return 0, fmt.Errorf("%w: truncated extra field", errGzip)
		}
		pos += 2 + int(binary.LittleEndian.Uint16(b[pos:]))
	}
	for _, bit := range []byte{8, 16} { // FNAME, FCOMMENT
		if flg&bit != 0 {
			i := bytes.IndexByte(b[min(pos, len(b)):], 0)
			if i < 0 {
				return 0, fmt.Errorf("%w: unterminated name or comment", errGzip)
			}
			pos += i + 1
		}
	}
	if flg&2 != 0 { // FHCRC
		pos += 2
	}
	if pos > len(b) || pos > maxGzHeader {
		return 0, fmt.Errorf("%w: header of %d bytes", errGzip, pos)
	}
	return pos, nil
}

type gzSink struct {
	plain bytes.Buffer
	units [][]byte
	max   int64
}

func (s *gzSink) Plain(p []byte) error {
	if int64(s.plain.Len())+int64(len(p)) > s.max {
		return fmt.Errorf("%w: inflates past %d bytes", errGzip, s.max)
	}
	s.plain.Write(p)
	return nil
}

func (s *gzSink) Unit(i int, d []byte) error {
	if i != len(s.units) {
		return fmt.Errorf("kinds: IR unit %d out of order", i)
	}
	s.units = append(s.units, append([]byte(nil), d...))
	return nil
}

// EncodeGzip analyses one gzip file. child builds the representation of the
// inflated bytes (EncodeTarLayer, or CDC for a plaintext that is not a tar).
func EncodeGzip(gz []byte, child func(plain []byte) (*Bundle, error)) (*Bundle, *GzipStats, error) {
	t0 := time.Now()
	hl, err := gzipHeaderLen(gz)
	if err != nil {
		return nil, nil, err
	}
	var sink *gzSink
	var ss *ir.StreamStats
	var rest []byte
	// The ratio profile set can exceed a unit's predictor work budget on some inputs
	// (an analysis-time limit); the cheaper profile sets are tried next. The profile
	// used is recorded in the skeleton, so decoding does not depend on the choice.
	for _, mode := range []string{"", "balanced", "fast"} {
		sink = &gzSink{max: min(MaxGzipInflated, int64(len(gz))*MaxGzipRatio+4096)}
		ss, rest, err = ir.EncodeStream(bytes.NewReader(gz[hl:]), ir.StreamConfig{Target: 16 << 10, Profile: ir.AutoProfile, Mode: mode}, sink)
		if err == nil || !errors.Is(err, ir.ErrLimit) {
			break
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errGzip, err)
	}
	if len(rest) != 8 {
		return nil, nil, fmt.Errorf("%w: %d bytes after the deflate stream (multi-member or trailing data)", errGzip, len(rest))
	}
	plain := sink.plain.Bytes()
	if binary.LittleEndian.Uint32(rest) != crc32.ChecksumIEEE(plain) || binary.LittleEndian.Uint32(rest[4:]) != uint32(len(plain)) {
		return nil, nil, fmt.Errorf("%w: trailer disagrees with the inflated bytes", errGzip)
	}
	kid, err := child(plain)
	if err != nil {
		return nil, nil, err
	}
	var table []byte
	table = binary.AppendUvarint(table, uint64(len(ss.Skeleton)))
	table = append(table, ss.Skeleton...)
	table = binary.AppendUvarint(table, uint64(len(sink.units)))
	var objs [][]byte
	st := &GzipStats{HeaderBytes: hl, SkeletonBytes: len(ss.Skeleton), Blocks: ss.Blocks, Units: len(sink.units), Plain: int64(len(plain))}
	for _, u := range sink.units {
		table = binary.AppendUvarint(table, uint64(len(u)))
		st.UnitBytes += len(u)
		if len(u) < gzUnitInline {
			table = append(table, u...)
			st.InlineUnits++
			continue
		}
		h := sha256.Sum256(u)
		table = append(table, h[:]...)
		objs = append(objs, u)
	}
	st.TableBytes = len(table)
	ms, tobjs, err := chunkTable(table)
	if err != nil {
		return nil, nil, err
	}
	out := &repz3.Rep{Kind: repz3.KindGzip, Size: int64(len(gz)), Members: ms}
	out.Members = append(out.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(plain), Size: int64(len(plain)), Child: kid.ID})
	out.Recipe = binary.AppendUvarint(nil, uint64(hl))
	out.Recipe = append(out.Recipe, gz[:hl]...)
	out.Recipe = append(out.Recipe, rest...)
	out.Recipe = binary.AppendUvarint(out.Recipe, uint64(len(ms)))
	st.Analyze = time.Since(t0)
	b, err := newBundle(out, append(objs, tobjs...), kid)
	return b, st, err
}

type gzipDecoder struct{}

type gzUnit struct {
	inline []byte
	sha    repz3.SHA
	size   int64
}

type gzRecipe struct {
	header, trailer []byte
	nTable          int
}

func gzipRecipe(c *repz3.Call) (*gzRecipe, error) {
	r := c.Rep.Recipe
	hl, n := binary.Uvarint(r)
	if n <= 0 || hl < 10 || hl > maxGzHeader || uint64(len(r)-n) < hl+8 {
		return nil, fmt.Errorf("%w: gzip recipe header", repz3.ErrCorrupt)
	}
	g := &gzRecipe{header: r[n : n+int(hl)], trailer: r[n+int(hl) : n+int(hl)+8]}
	nt, k := binary.Uvarint(r[n+int(hl)+8:])
	if k <= 0 || n+int(hl)+8+k != len(r) || nt < 1 || nt+1 != uint64(len(c.Rep.Members)) || c.Rep.Members[nt].Role != repz3.Derived {
		return nil, fmt.Errorf("%w: gzip recipe members", repz3.ErrCorrupt)
	}
	g.nTable = int(nt)
	if gl, err := gzipHeaderLen(g.header); err != nil || gl != len(g.header) {
		return nil, fmt.Errorf("%w: gzip header", repz3.ErrCorrupt)
	}
	return g, nil
}

func gzipTable(c *repz3.Call, g *gzRecipe) (skel []byte, units []gzUnit, err error) {
	t, err := readTable(c, g.nTable, maxGzTable)
	if err != nil {
		return nil, nil, err
	}
	bad := func(what string) error { return fmt.Errorf("%w: gzip table %s", repz3.ErrCorrupt, what) }
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
	units = make([]gzUnit, 0, n)
	for i := uint64(0); i < n; i++ {
		l, k := binary.Uvarint(t)
		if k <= 0 || l > 1<<30 {
			return nil, nil, bad("unit length")
		}
		t = t[k:]
		u := gzUnit{size: int64(l)}
		if l < gzUnitInline {
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

type crcReader struct {
	r   io.Reader
	crc uint32
	n   int64
}

func (c *crcReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.crc = crc32.Update(c.crc, crc32.IEEETable, p[:n])
	c.n += int64(n)
	return n, err
}

var errAbort = errors.New("kinds: gzip decode stopped")

// replayCause combines the outcome of a codec replay with that of the goroutine that streams
// the semantic plaintext (the DERIVED child). A child failure is the cause and the codec's
// complaint ("plaintext ends early") its consequence: reporting the child's error keeps its
// contract class (ErrMissing, ErrMismatch, ErrDepth ...) visible to errors.Is through any
// number of nested codec parents instead of burying it under the codec's own message.
func replayCause(replayErr, childErr error) error {
	switch {
	case childErr != nil && !errors.Is(childErr, errAbort):
		if replayErr != nil {
			return fmt.Errorf("%w (replay: %v)", childErr, replayErr)
		}
		return childErr
	case replayErr != nil:
		return replayErr
	}
	return childErr
}

func (gzipDecoder) Decode(c *repz3.Call) error {
	g, err := gzipRecipe(c)
	if err != nil {
		return err
	}
	skel, units, err := gzipTable(c, g)
	if err != nil {
		return err
	}
	pm := &c.Rep.Members[g.nTable]
	if _, err := c.W.Write(g.header); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { // the child's progressive output is the semantic plaintext
		err := c.WriteMember(g.nTable, pw)
		pw.CloseWithError(err)
		done <- err
	}()
	cr := &crcReader{r: pr}
	err = ir.ReconstructStream(c.W, skel, func(i int) ([]byte, error) {
		if i < 0 || i >= len(units) {
			return nil, fmt.Errorf("%w: unit %d of %d", repz3.ErrCorrupt, i, len(units))
		}
		if units[i].size < gzUnitInline {
			return units[i].inline, nil
		}
		return c.GetExact(units[i].sha, units[i].size)
	}, cr, ir.StreamDecodeOptions{})
	pr.CloseWithError(errAbort)
	werr := <-done
	if err = replayCause(err, werr); err != nil {
		return err
	}
	if cr.n != pm.Size || binary.LittleEndian.Uint32(g.trailer) != cr.crc || binary.LittleEndian.Uint32(g.trailer[4:]) != uint32(cr.n) {
		return fmt.Errorf("%w: gzip trailer disagrees with the replayed plaintext", repz3.ErrMismatch)
	}
	_, err = c.W.Write(g.trailer)
	return err
}

// Expand: the large IR units are named by the stored table.
func (gzipDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	g, err := gzipRecipe(c)
	if err != nil {
		return err
	}
	_, units, err := gzipTable(c, g)
	if err != nil {
		return err
	}
	for _, u := range units {
		if u.size >= gzUnitInline {
			emit(u.sha)
		}
	}
	return nil
}
