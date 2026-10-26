package diskimg

import (
	"fmt"
	"sort"
)

// Region classes (coarse by design; used for churn attribution).
const (
	ClassHeader     = "header"     // QCOW2 header cluster (+ extensions, backing name) / VHDX file type id
	ClassHeaders    = "headers"    // VHDX: two headers + two region tables + reserved
	ClassL1         = "l1"         // QCOW2 L1 table
	ClassL2         = "l2"         // QCOW2 L2 table
	ClassRefTable   = "reftable"   // QCOW2 refcount table
	ClassRefBlock   = "refblock"   // QCOW2 refcount block
	ClassData       = "data"       // QCOW2 data cluster
	ClassCompressed = "compressed" // QCOW2 host cluster holding compressed clusters
	ClassSnap       = "snapshots"
	ClassLog        = "log"      // VHDX
	ClassBAT        = "bat"      // VHDX
	ClassMetadata   = "metadata" // VHDX
	ClassPayload    = "payload"  // VHDX payload block
	ClassBitmap     = "bitmap"   // VHDX sector bitmap block
	ClassFree       = "free"     // unreferenced bytes inside the file (padding / allocation slack)
)

// Region is a half-open byte range of the exact image file with a structural class.
type Region struct {
	Off, Len int64
	Class    string
}

// ErrUnsupported marks a well-formed image using a feature this scanner does not model.
type ErrUnsupported struct{ What string }

func (e ErrUnsupported) Error() string { return "diskimg: unsupported: " + e.What }

// ErrFormat marks a malformed image.
type ErrFormat struct{ What string }

func (e ErrFormat) Error() string { return "diskimg: malformed: " + e.What }

func bad(f string, a ...any) error { return ErrFormat{fmt.Sprintf(f, a...)} }

const maxClaims = 1 << 22

// tile turns structural claims into an exact tiling of [0,size). Overlapping claims of different class are an
// error; identical-class overlaps merge. Gaps become ClassFree.
func tile(size int64, claims []Region) ([]Region, error) {
	if len(claims) > maxClaims {
		return nil, bad("too many structures (%d)", len(claims))
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Off != claims[j].Off {
			return claims[i].Off < claims[j].Off
		}
		return claims[i].Len > claims[j].Len
	})
	var out []Region
	var pos int64
	push := func(r Region) {
		if r.Len <= 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Class == r.Class && out[n-1].Off+out[n-1].Len == r.Off {
			out[n-1].Len += r.Len
			return
		}
		out = append(out, r)
	}
	for _, c := range claims {
		if c.Len <= 0 {
			return nil, bad("empty %s claim", c.Class)
		}
		end := c.Off + c.Len
		if c.Off < 0 || end < c.Off || end > size {
			return nil, bad("%s [%d,%d) outside file of %d bytes", c.Class, c.Off, end, size)
		}
		if c.Off < pos {
			last := &out[len(out)-1]
			switch {
			case coveredBySame(out, c):
			case last.Class == c.Class:
				last.Len = end - last.Off
				pos = end
			default:
				return nil, bad("%s [%d,%d) overlaps %s", c.Class, c.Off, end, last.Class)
			}
			continue
		}
		push(Region{pos, c.Off - pos, ClassFree})
		push(c)
		pos = end
	}
	push(Region{pos, size - pos, ClassFree})
	return out, nil
}

// coveredBySame reports whether c lies entirely inside already-tiled regions of its own class.
func coveredBySame(out []Region, c Region) bool {
	i := sort.Search(len(out), func(i int) bool { return out[i].Off+out[i].Len > c.Off })
	if i == len(out) {
		return false
	}
	r := out[i]
	return r.Class == c.Class && r.Off <= c.Off && c.Off+c.Len <= r.Off+r.Len
}
