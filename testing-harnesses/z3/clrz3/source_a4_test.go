package clrz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
)

type failedCLRSource struct{}

func (failedCLRSource) ReadAt([]byte, int64) (int, error) { return 0, io.ErrClosedPipe }

func TestA4BodyAtLegacyDifferential(t *testing.T) {
	for _, path := range []string{"testdata/fixture-body.dll", "testdata/fixture-r0.dll", "testdata/fixture-earlytype.dll"} {
		original, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		f := parse(t, original)
		for row := uint32(1); row <= f.Rows(TMethodDef); row++ {
			off, _, ok := f.RVAToOff(f.Col(original, TMethodDef, row, 0))
			if !ok {
				continue
			}
			for mutation := -1; mutation < 40; mutation++ {
				data := append([]byte(nil), original...)
				at := int(off) + mutation
				if mutation >= 0 && at < len(data) {
					data[at] ^= 255
				}
				for _, strict := range []bool{false, true} {
					old, oo := ParseBody(data, int64(off), strict)
					source := &clrSource{r: bytes.NewReader(data), size: int64(len(data)), lo: -1}
					got, goOK, e := parseBodyAt(source, int64(off), strict)
					if errors.Is(e, ErrLimit) {
						continue
					} // explicit narrower creation-window subset
					if e != nil || oo != goOK || !reflect.DeepEqual(old, got) {
						t.Fatalf("%s row %d mutation %d strict %v oldOK %v newOK %v err %v old %+v new %+v", path, row, mutation, strict, oo, goOK, e, old, got)
					}
				}
			}
		}
	}
}
func TestA4CLRAtSourceFailureAndBounds(t *testing.T) {
	data, e := os.ReadFile("testdata/fixture-body.dll")
	if e != nil {
		t.Fatal(e)
	}
	f := parse(t, data)
	if _, e = EncodeAt(f, failedCLRSource{}, StreamConfig{TempDir: t.TempDir(), Emit: func([]byte) error { return nil }}); !errors.Is(e, io.ErrClosedPipe) {
		t.Fatal("lost source error", e)
	}
	badFile := *f
	badFile.Tables[TMethodDef].Rows = 1<<20 + 1
	if _, e = newCtxAt(&badFile, &clrSource{r: bytes.NewReader(data), size: int64(len(data)), lo: -1}, 3); !errors.Is(e, ErrLimit) {
		t.Fatal("aggregate rows", e)
	}
	if _, e = limitedHeapEntries(make([]byte, (1<<18)+1)); !errors.Is(e, ErrLimit) {
		t.Fatal("heap entry budget", e)
	}
}

func TestA4SharedBodyParsedOnce(t *testing.T) {
	original, e := os.ReadFile("testdata/fixture-body.dll")
	if e != nil {
		t.Fatal(e)
	}
	f := parse(t, original)
	const n = 1024
	data := make([]byte, 16384)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(data[i*4:], 12288)
	}
	data[8192], data[8193] = 6, 0x2a
	section := f.Secs[0]
	section.RawOff = 0
	section.RawSize = uint32(len(data))
	section.RVA = 4096
	f.Secs = append(f.Secs[:0:0], section)
	f.Tables[TMethodDef] = Table{Off: 0, Rows: n, RowSize: 4, Cols: []ColLayout{{Off: 0, W: 4}}}
	f.Size = int64(len(data))
	source := &clrSource{r: bytes.NewReader(data), size: int64(len(data)), lo: -1}
	got, e := compactFindBodiesAt(f, source, true)
	if e != nil || len(got) != n {
		t.Fatal(e, len(got))
	}
	if source.bodyWork != 2 {
		t.Fatalf("shared body scanned %d bytes", source.bodyWork)
	}
	for i, b := range got {
		if b.Row != uint32(i+1) || b.Opaque || b.Body.Off != 8192 || b.Body.End != 8194 {
			t.Fatal("shared body changed", b)
		}
	}
}

func TestA4RepeatedLongSuffixWorkIsBounded(t *testing.T) {
	c := &Ctx{str: append(bytes.Repeat([]byte{'x'}, 8192), 0), K0: 3, workLimit: 20000}
	for i := 1; i < 512; i++ {
		c.StrExtra = append(c.StrExtra, i)
	}
	if e := c.prepareStringBounds(); e != nil {
		t.Fatal(e)
	}
	c.StrIndex()
	if !errors.Is(c.err, ErrLimit) || c.workUsed > c.workLimit {
		t.Fatalf("unbounded repeated entry hashing: %d / %d, %v", c.workUsed, c.workLimit, c.err)
	}
}
func TestA4DeepIdentityDependencyIsBounded(t *testing.T) {
	a := newAsm()
	a.add(TModule, 0, a.s("deep.dll"), 1, 0, 0)
	for i := 1; i <= 300; i++ {
		scope := uint32(0)
		if i < 300 {
			scope = cdx(CResolutionScope, TTypeRef, uint32(i+1))
		}
		a.add(TTypeRef, scope, a.s("Name"), 0)
	}
	data := a.image()
	f := parse(t, data)
	emitted := 0
	_, e := EncodeAt(f, bytes.NewReader(data), StreamConfig{TempDir: t.TempDir(), Emit: func([]byte) error { emitted++; return nil }})
	if !errors.Is(e, ErrLimit) || emitted != 0 {
		t.Fatal("deep identity chain did not fail before output", e, emitted)
	}
}
