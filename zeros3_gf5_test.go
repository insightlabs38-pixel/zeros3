package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"
)

const (
	gf5Window    = 64 << 10
	gf5MaxFile   = 64 << 20
	gf5MaxFrames = 8192
)

var gf5Bad = errors.New("GF5 invalid checkpoint frame")

type gf5Numeric struct{ Width, Mantissa, Exponent, Columns int }

func gf5Type(t repSTTensor) gf5Numeric {
	var n gf5Numeric
	switch t.DType {
	case "F16":
		n = gf5Numeric{2, 10, 5, 0}
	case "BF16":
		n = gf5Numeric{2, 7, 8, 0}
	case "F32":
		n = gf5Numeric{4, 23, 8, 0}
	default:
		return n
	}
	if len(t.Shape) > 1 && t.Shape[len(t.Shape)-1] > 0 && t.Shape[len(t.Shape)-1] <= 8192 {
		n.Columns = int(t.Shape[len(t.Shape)-1])
	}
	return n
}

func gf5Word(data []byte, width int) uint32 {
	if width == 2 {
		return uint32(binary.LittleEndian.Uint16(data))
	}
	return binary.LittleEndian.Uint32(data)
}
func gf5Put(data []byte, width int, v uint32) {
	if width == 2 {
		binary.LittleEndian.PutUint16(data, uint16(v))
		return
	}
	binary.LittleEndian.PutUint32(data, v)
}

func gf5Fields(target, base uint32, n gf5Numeric, inverse bool) uint32 {
	man := uint32(1<<n.Mantissa) - 1
	expo := uint32(1<<n.Exponent) - 1
	sign := (target ^ base) & (1 << uint(n.Mantissa+n.Exponent))
	er, mr := (target>>n.Mantissa)-(base>>n.Mantissa), target-base
	if inverse {
		er, mr = (target>>n.Mantissa)+(base>>n.Mantissa), target+base
	}
	return sign | (er&expo)<<n.Mantissa | (mr & man)
}

// Exchange bit-matrix diagonals in three batches. Applying the same
// transpose twice recovers the original bytes on any machine.
func gf5Transpose(x uint64) uint64 {
	t := (x ^ (x >> 7)) & 0x00aa00aa00aa00aa
	x ^= t ^ (t << 7)
	t = (x ^ (x >> 14)) & 0x0000cccc0000cccc
	x ^= t ^ (t << 14)
	t = (x ^ (x >> 28)) & 0x00000000f0f0f0f0
	return x ^ t ^ (t << 28)
}

func gf5Planes(words []uint32, width int) []byte {
	stride := (len(words) + 7) / 8
	data := make([]byte, stride*width*8)
	for at := range stride {
		block := words[at*8 : min((at+1)*8, len(words))]
		for octet := range width {
			var w uint64
			for j, v := range block {
				w |= uint64(byte(v>>(octet*8))) << (j * 8)
			}
			x := gf5Transpose(w)
			for bit := range 8 {
				data[(octet*8+bit)*stride+at] = byte(x >> (bit * 8))
			}
		}
	}
	return data
}

func gf5Unplanes(data []byte, words, width int) []uint32 {
	stride := (words + 7) / 8
	if len(data) != stride*width*8 {
		return nil
	}
	out := make([]uint32, words)
	for at := range stride {
		block := out[at*8 : min((at+1)*8, words)]
		for octet := range width {
			var x uint64
			for bit := range 8 {
				x |= uint64(data[(octet*8+bit)*stride+at]) << (bit * 8)
			}
			w := gf5Transpose(x)
			for j := range block {
				block[j] |= uint32(byte(w>>(j*8))) << (octet * 8)
			}
		}
	}
	return out
}

