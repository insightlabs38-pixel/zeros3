package diskimg

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"unicode/utf16"
)

// VHDX payload / bitmap BAT states (MS-VHDX 2.5.1.1).
const (
	VNotPresent = 0
	VUndefined  = 1
	VZero       = 2
	VUnmapped   = 3
	VFull       = 6
	VPartial    = 7
)

// GUIDs as stored in the file (Windows mixed-endian byte order).
var (
	guidBAT      = [16]byte{0x66, 0x77, 0xC2, 0x2D, 0x23, 0xF6, 0x00, 0x42, 0x9D, 0x64, 0x11, 0x5E, 0x9B, 0xFD, 0x4A, 0x08}
	guidMetadata = [16]byte{0x06, 0xA2, 0x7C, 0x8B, 0x90, 0x47, 0x9A, 0x4B, 0xB8, 0xFE, 0x57, 0x5F, 0x05, 0x0F, 0x88, 0x6E}
	guidFileParm = [16]byte{0x37, 0x67, 0xA1, 0xCA, 0x36, 0xFA, 0x43, 0x4D, 0xB3, 0xB6, 0x33, 0xF0, 0xAA, 0x44, 0xE7, 0x6B}
	guidVDSize   = [16]byte{0x24, 0x42, 0xA5, 0x2F, 0x1B, 0xCD, 0x76, 0x48, 0xB2, 0x11, 0x5D, 0xBE, 0xD8, 0x3B, 0xF4, 0xB8}
	guidVDID     = [16]byte{0xAB, 0x12, 0xCA, 0xBE, 0xE6, 0xB2, 0x23, 0x45, 0x93, 0xEF, 0xC3, 0x09, 0xE0, 0x00, 0xC7, 0x46}
	guidLSS      = [16]byte{0x1D, 0xBF, 0x41, 0x81, 0x6F, 0xA9, 0x09, 0x47, 0xBA, 0x47, 0xF2, 0x33, 0xA8, 0xFA, 0xAB, 0x5F}
	guidPSS      = [16]byte{0xC7, 0x48, 0xA3, 0xCD, 0x5D, 0x44, 0x71, 0x44, 0x9C, 0xC9, 0xE9, 0x88, 0x52, 0x51, 0xC5, 0x56}
	guidParent   = [16]byte{0x2D, 0x5F, 0xD3, 0xA8, 0x0B, 0xB3, 0x4D, 0x45, 0xAB, 0xF7, 0xD3, 0xD8, 0x48, 0x34, 0xAB, 0x0C}
	guidLocVHDX  = [16]byte{0xB7, 0xEF, 0x4A, 0xB0, 0x9E, 0xD1, 0x81, 0x4A, 0xB7, 0x89, 0x25, 0xB8, 0xE9, 0x44, 0x59, 0x13}
)

var crc32c = crc32.MakeTable(crc32.Castagnoli)

// VBlock is one payload block's BAT mapping.
type VBlock struct {
	State uint8
	Off   int64 // file offset of the block (0 if none)
	// Present lists, for VPartial blocks, the logical-sector runs (within the block) present in this file.
	Present []SecRun
}

// SecRun is a run of logical sectors inside a payload block.
type SecRun struct{ First, Count int }

// Vhdx is the bounded structural view of a VHDX file.
type Vhdx struct {
	FileSize      int64
	Seq           [2]uint64
	ActiveHeader  int
	FileWriteGUID [16]byte
	DataWriteGUID [16]byte
	LogGUID       [16]byte
	LogOff        int64
	LogLen        int64
	BlockSize     int64
	LSS, PSS      int64
	VirtSize      int64
	DiskID        [16]byte
	HasParent     bool
	Leave         bool
	Parent        map[string]string // parent locator key/values
	ChunkRatio    int64
	BATOff        int64
	BATLen        int64
	MetaOff       int64
	MetaLen       int64
	BATEntries    int64
	BitmapState   []uint8 // per chunk
	BitmapOff     []int64
	Blocks        []VBlock
	Regions       []Region

	// byte offsets (file) of fields that vary per produced file, for normalisation / churn analysis
	fieldOff map[string]int64
}

const (
	vMaxBlocks = 1 << 22
	vMaxLocKV  = 64
)

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// vchecksum verifies the CRC-32C stored at off+4 over n bytes with the field zeroed.
func vchecksum(b []byte, n int) bool {
	if len(b) < n {
		return false
	}
	t := append([]byte(nil), b[:n]...)
	want := le32(t[4:])
	clear(t[4:8])
	return crc32.Checksum(t, crc32c) == want
}

