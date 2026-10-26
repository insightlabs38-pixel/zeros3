// Package diskimg is the Z3-26 laboratory for sparse / differencing virtual-disk images
// (QCOW2, VHDX): a deterministic logical-disk model, bounded structural scanners that tile the
// exact image file and expose the native allocation mapping, and the floors used in the report.
//
// Two contracts are kept apart throughout: the *image file bytes* (what ZeroS3 must rebuild
// exactly) and the *guest-visible disk* (what a hypervisor sees; may depend on a parent).
package diskimg

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

const (
	KiB = 1 << 10
	MiB = 1 << 20
	GiB = 1 << 30
)

// Kind selects the deterministic byte generator of an extent.
type Kind uint8

const (
	KZero   Kind = iota // explicitly written zeros (indistinguishable from a hole in a raw file)
	KRandom             // incompressible
	KText               // compressible text-like lines
	KMeta               // 64-byte structured records, low entropy, unique
	KStruct             // 256-byte structured records (medium)
	KDup                // 1 MiB blocks drawn from 6 distinct random blocks
	KPage               // 4 KiB pages: alternating random / text
)

// Extent is one written range of the logical disk. Bytes are a pure function of (Kind, Seed, absolute offset).
type Extent struct {
	Off, Len int64
	Kind     Kind
	Seed     uint64
}

// Disk is a logical virtual disk: a size and an ordered list of writes (later wins).
type Disk struct {
	Size   int64
	Writes []Extent
}

func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func h2(a, b uint64) uint64 { return mix(a*0x9e3779b97f4a7c15 ^ mix(b)) }

var words = []string{"alpha", "bravo", "cache", "delta", "echo", "frame", "grain", "hash", "index", "journal", "kernel", "ledger"}

// fill writes the bytes of kind/seed for absolute logical offset off into dst.
func fill(dst []byte, k Kind, seed uint64, off int64) {
	switch k {
	case KZero:
		clear(dst)
	case KRandom:
		fillRand(dst, seed, off)
	case KText:
		const line = 64
		var tmp [line]byte
		for i := 0; i < len(dst); {
			ln := (off + int64(i)) / line
			in := int((off + int64(i)) % line)
			h := h2(seed, uint64(ln))
			s := fmt.Sprintf("%08x %s %s %s %s\n", uint32(ln), words[h%12], words[(h>>8)%12], words[(h>>16)%12], words[(h>>24)%12])
			copy(tmp[:], s)
			for j := len(s); j < line; j++ {
				tmp[j] = ' '
			}
			tmp[line-1] = '\n'
			i += copy(dst[i:], tmp[in:])
		}
	case KMeta:
		recs(dst, seed, off, 64)
	case KStruct:
		recs(dst, seed, off, 256)
	case KDup:
		for i := 0; i < len(dst); {
			abs := off + int64(i)
			blk := abs / MiB
			which := h2(seed, uint64(blk)) % 6
			in := abs % MiB
			n := min(int64(len(dst)-i), MiB-in)
			fillRand(dst[i:i+int(n)], h2(seed, 1000+which), in)
			i += int(n)
		}
	case KPage:
		for i := 0; i < len(dst); {
			abs := off + int64(i)
			pg := abs / 4096
			in := abs % 4096
			n := min(int64(len(dst)-i), 4096-in)
			if mix(seed^uint64(pg))&1 == 0 {
				fillRand(dst[i:i+int(n)], h2(seed, uint64(pg)), in)
			} else {
				fill(dst[i:i+int(n)], KText, h2(seed, uint64(pg)), in)
			}
			i += int(n)
		}
	}
}

func fillRand(dst []byte, seed uint64, off int64) {
	var w [8]byte
	i := 0
	for i < len(dst) {
		abs := off + int64(i)
		idx := uint64(abs >> 3)
		binary.LittleEndian.PutUint64(w[:], mix(seed^(idx*0xd6e8feb86659fd93)))
		i += copy(dst[i:], w[abs&7:])
	}
}

func recs(dst []byte, seed uint64, off int64, rl int) {
	rec := make([]byte, rl)
	for i := 0; i < len(dst); {
		abs := off + int64(i)
		n := abs / int64(rl)
		in := int(abs % int64(rl))
		h := h2(seed, uint64(n))
		clear(rec)
		binary.LittleEndian.PutUint64(rec[0:], uint64(n))
		binary.LittleEndian.PutUint64(rec[8:], 1_700_000_000+uint64(n)*3)
		binary.LittleEndian.PutUint32(rec[16:], uint32(h&0xf))
		binary.LittleEndian.PutUint64(rec[20:], uint64(n)*4096)
		binary.LittleEndian.PutUint32(rec[28:], uint32(h>>32))
		if rl > 64 {
			binary.LittleEndian.PutUint64(rec[32:], h)
			copy(rec[40:], words[h%12])
			binary.LittleEndian.PutUint64(rec[rl-8:], mix(h))
		}
		i += copy(dst[i:], rec[in:])
	}
}

// Piece is a flattened, non-overlapping, sorted extent of the final logical state (zeros and holes omitted).
type Piece = Extent

