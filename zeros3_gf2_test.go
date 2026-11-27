package main

import (
	"archive/tar"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	gf2FrameSize = 32 << 10
	gf2MaxObject = 8 << 20
)

var gf2BadProgram = errors.New("GF2 invalid compressed frame")

// One bounded bucket per seed keeps both an earlier and a later occurrence.
// The key is only a proposal. Each COPY is checked against exact source bits.
type gf2Index map[uint64][2]uint32

type gf2MatchConfig struct {
	name                        string
	alignments, stride, minimum int
	extendBack, relative        bool
}

func gf2Word(raw []byte, bit int) uint64 {
	at, shift := bit/8, uint(bit%8)
	word := binary.LittleEndian.Uint64(raw[at:])
	if shift != 0 {
		word = word>>shift | uint64(raw[at+8])<<(64-shift)
	}
	return word
}

func gf2IndexSource(base []byte, cfg gf2MatchConfig) gf2Index {
	slots := len(base) * cfg.alignments / cfg.stride
	index := make(gf2Index, slots)
	for start := 0; start+8 < len(base); start += cfg.stride {
		for shift := 0; shift < cfg.alignments; shift++ {
			bit := start*8 + shift
			key := gf2Word(base, bit)
			hit := index[key]
			if hit[0] == 0 {
				hit[0] = uint32(bit) + 1
			}
			hit[1] = uint32(bit) + 1
			index[key] = hit
		}
	}
	return index
}

func gf2MatchLength(base, target []byte, bit int) int {
	n := min(len(base)-bit/8-(bit%8+7)/8, len(target))
	at := 0
	for at+8 <= n && gf2Word(base, bit+8*at) == binary.LittleEndian.Uint64(target[at:]) {
		at += 8
	}
	for at < n {
		b := bit + 8*at
		src := base[b/8] >> (b % 8)
		if b%8 != 0 {
			src |= base[b/8+1] << (8 - b%8)
		}
		if src != target[at] {
			break
		}
		at++
	}
	return at
}

type gf2FrameStats struct{ copied, shifted int64 }

// Frame program 0 stores original bytes. Program 1 alternates literal and
// COPY operations. A COPY bit offset may be unaligned in the base, while
// the output is always byte aligned and bounded by the expected frame size.
func gf2FrameProgram(base, target []byte, index gf2Index, cfg gf2MatchConfig) ([]byte, gf2FrameStats) {
	out := []byte{1}
	if cfg.relative {
		out[0] = 2
	}
	var previousSourceEnd, previousOutputEnd int
	var stats gf2FrameStats
	lit := 0
	emitLiteral := func(to int) {
		if to == lit {
			return
		}
		out = binary.AppendUvarint(out, uint64(to-lit)<<1)
		out = append(out, target[lit:to]...)
	}
	for at := 0; at+cfg.minimum <= len(target); {
		n, bit, begin := 0, 0, at
		hit := index[binary.LittleEndian.Uint64(target[at:])]
		for _, pos := range hit {
			if pos == 0 {
				continue
			}
			candidate := int(pos) - 1
			forward := gf2MatchLength(base, target[at:], candidate)
			end := at + forward
			start := at
			for cfg.extendBack && start > lit && candidate >= 8 {
				prev := candidate - 8
				source := base[prev/8] >> (prev % 8)
				if prev%8 != 0 {
					source |= base[prev/8+1] << (8 - prev%8)
				}
				if source != target[start-1] {
					break
				}
				candidate = prev
				start--
			}
			if end-start > n {
				n, bit, begin = end-start, candidate, start
			}
		}
		if n < cfg.minimum {
			at++
			continue
		}
		emitLiteral(begin)
		out = binary.AppendUvarint(out, uint64(n)<<1|1)
		if cfg.relative {
			prediction := previousSourceEnd + (begin-previousOutputEnd)*8
			out = binary.AppendVarint(out, int64(bit-prediction))
		} else {
			out = binary.AppendUvarint(out, uint64(bit))
		}
		previousSourceEnd, previousOutputEnd = bit+8*n, begin+n
		stats.copied += int64(n)
		if bit%8 != 0 {
			stats.shifted += int64(n)
		}
		at = begin + n
		lit = at
	}
	emitLiteral(len(target))
	if len(out) >= 1+len(target) {
		return append([]byte{0}, target...), gf2FrameStats{}
	}
	return out, stats
}

