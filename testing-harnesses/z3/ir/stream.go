package ir

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"zeros3-z3/deflate"
)

// Streaming encode and reconstruct (Z3-08G). Memory is bounded independently
// of the stream length: a plaintext ring (match window, warm-up window and the
// current unit), the current unit's token runs, the current block's tokens
// (only while its header might be predicted: at most maxPredictTokens) and the
// accumulated skeleton, which grows with blocks and units (a few bytes per
// 16 KiB of plaintext), not with bytes. The whole-stream encoder (Encode) is the
// reference oracle: with a fixed profile the units are byte-identical.
//
// Only token-run units (PTokRun) can stream: whole-stream and per-block units
// are unbounded by definition.

// StreamConfig configures EncodeStream.
type StreamConfig struct {
	Target       int   // token-run unit target in plaintext bytes (default 16 KiB)
	Profile      uint8 // predictor profile, or AutoProfile
	NoHeaderPred bool
	Mode         string // profile set for AutoProfile: ratio | balanced | fast
	// Auto profile: units 0..ProbeFirst-1 and every ProbeEvery-th unit are
	// encoded with every profile and the smallest wins until the next probe.
	ProbeFirst, ProbeEvery int
}

// StreamSink receives the encoder's outputs in order.
type StreamSink interface {
	// Plain gets the semantic plaintext, sequentially.
	Plain(p []byte) error
	// Unit gets the i-th encoded unit.
	Unit(i int, data []byte) error
}

// Index parses the range index collected while encoding.
func (s *StreamStats) Index() (*RangeIndex, error) { return ParseRangeIndex(s.RangeIndexData, s.Units) }

// StreamStats describes one streaming encode.
type StreamStats struct {
	Skeleton       []byte
	Blocks, Units  int
	Tokens         int64
	PlainLen       int64
	StreamLen      int64
	UnitBytes      int64
	RingBytes      int
	PredictedHdrs  int
	RangeIndexData []byte // delta-varint compressed bit offset at which each unit's first token is written
	Pred           PredStats
}

const warmMax = 128 << 10 // largest warm-up any profile or experiment asks for

func ringFor(hi int) int {
	need := hi + 2*warmMax + 4096
	sz := 1 << 16
	for sz < need {
		sz <<= 1
	}
	return sz
}

type countCRC struct {
	n   int64
	crc uint32
}

func (c *countCRC) Write(b []byte) (int, error) {
	c.n += int64(len(b))
	c.crc = crc32.Update(c.crc, crc32.IEEETable, b)
	return len(b), nil
}

type streamEnc struct {
	cfg  StreamConfig
	sink StreamSink
	win  *deflate.Window
	w    *deflate.StreamWriter
	cc   countCRC

	lo, hi int
	mask   uint64
	h      uint64
	size   int
	pend   bool

	runs     []Run
	lits     uint32
	unitTok  int
	unitPos  int64 // plaintext offset of the open unit's first token
	pos      int64 // plaintext offset of the next token
	emitted  int64
	nunits   int
	lastBits int64
	idxB     []byte
	curProf  uint8
	probeAt  int

	blockB     []byte // serialised block entries: the skeleton is kept serialised, not as structs
	nblocks    int
	prevBlockN int
	unitB      []byte // serialised unit index
	cur        deflate.Block
	toks       []deflate.Token
	over       bool
	blkPlain   int
	stored     []byte
	stats      StreamStats
	pr         predictor
}