// Flatten resolves overlaps (later wins) into sorted disjoint pieces; zero writes become gaps.
func (d *Disk) Flatten() []Piece {
	var cur []Piece // sorted, disjoint
	for _, w := range d.Writes {
		if w.Len <= 0 {
			continue
		}
		wend := w.Off + w.Len
		lo := sort.Search(len(cur), func(i int) bool { return cur[i].Off+cur[i].Len > w.Off })
		hi := lo
		for hi < len(cur) && cur[hi].Off < wend {
			hi++
		}
		var mid []Piece
		for _, p := range cur[lo:hi] {
			if p.Off < w.Off {
				mid = append(mid, Piece{Off: p.Off, Len: w.Off - p.Off, Kind: p.Kind, Seed: p.Seed})
			}
		}
		if w.Kind != KZero {
			mid = append(mid, w)
		}
		for _, p := range cur[lo:hi] {
			if pend := p.Off + p.Len; pend > wend {
				mid = append(mid, Piece{Off: wend, Len: pend - wend, Kind: p.Kind, Seed: p.Seed})
			}
		}
		cur = append(cur[:lo:lo], append(mid, cur[hi:]...)...)
	}
	return cur
}

// Stream writes the whole logical disk (zeros for gaps) to w in order; memory is O(1 MiB).
func (d *Disk) Stream(w io.Writer, pieces []Piece) error {
	zero := make([]byte, MiB)
	buf := make([]byte, MiB)
	var pos int64
	gap := func(to int64) error {
		for pos < to {
			n := min(to-pos, MiB)
			if _, err := w.Write(zero[:n]); err != nil {
				return err
			}
			pos += n
		}
		return nil
	}
	for _, p := range pieces {
		if err := gap(p.Off); err != nil {
			return err
		}
		for o := p.Off; o < p.Off+p.Len; {
			n := min(p.Off+p.Len-o, MiB)
			fill(buf[:n], p.Kind, p.Seed, o)
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			o += n
			pos = o
		}
	}
	return gap(d.Size)
}

// SHA256 is the guest-disk digest straight from the generator (never touches a raw file).
func (d *Disk) SHA256() ([32]byte, error) {
	h := sha256.New()
	if err := d.Stream(h, d.Flatten()); err != nil {
		return [32]byte{}, err
	}
	var s [32]byte
	copy(s[:], h.Sum(nil))
	return s, nil
}

// WriteRaw materialises the logical disk as a sparse raw file (holes where nothing non-zero was written).
func (d *Disk) WriteRaw(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(d.Size); err != nil {
		return err
	}
	buf := make([]byte, MiB)
	for _, p := range d.Flatten() {
		for o := p.Off; o < p.Off+p.Len; {
			n := min(p.Off+p.Len-o, MiB)
			fill(buf[:n], p.Kind, p.Seed, o)
			if _, err := f.WriteAt(buf[:n], o); err != nil {
				return err
			}
			o += n
		}
	}
	return f.Close()
}

// Clone copies the disk so a revision can append writes.
func (d *Disk) Clone() *Disk {
	return &Disk{Size: d.Size, Writes: append([]Extent(nil), d.Writes...)}
}

// Region names for the base corpus (offsets are fixed so tests and the report can refer to them).
const (
	OffMeta   = 0
	OffText   = 64 * MiB
	OffRand   = 256 * MiB
	OffDup    = 512 * MiB
	OffStruct = 1024 * MiB
	OffScat   = 2048 * MiB
	OffZeroR  = 3072 * MiB // explicit logical-zero range
	OffHole   = 3584 * MiB // never written
)

// BaseDisk builds the primary corpus: capacity size (4 GiB for the shootout), ~400 MiB of payload.
func BaseDisk(size int64, seed uint64) *Disk {
	d := &Disk{Size: size}
	add := func(off, ln int64, k Kind, s uint64) { d.Writes = append(d.Writes, Extent{off, ln, k, h2(seed, s)}) }
	add(OffMeta, 16*MiB, KMeta, 1)
	add(OffText, 64*MiB, KText, 2)
	add(OffRand, 64*MiB, KRandom, 3)
	add(OffDup, 128*MiB, KDup, 4)
	add(OffStruct, 100*MiB, KStruct, 5)
	// scattered allocated pages in [2 GiB, 2.5 GiB): 8192 distinct 4 KiB pages
	seen := map[int64]bool{}
	var pages []int64
	for i := uint64(0); len(pages) < 8192; i++ {
		pg := int64(h2(seed, 6000+i) % uint64(512*MiB/4096))
		if !seen[pg] {
			seen[pg] = true
			pages = append(pages, pg)
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i] < pages[j] })
	for _, pg := range pages {
		d.Writes = append(d.Writes, Extent{OffScat + pg*4096, 4096, KPage, h2(seed, 7)})
	}
	// explicit zero range + a zero island inside the random extent (written zeros over data)
	d.Writes = append(d.Writes, Extent{OffZeroR, 64 * MiB, KZero, 0}, Extent{OffRand + 40*MiB, MiB, KZero, 0})
	return d
}

// FillExtent writes into dst the bytes of piece p at absolute offset o (dst is one sector or any slice).
func FillExtent(dst []byte, p Piece, o int64) { fill(dst, p.Kind, p.Seed, o) }
