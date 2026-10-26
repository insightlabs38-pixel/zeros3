package frontier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// EROFS structural scanner (Z3-24). EROFS metadata (superblock, inodes,
// directories, chunk indexes) is never compressed, so the tree and every
// uncompressed file's placement are readable with no codec. Compressed files'
// physical clusters live behind per-file lcluster indexes; this scanner does not
// decode those (that would be Z3-25 work) and instead takes extents from an
// injected oracle (ExtentFn, dump.erofs in the lab). Bounded like ScanSquashfs.

var ErrErofs = errors.New("erofs: malformed image")

const erofsMagic = 0xE0F5E1E2

type ErSuper struct {
	BlkBits   uint8
	RootNid   uint16
	Inos      uint64
	BuildTime uint64
	Blocks    uint32
	MetaBlk   uint32
	XattrBlk  uint32
	Incompat  uint32
	PackedNid uint64
	Compat    uint32
}

type ErFile struct {
	Path    string
	Nid     uint64
	Size    int64
	Layout  int        // 0 plain 1 compressed-full 2 inline 3 compressed-compact 4 chunk
	Extents []Span     // physical data spans (clusters / blocks / chunks), in logical order
	Logical []ErExtent // compressed files: logical->physical map from the oracle
	Tail    Span       // inline tail (layout 2), absolute
	Holes   int        // NULL chunks (chunk-based)
	Nlink   int
	Meta    Span // inode header + xattr area
}

type ErRegion struct {
	Off, Len int64
	Kind     string // super inode dir data tail frag pad metapad
	File     string
}

type ErImage struct {
	Size      int64
	Super     ErSuper
	Files     map[string]*ErFile
	Regions   []ErRegion
	Dirs      int
	Compress  int // compressed files
	Packed    *ErFile
	HoleBytes int64
	// Path resolution oracle: the inode header span of every path, the data spans of
	// every directory, and the directory block that holds each entry (all absolute).
	InodeAt map[string]Span
	DirData map[string][]Span
	EntryAt map[string]Span
}

// ErExtent maps a logical byte range of a compressed file to its physical cluster.
type ErExtent struct{ LOff, LLen, POff, PLen int64 }

// ExtentFn returns the logical->physical extents of a compressed inode.
type ExtentFn func(nid uint64, path string) ([]ErExtent, error)

type erIno struct {
	nid         uint64
	off         int64
	format      uint16
	ext         bool
	layout      int
	mode        uint16
	size        int64
	iu          uint32
	nlink       int
	hdr         int // inode header + xattr bytes
	xattrIcount uint16
}

func readInode(r io.ReaderAt, sb *ErSuper, size int64, nid uint64) (*erIno, error) {
	bs := int64(1) << sb.BlkBits
	off := int64(sb.MetaBlk)*bs + int64(nid)*32
	if off < 0 || off+64 > size && off+32 > size {
		return nil, fmt.Errorf("%w: inode %d outside image", ErrErofs, nid)
	}
	var b [64]byte
	n := int64(64)
	if off+n > size {
		n = size - off
	}
	if _, err := r.ReadAt(b[:n], off); err != nil && err != io.EOF {
		return nil, err
	}
	in := &erIno{nid: nid, off: off, format: binary.LittleEndian.Uint16(b[0:])}
	in.ext = in.format&1 == 1
	in.layout = int(in.format >> 1 & 7)
	in.xattrIcount = binary.LittleEndian.Uint16(b[2:])
	in.mode = binary.LittleEndian.Uint16(b[4:])
	if in.ext {
		if n < 64 {
			return nil, fmt.Errorf("%w: truncated extended inode", ErrErofs)
		}
		in.size = int64(binary.LittleEndian.Uint64(b[8:]))
		in.iu = binary.LittleEndian.Uint32(b[16:])
		in.nlink = int(binary.LittleEndian.Uint32(b[44:]))
		in.hdr = 64
	} else {
		in.size = int64(binary.LittleEndian.Uint32(b[8:]))
		in.iu = binary.LittleEndian.Uint32(b[16:])
		in.nlink = int(binary.LittleEndian.Uint16(b[6:]))
		in.hdr = 32
	}
	if in.size < 0 {
		return nil, fmt.Errorf("%w: negative size", ErrErofs)
	}
	if in.xattrIcount > 0 {
		in.hdr += 12 + (int(in.xattrIcount)-1)*4
	}
	return in, nil
}

