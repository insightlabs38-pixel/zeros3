package dexz3

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sort"
	"zeros3-z3/idxref"
)

type streamOffCoder struct{ kinds [numOffKinds]*streamOffKind }
type streamOffKind struct {
	list    []int64
	seen    []uint64
	prevNew int
	cls     *idxref.ClassIndex
}

// Mechanically adapted offset identities; one scratch window replaces masked whole bytes.
func newOffCoderAt(f *File, w *Walked, masked io.ReaderAt, k0 int) (*streamOffCoder, error) {
	scratch := make([]byte, 64<<10)
	oc := &streamOffCoder{}
	type tab struct {
		list []int64
		cls  *idxref.ClassIndex
	}
	byType := map[uint16]*tab{}
	for k := OffKind(1); k < numOffKinds; k++ {
		t := OffTarget[k]
		tb := byType[t]
		if tb == nil {
			tb = &tab{}
			var end []int64
			for _, it := range w.Items {
				if it.Type == t {
					tb.list = append(tb.list, it.Off)
					end = append(end, it.Off+it.Len)
				}
			}
			if t == TypeStringData {
				for i, o := range w.StrOff {
					tb.list = append(tb.list, int64(o))
					end = append(end, int64(w.StrEnd[i]))
				}
			}
			if !sort.SliceIsSorted(tb.list, func(i, j int) bool { return tb.list[i] < tb.list[j] }) {
				return nil, bad("offset items not sorted")
			}
			hs := make([]int, len(tb.list))
			ids := make([]Identity, len(tb.list))
			for i, o := range tb.list {

				hs[i] = i
				h := sha256.New()
				h.Write([]byte{'O', byte(t), byte(t >> 8)})
				if _, err := io.CopyBuffer(h, io.NewSectionReader(masked, o, end[i]-o), scratch); err != nil {
					return nil, err
				}
				h.Sum(ids[i][:0])
			}
			tb.cls = idxref.NewClassIndex(hs, ids, k0)
			byType[t] = tb
		}
		oc.kinds[k] = &streamOffKind{list: tb.list, seen: make([]uint64, (len(tb.list)+63)/64), prevNew: -1, cls: tb.cls}
	}
	return oc, nil
}

func (oc *streamOffCoder) put(kind OffKind, pos int64) ([]byte, error) {
	ko := oc.kinds[kind]
	o := sort.Search(len(ko.list), func(i int) bool { return ko.list[i] >= pos })
	ok := o < len(ko.list) && ko.list[o] == pos
	if !ok {
		return nil, bad("offset %d is not the start of a %#x item", pos, OffTarget[kind])
	}
	if ko.seen[o/64]&(1<<uint(o%64)) == 0 {
		ko.seen[o/64] |= 1 << uint(o%64)
		d := o - ko.prevNew
		ko.prevNew = o
		return binary.AppendUvarint(nil, idxref.Zigzag(int64(d))<<1), nil
	}
	return ko.cls.Put(binary.AppendUvarint(nil, 1), o)
}

// Mechanically reused decoder symbols with compact seen bits.
func (oc *streamOffCoder) get(kind OffKind, b []byte) (int64, []byte, error) {
	ko := oc.kinds[kind]
	u, k := binary.Uvarint(b)
	if k <= 0 {
		return 0, nil, bad("offset stream truncated")
	}
	b = b[k:]
	var o int
	if u&1 == 0 {
		o = ko.prevNew + int(idxref.Unzigzag(u>>1))
		if o < 0 || o >= len(ko.list) {
			return 0, nil, bad("offset ordinal %d outside %d items", o, len(ko.list))
		}
		ko.prevNew = o
		ko.seen[o/64] |= 1 << uint(o%64)
	} else {
		if u != 1 || ko.cls == nil {
			return 0, nil, bad("offset stream symbol")
		}
		h, rest, err := ko.cls.Get(b)
		if err != nil {
			return 0, nil, idxErr(err)
		}
		b, o = rest, h
	}
	return ko.list[o], b, nil
}