// Huffman payloads use exact bit interpretations throughout. Reversible
// candidates include XOR, modular zigzag, independent field planes,
// within-tile row lifting, and integer two-checkpoint prediction.
func gf5FPEType(n gf5Numeric) ncNumcoreDType {
	switch {
	case n.Width == 2 && n.Mantissa == 10 && n.Exponent == 5:
		return ncNumcoreDF16
	case n.Width == 2 && n.Mantissa == 7 && n.Exponent == 8:
		return ncNumcoreDBF16
	case n.Width == 4 && n.Mantissa == 23 && n.Exponent == 8:
		return ncNumcoreDF32
	}
	return ncNumcoreDRaw
}

func gf5Encode(mode byte, n gf5Numeric, target, base, prior []byte) []byte {
	if mode == 0 {
		return target
	}
	width := n.Width
	words := len(target) / width
	var mask uint32 = ^uint32(0)
	if width == 2 {
		mask = 65535
	}
	residual := make([]uint32, words)
	for i := range residual {
		at := i * width
		v, b := gf5Word(target[at:], width), gf5Word(base[at:], width)
		switch mode {
		case 1, 6:
			residual[i] = v ^ b
		case 2:
			d := (v - b) & mask
			residual[i] = ((d << 1) ^ -(d >> uint(width*8-1))) & mask
		case 3, 4:
			residual[i] = gf5Fields(v, b, n, false)
		case 5:
			prediction := (2*b - gf5Word(prior[at:], width)) & mask
			residual[i] = gf5Fields(v, prediction, n, false)
		}
	}
	if mode == 4 {
		// Reverse iteration keeps the preceding row in the untouched residual.
		for i := len(residual) - 1; i >= n.Columns; i-- {
			residual[i] ^= residual[i-n.Columns]
		}
	}
	if mode >= 3 && mode <= 5 {
		return gf5Planes(residual, width)
	}
	if mode == 6 {
		head := (words + 7) / 8
		out := make([]byte, head, head+len(target))
		var word [4]byte
		for i, v := range residual {
			if v == 0 {
				continue
			}
			out[i/8] |= 1 << (i % 8)
			gf5Put(word[:], width, v)
			out = append(out, word[:width]...)
		}
		return out
	}
	out := make([]byte, len(target))
	for i, v := range residual {
		gf5Put(out[i*width:], width, v)
	}
	return out
}

func gf5Decode(mode byte, n gf5Numeric, encoded, base, prior []byte, length int) ([]byte, error) {
	if length < 1 || length > gf5Window {
		return nil, gf5Bad
	}
	if mode == 0 {
		if len(encoded) != length {
			return nil, gf5Bad
		}
		return encoded, nil
	}
	if mode == 7 {
		typ := gf5FPEType(n)
		if typ == ncNumcoreDRaw || length%typ.Width() != 0 {
			return nil, gf5Bad
		}
		result, err := ncNumcoreFPEDecode(typ, encoded, length/typ.Width())
		if err != nil || len(result) != length {
			return nil, gf5Bad
		}
		return result, nil
	}
	width := n.Width
	if (width != 2 && width != 4) || length%width != 0 || len(base) != length ||
		n.Exponent < 1 || n.Mantissa < 1 || n.Exponent+n.Mantissa != width*8-1 || mode > 6 {
		return nil, gf5Bad
	}
	words := length / width
	if mode == 4 && (n.Columns < 1 || n.Columns > words) || mode == 5 && len(prior) != length {
		return nil, gf5Bad
	}
	var residual []uint32
	switch mode {
	case 3, 4, 5:
		residual = gf5Unplanes(encoded, words, width)
		if residual == nil {
			return nil, gf5Bad
		}
	case 6:
		head := (words + 7) / 8
		if len(encoded) < head {
			return nil, gf5Bad
		}
		residual = make([]uint32, words)
		left := encoded[head:]
		for i := range residual {
			if encoded[i/8]&(1<<(i%8)) == 0 {
				continue
			}
			if len(left) < width {
				return nil, gf5Bad
			}
			residual[i] = gf5Word(left, width)
			left = left[width:]
		}
		if len(left) != 0 {
			return nil, gf5Bad
		}
	default:
		if len(encoded) != length {
			return nil, gf5Bad
		}
		residual = make([]uint32, words)
		for i := range residual {
			residual[i] = gf5Word(encoded[i*width:], width)
		}
	}
	if mode == 4 {
		for i := n.Columns; i < len(residual); i++ {
			residual[i] ^= residual[i-n.Columns]
		}
	}
	out := make([]byte, length)
	var mask uint32 = ^uint32(0)
	if width == 2 {
		mask = 65535
	}
	for i, v := range residual {
		at := i * width
		b := gf5Word(base[at:], width)
		var value uint32
		switch mode {
		case 1, 6:
			value = v ^ b
		case 2:
			value = b + ((v >> 1) ^ -(v & 1))
		case 3, 4:
			value = gf5Fields(v, b, n, true)
		case 5:
			value = gf5Fields(v, (2*b-gf5Word(prior[at:], width))&mask, n, true)
		}
		gf5Put(out[at:], width, value)
	}
	return out, nil
}

