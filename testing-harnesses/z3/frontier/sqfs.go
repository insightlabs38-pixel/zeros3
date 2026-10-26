package frontier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SquashFS 4.0 structural scanner (Z3-24). It walks the superblock, the
// metadata-block chains (inode and directory tables), the fragment table and
// every regular file's block list, without decoding any data block. Metadata
// blocks are the only compressed structures it must read, so the codec is
// injected (Decompress): that dependence is itself a finding. Bounded: every
// offset/length is range-checked, counts are limited by the superblock inode
// count and explicit maxima, and nothing allocates from a hostile length alone.

type Decompress func(src []byte, max int) ([]byte, error)

var ErrSquashfs = errors.New("squashfs: malformed image")

const sqMetaMax = 8192

type SqSuper struct {
	Magic, Inodes, MkfsTime, BlockSize, Frags  uint32
	Comp, BlockLog, Flags, NoIDs, Major, Minor uint16
	RootInode, BytesUsed                       uint64
	IDTable, XattrTable, InodeTable, DirTable  uint64
	FragTable, LookupTable                     uint64
}

// SqBlock is one physical region of the image.
type SqBlock struct {
	Off, Len int64
	Kind     string // super data frag inode dir fragtab lookup idtab xattr pad
	File     string // owning file for data blocks
	Idx      int
	Raw      bool // stored uncompressed
}

type SqFile struct {
	Path   string
	Size   int64
	Start  int64
	Frag   int64 // fragment index or -1
	FragOf int64
	Blocks []SqBlock // data blocks in file order (sparse blocks omitted)
	Holes  int       // sparse (size 0) blocks
	Nlink  int
	// metadata blocks needed to resolve this path (inode/dir tables), disk spans
	Meta []Span
}

type SqImage struct {
	Size    int64
	Super   SqSuper
	Blocks  []SqBlock // every region, sorted, tiling [0,Size)
	Files   map[string]*SqFile
	FragBlk []SqBlock         // fragment blocks (index = fragment number)
	FragMD  []SqBlock         // fragment-table metadata blocks (512 entries each)
	FragIdx Span              // fragment-table index
	DirMeta map[string][]Span // directory path -> metadata blocks needed to list it
	Dirs    int
}

type metaChain struct {
	base  int64 // disk offset of the chain start (spans returned are absolute)
	buf   []byte
	disk  []int64 // disk offset of each block
	bufAt []int   // buffer offset of each block start
	dlen  []int64
}

func (m *metaChain) pos(blockOff int64, off int) (int, error) {
	i := sort.Search(len(m.disk), func(i int) bool { return m.disk[i] >= blockOff })
	if i == len(m.disk) || m.disk[i] != blockOff {
		return 0, fmt.Errorf("%w: no metadata block at %d", ErrSquashfs, blockOff)
	}
	p := m.bufAt[i] + off
	end := len(m.buf)
	if i+1 < len(m.bufAt) {
		end = m.bufAt[i+1]
	}
	if off < 0 || p > end {
		return 0, fmt.Errorf("%w: offset %d outside metadata block", ErrSquashfs, off)
	}
	return p, nil
}

// spans returns the absolute spans of every metadata block covering buffer range [p0,p1].
func (m *metaChain) spans(p0, p1 int) []Span {
	i := sort.Search(len(m.bufAt), func(i int) bool { return m.bufAt[i] > p0 }) - 1
	j := sort.Search(len(m.bufAt), func(i int) bool { return m.bufAt[i] > p1 }) - 1
	var out []Span
	for k := max(i, 0); k <= j && k < len(m.disk); k++ {
		out = append(out, Span{m.base + m.disk[k], m.dlen[k]})
	}
	return out
}

// readChain reads consecutive metadata blocks in [start,end) (disk offsets).
func readChain(r io.ReaderAt, start, end int64, dec Decompress, base int64) (*metaChain, error) {
	m := &metaChain{base: base}
	for off := start; off < end; {
		var h [2]byte
		if _, err := r.ReadAt(h[:], off); err != nil {
			return nil, err
		}
		v := binary.LittleEndian.Uint16(h[:])
		n := int64(v & 0x7fff)
		if n == 0 || n > sqMetaMax || off+2+n > end {
			return nil, fmt.Errorf("%w: metadata block at %d size %d", ErrSquashfs, off, n)
		}
		raw := make([]byte, n)
		if _, err := r.ReadAt(raw, off+2); err != nil {
			return nil, err
		}
		if v&0x8000 == 0 {
			var err error
			if raw, err = dec(raw, sqMetaMax); err != nil {
				return nil, fmt.Errorf("metadata block at %d: %w", off, err)
			}
		}
		m.disk = append(m.disk, off-base)
		m.bufAt = append(m.bufAt, len(m.buf))
		m.dlen = append(m.dlen, 2+n)
		m.buf = append(m.buf, raw...)
		off += 2 + n
	}
	return m, nil
}