// EncodeStream parses the raw DEFLATE stream at the start of r in bounded
// memory and emits plaintext and units to sink. Bytes read from r beyond the
// stream are returned in rest.
func EncodeStream(r io.Reader, cfg StreamConfig, sink StreamSink) (st *StreamStats, rest []byte, err error) {
	if cfg.Target == 0 {
		cfg.Target = 16 << 10
	}
	if cfg.ProbeFirst == 0 {
		cfg.ProbeFirst = 4
	}
	if cfg.ProbeEvery == 0 {
		cfg.ProbeEvery = 64
	}
	target := max(cfg.Target, 64)
	e := &streamEnc{cfg: cfg, sink: sink, lo: target / 4, hi: target * 4, mask: uint64(pow2floor(target)) - 1}
	e.win = deflate.NewWindow(ringFor(e.hi))
	e.w = deflate.NewStreamWriter(&e.cc)
	if cfg.Profile != AutoProfile {
		e.curProf = cfg.Profile
	}
	res, err := deflate.StreamParse(r, e.win, e, deflate.Options{MaxBlocks: maxBlocks})
	if err != nil {
		return nil, nil, err
	}
	if err := e.flushUnit(); err != nil {
		return nil, nil, err
	}
	if err := e.w.Finish(res.TailPadBits, res.TailPadLen); err != nil {
		return nil, nil, err
	}
	if err := e.emitPlain(e.win.Len()); err != nil {
		return nil, nil, err
	}
	// skeleton = Marshal() of the structs, assembled from the serialised pieces
	sk := []byte{skelVersion, byte(Pred), e.curProf}
	sk = uvarint(sk, uint64(e.nblocks))
	sk = append(sk, e.blockB...)
	sk = append(sk, padMarker(res.TailPadBits, res.TailPadLen))
	sk = uvarint(sk, uint64(e.nunits))
	sk = append(sk, e.unitB...)
	sk = uvarint(sk, uint64(e.cc.n))
	sk = binary.LittleEndian.AppendUint32(sk, e.cc.crc)
	e.stats.Skeleton = sk
	e.stats.Blocks, e.stats.Units, e.stats.Tokens = e.nblocks, e.nunits, res.Tokens
	e.stats.PlainLen, e.stats.StreamLen, e.stats.RingBytes = e.win.Len(), e.cc.n, e.win.Size()
	e.stats.RangeIndexData = e.idxB
	e.stats.Pred = e.pr.Stats
	return &e.stats, res.Rest, nil
}

func (e *streamEnc) emitPlain(upto int64) error {
	for e.emitted < upto {
		n := min(upto-e.emitted, 64<<10)
		if err := e.sink.Plain(e.win.Read(e.emitted, e.emitted+n)); err != nil {
			return err
		}
		e.emitted += n
	}
	return nil
}

func (e *streamEnc) gear(p []byte) {
	for _, c := range p {
		e.h = e.h<<1 + gear[c]
	}
}

func (e *streamEnc) BeginBlock(b *deflate.Block) error {
	e.cur = *b
	e.cur.Tokens = nil
	e.cur.CLSyms = append([]deflate.CLSym(nil), b.CLSyms...)
	e.toks, e.over, e.blkPlain, e.stored = e.toks[:0], false, 0, e.stored[:0]
	if b.Kind == deflate.Stored {
		if err := e.flushUnit(); err != nil { // a unit never spans a stored block
			return err
		}
		return e.w.Begin(b)
	}
	return e.w.Begin(b)
}

func (e *streamEnc) StoredData(p []byte) error {
	e.stored = append(e.stored, p...)
	e.gear(p)
	e.pos += int64(len(p))
	e.size, e.pend = 0, false
	if e.win.Len()-e.emitted >= 64<<10 {
		return e.emitPlain(e.win.Len())
	}
	return nil
}

func (e *streamEnc) Token(t deflate.Token) error {
	if e.size > 0 && (e.pend || e.size >= e.hi) {
		if err := e.flushUnit(); err != nil {
			return err
		}
	}
	n := tokenPlain(t)
	if e.unitTok == 0 {
		e.unitPos = e.pos
		bp := e.w.BitPos()
		e.idxB = binary.AppendUvarint(e.idxB, uint64(bp-e.lastBits))
		e.lastBits = bp
	}
	if t.IsLiteral() {
		e.h = e.h<<1 + gear[byte(t)]
		e.size++
		if e.size >= e.lo && e.h&e.mask == 0 {
			e.pend = true
		}
		e.lits++
	} else {
		for _, c := range e.win.Read(e.pos, e.pos+int64(n)) {
			e.h = e.h<<1 + gear[c]
			e.size++
			if e.size >= e.lo && e.h&e.mask == 0 {
				e.pend = true
			}
		}
		e.runs = append(e.runs, Run{Lits: e.lits, M: t})
		e.lits = 0
	}
	e.unitTok++
	e.pos += int64(n)
	e.blkPlain += n
	if len(e.toks) < maxPredictTokens {
		e.toks = append(e.toks, t)
	} else {
		e.over = true
	}
	if err := e.w.Token(t); err != nil {
		return err
	}
	if e.win.Len()-e.emitted >= 64<<10 {
		return e.emitPlain(e.win.Len())
	}
	return nil
}