type gf5Frame struct {
	Part, Digest [32]byte
	Size, Offset int
	Base, Prior  int // -1 for an unused reference
	Numeric      gf5Numeric
	Mode         byte
}

type gf5Object struct {
	First, End, Size int
	Digest           [32]byte
}

type gf5Manifest struct {
	Objects []gf5Object
	Frames  []gf5Frame
}

type gf5TensorKey struct {
	name string
	off  int
}
type gf5Candidate struct {
	frame gf5Frame
	wire  []byte
}

func gf5BuildPack(t *testing.T, l *authLab, input [][]byte, mode string) ([32]byte, [8]int, int64) {
	t.Helper()
	var b *packBuilder
	finish := func() {
		path, entries, err := b.Finish()
		mustOK(t, err)
		_, err = l.s.publishPack(tierHot, path, entries)
		mustOK(t, err)
	}
	seen := map[[32]byte]bool{}
	layouts := make([]*repSTFile, len(input))
	prev := make([]map[gf5TensorKey]int, len(input))
	for i, raw := range input {
		layout, err := repSTParse(bytes.NewReader(raw), int64(len(raw)), repSTOptions{})
		mustOK(t, err)
		layouts[i] = layout
		prev[i] = make(map[gf5TensorKey]int)
	}
	var manifest gf5Manifest
	var modes [8]int
	var codedBytes int64
	for obj, raw := range input {
		var err error
		b, err = l.s.newPackBuilder(tierHot, newCompressor(true), 1024)
		mustOK(t, err)
		defer b.Abort()
		root := gf5Object{First: len(manifest.Frames), Size: len(raw), Digest: sha256.Sum256(raw)}
		stage := func(off, n int, name string, relative int, typ gf5Numeric) {
			actual := raw[off : off+n]
			frame := gf5Frame{Digest: sha256.Sum256(actual), Size: n, Offset: off, Base: -1, Prior: -1, Numeric: typ}
			best := gf5Candidate{frame, actual}
			bestBytes := packPriceLen(actual)
			if mode == "anchor-fpe" && gf5FPEType(typ) != ncNumcoreDRaw {
				if data, ok := ncNumcoreFPEEncode(gf5FPEType(typ), actual); ok {
					if price := packPriceLen(data); price+24 < bestBytes {
						f := frame
						f.Mode = 7
						best, bestBytes = gf5Candidate{f, data}, price
					}
				}
			}
			if mode != "direct" && obj > 0 && name != "" && typ.Width != 0 {
				available := func(v int) (int, []byte) {
					id, ok := prev[v][gf5TensorKey{name, relative}]
					if !ok {
						return -1, nil
					}
					origin := manifest.Frames[id]
					original, ok := layouts[v].Lookup(name)
					target, _ := layouts[obj].Lookup(name)
					if !ok || original.DType != target.DType || !slices.Equal(original.Shape, target.Shape) || origin.Size != n {
						return -1, nil
					}
					return id, input[v][origin.Offset : origin.Offset+n]
				}
				original, base := available(obj - 1)
				earlier, prior := -1, []byte(nil)
				if obj > 1 {
					earlier, prior = available(obj - 2)
				}
				if base != nil {
					choices := []byte{1, 2, 3, 4, 5, 6}
					switch mode {
					case "xor":
						choices = []byte{1}
					case "integer-delta":
						choices = []byte{2}
					}
					for _, kind := range choices {
						if kind == 4 && (typ.Columns == 0 || typ.Columns > n/typ.Width) || kind == 5 && prior == nil {
							continue
						}
						coded := gf5Encode(kind, typ, actual, base, prior)
						cost := packPriceLen(coded)
						if cost+24 >= bestBytes {
							continue
						}
						derived := frame
						derived.Mode, derived.Base = kind, original
						if kind == 5 {
							derived.Prior = earlier
						}
						best, bestBytes = gf5Candidate{derived, coded}, cost
					}
				}
			}
			best.frame.Part = gf2AddPackRecord(b, seen, best.wire)
			prev[obj][gf5TensorKey{name, relative}] = len(manifest.Frames)
			modes[best.frame.Mode]++
			codedBytes += int64(len(best.wire))
			manifest.Frames = append(manifest.Frames, best.frame)
		}
		// Exact container header bytes live as independent direct frames.
		head := int(layouts[obj].DataStart)
		for off := 0; off < head; off += gf5Window {
			stage(off, min(gf5Window, head-off), "", off, gf5Numeric{})
		}
		pos := head
		for _, tensor := range layouts[obj].Tensors {
			start, end := head+int(tensor.Begin), head+int(tensor.End)
			if start != pos {
				t.Fatal("non-contiguous exact tensor offsets")
			}
			typ := gf5Type(tensor)
			size := gf5Window
			if typ.Columns > 0 {
				width := typ.Width * typ.Columns
				size -= size % width
			}
			for off := start; off < end; off += size {
				stage(off, min(size, end-off), tensor.Name, off-start, typ)
			}
			pos = end
		}
		if pos != len(raw) {
			t.Fatal("tensor windows did not cover original checkpoint")
		}
		root.End = len(manifest.Frames)
		manifest.Objects = append(manifest.Objects, root)
		if obj != len(input)-1 {
			finish()
		}
	}
	index := gf5EncodeManifest(manifest)
	if len(index) > repDirectMax || len(manifest.Frames) > gf5MaxFrames {
		t.Fatal("checkpoint index outside experiment bound")
	}
	root := gf2AddPackRecord(b, seen, index)
	finish()
	return root, modes, codedBytes
}