// ScanVhdx parses the file type identifier, headers, region tables, metadata, BAT and sector bitmaps and tiles the file.
// Pending log entries are reported through LogGUID != 0 and make the scan fail (replay is out of scope).
func ScanVhdx(r io.ReaderAt, size int64) (*Vhdx, error) {
	if size < 1*MiB {
		return nil, bad("file shorter than 1 MiB")
	}
	fh, err := readFull(r, size, 0, 8)
	if err != nil {
		return nil, err
	}
	if string(fh) != "vhdxfile" {
		return nil, bad("not a VHDX file")
	}
	v := &Vhdx{FileSize: size, fieldOff: map[string]int64{}}
	// headers
	var hb [2][]byte
	valid := [2]bool{}
	for i, off := range []int64{64 * KiB, 128 * KiB} {
		b, err := readFull(r, size, off, 4096)
		if err != nil {
			return nil, err
		}
		hb[i] = b
		if string(b[:4]) == "head" && vchecksum(b, 4096) && le16(b[66:]) == 1 {
			valid[i] = true
			v.Seq[i] = le64(b[8:])
		}
	}
	switch {
	case !valid[0] && !valid[1]:
		return nil, bad("no valid header")
	case valid[0] && valid[1]:
		if v.Seq[1] > v.Seq[0] {
			v.ActiveHeader = 1
		}
	case valid[1]:
		v.ActiveHeader = 1
	}
	h := hb[v.ActiveHeader]
	copy(v.FileWriteGUID[:], h[16:32])
	copy(v.DataWriteGUID[:], h[32:48])
	copy(v.LogGUID[:], h[48:64])
	if le16(h[64:]) != 0 {
		return nil, ErrUnsupported{"log version"}
	}
	v.LogLen = int64(le32(h[68:]))
	v.LogOff = int64(le64(h[72:]))
	if v.LogGUID != [16]byte{} {
		return nil, ErrUnsupported{"pending log entries (replay not modelled)"}
	}
	// region tables
	var rt []byte
	for _, off := range []int64{192 * KiB, 256 * KiB} {
		b, err := readFull(r, size, off, 64*KiB)
		if err != nil {
			return nil, err
		}
		if string(b[:4]) == "regi" && vchecksum(b, 64*KiB) {
			rt = b
			break
		}
	}
	if rt == nil {
		return nil, bad("no valid region table")
	}
	n := int(le32(rt[8:]))
	if n > 2047 {
		return nil, bad("region count %d", n)
	}
	for i := 0; i < n; i++ {
		e := rt[16+32*i:]
		var g [16]byte
		copy(g[:], e[:16])
		off, ln, req := int64(le64(e[16:])), int64(le32(e[24:])), le32(e[28:])&1 != 0
		if off < MiB || off%MiB != 0 || ln == 0 || ln%MiB != 0 || off+ln > size || off+ln < off {
			return nil, bad("region %d [%d,+%d)", i, off, ln)
		}
		switch g {
		case guidBAT:
			v.BATOff, v.BATLen = off, ln
		case guidMetadata:
			v.MetaOff, v.MetaLen = off, ln
		default:
			if req {
				return nil, ErrUnsupported{"unknown required region"}
			}
		}
	}
	if v.BATLen == 0 || v.MetaLen == 0 {
		return nil, bad("missing BAT or metadata region")
	}
	if err := v.readMetadata(r); err != nil {
		return nil, err
	}
	claims := []Region{
		{0, 64 * KiB, ClassHeader}, {64 * KiB, 256 * KiB, ClassHeaders},
		{v.BATOff, v.BATLen, ClassBAT}, {v.MetaOff, v.MetaLen, ClassMetadata},
	}
	if v.LogLen > 0 {
		if v.LogOff < MiB || v.LogOff%MiB != 0 || v.LogLen%MiB != 0 || v.LogOff+v.LogLen > size {
			return nil, bad("log region")
		}
		claims = append(claims, Region{v.LogOff, v.LogLen, ClassLog})
	}
	cl2, err := v.readBAT(r)
	if err != nil {
		return nil, err
	}
	v.Regions, err = tile(size, append(claims, cl2...))
	return v, err
}