type sqCur struct {
	b   []byte
	pos int
	err error
}

func (c *sqCur) take(n int) []byte {
	if c.err != nil || n < 0 || c.pos+n > len(c.b) {
		c.err = fmt.Errorf("%w: truncated structure", ErrSquashfs)
		return make([]byte, max(n, 0)&0xff)
	}
	s := c.b[c.pos : c.pos+n]
	c.pos += n
	return s
}
func (c *sqCur) u16() uint16 { return binary.LittleEndian.Uint16(pad(c.take(2), 2)) }
func (c *sqCur) u32() uint32 { return binary.LittleEndian.Uint32(pad(c.take(4), 4)) }
func (c *sqCur) u64() uint64 { return binary.LittleEndian.Uint64(pad(c.take(8), 8)) }
func pad(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	return make([]byte, n)
}

// ScanSquashfs scans the image. maxInodes bounds directory traversal.
func ScanSquashfs(r io.ReaderAt, size int64, dec Decompress, maxInodes int) (*SqImage, error) {
	var sb [96]byte
	if size < 96 {
		return nil, fmt.Errorf("%w: too small", ErrSquashfs)
	}
	if _, err := r.ReadAt(sb[:], 0); err != nil {
		return nil, err
	}
	s := SqSuper{Magic: binary.LittleEndian.Uint32(sb[0:]), Inodes: binary.LittleEndian.Uint32(sb[4:]), MkfsTime: binary.LittleEndian.Uint32(sb[8:]),
		BlockSize: binary.LittleEndian.Uint32(sb[12:]), Frags: binary.LittleEndian.Uint32(sb[16:]), Comp: binary.LittleEndian.Uint16(sb[20:]),
		BlockLog: binary.LittleEndian.Uint16(sb[22:]), Flags: binary.LittleEndian.Uint16(sb[24:]), NoIDs: binary.LittleEndian.Uint16(sb[26:]),
		Major: binary.LittleEndian.Uint16(sb[28:]), Minor: binary.LittleEndian.Uint16(sb[30:]), RootInode: binary.LittleEndian.Uint64(sb[32:]),
		BytesUsed: binary.LittleEndian.Uint64(sb[40:]), IDTable: binary.LittleEndian.Uint64(sb[48:]), XattrTable: binary.LittleEndian.Uint64(sb[56:]),
		InodeTable: binary.LittleEndian.Uint64(sb[64:]), DirTable: binary.LittleEndian.Uint64(sb[72:]), FragTable: binary.LittleEndian.Uint64(sb[80:]),
		LookupTable: binary.LittleEndian.Uint64(sb[88:])}
	if s.Magic != 0x73717368 || s.Major != 4 || s.BlockSize < 4096 || s.BlockSize > 1<<20 || s.BlockSize != 1<<s.BlockLog {
		return nil, fmt.Errorf("%w: bad superblock", ErrSquashfs)
	}
	if int64(s.BytesUsed) > size || s.BytesUsed < 96 || int(s.Inodes) > maxInodes {
		return nil, fmt.Errorf("%w: bytes_used %d / inodes %d out of bounds", ErrSquashfs, s.BytesUsed, s.Inodes)
	}
	const none = ^uint64(0)
	starts := []int64{}
	for _, v := range []uint64{s.InodeTable, s.DirTable, s.FragTable, s.LookupTable, s.IDTable, s.XattrTable} {
		if v != none {
			if v >= s.BytesUsed || v < 96 {
				return nil, fmt.Errorf("%w: table offset %d out of range", ErrSquashfs, v)
			}
			starts = append(starts, int64(v))
		}
	}
	starts = append(starts, int64(s.BytesUsed))
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	endOf := func(v uint64) int64 {
		for _, x := range starts {
			if x > int64(v) {
				return x
			}
		}
		return int64(s.BytesUsed)
	}
	img := &SqImage{Size: size, Super: s, Files: map[string]*SqFile{}, DirMeta: map[string][]Span{}}
	inodes, err := readChain(r, int64(s.InodeTable), endOf(s.InodeTable), dec, int64(s.InodeTable))
	if err != nil {
		return nil, err
	}
	dirs, err := readChain(r, int64(s.DirTable), endOf(s.DirTable), dec, int64(s.DirTable))
	if err != nil {
		return nil, err
	}
	for i := range inodes.disk {
		img.Blocks = append(img.Blocks, SqBlock{Off: int64(s.InodeTable) + inodes.disk[i], Len: inodes.dlen[i], Kind: "inode", Idx: i})
	}
	for i := range dirs.disk {
		img.Blocks = append(img.Blocks, SqBlock{Off: int64(s.DirTable) + dirs.disk[i], Len: dirs.dlen[i], Kind: "dir", Idx: i})
	}
	// fragment table: index of metadata block offsets, then 16-byte entries
	if s.Frags > 0 {
		if s.FragTable == none {
			return nil, fmt.Errorf("%w: fragments without table", ErrSquashfs)
		}
		nIdx := (int64(s.Frags) + 511) / 512
		ft := int64(s.FragTable)
		if ft+nIdx*8 > int64(s.BytesUsed) {
			return nil, fmt.Errorf("%w: fragment index overruns", ErrSquashfs)
		}
		var fent []byte
		for i := int64(0); i < nIdx; i++ {
			var o [8]byte
			r.ReadAt(o[:], ft+i*8)
			mo := int64(binary.LittleEndian.Uint64(o[:]))
			if mo < 96 || mo+2 > int64(s.BytesUsed) {
				return nil, fmt.Errorf("%w: fragment metadata offset", ErrSquashfs)
			}
			c, err := readChain(r, mo, mo+2+int64(binary.LittleEndian.Uint16(func() []byte { var h [2]byte; r.ReadAt(h[:], mo); return h[:] }())&0x7fff), dec, 0)
			if err != nil {
				return nil, err
			}
			fent = append(fent, c.buf...)
			img.Blocks = append(img.Blocks, SqBlock{Off: mo, Len: c.dlen[0], Kind: "fragtab"})
			img.FragMD = append(img.FragMD, SqBlock{Off: mo, Len: c.dlen[0], Kind: "fragtab"})
		}
		img.FragIdx = Span{ft, nIdx * 8}
		img.Blocks = append(img.Blocks, SqBlock{Off: ft, Len: nIdx * 8, Kind: "fragtab"})
		for i := 0; i < int(s.Frags); i++ {
			if (i+1)*16 > len(fent) {
				return nil, fmt.Errorf("%w: fragment entry %d", ErrSquashfs, i)
			}
			st := int64(binary.LittleEndian.Uint64(fent[i*16:]))
			sz := binary.LittleEndian.Uint32(fent[i*16+8:])
			b := SqBlock{Off: st, Len: int64(sz & 0xffffff), Kind: "frag", Idx: i, Raw: sz&(1<<24) != 0}
			if b.Off < 96 || b.Off+b.Len > int64(s.InodeTable) {
				return nil, fmt.Errorf("%w: fragment %d out of range", ErrSquashfs, i)
			}
			img.FragBlk = append(img.FragBlk, b)
			img.Blocks = append(img.Blocks, b)
		}
	}
	// walk the tree
	seenIno := map[uint64]*SqFile{}
	var walk func(ref uint64, path string, depth int, meta []Span) error
	count := 0
	walk = func(ref uint64, path string, depth int, meta []Span) error {
		if depth > 64 {
			return fmt.Errorf("%w: directory depth", ErrSquashfs)
		}
		if count++; count > maxInodes {
			return fmt.Errorf("%w: more than %d inodes", ErrSquashfs, maxInodes)
		}
		p, err := inodes.pos(int64(ref>>16), int(ref&0xffff))
		if err != nil {
			return err
		}
		meta = append(append([]Span(nil), meta...), inodes.spans(p, p)...)
		c := &sqCur{b: inodes.buf, pos: p}
		typ := c.u16()
		c.take(2 + 2 + 2 + 4 + 4) // mode uid gid mtime ino
		switch typ {
		case 1, 8: // directory
			var dblk, doff, dsize uint32
			var idxCount uint16
			if typ == 1 {
				dblk = c.u32()
				c.u32() // nlink
				dsize = uint32(c.u16())
				doff = uint32(c.u16())
				c.u32() // parent inode
			} else {
				c.u32()
				dsize = c.u32()
				dblk = c.u32()
				c.u32()
				idxCount = c.u16()
				doff = uint32(c.u16())
				c.u32()
				for i := 0; i < int(idxCount); i++ {
					c.u32()
					c.u32()
					nl := int(c.u32())
					c.take(nl + 1)
				}
			}
			if c.err != nil {
				return c.err
			}
			img.Dirs++
			if dsize < 3 {
				return nil
			}
			dp, err := dirs.pos(int64(dblk), int(doff))
			if err != nil {
				return err
			}
			end := dp + int(dsize) - 3
			if end > len(dirs.buf) {
				return fmt.Errorf("%w: directory listing overruns table", ErrSquashfs)
			}
			dm := append([]Span(nil), meta...)
			dm = append(dm, dirs.spans(dp, max(end-1, dp))...)
			img.DirMeta[path] = dm
			dc := &sqCur{b: dirs.buf[:end], pos: dp}
			for dc.pos < end {
				cnt := int(dc.u32()) + 1
				iblk := dc.u32()
				dc.u32() // inode base
				for i := 0; i < cnt; i++ {
					eoff := dc.u16()
					dc.u16()
					dc.u16()
					nl := int(dc.u16()) + 1
					name := string(dc.take(nl))
					if dc.err != nil {
						return dc.err
					}
					if strings.ContainsAny(name, "/\x00") {
						return fmt.Errorf("%w: bad name", ErrSquashfs)
					}
					child := name
					if path != "" {
						child = path + "/" + name
					}
					if err := walk(uint64(iblk)<<16|uint64(eoff), child, depth+1, dm); err != nil {
						return err
					}
				}
			}
		case 2, 9: // regular
			f := &SqFile{Path: path, Frag: -1, Nlink: 1}
			var fragIdx, fragOff uint32
			var fsize uint64
			if typ == 2 {
				f.Start = int64(c.u32())
				fragIdx, fragOff, fsize = c.u32(), c.u32(), uint64(c.u32())
			} else {
				f.Start = int64(c.u64())
				fsize = c.u64()
				c.u64()
				f.Nlink = int(c.u32())
				fragIdx, fragOff = c.u32(), c.u32()
				c.u32()
			}
			f.Size, f.Meta = int64(fsize), meta
			nb := fsize / uint64(s.BlockSize)
			if fragIdx != 0xffffffff {
				f.Frag, f.FragOf = int64(fragIdx), int64(fragOff)
				if int(fragIdx) >= int(s.Frags) {
					return fmt.Errorf("%w: fragment index %d", ErrSquashfs, fragIdx)
				}
			} else if fsize%uint64(s.BlockSize) != 0 {
				nb++
			}
			if nb > uint64(len(inodes.buf)-c.pos)/4 {
				return fmt.Errorf("%w: block list overruns (%d blocks)", ErrSquashfs, nb)
			}
			if prev := seenIno[ref]; prev != nil { // hard link: same inode, another name
				img.Files[path] = prev
				return nil
			}
			off := f.Start
			for i := uint64(0); i < nb; i++ {
				sz := c.u32()
				ln := int64(sz & 0xffffff)
				if ln == 0 {
					f.Holes++
					continue
				}
				f.Blocks = append(f.Blocks, SqBlock{Off: off, Len: ln, Kind: "data", File: path, Idx: int(i), Raw: sz&(1<<24) != 0})
				off += ln
			}
			if c.err != nil {
				return c.err
			}
			if off > int64(s.InodeTable) {
				return fmt.Errorf("%w: file %s data overruns", ErrSquashfs, path)
			}
			seenIno[ref] = f
			img.Files[path] = f
			img.Blocks = append(img.Blocks, f.Blocks...)
		}
		return nil
	}
	if err := walk(s.RootInode, "", 0, nil); err != nil {
		return nil, err
	}
	// remaining table regions as single blocks, then fill gaps
	add := func(v uint64, kind string, covered bool) {
		if v == none || covered {
			return
		}
		e := endOf(v)
		img.Blocks = append(img.Blocks, SqBlock{Off: int64(v), Len: e - int64(v), Kind: kind})
	}
	add(s.LookupTable, "lookup", false)
	add(s.IDTable, "idtab", false)
	add(s.XattrTable, "xattr", false)
	img.Blocks = append(img.Blocks, SqBlock{Off: 0, Len: 96, Kind: "super"})
	sort.Slice(img.Blocks, func(i, j int) bool { return img.Blocks[i].Off < img.Blocks[j].Off })
	var out []SqBlock
	pos := int64(0)
	for _, b := range img.Blocks {
		if b.Off < pos {
			continue // overlapping duplicate (fragment table pieces counted once)
		}
		if b.Off > pos {
			out = append(out, SqBlock{Off: pos, Len: b.Off - pos, Kind: "pad"})
		}
		out = append(out, b)
		pos = b.Off + b.Len
	}
	if pos < size {
		out = append(out, SqBlock{Off: pos, Len: size - pos, Kind: "pad"})
	}
	img.Blocks = out
	return img, nil
}