const gf5FrameWire = 86
const gf5ObjectWire = 44

func gf5EncodeManifest(m gf5Manifest) []byte {
	out := append([]byte{}, "GF51"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(m.Objects)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(m.Frames)))
	for _, o := range m.Objects {
		for _, n := range []int{o.First, o.End, o.Size} {
			out = binary.LittleEndian.AppendUint32(out, uint32(n))
		}
		out = append(out, o.Digest[:]...)
	}
	for _, f := range m.Frames {
		out = append(out, f.Part[:]...)
		out = append(out, f.Digest[:]...)
		for _, n := range []int{f.Size, f.Offset, f.Base + 1, f.Prior + 1} {
			out = binary.LittleEndian.AppendUint32(out, uint32(n))
		}
		out = append(out, f.Mode, byte(f.Numeric.Width), byte(f.Numeric.Mantissa), byte(f.Numeric.Exponent))
		out = binary.LittleEndian.AppendUint16(out, uint16(f.Numeric.Columns))
	}
	return out
}

func gf5DecodeManifest(raw []byte) (gf5Manifest, error) {
	var m gf5Manifest
	if len(raw) < 12 || string(raw[:4]) != "GF51" {
		return m, gf5Bad
	}
	objects, frames := int(binary.LittleEndian.Uint32(raw[4:])), int(binary.LittleEndian.Uint32(raw[8:]))
	if objects < 1 || objects > 4 || frames < 1 || frames > gf5MaxFrames ||
		len(raw)-12 != objects*gf5ObjectWire+frames*gf5FrameWire {
		return m, gf5Bad
	}
	raw = raw[12:]
	readInt := func() int { v := int(binary.LittleEndian.Uint32(raw)); raw = raw[4:]; return v }
	getSum := func() [32]byte { sum := [32]byte(raw[:32]); raw = raw[32:]; return sum }
	m.Objects = make([]gf5Object, objects)
	for i := range m.Objects {
		m.Objects[i] = gf5Object{First: readInt(), End: readInt(), Size: readInt(), Digest: getSum()}
	}
	m.Frames = make([]gf5Frame, frames)
	for i := range m.Frames {
		f := gf5Frame{Part: getSum(), Digest: getSum(), Size: readInt(), Offset: readInt(), Base: readInt() - 1, Prior: readInt() - 1}
		f.Mode = raw[0]
		f.Numeric = gf5Numeric{int(raw[1]), int(raw[2]), int(raw[3]), int(binary.LittleEndian.Uint16(raw[4:]))}
		raw = raw[6:]
		m.Frames[i] = f
	}
	return m, nil
}

