package zstdz3

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Replay: rebuilding the original bytes. Literals are not stored anywhere; they are the
// plaintext bytes that the sequences do not copy, so a block needs only its own
// plaintext, its decisions and the tables it inherits.

// ErrMismatch reports that the replayed bytes contradict the description (checksum, a
// short or long plaintext, an RLE block over non-uniform bytes).
var ErrMismatch = fmt.Errorf("zstdz3: replay disagrees with the description")

func encodeTriples(trip []seqRec) []byte {
	b := make([]byte, 0, 4*len(trip))
	for _, t := range trip {
		b = binary.AppendUvarint(b, uint64(t.ll))
		b = binary.AppendUvarint(b, uint64(t.ml-3))
		b = binary.AppendUvarint(b, uint64(t.of))
	}
	return b
}

// readTriples reads n sequences of the unit stream; varints must be minimal.
func readTriples(r io.ByteReader, n int) ([]seqRec, error) {
	trip := make([]seqRec, n)
	for i := range trip {
		var v [3]uint64
		for k := range v {
			x, err := readMinimalUvarint(r)
			if err != nil {
				return nil, err
			}
			v[k] = x
		}
		if v[0] > maxBlock || v[1] > maxBlock || v[2] == 0 || v[2] > 1<<32-1 {
			return nil, badIR("sequence value")
		}
		trip[i] = seqRec{ll: uint32(v[0]), ml: uint32(v[1]) + 3, of: uint32(v[2])}
	}
	return trip, nil
}

func readMinimalUvarint(r io.ByteReader) (uint64, error) {
	var x uint64
	var s uint
	for i := 0; ; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, badIR("truncated unit stream")
		}
		if i == 9 && c > 1 {
			return 0, badIR("varint overflow")
		}
		if c < 0x80 {
			if i > 0 && c == 0 {
				return 0, badIR("non-minimal varint")
			}
			return x | uint64(c)<<s, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
}

func litHeader(mode, form uint8, regen, comp int) ([]byte, error) {
	if mode <= litRLE {
		switch form {
		case 0:
			if regen > 31 {
				return nil, ErrMismatch
			}
			return []byte{mode | byte(regen)<<3}, nil
		case 1:
			if regen > 4095 {
				return nil, ErrMismatch
			}
			return []byte{mode | 1<<2 | byte(regen&15)<<4, byte(regen >> 4)}, nil
		default:
			if regen >= 1<<20 {
				return nil, ErrMismatch
			}
			return []byte{mode | 3<<2 | byte(regen&15)<<4, byte(regen >> 4), byte(regen >> 12)}, nil
		}
	}
	w := [4]uint{10, 10, 14, 18}[form&3]
	if uint(regen) >= 1<<w || uint(comp) >= 1<<w {
		return nil, ErrMismatch
	}
	v := uint64(mode) | uint64(form)<<2 | uint64(regen)<<4 | uint64(comp)<<(4+w)
	n := [4]int{3, 3, 4, 5}[form&3]
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return b[:n], nil
}

func seqCountHeader(n int) []byte {
	switch {
	case n == 0:
		return []byte{0}
	case n < 128:
		return []byte{byte(n)}
	case n < 0x7f00:
		return []byte{byte(n>>8) + 128, byte(n)}
	}
	return []byte{255, byte(n - 0x7f00), byte((n - 0x7f00) >> 8)}
}

func encodeSeqs(tabs [3]*fseTable, trip []seqRec, rank [3]uint32) ([]byte, error) {
	n := len(trip)
	codes := func(i int) (c [3]int, ok bool) {
		t := trip[i]
		c[iLL], c[iML], c[iOF] = llCode(t.ll), mlCode(t.ml), ofCode(t.of)
		ok = t.ml >= 3 && t.of >= 1 && c[iLL] >= 0 && c[iML] >= 0 && t.ll-llBase[c[iLL]] < 1<<llBits[c[iLL]] && t.ml-mlBase[c[iML]] < 1<<mlBits[c[iML]]
		return
	}
	var w revWriter
	extras := func(i int, c [3]int) {
		t := trip[i]
		w.write(t.ll-llBase[c[iLL]], int(llBits[c[iLL]]))
		w.write(t.ml-mlBase[c[iML]], int(mlBits[c[iML]]))
		w.write(t.of-1<<uint(c[iOF]), c[iOF])
	}
	c, ok := codes(n - 1)
	if !ok {
		return nil, ErrMismatch
	}
	var st [3]int
	for k := 0; k < 3; k++ {
		if st[k], ok = tabs[k].stateAt(c[k], rank[k]); !ok {
			return nil, ErrMismatch
		}
	}
	extras(n-1, c)
	for i := n - 2; i >= 0; i-- {
		if c, ok = codes(i); !ok {
			return nil, ErrMismatch
		}
		for _, k := range [3]int{iOF, iML, iLL} {
			ps, bits, nb, ok := tabs[k].prev(c[k], st[k])
			if !ok {
				return nil, ErrMismatch
			}
			w.write(bits, nb)
			st[k] = ps
		}
		extras(i, c)
	}
	w.write(uint32(st[iML]), int(tabs[iML].acc))
	w.write(uint32(st[iOF]), int(tabs[iOF].acc))
	w.write(uint32(st[iLL]), int(tabs[iLL].acc))
	return w.finish(), nil
}

