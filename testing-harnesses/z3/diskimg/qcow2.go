package diskimg

import (
	"encoding/binary"
	"io"
	"math/bits"
)

// Guest-cluster states of a QCOW2 L2 entry.
const (
	QUnalloc    = iota // inherit backing (or zeros without backing)
	QZero              // reads as zeros (explicit; hides backing data)
	QData              // data cluster at Host
	QCompressed        // compressed cluster at Host, CSize stored bytes
)

// QEntry is one guest cluster's mapping.
type QEntry struct {
	State uint8
	Host  int64
	CSize int64
	Sub   uint64 // extended-L2 bitmap: low 32 allocated, high 32 zero (subcluster states)
}

// Qcow2 is the bounded structural view of a QCOW2 image.
type Qcow2 struct {
	FileSize    int64
	Version     uint32
	ClusterBits uint32
	ClusterSize int64
	VirtSize    int64
	L1Size      uint32
	L1Off       int64
	RefTabOff   int64
	RefTabClust uint32
	RefOrder    uint32
	Incompat    uint64
	ExtendedL2  bool
	Compression uint8 // 0 zlib, 1 zstd
	Backing     string
	BackingOff  int64
	Map         []QEntry // by guest cluster
	L2Off       []int64  // L2 table host offset per L1 index (0 = absent)
	Regions     []Region // exact tiling of the file
}

const (
	qMaxGuestClusters = 1 << 24
	qMaxL1            = 1 << 21
	qMaxRefTable      = 1 << 20
	qMaxBacking       = 1023
)