func gf5Open(s *Store, id [32]byte) (*gf5Manifest, error) {
	raw, err := s.casRead(id)
	if err != nil {
		return nil, err
	}
	m, err := gf5DecodeManifest(raw)
	if err != nil {
		return nil, err
	}
	first := 0
	for _, o := range m.Objects {
		if o.First != first || o.End <= o.First || o.End > len(m.Frames) || o.Size < 1 || o.Size > gf5MaxFile {
			return nil, gf5Bad
		}
		at := 0
		for k := o.First; k < o.End; k++ {
			f := m.Frames[k]
			if f.Offset != at || f.Size < 1 || f.Size > gf5Window || f.Mode > 7 || f.Size > o.Size-at {
				return nil, gf5Bad
			}
			if f.Mode > 0 && f.Mode < 7 && (f.Base < 0 || f.Base >= o.First || m.Frames[f.Base].Size != f.Size) || f.Mode == 5 && (f.Prior < 0 || f.Prior >= o.First || m.Frames[f.Prior].Size != f.Size) {
				return nil, gf5Bad
			}
			at += f.Size
		}
		if at != o.Size {
			return nil, gf5Bad
		}
		first = o.End
	}
	if first != len(m.Frames) {
		return nil, gf5Bad
	}
	return &m, nil
}