type gf2RecordRef struct{ id, sum [32]byte }
type gf2RootRef struct {
	index [32]byte
	size  int
	sum   [32]byte
}

func gf2AddPackRecord(builder *packBuilder, seen map[[32]byte]bool, data []byte) [32]byte {
	sum := sha256.Sum256(data)
	if !seen[sum] {
		builder.Add(sum, data)
		seen[sum] = true
	}
	return sum
}

func gf2BuildNative(t *testing.T, l *authLab, base, target []byte, cfg gf2MatchConfig) (gf2RootRef, gf2FrameStats) {
	t.Helper()
	if len(base) > gf2MaxObject || len(target) > gf2MaxObject || len(base) == 0 || len(target) == 0 {
		t.Fatal("fixture outside GF2 bound")
	}
	b, err := l.s.newPackBuilder(tierHot, newCompressor(true), 256)
	mustOK(t, err)
	defer b.Abort()
	seen := map[[32]byte]bool{}
	var baseRefs, targetRefs []gf2RecordRef
	for at := 0; at < len(base); at += gf2FrameSize {
		raw := base[at:min(at+gf2FrameSize, len(base))]
		id := gf2AddPackRecord(b, seen, append([]byte{0}, raw...))
		baseRefs = append(baseRefs, gf2RecordRef{id, sha256.Sum256(raw)})
	}
	var index gf2Index
	if cfg.alignments != 0 {
		index = gf2IndexSource(base, cfg)
	}
	var stats gf2FrameStats
	for at := 0; at < len(target); at += gf2FrameSize {
		raw := target[at:min(at+gf2FrameSize, len(target))]
		encoded, frameStats := gf2FrameProgram(base, raw, index, cfg)
		stats.copied += frameStats.copied
		stats.shifted += frameStats.shifted
		id := gf2AddPackRecord(b, seen, encoded)
		targetRefs = append(targetRefs, gf2RecordRef{id, sha256.Sum256(raw)})
	}
	meta := binary.AppendUvarint(nil, uint64(len(base)))
	meta = binary.AppendUvarint(meta, uint64(len(target)))
	for _, refs := range [][]gf2RecordRef{baseRefs, targetRefs} {
		for _, ref := range refs {
			meta = append(meta, ref.id[:]...)
			meta = append(meta, ref.sum[:]...)
		}
	}
	root := gf2RootRef{gf2AddPackRecord(b, seen, meta), len(target), sha256.Sum256(target)}
	path, entries, err := b.Finish()
	mustOK(t, err)
	_, err = l.s.publishPack(tierHot, path, entries)
	mustOK(t, err)
	return root, stats
}

type gf2Reader struct {
	s                    *Store
	base, target         []gf2RecordRef
	baseSize, targetSize int
}

func gf2Open(s *Store, root gf2RootRef) (*gf2Reader, error) {
	data, err := s.casRead(root.index)
	if err != nil {
		return nil, err
	}
	base, n := binary.Uvarint(data)
	if n <= 0 || base == 0 || base > gf2MaxObject {
		return nil, gf2BadProgram
	}
	data = data[n:]
	target, n := binary.Uvarint(data)
	if n <= 0 || target == 0 || target > gf2MaxObject || target != uint64(root.size) {
		return nil, gf2BadProgram
	}
	data = data[n:]
	nb, nt := (int(base)+gf2FrameSize-1)/gf2FrameSize, (int(target)+gf2FrameSize-1)/gf2FrameSize
	if len(data) != (nb+nt)*64 {
		return nil, gf2BadProgram
	}
	refs := make([]gf2RecordRef, nb+nt)
	for i := range refs {
		copy(refs[i].id[:], data[:32])
		copy(refs[i].sum[:], data[32:64])
		data = data[64:]
	}
	return &gf2Reader{s, refs[:nb], refs[nb:], int(base), int(target)}, nil
}