// buildCompressed re-emits a compressed block's bytes from its record, its plaintext,
// its sequences and the state it inherits (read-only).
func buildCompressed(st *codingState, k *BlockRec, plain []byte, trip []seqRec) ([]byte, error) {
	lits := plain
	if k.NSeq > 0 {
		lits = make([]byte, 0, k.Regen)
		pos := 0
		for _, t := range trip {
			end := pos + int(t.ll) + int(t.ml)
			if end > len(plain) {
				return nil, ErrMismatch
			}
			lits = append(lits, plain[pos:pos+int(t.ll)]...)
			pos = end
		}
		lits = append(lits, plain[pos:]...)
	}
	if len(lits) != int(k.Regen) {
		return nil, ErrMismatch
	}
	var out []byte
	switch k.LitMode {
	case litRaw:
		h, err := litHeader(litRaw, k.LitForm, len(lits), 0)
		if err != nil {
			return nil, err
		}
		out = append(h, lits...)
	case litRLE:
		if len(lits) == 0 {
			return nil, ErrMismatch
		}
		h, err := litHeader(litRLE, k.LitForm, len(lits), 0)
		if err != nil {
			return nil, err
		}
		out = append(h, lits[0])
	default:
		tree := st.huff
		var desc []byte
		if k.LitMode == litComp {
			var n int
			var err error
			if tree, n, err = readHuffTree(k.TreeDesc); err != nil || n != len(k.TreeDesc) {
				return nil, ErrMismatch
			}
			desc = k.TreeDesc
		}
		if tree == nil || len(lits) == 0 {
			return nil, ErrMismatch
		}
		body := append([]byte(nil), desc...)
		if k.LitForm == 0 {
			var ok bool
			if body, ok = tree.encodeStream(body, lits); !ok {
				return nil, ErrMismatch
			}
		} else {
			seg := (len(lits) + 3) / 4
			var sizes [3]int
			var streams []byte
			for i := 0; i < 4; i++ {
				lo, hi := min(i*seg, len(lits)), min((i+1)*seg, len(lits))
				if i == 3 {
					hi = len(lits)
				}
				before := len(streams)
				var ok bool
				if streams, ok = tree.encodeStream(streams, lits[lo:hi]); !ok {
					return nil, ErrMismatch
				}
				if i < 3 {
					if sizes[i] = len(streams) - before; sizes[i] > 65535 {
						return nil, ErrMismatch
					}
				}
			}
			for _, s := range sizes {
				body = binary.LittleEndian.AppendUint16(body, uint16(s))
			}
			body = append(body, streams...)
		}
		h, err := litHeader(k.LitMode, k.LitForm, len(lits), len(body))
		if err != nil {
			return nil, err
		}
		out = append(h, body...)
	}
	out = append(out, seqCountHeader(int(k.NSeq))...)
	if k.NSeq == 0 {
		return out, nil
	}
	tabs, used, err := parseSeqTables(k.Modes, k.TabBytes, st.fse)
	if err != nil || used != len(k.TabBytes) {
		return nil, ErrMismatch
	}
	stream, err := encodeSeqs(tabs, trip, k.Rank)
	if err != nil {
		return nil, err
	}
	out = append(out, k.Modes)
	out = append(out, k.TabBytes...)
	return append(out, stream...), nil
}

func blockHeader(last bool, typ BlockType, size int) []byte {
	v := uint32(size)<<3 | uint32(typ)<<1
	if last {
		v |= 1
	}
	return []byte{byte(v), byte(v >> 8), byte(v >> 16)}
}

// replayFrame is the per-frame state replay carries: the coding tables a block inherits
// and, for model-coded sequences, the plaintext window and its match index.
type replayFrame struct {
	st    codingState
	hist  history
	model *matchModel
	bmax  int
}