func (m *gf5Manifest) read(s *Store, obj, off, length int) ([]byte, int64, error) {
	if obj < 0 || obj >= len(m.Objects) {
		return nil, 0, gf5Bad
	}
	root := m.Objects[obj]
	if off < 0 || length < 0 || off > root.Size || length > root.Size-off {
		return nil, 0, gf5Bad
	}
	source := s.newRepSource(nil, nil)
	defer source.close()
	var sourceBytes int64
	var load func(int, int) ([]byte, error)
	cache := map[int][]byte{}
	load = func(i, depth int) ([]byte, error) {
		if depth > 3 {
			return nil, gf5Bad
		}
		if b := cache[i]; b != nil {
			return b, nil
		}
		f := m.Frames[i]
		raw, err := repReadObject(source, f.Part, -1, gf5Window*2)
		if err != nil {
			return nil, err
		}
		sourceBytes += int64(len(raw))
		var b, p []byte
		if f.Mode > 0 && f.Mode < 7 {
			b, err = load(f.Base, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if f.Mode == 5 {
			p, err = load(f.Prior, depth+1)
			if err != nil {
				return nil, err
			}
		}
		result, err := gf5Decode(f.Mode, f.Numeric, raw, b, p, f.Size)
		if err != nil {
			return nil, err
		}
		if sha256.Sum256(result) != f.Digest {
			return nil, gf5Bad
		}
		cache[i] = result
		return result, nil
	}
	i := sort.Search(root.End-root.First, func(k int) bool { f := m.Frames[root.First+k]; return f.Offset+f.Size > off }) + root.First
	result := make([]byte, 0, length)
	whole := off == 0 && length == root.Size
	for i < root.End && len(result) < length {
		if whole && (i-root.First)%8 == 0 {
			var keys [][32]byte
			covered := map[[32]byte]bool{}
			var include func(int)
			include = func(k int) {
				f := m.Frames[k]
				if covered[f.Part] {
					return
				}
				covered[f.Part] = true
				keys = append(keys, f.Part)
				if f.Mode > 0 && f.Mode < 7 {
					include(f.Base)
				}
				if f.Mode == 5 {
					include(f.Prior)
				}
			}
			for k := i; k < min(i+8, root.End); k++ {
				include(k)
			}
			source.PrefetchSet(keys)
		}
		frame := m.Frames[i]
		b, err := load(i, 0)
		if err != nil {
			return nil, sourceBytes, err
		}
		begin, end := max(0, off-frame.Offset), min(frame.Size, off+length-frame.Offset)
		result = append(result, b[begin:end]...)
		// Parents are nearby tensor tiles. Keep request memory bounded on
		// whole reads instead of retaining every decoded frame.
		if len(cache) > 16 {
			clear(cache)
		}
		i++
	}
	if len(result) != length {
		return nil, sourceBytes, gf5Bad
	}
	return result, sourceBytes, nil
}

func gf5FixtureReads(t *testing.T) [][]byte {
	t.Helper()
	root := os.Getenv("ZEROS3_GF5_FIXTURES")
	if root == "" {
		t.Skip("set ZEROS3_GF5_FIXTURES to pinned public checkpoints")
	}
	data, err := os.ReadFile("testing-harnesses/grand-frontier/GF5/inputs.json")
	mustOK(t, err)
	var rows []struct {
		File, SHA256 string
		Bytes        int
	}
	mustOK(t, json.Unmarshal(data, &rows))
	var out [][]byte
	for _, row := range rows {
		raw, err := os.ReadFile(filepath.Join(root, row.File))
		mustOK(t, err)
		if len(raw) != row.Bytes || fmt.Sprintf("%x", sha256.Sum256(raw)) != row.SHA256 {
			t.Fatal("public checkpoint failed pinned SHA-256")
		}
		out = append(out, raw)
	}
	return out
}

func TestGF5TensorResiduals(t *testing.T) {
	for _, kind := range []string{"F16", "BF16", "F32"} {
		typ := gf5Type(repSTTensor{DType: kind, Shape: []int64{7, 11}})
		base, prior, target := genRandomBytes(1947, 77*typ.Width), genRandomBytes(8211, 77*typ.Width), genRandomBytes(17, 77*typ.Width)
		// Deliberately preserve both zero signs and incompatible NaN payloads.
		sign := uint32(1) << uint(typ.Width*8-1)
		special := (uint32(1)<<uint(typ.Exponent) - 1) << uint(typ.Mantissa)
		for i, v := range []uint32{0, sign, special | 5, sign | special | 19} {
			gf5Put(target[i*typ.Width:], typ.Width, v)
		}
		for mode := byte(1); mode <= 6; mode++ {
			wire := gf5Encode(mode, typ, target, base, prior)
			got, err := gf5Decode(mode, typ, wire, base, prior, len(target))
			mustOK(t, err)
			if !bytes.Equal(got, target) {
				t.Fatalf("%s mode %d altered exact bits", kind, mode)
			}
			if _, err := gf5Decode(mode, typ, wire[:len(wire)-1], base, prior, len(target)); err == nil {
				t.Fatal("accepted truncated bit-plane or residual")
			}
		}
	}
}

func TestGF5CheckpointPanel(t *testing.T) {
	input := gf5FixtureReads(t)
	for _, mode := range []string{"direct", "xor", "integer-delta", "adaptive", "anchor-fpe"} {
		t.Run(mode, func(t *testing.T) {
			l := newAuthLab(t)
			before, cpu := time.Now(), fb13CPU()
			index, selected, coded := gf5BuildPack(t, l, input, mode)
			buildNS, buildCPU := time.Since(before).Nanoseconds(), int64(fb13CPU()-cpu)
			l.close()
			total, packs, _ := fb13Allocation(t, l.dir)
			l.open()
			before = time.Now()
			catalog, err := gf5Open(l.s, index)
			mustOK(t, err)
			startNS := time.Since(before).Nanoseconds()
			var whole, small, fetch []int64
			readCPU := fb13CPU()
			for i, raw := range input {
				before = time.Now()
				got, cost, err := catalog.read(l.s, i, 0, len(raw))
				whole = append(whole, time.Since(before).Nanoseconds())
				mustOK(t, err)
				if sha256.Sum256(got) != catalog.Objects[i].Digest || !bytes.Equal(got, raw) {
					t.Fatal("checkpoint restore differs")
				}
				for k := range 18 {
					at := (len(raw) - 4096) * k / 17
					before = time.Now()
					slice, cost, err := catalog.read(l.s, i, at, 4096)
					small = append(small, time.Since(before).Nanoseconds())
					fetch = append(fetch, cost)
					mustOK(t, err)
					if !bytes.Equal(slice, raw[at:at+4096]) {
						t.Fatal("checkpoint range differs")
					}
				}
				t.Logf("verified original checkpoint %d using %d logical record bytes", i, cost)
			}
			readDurationCPU := int64(fb13CPU() - readCPU)
			kept, parts := gf5PrunePackedCheckpoint(t, l, index, catalog, len(input)-1, input[len(input)-1])
			held := catalog.Objects[len(input)-1]
			var heldModes [8]int
			for _, f := range catalog.Frames[held.First:held.End] {
				heldModes[f.Mode]++
			}
			row, err := json.Marshal(map[string]any{"lane": mode, "allocated": total, "pack_allocated": packs,
				"source_logical": len(input[0]) * len(input), "frame_mode_counts": selected, "unpacked_payload_bytes": coded,
				"build_wall_ns": buildNS, "build_cpu_ns": buildCPU, "startup_ns": startNS, "read_cpu_ns": readDurationCPU,
				"retained_last_allocated": kept, "retained_last_records": parts, "heldout_frame_modes": heldModes,
				"whole_p50_p95_p99_ns": gf0Percentiles(whole), "range_p50_p95_p99_ns": gf0Percentiles(small),
				"range_decoded_record_p95": fb14Quantile(fetch, 95)})
			mustOK(t, err)
			fmt.Printf("GF5PACK %s\n", row)
		})
	}
}

func gf5ControlFile(t *testing.T, dtype string, order []string, tensors map[string][]byte) []byte {
	t.Helper()
	width := repSTDTypeBits[dtype] / 8
	table := map[string]any{"__metadata__": map[string]string{"kind": "generated state control", "order": fmt.Sprint(order)}}
	var body []byte
	for _, name := range order {
		raw := tensors[name]
		if len(raw)%(width*64) != 0 {
			t.Fatal("unaligned control tensor")
		}
		table[name] = map[string]any{"dtype": dtype, "shape": []int{len(raw) / (width * 64), 64}, "data_offsets": []int{len(body), len(body) + len(raw)}}
		body = append(body, raw...)
	}
	head, err := json.Marshal(table)
	mustOK(t, err)
	// Preserve intentional padding and the original JSON ordering verbatim.
	head = append(head, []byte("      ")...)
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(head)))
	out = append(out, head...)
	return append(out, body...)
}