func (r *gf2Reader) getFrame(ref gf2RecordRef, size, baseSize int, load func(int, int) ([]byte, error)) ([]byte, error) {
	data, err := r.s.casRead(ref.id)
	if err != nil {
		return nil, err
	}
	out, err := bitPatchApplyFrame(data, size, baseSize, load)
	if err != nil {
		return nil, err
	}
	if sha256.Sum256(out) != ref.sum {
		return nil, gf2BadProgram
	}
	return out, nil
}

func (r *gf2Reader) read(start, length int) ([]byte, error) {
	if start < 0 || length < 0 || start > r.targetSize || length > r.targetSize-start {
		return nil, gf2BadProgram
	}
	if length == 0 {
		return nil, nil
	}
	baseCache := map[int][]byte{}
	load := func(off, n int) ([]byte, error) {
		data := make([]byte, 0, n)
		for at := off; at < off+n; {
			i := at / gf2FrameSize
			chunk := baseCache[i]
			if chunk == nil {
				var err error
				chunk, err = r.getFrame(r.base[i], min(gf2FrameSize, r.baseSize-i*gf2FrameSize), 0, nil)
				if err != nil {
					return nil, err
				}
				// Bound transient cache size. Whole reads can move through an entire
				// base, but a random range should pay only the requested base frames.
				if len(baseCache) >= 16 {
					clear(baseCache)
				}
				baseCache[i] = chunk
			}
			pos := at - i*gf2FrameSize
			count := min(off+n-at, len(chunk)-pos)
			data = append(data, chunk[pos:pos+count]...)
			at += count
		}
		return data, nil
	}
	result := make([]byte, 0, length)
	for at := start; at < start+length; {
		i := at / gf2FrameSize
		frame, err := r.getFrame(r.target[i], min(gf2FrameSize, r.targetSize-i*gf2FrameSize), r.baseSize, load)
		if err != nil {
			return nil, err
		}
		pos := at - i*gf2FrameSize
		n := min(start+length-at, len(frame)-pos)
		result = append(result, frame[pos:pos+n]...)
		at += n
	}
	return result, nil
}

func TestGF2ShiftedProgram(t *testing.T) {
	// Incompressible bytes shifted by three bits. The bit-copy path should
	// recognize the exact span. No aligned byte matcher can infer the shift.
	base := make([]byte, 128<<10)
	for i := range base {
		if i%32 == 0 {
			block := sha256.Sum256(binary.LittleEndian.AppendUint64(nil, uint64(i)))
			copy(base[i:], block[:])
		}
	}
	target := make([]byte, len(base)-1)
	for i := range target {
		target[i] = base[i]>>3 | base[i+1]<<5
	}
	// Preserve an unmatched interior and the bytes on both sides.
	copy(target[gf2FrameSize-8:], []byte("literal spanning a frame"))
	l := newAuthLab(t)
	root, stats := gf2BuildNative(t, l, base, target, gf2MatchConfig{"control", 8, 16, 24, false, false})
	if stats.shifted < int64(len(target))*9/10 {
		t.Fatalf("shifted span was missed: %+v", stats)
	}
	l.close()
	l.open()
	r, err := gf2Open(l.s, root)
	mustOK(t, err)
	for _, span := range [][2]int{{0, len(target)}, {gf2FrameSize - 5, 4096}, {len(target) - 4096, 4096}} {
		got, err := r.read(span[0], span[1])
		mustOK(t, err)
		if !bytes.Equal(got, target[span[0]:span[0]+span[1]]) {
			t.Fatal("original compressed span mismatch")
		}
	}
	if _, err := r.read(len(target), 1); err == nil {
		t.Fatal("accepted out-of-range read")
	}
	for _, bad := range [][]byte{{}, {1}, {1, 5, 255}, {1, 17, 127}, {1, 2, 1, 3}, {0, 1, 2}} {
		if _, err := bitPatchApplyFrame(bad, 1, 0, nil); err == nil {
			t.Fatalf("accepted malformed program %x", bad)
		}
	}
	t.Logf("verified %d shifted bytes of %d compressed bytes", stats.shifted, len(target))
}