// rebuildBlock produces the block's content bytes (without the 3-byte header) and the
// zstd block type, advancing the inherited state and consuming the block's unit. plain
// is the block's semantic plaintext.
func (rf *replayFrame) rebuildBlock(k *BlockRec, plain []byte, units *bufio.Reader) (content []byte, typ BlockType, err error) {
	start, total := len(rf.hist.buf), rf.hist.total
	if rf.model != nil { // the window is only needed by model-coded sequences
		rf.hist.reserve(len(plain))
		start = len(rf.hist.buf)
		rf.hist.buf = append(rf.hist.buf, plain...)
	}
	rf.hist.total += int64(len(plain))
	switch k.Type {
	case BlockRaw:
		return plain, BlockRaw, nil
	case BlockRLE:
		for _, c := range plain {
			if c != k.RLE {
				return nil, 0, ErrMismatch
			}
		}
		return []byte{k.RLE}, BlockRLE, nil
	case BlockFallback:
		content = make([]byte, k.CompLen)
		if _, err := io.ReadFull(units, content); err != nil {
			return nil, 0, badIR("truncated unit stream")
		}
	default:
		var trip []seqRec
		if k.NSeq > 0 {
			if k.Model {
				if rf.model == nil {
					return nil, 0, badIR("model-coded sequences in a frame the model does not cover")
				}
				v := &blockView{buf: rf.hist.buf, base: total - int64(start), start: total, end: rf.hist.total}
				trip, err = rf.model.decode(units, v, int(k.NSeq))
			} else {
				trip, err = readTriples(units, int(k.NSeq))
			}
			if err != nil {
				return nil, 0, err
			}
		}
		if content, err = buildCompressed(&rf.st, k, plain, trip); err != nil {
			return nil, 0, err
		}
	}
	if len(content) > rf.bmax {
		return nil, 0, ErrMismatch
	}
	if _, err := scanBlock(&rf.st, content, rf.bmax); err != nil {
		return nil, 0, fmt.Errorf("%w: rebuilt block: %v", ErrMismatch, err)
	}
	return content, BlockCompressed, nil
}

// Replay writes the original stream described by skel, taking units from units and the
// semantic plaintext from plain (read sequentially, consumed exactly). It returns the
// bytes written. Memory is one block, the inherited tables and the skeleton.
func Replay(w io.Writer, skel []byte, units io.Reader, plain io.Reader, lim Limits) (int64, error) {
	s, err := Unmarshal(skel, lim)
	if err != nil {
		return 0, err
	}
	return ReplaySkeleton(w, s, units, plain)
}

// ReplaySkeleton is Replay for an already validated skeleton.
func ReplaySkeleton(w io.Writer, s *Skeleton, unitStream io.Reader, plain io.Reader) (int64, error) {
	units := bufio.NewReaderSize(unitStream, 1<<16)
	var n int64
	put := func(b ...[]byte) error {
		for _, p := range b {
			m, err := w.Write(p)
			n += int64(m)
			if err != nil {
				return err
			}
		}
		return nil
	}
	var buf []byte
	for fi := range s.Frames {
		f := &s.Frames[fi]
		if f.Skippable {
			var h [8]byte
			binary.LittleEndian.PutUint32(h[:], magicSkip|uint32(f.SkipLow))
			binary.LittleEndian.PutUint32(h[4:], uint32(len(f.Skip)))
			if err := put(h[:], f.Skip); err != nil {
				return n, err
			}
			continue
		}
		fh, err := parseFrameHeader(f.Header, Limits{MaxWindow: 1 << 62, MaxFrameDecoded: 1 << 62})
		if err != nil {
			return n, err
		}
		var m [4]byte
		binary.LittleEndian.PutUint32(m[:], magicStd)
		if err := put(m[:], f.Header); err != nil {
			return n, err
		}
		rf := &replayFrame{bmax: min(fh.blockMax(), maxBlock), hist: history{window: fh.window}}
		for bi := range f.Blocks {
			if f.Blocks[bi].Model {
				rf.model = newMatchModel(fh.window)
				break
			}
		}
		sum := newXXH64()
		for bi := range f.Blocks {
			k := &f.Blocks[bi]
			buf = fitBuf(buf, int(k.DecLen))
			if _, err := io.ReadFull(plain, buf); err != nil {
				return n, fmt.Errorf("%w: semantic plaintext ends early: %v", ErrMismatch, err)
			}
			sum.write(buf)
			content, typ, err := rf.rebuildBlock(k, buf, units)
			if err != nil {
				return n, fmt.Errorf("frame %d block %d: %w", fi, bi, err)
			}
			size := len(content)
			if typ == BlockRLE {
				size = int(k.DecLen)
			}
			if err := put(blockHeader(k.Last, typ, size), content); err != nil {
				return n, err
			}
		}
		if fh.checksum {
			if uint32(sum.sum()) != f.Checksum {
				return n, fmt.Errorf("%w: content checksum", ErrMismatch)
			}
			var c [4]byte
			binary.LittleEndian.PutUint32(c[:], f.Checksum)
			if err := put(c[:]); err != nil {
				return n, err
			}
		}
	}
	var one [1]byte
	if m, _ := io.ReadFull(plain, one[:]); m != 0 {
		return n, fmt.Errorf("%w: semantic plaintext is longer than the description", ErrMismatch)
	}
	if _, err := units.ReadByte(); err != io.EOF {
		return n, badIR("unit stream is longer than the description")
	}
	return n, nil
}