func TestGF5LayoutAndDTypeControls(t *testing.T) {
	a := map[string][]byte{
		"weight":           genRandomBytes(295, 64<<10),
		"optimizer.moment": genRandomBytes(628, 32<<10),
	}
	b := map[string][]byte{"weight": slices.Clone(a["weight"]), "optimizer.moment": slices.Clone(a["optimizer.moment"])}
	b["weight"][831] ^= 19
	b["optimizer.moment"][5709] ^= 5
	unknown := map[string][]byte{"weight": genRandomBytes(8251, 64<<10), "optimizer.moment": genRandomBytes(119, 32<<10)}
	input := [][]byte{
		gf5ControlFile(t, "F16", []string{"weight", "optimizer.moment"}, a),
		gf5ControlFile(t, "F16", []string{"optimizer.moment", "weight"}, b),
		gf5ControlFile(t, "BF16", []string{"weight", "optimizer.moment"}, unknown),
	}
	l := newAuthLab(t)
	index, modes, _ := gf5BuildPack(t, l, input, "adaptive")
	if modes[0] == 0 || modes[6] == 0 {
		t.Fatal("control did not test both fallback and sparse exact changes")
	}
	l.close()
	l.open()
	catalog, err := gf5Open(l.s, index)
	mustOK(t, err)
	for i, raw := range input {
		got, _, err := catalog.read(l.s, i, 0, len(raw))
		mustOK(t, err)
		if !bytes.Equal(got, raw) {
			t.Fatal("layout change lost original bytes")
		}
	}
	last := catalog.Objects[2]
	for _, f := range catalog.Frames[last.First:last.End] {
		if f.Mode != 0 {
			t.Fatal("dtype mismatch selected a numerical ancestor")
		}
	}
}