type gf2PatchRow struct {
	Family, Lane                                                   string
	Round                                                          int
	Logical, Allocated, PackAllocated, TargetCopied, TargetShifted int64
	BuildCPUNS, BuildWallNS, ReopenNS                              int64
	WholeNS, RangeNS                                               [2][3]int64
	WholeCPUNS, RangeCPUNS                                         [2]int64
}

func gf2PatchPanel(t *testing.T, family string, base, target []byte) {
	t.Helper()
	for round := range 2 {
		configs := []gf2MatchConfig{
			{"alignments-0", 0, 16, 24, false, false},
			{"alignments-1", 1, 16, 24, false, false},
			{"alignments-8", 8, 16, 24, false, false},
		}
		if strings.Contains(family, "typescript") && os.Getenv("ZEROS3_GF2_TUNE") == "1" {
			configs = []gf2MatchConfig{
				{"eight-back", 8, 16, 17, true, false},
				{"eight-dense", 8, 4, 12, true, false},
				{"eight-dense16", 8, 4, 16, true, false},
				{"eight-dense8", 8, 8, 8, true, false},
			}
		}
		if selected := os.Getenv("ZEROS3_GF2_RELATIVE"); selected != "" {
			configs = []gf2MatchConfig{{"relative-4", 8, 4, 8, true, true}}
			if selected == "tune" {
				configs = append(configs, gf2MatchConfig{"relative-8", 8, 8, 8, true, true}, gf2MatchConfig{"relative-2", 8, 2, 8, true, true})
			}
		}
		if round != 0 {
			for i, j := 0, len(configs)-1; i < j; i, j = i+1, j-1 {
				configs[i], configs[j] = configs[j], configs[i]
			}
		}
		for _, cfg := range configs {
			row := gf2PatchRow{Family: family, Lane: cfg.name, Round: round,
				Logical: int64(len(base) + len(target))}
			l := newAuthLab(t)
			begin, cpu := time.Now(), fb13CPU()
			root, stats := gf2BuildNative(t, l, base, target, cfg)
			row.BuildWallNS, row.BuildCPUNS = time.Since(begin).Nanoseconds(), int64(fb13CPU()-cpu)
			row.TargetCopied, row.TargetShifted = stats.copied, stats.shifted
			l.close()
			begin = time.Now()
			l.open()
			row.ReopenNS = time.Since(begin).Nanoseconds()
			for pass := range 2 {
				var times []int64
				cpu = fb13CPU()
				for sample := range 24 {
					// Repeated and evenly spaced probes include frame boundaries.
					length := min(4096, len(target))
					start := (len(target) - length) * (sample % 12) / 11
					begin = time.Now()
					r, err := gf2Open(l.s, root)
					mustOK(t, err)
					got, err := r.read(start, length)
					times = append(times, time.Since(begin).Nanoseconds())
					mustOK(t, err)
					if !bytes.Equal(got, target[start:start+length]) {
						t.Fatal("patch range digest mismatch")
					}
				}
				row.RangeNS[pass], row.RangeCPUNS[pass] = gf0Percentiles(times), int64(fb13CPU()-cpu)
				times = nil
				cpu = fb13CPU()
				for range 6 {
					begin = time.Now()
					r, err := gf2Open(l.s, root)
					mustOK(t, err)
					got, err := r.read(0, len(target))
					if sha256.Sum256(got) != root.sum {
						t.Fatal("patch whole-object digest mismatch")
					}
					times = append(times, time.Since(begin).Nanoseconds())
					mustOK(t, err)
					if !bytes.Equal(got, target) {
						t.Fatal("patch whole-object bytes mismatch")
					}
				}
				row.WholeNS[pass], row.WholeCPUNS[pass] = gf0Percentiles(times), int64(fb13CPU()-cpu)
			}
			l.close()
			row.Allocated, row.PackAllocated, _ = fb13Allocation(t, l.dir)
			line, err := json.Marshal(row)
			mustOK(t, err)
			fmt.Printf("GF2PATCH %s\n", line)
		}
	}
}