func (v *Vhdx) readMetadata(r io.ReaderAt) error {
	tb, err := readFull(r, v.FileSize, v.MetaOff, 64*KiB)
	if err != nil {
		return err
	}
	if string(tb[:8]) != "metadata" {
		return bad("metadata table signature")
	}
	cnt := int(le16(tb[10:]))
	if cnt > 2047 {
		return bad("metadata entries %d", cnt)
	}
	have := map[[16]byte]bool{}
	item := func(i int) (id [16]byte, off, ln int64, req bool, err error) {
		e := tb[32+32*i:]
		copy(id[:], e[:16])
		off, ln, req = int64(le32(e[16:])), int64(le32(e[20:])), le32(e[24:])&4 != 0
		if off < 64*KiB || ln < 0 || off+ln > v.MetaLen {
			err = bad("metadata item %d [%d,+%d)", i, off, ln)
		}
		return
	}
	var locOff, locLen int64
	for i := 0; i < cnt; i++ {
		id, off, ln, req, err := item(i)
		if err != nil {
			return err
		}
		if have[id] {
			return bad("duplicate metadata item")
		}
		have[id] = true
		get := func(want int64) ([]byte, error) {
			if ln != want {
				return nil, bad("metadata item length %d", ln)
			}
			return readFull(r, v.FileSize, v.MetaOff+off, int(ln))
		}
		switch id {
		case guidFileParm:
			b, err := get(8)
			if err != nil {
				return err
			}
			v.BlockSize = int64(le32(b))
			fl := le32(b[4:])
			v.Leave, v.HasParent = fl&1 != 0, fl&2 != 0
			v.fieldOff["fileparams"] = v.MetaOff + off
		case guidVDSize:
			b, err := get(8)
			if err != nil {
				return err
			}
			vs := le64(b)
			if vs > 1<<62 {
				return bad("virtual size")
			}
			v.VirtSize = int64(vs)
		case guidVDID:
			b, err := get(16)
			if err != nil {
				return err
			}
			copy(v.DiskID[:], b)
			v.fieldOff["diskid"] = v.MetaOff + off
		case guidLSS:
			b, err := get(4)
			if err != nil {
				return err
			}
			v.LSS = int64(le32(b))
		case guidPSS:
			b, err := get(4)
			if err != nil {
				return err
			}
			v.PSS = int64(le32(b))
		case guidParent:
			locOff, locLen = off, ln
		default:
			if req {
				return ErrUnsupported{"unknown required metadata item"}
			}
		}
	}
	for _, g := range [][16]byte{guidFileParm, guidVDSize, guidVDID, guidLSS, guidPSS} {
		if !have[g] {
			return bad("missing required metadata item")
		}
	}
	if v.BlockSize < MiB || v.BlockSize > 256*MiB || v.BlockSize&(v.BlockSize-1) != 0 {
		return bad("block size %d", v.BlockSize)
	}
	if v.LSS != 512 && v.LSS != 4096 || v.PSS != 512 && v.PSS != 4096 {
		return bad("sector sizes %d/%d", v.LSS, v.PSS)
	}
	if v.VirtSize == 0 || v.VirtSize%v.LSS != 0 {
		return bad("virtual size %d", v.VirtSize)
	}
	if v.HasParent != have[guidParent] {
		return bad("HasParent flag disagrees with parent locator presence")
	}
	if v.HasParent {
		return v.readLocator(r, locOff, locLen)
	}
	return nil
}

func (v *Vhdx) readLocator(r io.ReaderAt, off, ln int64) error {
	if ln < 20 || ln > 64*KiB {
		return bad("parent locator length %d", ln)
	}
	b, err := readFull(r, v.FileSize, v.MetaOff+off, int(ln))
	if err != nil {
		return err
	}
	var typ [16]byte
	copy(typ[:], b[:16])
	if typ != guidLocVHDX {
		return ErrUnsupported{"parent locator type"}
	}
	kv := int(le16(b[18:]))
	if kv > vMaxLocKV || 20+12*kv > len(b) {
		return bad("parent locator entries %d", kv)
	}
	v.Parent = map[string]string{}
	str := func(o, l int) (string, bool) {
		if o < 0 || l < 0 || l%2 != 0 || o+l > len(b) || o < 20+12*kv {
			return "", false
		}
		u := make([]uint16, l/2)
		for i := range u {
			u[i] = le16(b[o+2*i:])
		}
		return string(utf16.Decode(u)), true
	}
	for i := 0; i < kv; i++ {
		e := b[20+12*i:]
		k, ok1 := str(int(le32(e)), int(le16(e[8:])))
		val, ok2 := str(int(le32(e[4:])), int(le16(e[10:])))
		if !ok1 || !ok2 {
			return bad("parent locator entry %d", i)
		}
		if _, dup := v.Parent[k]; dup {
			return bad("duplicate parent locator key %q", k)
		}
		v.Parent[k] = val
		v.fieldOff["loc:"+k] = v.MetaOff + off + int64(le32(e[4:]))
	}
	if _, ok := v.Parent["parent_linkage"]; !ok {
		return bad("parent locator without parent_linkage")
	}
	return nil
}