func (e *streamEnc) EndBlock(b *deflate.Block, tokens int) error {
	m := BlockMeta{Final: b.Final, Kind: b.Kind}
	switch b.Kind {
	case deflate.Stored:
		if err := e.w.Stored(b, e.stored); err != nil {
			return err
		}
		m.PadBits, m.PadLen, m.StoredLen = b.PadBits, b.PadLen, b.PlainLen
		m.AutoPad = !e.cfg.NoHeaderPred && b.PadBits == 0
	default:
		if err := e.w.End(); err != nil {
			return err
		}
		m.NTokens = tokens
		if b.Kind == deflate.Dynamic {
			m.HLIT, m.HDIST, m.HCLEN, m.CLLens, m.CLSyms = e.cur.HLIT, e.cur.HDIST, e.cur.HCLEN, e.cur.CLLens, e.cur.CLSyms
			if !e.cfg.NoHeaderPred && !e.over {
				blk := e.cur
				blk.Tokens, blk.PlainLen = e.toks, e.blkPlain
				if blockPredicted(&blk) {
					m.Pred = true
					e.stats.PredictedHdrs++
				}
			}
		}
	}
	e.blockB = encodeBlockMeta(e.blockB, &m, e.prevBlockN)
	if m.Kind != deflate.Stored {
		e.prevBlockN = m.NTokens
	}
	e.nblocks++
	return nil
}

// flushUnit encodes the open unit (if any) and hands it to the sink.
func (e *streamEnc) flushUnit() error {
	e.size, e.pend = 0, false
	if e.unitTok == 0 {
		return nil
	}
	if e.lits > 0 {
		e.runs = append(e.runs, Run{Lits: e.lits})
	}
	plainLen := int(e.pos - e.unitPos)
	wbase := max(0, e.unitPos-warmMax)
	plain := e.win.Read(wbase, e.unitPos+int64(plainLen))
	rel := int(e.unitPos - wbase)
	k := e.nunits
	probe := e.cfg.Profile == AutoProfile && (k < e.cfg.ProbeFirst || k%e.cfg.ProbeEvery == 0)
	var data []byte
	var err error
	if probe {
		bestSize := int(^uint(0) >> 1)
		for _, id := range ModeProfiles(e.cfg.Mode) {
			prof := &Profiles[id]
			p := newPredictor(plain, prof.ID)
			d, err := p.encode(e.runs, rel, plainLen, nil)
			if err != nil {
				continue
			}
			if len(d) < bestSize {
				bestSize, data, e.curProf = len(d), d, prof.ID
			}
		}
		if data == nil {
			return fmt.Errorf("ir: no profile could encode unit %d", k)
		}
	} else {
		e.pr.plain = plain
		e.pr.prof = &Profiles[e.curProf]
		if data, err = e.pr.encode(e.runs, rel, plainLen, nil); err != nil {
			return fmt.Errorf("ir: predictive unit %d: %w", k, err)
		}
	}
	if err := e.sink.Unit(k, data); err != nil {
		return err
	}
	e.stats.UnitBytes += int64(len(data))
	e.unitB = uvarint(e.unitB, uint64(plainLen))
	e.unitB = uvarint(e.unitB, uint64(e.unitTok))
	e.nunits++
	e.runs, e.lits, e.unitTok = e.runs[:0], 0, 0
	return nil
}

// ---------------------------------------------------------------------------

// StreamDecodeOptions bound the streaming reconstruction.
type StreamDecodeOptions struct {
	MaxUnitPlain int // largest unit (plaintext bytes) accepted; default 1 MiB
}