// ScanErofs scans the image. ext may be nil (compressed files then have no extents).
func ScanErofs(r io.ReaderAt, size int64, ext ExtentFn, maxInodes int) (*ErImage, error) {
	if size < 1024+128 {
		return nil, fmt.Errorf("%w: too small", ErrErofs)
	}
	var sbb [128]byte
	if _, err := r.ReadAt(sbb[:], 1024); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(sbb[0:]) != erofsMagic {
		return nil, fmt.Errorf("%w: bad magic", ErrErofs)
	}
	sb := ErSuper{Compat: binary.LittleEndian.Uint32(sbb[8:]), BlkBits: sbb[12], RootNid: binary.LittleEndian.Uint16(sbb[14:]), Inos: binary.LittleEndian.Uint64(sbb[16:]),
		BuildTime: binary.LittleEndian.Uint64(sbb[24:]), Blocks: binary.LittleEndian.Uint32(sbb[36:]), MetaBlk: binary.LittleEndian.Uint32(sbb[40:]),
		XattrBlk: binary.LittleEndian.Uint32(sbb[44:]), Incompat: binary.LittleEndian.Uint32(sbb[80:]), PackedNid: binary.LittleEndian.Uint64(sbb[96:])}
	if sb.BlkBits < 9 || sb.BlkBits > 16 || int64(sb.Blocks)<<sb.BlkBits > size || sb.Inos > uint64(maxInodes) {
		return nil, fmt.Errorf("%w: bad superblock (blkbits %d blocks %d inos %d)", ErrErofs, sb.BlkBits, sb.Blocks, sb.Inos)
	}
	bs := int64(1) << sb.BlkBits
	img := &ErImage{Size: size, Super: sb, Files: map[string]*ErFile{}, InodeAt: map[string]Span{}, DirData: map[string][]Span{}, EntryAt: map[string]Span{}}
	regs := []ErRegion{{0, min(size, 1024+128), "super", ""}}
	addReg := func(off, n int64, kind, file string) {
		if n > 0 && off >= 0 && off+n <= size {
			regs = append(regs, ErRegion{off, n, kind, file})
		}
	}
	seen := map[uint64]*ErFile{}
	count := 0
	var visit func(nid uint64, path string, depth int) error
	// dataOf reads a plain/inline directory's bytes (blocks then tail).
	type dseg struct{ dataOff, abs, n int64 }
	dirBytes := func(in *erIno) ([]byte, []dseg, error) {
		var segs []dseg
		if in.size > 64<<20 {
			return nil, nil, fmt.Errorf("%w: directory too large", ErrErofs)
		}
		out := make([]byte, 0, in.size)
		full := in.size / bs * bs
		if full > 0 {
			if in.layout != 0 && in.layout != 2 {
				return nil, nil, fmt.Errorf("%w: directory layout %d", ErrErofs, in.layout)
			}
			start := int64(in.iu) * bs
			if start+full > size {
				return nil, nil, fmt.Errorf("%w: directory blocks outside image", ErrErofs)
			}
			buf := make([]byte, full)
			if _, err := r.ReadAt(buf, start); err != nil {
				return nil, nil, err
			}
			out = append(out, buf...)
			segs = append(segs, dseg{0, start, full})
			addReg(start, full, "dir", "")
		}
		if tail := in.size - full; tail > 0 {
			if in.layout != 2 && in.layout != 0 {
				return nil, nil, fmt.Errorf("%w: directory tail layout", ErrErofs)
			}
			var start int64
			if in.layout == 2 {
				start = in.off + int64(in.hdr)
			} else {
				start = int64(in.iu)*bs + full
			}
			if start+tail > size {
				return nil, nil, fmt.Errorf("%w: directory tail outside image", ErrErofs)
			}
			buf := make([]byte, tail)
			if _, err := r.ReadAt(buf, start); err != nil {
				return nil, nil, err
			}
			out = append(out, buf...)
			segs = append(segs, dseg{full, start, tail})
			addReg(start, tail, "dir", "")
		}
		return out, segs, nil
	}
	visit = func(nid uint64, path string, depth int) error {
		if depth > 64 {
			return fmt.Errorf("%w: depth", ErrErofs)
		}
		if count++; count > maxInodes {
			return fmt.Errorf("%w: more than %d inodes", ErrErofs, maxInodes)
		}
		in, err := readInode(r, &sb, size, nid)
		if err != nil {
			return err
		}
		addReg(in.off, int64(in.hdr), "inode", path)
		img.InodeAt[path] = Span{in.off, int64(in.hdr)}
		switch in.mode & 0xf000 {
		case 0x4000: // directory
			img.Dirs++
			data, segs, err := dirBytes(in)
			if err != nil {
				return err
			}
			for _, g := range segs {
				img.DirData[path] = append(img.DirData[path], Span{g.abs, g.n})
			}
			for b := int64(0); b < int64(len(data)); b += bs {
				blk := data[b:min(b+bs, int64(len(data)))]
				if len(blk) < 12 {
					return fmt.Errorf("%w: short directory block", ErrErofs)
				}
				n0 := int(binary.LittleEndian.Uint16(blk[8:]))
				if n0 < 12 || n0%12 != 0 || n0 > len(blk) {
					return fmt.Errorf("%w: bad first nameoff %d", ErrErofs, n0)
				}
				cnt := n0 / 12
				for i := 0; i < cnt; i++ {
					d := blk[i*12:]
					cn := binary.LittleEndian.Uint64(d)
					no := int(binary.LittleEndian.Uint16(d[8:]))
					end := len(blk)
					if i+1 < cnt {
						end = int(binary.LittleEndian.Uint16(blk[(i+1)*12+8:]))
					}
					if no < n0 || no > end || end > len(blk) {
						return fmt.Errorf("%w: bad dirent name offsets", ErrErofs)
					}
					nm := blk[no:end]
					if j := strings.IndexByte(string(nm), 0); j >= 0 {
						nm = nm[:j]
					}
					name := string(nm)
					if name == "." || name == ".." {
						continue
					}
					if name == "" || strings.Contains(name, "/") {
						return fmt.Errorf("%w: bad name", ErrErofs)
					}
					child := name
					if path != "" {
						child = path + "/" + name
					}
					for _, g := range segs {
						if b >= g.dataOff && b < g.dataOff+g.n {
							img.EntryAt[child] = Span{g.abs + b - g.dataOff, min(bs, g.dataOff+g.n-b)}
						}
					}
					if err := visit(cn, child, depth+1); err != nil {
						return err
					}
				}
			}
		case 0x8000: // regular
			if prev := seen[nid]; prev != nil {
				img.Files[path] = prev
				return nil
			}
			f := &ErFile{Path: path, Nid: nid, Size: in.size, Layout: in.layout, Nlink: in.nlink, Meta: Span{in.off, int64(in.hdr)}}
			seen[nid] = f
			img.Files[path] = f
			switch in.layout {
			case 0, 2:
				full := in.size / bs * bs
				if in.layout == 0 {
					full = (in.size + bs - 1) / bs * bs
				}
				if full > 0 {
					st := int64(in.iu) * bs
					if st+full > size {
						return fmt.Errorf("%w: file %s data outside image", ErrErofs, path)
					}
					f.Extents = append(f.Extents, Span{st, full})
					addReg(st, full, "data", path)
				}
				if in.layout == 2 {
					if tail := in.size - in.size/bs*bs; tail > 0 {
						st := in.off + int64(in.hdr)
						if st+tail > size {
							return fmt.Errorf("%w: inline tail outside image", ErrErofs)
						}
						f.Tail = Span{st, tail}
						addReg(st, tail, "tail", path)
					}
				}
			case 1, 3:
				img.Compress++
				if ext != nil {
					sp, err := ext(nid, "/"+path)
					if err != nil {
						return fmt.Errorf("extents %s: %w", path, err)
					}
					for _, e := range sp {
						if e.POff < 0 || e.PLen < 0 || e.POff+e.PLen > size {
							return fmt.Errorf("%w: extent outside image", ErrErofs)
						}
						if e.PLen == 0 { // data lives in the packed inode (-Efragments) or is inline
							continue
						}
						addReg(e.POff, e.PLen, "data", path)
						f.Extents = append(f.Extents, Span{e.POff, e.PLen})
					}
					f.Logical = sp
				}
			case 4:
				bits := uint(in.iu&0x1f) + uint(sb.BlkBits)
				if bits > 40 {
					return fmt.Errorf("%w: chunk bits", ErrErofs)
				}
				csz := int64(1) << bits
				n := (in.size + csz - 1) / csz
				esz := int64(4)
				if in.iu&0x20 != 0 {
					esz = 8
				}
				if n*esz > size || in.off+int64(in.hdr)+n*esz > size {
					return fmt.Errorf("%w: chunk index outside image", ErrErofs)
				}
				idx := make([]byte, n*esz)
				if _, err := r.ReadAt(idx, in.off+int64(in.hdr)); err != nil {
					return err
				}
				addReg(in.off+int64(in.hdr), n*esz, "inode", path)
				for i := int64(0); i < n; i++ {
					var blk uint64
					if esz == 4 {
						blk = uint64(binary.LittleEndian.Uint32(idx[i*4:]))
						if blk == 0xffffffff {
							blk = ^uint64(0)
						}
					} else {
						lo := uint64(binary.LittleEndian.Uint32(idx[i*8+4:]))
						hi := uint64(binary.LittleEndian.Uint16(idx[i*8:]))
						blk = hi<<32 | lo
						if lo == 0xffffffff {
							blk = ^uint64(0)
						}
					}
					ln := min(csz, in.size-i*csz)
					if blk == ^uint64(0) {
						f.Holes++
						img.HoleBytes += ln
						continue
					}
					st := int64(blk) * bs
					if st < 0 || st+ln > size || blk > uint64(size) {
						return fmt.Errorf("%w: chunk outside image", ErrErofs)
					}
					f.Extents = append(f.Extents, Span{st, ln})
					addReg(st, ln, "data", path)
				}
			default:
				return fmt.Errorf("%w: file layout %d", ErrErofs, in.layout)
			}
		}
		return nil
	}
	if err := visit(uint64(sb.RootNid), "", 0); err != nil {
		return nil, err
	}
	if sb.PackedNid != 0 && ext != nil {
		if in, err := readInode(r, &sb, size, sb.PackedNid); err == nil {
			addReg(in.off, int64(in.hdr), "inode", "<packed>")
			if sp, err := ext(sb.PackedNid, ""); err == nil {
				img.Packed = &ErFile{Path: "<packed>", Nid: sb.PackedNid, Size: in.size, Layout: in.layout, Logical: sp}
				for _, e := range sp {
					img.Packed.Extents = append(img.Packed.Extents, Span{e.POff, e.PLen})
					addReg(e.POff, e.PLen, "frag", "<packed>")
				}
			}
		}
	}
	sort.SliceStable(regs, func(i, j int) bool { return regs[i].Off < regs[j].Off })
	metaStart := int64(sb.MetaBlk) * bs
	pos := int64(0)
	for _, g := range regs {
		if g.Off < pos {
			if g.Off+g.Len <= pos {
				continue
			}
			g.Len -= pos - g.Off
			g.Off = pos
		}
		if g.Off > pos {
			k := "pad"
			if pos >= metaStart {
				k = "metapad"
			}
			img.Regions = append(img.Regions, ErRegion{pos, g.Off - pos, k, ""})
		}
		img.Regions = append(img.Regions, g)
		pos = g.Off + g.Len
	}
	if pos < size {
		k := "pad"
		if pos >= metaStart {
			k = "metapad"
		}
		img.Regions = append(img.Regions, ErRegion{pos, size - pos, k, ""})
	}
	return img, nil
}
