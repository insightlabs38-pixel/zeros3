package clrz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"zeros3-z3/idxref"
)

func TestA4LayoutChildCountOverflow(t *testing.T) {
	data := build(spec{types: 2, methods: 2, fields: 1})
	f := parse(t, data)
	rep, e := Encode(f, data, Config{})
	if e != nil {
		t.Fatal(e)
	}
	c, e := newCtxDecode(f, data, 3)
	if e != nil {
		t.Fatal(e)
	}
	n, e := tableSlotCount(f)
	if e != nil {
		t.Fatal(e)
	}
	slots, e := compactTableSlots(f, data, make([]Slot, 0, n))
	if e != nil {
		t.Fatal(e)
	}
	var ls []lent
	for i, s := range slots {
		if s.Sp == SpLayout {
			ls = append(ls, lent{s.Off, 0, i})
		} else if s.Self >= 0 && s.Sp == SpRow {
			ls = append(ls, lent{s.Off, 1, i})
		}
	}
	for i, b := range c.Blobs {
		ls = append(ls, lent{f.Blob.Off + int64(b.Off), 2, i})
	}
	ls = append(ls, lent{f.Str.Off, 3, 0})
	sortLents(ls)
	payload, streams, e := idxref.SplitComp(rep.Comps[0], NumStreams)
	if e != nil {
		t.Fatal(e)
	}
	l := streams[StL]
	at := 0
	found := false
	for _, entry := range ls {
		v, k := binary.Uvarint(l[at:])
		if k <= 0 {
			t.Fatal("layout parse")
		}
		if entry.kind == 0 && slots[entry.slot].Lay == LList {
			changed := append([]byte(nil), l[:at]...)
			changed = binary.AppendUvarint(changed, v+(1<<32))
			changed = append(changed, l[at+k:]...)
			streams[StL] = changed
			found = true
			break
		}
		at += k
		if entry.kind == 3 {
			for j := uint64(0); j < v; j++ {
				_, k = binary.Uvarint(l[at:])
				if k <= 0 {
					t.Fatal("suffix parse")
				}
				at += k
			}
		}
	}
	if !found {
		t.Fatal("fixture has no child list")
	}
	comps := [][]byte{idxref.PackComp(payload, streams)}
	got, e := Decode(rep.Meta, comps)
	if e == nil {
		t.Fatalf("accepted wrapped child count (identical=%v)", bytes.Equal(got, data))
	}
}

func TestA4AggregateSignatureSlots(t *testing.T) {
	sig := append([]byte{7}, comp(65000)...)
	for i := 0; i < 65000; i++ {
		sig = append(sig, 0x12, 5)
	}
	var blob []byte
	var entries []BlobEntry
	var marks []byte
	for i := 0; i < 33; i++ {
		off := len(blob)
		blob = append(blob, comp(len(sig))...)
		data := len(blob)
		blob = append(blob, sig...)
		entries = append(entries, BlobEntry{uint32(off), uint32(data), uint32(len(sig))})
		marks = append(marks, BlobLocal)
	}
	f := &File{HasBlob: true, Blob: Stream{Size: int64(len(blob))}}
	if _, e := countCompactSlots(f, blob, entries, marks, nil); !errors.Is(e, ErrLimit) {
		t.Fatalf("aggregate signature limit: %v", e)
	}
}