func (v *Vhdx) readBAT(r io.ReaderAt) ([]Region, error) {
	nb := (v.VirtSize + v.BlockSize - 1) / v.BlockSize
	if nb > vMaxBlocks {
		return nil, bad("%d payload blocks", nb)
	}
	v.ChunkRatio = (1 << 23) * v.LSS / v.BlockSize
	chunks := (nb + v.ChunkRatio - 1) / v.ChunkRatio
	entries := nb + (nb-1)/v.ChunkRatio
	if v.HasParent {
		entries = chunks * (v.ChunkRatio + 1)
	}
	v.BATEntries = entries
	if entries*8 > v.BATLen {
		return nil, bad("BAT of %d entries does not fit region of %d bytes", entries, v.BATLen)
	}
	bat, err := readFull(r, v.FileSize, v.BATOff, int(entries*8))
	if err != nil {
		return nil, err
	}
	ent := func(i int64) (uint8, int64, error) {
		e := le64(bat[i*8:])
		st := uint8(e & 7)
		if e&(0x1ffff<<3) != 0 {
			return 0, 0, bad("BAT[%d] reserved bits", i)
		}
		return st, int64(e>>20) * MiB, nil
	}
	var claims []Region
	v.Blocks = make([]VBlock, nb)
	v.BitmapState = make([]uint8, chunks)
	v.BitmapOff = make([]int64, chunks)
	for c := int64(0); c < chunks; c++ {
		bi := c*(v.ChunkRatio+1) + v.ChunkRatio
		if bi >= entries {
			continue
		}
		st, off, err := ent(bi)
		if err != nil {
			return nil, err
		}
		switch st {
		case VNotPresent:
		case VFull:
			if off < MiB || off%MiB != 0 || off+MiB > v.FileSize {
				return nil, bad("sector bitmap %d at %d", c, off)
			}
			claims = append(claims, Region{off, MiB, ClassBitmap})
			v.BitmapOff[c], v.BitmapState[c] = off, st
		default:
			return nil, bad("sector bitmap state %d", st)
		}
	}
	for i := int64(0); i < nb; i++ {
		st, off, err := ent(i + i/v.ChunkRatio)
		if err != nil {
			return nil, err
		}
		blk := VBlock{State: st, Off: off}
		switch st {
		case VNotPresent, VUndefined, VZero, VUnmapped, VFull, VPartial:
		default:
			return nil, bad("block %d state %d", i, st)
		}
		if st == VFull || st == VPartial || off != 0 { // retained (zero/unmapped) allocations still occupy the file
			if off < MiB || off%MiB != 0 || off+v.BlockSize > v.FileSize {
				return nil, bad("block %d (state %d) offset %d", i, st, off)
			}
			claims = append(claims, Region{off, v.BlockSize, ClassPayload})
		}
		if st == VPartial {
			if !v.HasParent {
				return nil, bad("partially present block %d in a non-differencing file", i)
			}
			c := i / v.ChunkRatio
			if v.BitmapState[c] != VFull {
				return nil, bad("partially present block %d without sector bitmap", i)
			}
			runs, err := v.bitmapRuns(r, c, i)
			if err != nil {
				return nil, err
			}
			blk.Present = runs
		}
		v.Blocks[i] = blk
	}
	return claims, nil
}

// bitmapRuns returns present-sector runs of block i from chunk c's sector bitmap.
func (v *Vhdx) bitmapRuns(r io.ReaderAt, c, i int64) ([]SecRun, error) {
	spb := v.BlockSize / v.LSS
	first := (i % v.ChunkRatio) * spb
	// bits [first, first+spb) of the chunk bitmap
	b, err := readFull(r, v.FileSize, v.BitmapOff[c]+first/8, int((spb+7)/8))
	if err != nil {
		return nil, err
	}
	if first%8 != 0 {
		return nil, bad("unaligned bitmap slice")
	}
	var runs []SecRun
	for s := int64(0); s < spb; s++ {
		if b[s/8]>>(uint(s)%8)&1 == 0 {
			continue
		}
		if n := len(runs); n > 0 && int64(runs[n-1].First+runs[n-1].Count) == s {
			runs[n-1].Count++
		} else {
			runs = append(runs, SecRun{int(s), 1})
		}
	}
	return runs, nil
}

// FieldOffsets exposes file offsets of per-file variable fields for normalisation/attribution.
func (v *Vhdx) FieldOffsets() map[string]int64 { return v.fieldOff }

func crc32Sum(b []byte) uint32 { return crc32.Checksum(b, crc32c) }