// ReconstructStream rebuilds the DEFLATE stream from the skeleton, the units
// and the sequential semantic plaintext, writing to w as it goes. Memory is
// bounded by the skeleton, one unit and the plaintext ring. The recorded
// length and CRC-32 are checked at the end: bytes written to w are only
// trustworthy once it returns nil.
func ReconstructStream(w io.Writer, skel []byte, unit func(i int) ([]byte, error), plain io.Reader, opt StreamDecodeOptions) error {
	if opt.MaxUnitPlain == 0 {
		opt.MaxUnitPlain = 1 << 20
	}
	sk, err := ParseSkeleton(skel, 1<<40)
	if err != nil {
		return err
	}
	if sk.Codec != Pred {
		return corrupt("streaming reconstruction supports the predictive codec only")
	}
	win := deflate.NewWindow(opt.MaxUnitPlain + 2*warmMax + 4096)
	var cc countCRC
	sw := deflate.NewStreamWriter(io.MultiWriter(&cc, w))
	rd := &plainFeed{r: plain, win: win}

	// plaintext placement of units around stored blocks (as in Decode)
	type stored struct{ atTok, n int }
	var sts []stored
	tok := 0
	for i := range sk.Blocks {
		if sk.Blocks[i].Kind == deflate.Stored {
			sts = append(sts, stored{tok, sk.Blocks[i].StoredLen})
		}
		tok += sk.Blocks[i].NTokens
	}
	startPos := make([]int64, len(sk.Units))
	{
		var acc int64
		si, ti := 0, 0
		for k, u := range sk.Units {
			for si < len(sts) && sts[si].atTok <= ti {
				acc += int64(sts[si].n)
				si++
			}
			if si < len(sts) && sts[si].atTok < ti+u.NTokens {
				return corrupt("unit %d spans a stored block", k)
			}
			if u.PlainLen > opt.MaxUnitPlain {
				return fmt.Errorf("%w: unit %d has %d plaintext bytes (max %d)", ErrLimit, k, u.PlainLen, opt.MaxUnitPlain)
			}
			startPos[k] = acc
			acc += int64(u.PlainLen)
			ti += u.NTokens
		}
	}

	var (
		pos      int64
		unitIdx  int // next unit to load
		runs     []Run
		ri       int
		lits     uint32
		unitLeft int
	)
	loadUnit := func() error {
		if unitIdx >= len(sk.Units) {
			return corrupt("token stream exhausted")
		}
		u := sk.Units[unitIdx]
		if startPos[unitIdx] != pos {
			return corrupt("unit %d starts at %d, expected %d", unitIdx, startPos[unitIdx], pos)
		}
		if err := rd.upto(pos + int64(u.PlainLen)); err != nil {
			return err
		}
		data, err := unit(unitIdx)
		if err != nil {
			return err
		}
		wbase := max(0, pos-warmMax)
		pl := win.Read(wbase, pos+int64(u.PlainLen))
		p := newPredictor(pl, 0)
		if runs, err = p.decodeUnit(data, int(pos-wbase), u.PlainLen); err != nil {
			return fmt.Errorf("unit %d: %w", unitIdx, err)
		}
		n, pl2 := 0, 0
		for _, r := range runs {
			n += r.tokens()
			pl2 += r.plainLen()
		}
		if n != u.NTokens || pl2 != u.PlainLen {
			return corrupt("unit %d decodes to %d tokens / %d bytes, index says %d / %d", unitIdx, n, pl2, u.NTokens, u.PlainLen)
		}
		ri, lits, unitLeft = 0, 0, u.NTokens
		unitIdx++
		return nil
	}
	nextTok := func() (deflate.Token, error) {
		for unitLeft == 0 {
			if err := loadUnit(); err != nil {
				return 0, err
			}
		}
		run := runs[ri]
		var t deflate.Token
		if lits < run.Lits {
			if err := rd.upto(pos + 1); err != nil {
				return 0, err
			}
			t = deflate.Literal(win.Read(pos, pos+1)[0])
			lits++
			pos++
		} else {
			t = run.M
			if t == 0 {
				return 0, corrupt("unit run without a match")
			}
			ri, lits = ri+1, 0
			if int64(t.Distance()) > pos {
				return 0, corrupt("match distance %d at %d", t.Distance(), pos)
			}
			pos += int64(t.Length())
		}
		unitLeft--
		return t, nil
	}

	for bi := range sk.Blocks {
		m := &sk.Blocks[bi]
		b := deflate.Block{Final: m.Final, Kind: m.Kind}
		switch m.Kind {
		case deflate.Stored:
			b.PadBits, b.PadLen, b.PlainLen, b.AutoPad = m.PadBits, m.PadLen, m.StoredLen, m.AutoPad
			if err := rd.upto(pos + int64(m.StoredLen)); err != nil {
				return err
			}
			if m.StoredLen > opt.MaxUnitPlain {
				return fmt.Errorf("%w: stored block of %d bytes", ErrLimit, m.StoredLen)
			}
			data := win.Read(pos, pos+int64(m.StoredLen))
			if err := sw.Begin(&b); err != nil {
				return err
			}
			if err := sw.Stored(&b, data); err != nil {
				return err
			}
			pos += int64(m.StoredLen)
			continue
		}
		var buf []deflate.Token
		if m.Kind == deflate.Dynamic && !m.Pred {
			b.HLIT, b.HDIST, b.HCLEN, b.CLLens, b.CLSyms = m.HLIT, m.HDIST, m.HCLEN, m.CLLens, m.CLSyms
			if b.LitLens, b.DistLens = ExpandLengths(m); b.LitLens == nil {
				return corrupt("block %d code lengths", bi)
			}
		}
		if m.Pred { // the header depends on every token of the block
			start := pos
			buf = make([]deflate.Token, 0, m.NTokens)
			for j := 0; j < m.NTokens; j++ {
				t, err := nextTok()
				if err != nil {
					return err
				}
				buf = append(buf, t)
			}
			kind, pm, ok := zlibBlock(buf, int(pos-start))
			if !ok || kind != deflate.Dynamic {
				return corrupt("block %d: predicted header is not dynamic", bi)
			}
			b.HLIT, b.HDIST, b.HCLEN, b.CLLens, b.CLSyms = pm.HLIT, pm.HDIST, pm.HCLEN, pm.CLLens, pm.CLSyms
			if b.LitLens, b.DistLens = ExpandLengths(&pm); b.LitLens == nil {
				return corrupt("block %d: predicted code lengths", bi)
			}
		}
		if err := sw.Begin(&b); err != nil {
			return err
		}
		if m.Pred {
			for _, t := range buf {
				if err := sw.Token(t); err != nil {
					return err
				}
			}
		} else {
			for j := 0; j < m.NTokens; j++ {
				t, err := nextTok()
				if err != nil {
					return err
				}
				if err := sw.Token(t); err != nil {
					return err
				}
			}
		}
		if err := sw.End(); err != nil {
			return err
		}
	}
	if unitLeft != 0 || unitIdx != len(sk.Units) {
		return corrupt("units not fully consumed")
	}
	if err := sw.Finish(sk.TailPadBits, sk.TailPadLen); err != nil {
		return err
	}
	if err := rd.drainCheck(pos); err != nil {
		return err
	}
	if int(cc.n) != sk.StreamLen || cc.crc != sk.CRC {
		return corrupt("replayed stream is %d bytes crc %08x, skeleton says %d crc %08x", cc.n, cc.crc, sk.StreamLen, sk.CRC)
	}
	return nil
}

// plainFeed reads the semantic plaintext into the ring on demand.
type plainFeed struct {
	r   io.Reader
	win *deflate.Window
	buf []byte
	eof bool
}

func (f *plainFeed) upto(n int64) error {
	for f.win.Len() < n {
		if f.eof {
			return corrupt("semantic plaintext ends at %d, need %d", f.win.Len(), n)
		}
		if f.buf == nil {
			f.buf = make([]byte, 32<<10)
		}
		want := int(min(int64(len(f.buf)), n-f.win.Len()))
		k, err := f.r.Read(f.buf[:want])
		f.win.Write(f.buf[:k])
		if err != nil {
			if errors.Is(err, io.EOF) {
				f.eof = true
				continue
			}
			return err
		}
	}
	return nil
}

// drainCheck verifies the plaintext reader holds exactly total bytes.
func (f *plainFeed) drainCheck(total int64) error {
	if f.win.Len() != total {
		if f.win.Len() < total {
			return corrupt("plaintext shorter than the stream needs")
		}
		return corrupt("plaintext longer than the stream needs")
	}
	var b [1]byte
	if n, _ := f.r.Read(b[:]); n != 0 {
		return corrupt("plaintext longer than the stream needs")
	}
	return nil
}

var _ = binary.LittleEndian