func be64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func readFull(r io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > size || int64(n) > size-off {
		return nil, bad("read [%d,+%d) beyond file of %d bytes", off, n, size)
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil && !(err == io.EOF && true) {
		return nil, err
	}
	return b, nil
}

// ScanQcow2 parses header, L1, L2, refcount structures and tiles the file. It never reads data clusters.
func ScanQcow2(r io.ReaderAt, size int64) (*Qcow2, error) {
	h, err := readFull(r, size, 0, int(min(size, 112)))
	if err != nil {
		return nil, err
	}
	if len(h) < 72 || string(h[:4]) != "QFI\xfb" {
		return nil, bad("not a QCOW2 file")
	}
	q := &Qcow2{FileSize: size}
	q.Version = be32(h[4:])
	if q.Version != 2 && q.Version != 3 {
		return nil, ErrUnsupported{"qcow2 version"}
	}
	q.BackingOff = int64(be64(h[8:]))
	bsz := be32(h[16:])
	q.ClusterBits = be32(h[20:])
	if q.ClusterBits < 9 || q.ClusterBits > 21 {
		return nil, bad("cluster_bits %d", q.ClusterBits)
	}
	q.ClusterSize = 1 << q.ClusterBits
	vs := be64(h[24:])
	if vs > 1<<62 {
		return nil, bad("virtual size")
	}
	q.VirtSize = int64(vs)
	if be32(h[32:]) != 0 {
		return nil, ErrUnsupported{"encryption"}
	}
	q.L1Size = be32(h[36:])
	q.L1Off = int64(be64(h[40:]))
	q.RefTabOff = int64(be64(h[48:]))
	q.RefTabClust = be32(h[56:])
	if be32(h[60:]) != 0 {
		return nil, ErrUnsupported{"internal snapshots"}
	}
	q.RefOrder = 4
	hdrLen := int64(72)
	if q.Version == 3 {
		if len(h) < 104 {
			return nil, bad("short v3 header")
		}
		q.Incompat = be64(h[72:])
		q.RefOrder = be32(h[96:])
		hdrLen = int64(be32(h[100:]))
		if hdrLen < 104 || hdrLen%8 != 0 || hdrLen > q.ClusterSize {
			return nil, bad("header_length %d", hdrLen)
		}
		if q.Incompat&^uint64(1<<3|1<<4) != 0 {
			return nil, ErrUnsupported{"incompatible features (dirty/corrupt/external data/unknown)"}
		}
		q.ExtendedL2 = q.Incompat&(1<<4) != 0
		if q.Incompat&(1<<3) != 0 {
			if len(h) < 105 {
				return nil, bad("compression type missing")
			}
			q.Compression = h[104]
		}
	}
	if q.RefOrder > 6 {
		return nil, bad("refcount_order %d", q.RefOrder)
	}
	cs := q.ClusterSize
	var claims []Region
	hdrEnd := cs
	if q.BackingOff != 0 {
		if bsz == 0 || bsz > qMaxBacking || q.BackingOff < hdrLen || q.BackingOff+int64(bsz) > size || q.BackingOff+int64(bsz) > cs {
			return nil, bad("backing file name")
		}
		nb, err := readFull(r, size, q.BackingOff, int(bsz))
		if err != nil {
			return nil, err
		}
		q.Backing = string(nb)
	}
	claims = append(claims, Region{0, min(cs, size), ClassHeader})
	if size < cs {
		return nil, bad("file shorter than one cluster")
	}
	_ = hdrEnd

	nGuest := (q.VirtSize + cs - 1) / cs
	if nGuest > qMaxGuestClusters {
		return nil, bad("virtual size %d too large for scanner", q.VirtSize)
	}
	entSize := int64(8)
	if q.ExtendedL2 {
		entSize = 16
	}
	l2n := cs / entSize
	needL1 := (nGuest + l2n - 1) / l2n
	if int64(q.L1Size) < needL1 || q.L1Size > qMaxL1 {
		return nil, bad("l1_size %d (need %d)", q.L1Size, needL1)
	}
	if q.L1Size > 0 {
		l1b := int64(q.L1Size) * 8
		if q.L1Off <= 0 || q.L1Off%cs != 0 || q.L1Off+l1b > size {
			return nil, bad("l1 table offset")
		}
		claims = append(claims, Region{q.L1Off, roundUp(l1b, cs), ClassL1})
	}
	l1, err := readFull(r, size, q.L1Off, int(q.L1Size)*8)
	if q.L1Size == 0 {
		l1, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.Map = make([]QEntry, nGuest)
	q.L2Off = make([]int64, q.L1Size)
	const offMask = 0x00fffffffffffe00
	for i := 0; i < int(q.L1Size); i++ {
		e := be64(l1[i*8:])
		off := int64(e & offMask)
		if e&^(offMask|1<<63) != 0 {
			return nil, bad("l1[%d] reserved bits", i)
		}
		if off == 0 {
			continue
		}
		if off%cs != 0 || off+cs > size {
			return nil, bad("l2 table %d offset %d", i, off)
		}
		q.L2Off[i] = off
		claims = append(claims, Region{off, cs, ClassL2})
		tab, err := readFull(r, size, off, int(cs))
		if err != nil {
			return nil, err
		}
		for j := int64(0); j < l2n; j++ {
			g := int64(i)*l2n + j
			if g >= nGuest {
				break
			}
			ent := be64(tab[j*entSize:])
			var sub uint64
			if q.ExtendedL2 {
				sub = be64(tab[j*entSize+8:])
			}
			qe, host, hl, err := q.decode(ent, sub)
			if err != nil {
				return nil, bad("l2 entry guest cluster %d: %v", g, err)
			}
			if hl > 0 {
				if host+hl > size {
					return nil, bad("guest cluster %d data [%d,+%d) beyond file", g, host, hl)
				}
				cls := ClassData
				if qe.State == QCompressed {
					cls = ClassCompressed
				}
				a := host &^ (cs - 1)
				b := roundUp(host+hl, cs)
				claims = append(claims, Region{a, min(b-a, size-a), cls})
			}
			q.Map[g] = qe
		}
	}
	// refcount structures
	if q.RefTabClust > 0 {
		if q.RefTabClust > qMaxRefTable || q.RefTabOff <= 0 || q.RefTabOff%cs != 0 || q.RefTabOff+int64(q.RefTabClust)*cs > size {
			return nil, bad("refcount table")
		}
		claims = append(claims, Region{q.RefTabOff, int64(q.RefTabClust) * cs, ClassRefTable})
		tb, err := readFull(r, size, q.RefTabOff, int(int64(q.RefTabClust)*cs))
		if err != nil {
			return nil, err
		}
		for i := 0; i+8 <= len(tb); i += 8 {
			off := int64(be64(tb[i:]) & offMask)
			if off == 0 {
				continue
			}
			if off%cs != 0 || off+cs > size {
				return nil, bad("refcount block offset %d", off)
			}
			claims = append(claims, Region{off, cs, ClassRefBlock})
		}
	}
	if q.Version == 3 && q.BackingOff != 0 {
		// backing name lives inside the header cluster (checked above)
	}
	q.Regions, err = tile(size, claims)
	return q, err
}

func roundUp(v, a int64) int64 { return (v + a - 1) / a * a }

// decode interprets an L2 entry; returns the entry and the host byte range it owns (hl bytes at host).
func (q *Qcow2) decode(e, sub uint64) (QEntry, int64, int64, error) {
	cs := q.ClusterSize
	if e&(1<<62) != 0 { // compressed
		if q.ExtendedL2 {
			if sub != 0 {
				return QEntry{}, 0, 0, bad("compressed ext-l2 with bitmap")
			}
		}
		shift := uint(62 - (q.ClusterBits - 8))
		host := int64(e & (1<<shift - 1))
		nsec := int64((e>>shift)&(1<<(q.ClusterBits-8)-1)) + 1
		cl := nsec*512 - (host & 511)
		if host < cs {
			return QEntry{}, 0, 0, bad("compressed host %d", host)
		}
		return QEntry{State: QCompressed, Host: host, CSize: cl}, host, cl, nil
	}
	const offMask = 0x00fffffffffffe00
	host := int64(e & offMask)
	if e&^(offMask|1<<63|1) != 0 {
		return QEntry{}, 0, 0, bad("reserved bits set")
	}
	zero := e&1 != 0
	if q.ExtendedL2 {
		if zero {
			return QEntry{}, 0, 0, bad("zero flag set in extended l2 entry")
		}
		alloc, zer := uint32(sub), uint32(sub>>32)
		if alloc&zer != 0 {
			return QEntry{}, 0, 0, bad("subcluster both allocated and zero")
		}
		if host == 0 {
			if alloc != 0 {
				return QEntry{}, 0, 0, bad("subclusters allocated without cluster")
			}
			if zer == 0xffffffff {
				return QEntry{State: QZero, Sub: sub}, 0, 0, nil
			}
			return QEntry{State: QUnalloc, Sub: sub}, 0, 0, nil
		}
		if host%cs != 0 || host < cs {
			return QEntry{}, 0, 0, bad("data cluster offset %d", host)
		}
		// the file may end inside the last data cluster: only the allocated subclusters must exist
		// (a cluster whose subclusters are all zero/unallocated may sit beyond EOF: nothing was written there)
		used := int64(bits.Len32(alloc)) * (cs / 32)
		return QEntry{State: QData, Host: host, Sub: sub}, host, used, nil
	}
	switch {
	case host == 0 && !zero:
		return QEntry{State: QUnalloc}, 0, 0, nil
	case host == 0:
		return QEntry{State: QZero}, 0, 0, nil
	}
	if host%cs != 0 || host < cs {
		return QEntry{}, 0, 0, bad("data cluster offset %d", host)
	}
	if zero {
		return QEntry{State: QZero, Host: host}, host, cs, nil
	}
	return QEntry{State: QData, Host: host}, host, cs, nil
}