func FuzzGF5TensorWire(f *testing.F) {
	f.Add(uint8(2), uint16(128), []byte{1, 0, 3, 0, 5})
	f.Add(uint8(3), uint16(10), []byte{3, 7})
	f.Add(uint8(6), uint16(8), []byte{1, 0, 2})
	f.Fuzz(func(t *testing.T, mode uint8, words uint16, payload []byte) {
		n := int(words) % 257
		length := n * 2
		data := make([]byte, length)
		got, err := gf5Decode(mode, gf5Numeric{2, 10, 5, 16}, payload, data, data, length)
		if err == nil && len(got) != length {
			t.Fatal("numerical payload bypassed exact length")
		}
	})
}

// Repack only the verified closure for one retained checkpoint. Older
// frame rows stay in the index as candidate internal vertices. A parent
// without reachable descendants contributes no physical record.
func gf5PrunePackedCheckpoint(t *testing.T, l *authLab, id [32]byte, catalog *gf5Manifest, obj int, expected []byte) (int64, int) {
	t.Helper()
	dst := newAuthLab(t)
	b, err := dst.s.newPackBuilder(tierHot, newCompressor(true), 1024)
	mustOK(t, err)
	defer b.Abort()
	seen := map[[32]byte]bool{}
	visited := make([]bool, len(catalog.Frames))
	var stage func(int)
	stage = func(k int) {
		if visited[k] {
			return
		}
		visited[k] = true
		f := catalog.Frames[k]
		if f.Mode > 0 && f.Mode < 7 {
			stage(f.Base)
		}
		if f.Mode == 5 {
			stage(f.Prior)
		}
		raw, err := l.s.casRead(f.Part)
		mustOK(t, err)
		if gf2AddPackRecord(b, seen, raw) != f.Part {
			t.Fatal("checkpoint carry altered a payload")
		}
	}
	root := catalog.Objects[obj]
	for k := root.First; k < root.End; k++ {
		stage(k)
	}
	index, err := l.s.casRead(id)
	mustOK(t, err)
	if gf2AddPackRecord(b, seen, index) != id {
		t.Fatal("checkpoint carry altered the index")
	}
	path, entries, err := b.Finish()
	mustOK(t, err)
	_, err = dst.s.publishPack(tierHot, path, entries)
	mustOK(t, err)
	dst.close()
	allocated, _, _ := fb13Allocation(t, dst.dir)
	dst.open()
	packed, err := gf5Open(dst.s, id)
	mustOK(t, err)
	raw, _, err := packed.read(dst.s, obj, 0, len(expected))
	mustOK(t, err)
	if !bytes.Equal(raw, expected) {
		t.Fatal("pruning broke the retained checkpoint")
	}
	return allocated, len(seen)
}
