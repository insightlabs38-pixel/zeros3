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

func TestA4SharedReferenceDenseBodyCountsOnce(t *testing.T) {
	const n = 1 << 16
	tokens := make([]TokSlot, n)
	for i := range tokens {
		tokens[i] = TokSlot{Off: 100 + int64(i)*5, Val: 0x06000001, Class: TkMethod}
	}
	bodies := make([]BodyInfo, 64)
	for i := range bodies {
		bodies[i] = BodyInfo{Row: uint32(i + 1), Body: Body{Off: 100, Toks: tokens}}
	}
	got, e := compactILSlotsAt(nil, bodies, make([]Slot, 0, n))
	if e != nil || len(got) != n {
		t.Fatalf("one physical body referenced by 64 methods: slots=%d error=%v", len(got), e)
	}
	if count, e := countCompactSlotsAt(&File{}, nil, nil, nil, nil, bodies); e != nil || count != n {
		t.Fatalf("shared reference budget: %d %v", count, e)
	}
}

func TestA4SharedBodyValidationVisitsOnce(t *testing.T) {
	const aliases = 8192
	f := &File{}
	f.Tables[TMethodDef].Rows = 1
	f.Tables[TStandAloneSig].Rows = 1
	tokens := make([]TokSlot, 65536)
	for i := range tokens {
		tokens[i] = TokSlot{Off: 100 + int64(i)*5, Class: TkMethod, Val: 0x06000001}
	}
	bodies := make([]BodyInfo, aliases)
	for i := range bodies {
		bodies[i] = BodyInfo{Row: uint32(i + 1), Body: Body{Off: 80, End: 400000, LocalOff: 88, Toks: tokens}}
	}
	visits := 0
	validatePhysicalBodies(f, &HeapView{}, bodies, func(*BodyInfo) uint32 { visits++; return 0x11000001 })
	if visits != 1 {
		t.Fatalf("one shared physical body validated %d times", visits)
	}
	for _, b := range bodies {
		if b.Opaque {
			t.Fatal("valid alias became opaque")
		}
	}
}