func gf2GzipFixture(t *testing.T, data []byte, level, flush int, name string) []byte {
	t.Helper()
	var result bytes.Buffer
	w, err := gzip.NewWriterLevel(&result, level)
	mustOK(t, err)
	w.Name = name
	w.Header.ModTime = time.Unix(int64(len(name)+flush), 0)
	for at := 0; at < len(data); at += flush {
		_, err = w.Write(data[at:min(at+flush, len(data))])
		mustOK(t, err)
		if at+flush < len(data) {
			mustOK(t, w.Flush())
		}
	}
	mustOK(t, w.Close())
	return result.Bytes()
}

func gf2TarFixture(t *testing.T, reverse bool) []byte {
	t.Helper()
	var result bytes.Buffer
	w := tar.NewWriter(&result)
	for i := range 8 {
		id := i
		if reverse {
			id = 7 - i
		}
		data := []byte(strings.Repeat(fmt.Sprintf("column %d and common data\n", id), 4000))
		mustOK(t, w.WriteHeader(&tar.Header{Name: fmt.Sprintf("record/%d.txt", id), Mode: 0600,
			Size: int64(len(data)), ModTime: time.Unix(int64(id), 0)}))
		_, err := w.Write(data)
		mustOK(t, err)
	}
	mustOK(t, w.Close())
	return result.Bytes()
}

func TestGF2NativePatchPanel(t *testing.T) {
	root, families := gf0Corpus(t)
	names := []string{"typescript-gzip", "parquet"}
	heldout := os.Getenv("ZEROS3_GF2_HELDOUT") == "1"
	if heldout {
		names = []string{"urllib3-gzip", "typescript-gzip-adjacent"}
	}
	for _, fam := range families {
		if fam.Name != names[0] && (len(names) == 1 || fam.Name != names[1]) {
			continue
		}
		t.Run(fam.Name, func(t *testing.T) {
			base, _ := gf0ReadFixture(t, root, fam, 0)
			target, _ := gf0ReadFixture(t, root, fam, 1)
			gf2PatchPanel(t, fam.Name, base, target)
		})
	}
	if heldout {
		return
	}
	raw := gf2TarFixture(t, false)
	fixed := gf2GzipFixture(t, raw, flate.HuffmanOnly, len(raw), "source.tar")
	changed := gf2GzipFixture(t, raw, flate.BestCompression, 128<<10, "another.tar")
	t.Run("equal-decoded-different-entropy-codes", func(t *testing.T) { gf2PatchPanel(t, t.Name(), fixed, changed) })
	reordered := gf2TarFixture(t, true)
	t.Run("reordered-entries-and-blocks", func(t *testing.T) {
		gf2PatchPanel(t, t.Name(), changed, gf2GzipFixture(t, reordered, flate.BestCompression, 131<<10, "filtered.tar"))
	})
}

func FuzzGF2FrameProgram(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4}, uint16(4), []byte{})
	f.Add([]byte{1, 9, 3}, uint16(4), []byte{1, 2, 3, 4, 5})
	f.Add([]byte{2, 9, 6}, uint16(4), []byte{1, 2, 3, 4, 5})
	f.Fuzz(func(t *testing.T, program []byte, size uint16, base []byte) {
		if len(base) > 128<<10 || len(program) > 128<<10 {
			t.Skip()
		}
		load := func(off, n int) ([]byte, error) {
			if off < 0 || n < 1 || off > len(base) || n > len(base)-off {
				t.Fatal("unchecked source span")
			}
			return base[off : off+n], nil
		}
		got, err := bitPatchApplyFrame(program, int(size), len(base), load)
		if err == nil && len(got) != int(size) {
			t.Fatal("unchecked frame size")
		}
	})
}
