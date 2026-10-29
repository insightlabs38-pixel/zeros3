package dexz3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"zeros3-z3/econx"
)

func TestA4StreamedDEXIdentity(t *testing.T) {
	for _, data := range [][]byte{(&tDex{}).build(), small([]uint16{0x000e}).build(), fixture(t, "fixture-api24.dex"), fixture(t, "fixture-api28.dex")} {
		f, err := parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, auto := range []int{0, 12} {
			for k := 1; k <= 4; k++ {
				if auto > 0 && k != 3 {
					continue
				}
				for _, target := range []int{1024, 2048, 4096} {
					old, err := Encode(f, data, Config{K0: k, Auto: auto, Cut: econx.UnitCutBytes(target)})
					if err != nil {
						t.Fatal(err)
					}
					var comps [][]byte
					dir := t.TempDir()
					got, err := EncodeAt(f, bytes.NewReader(data), StreamConfig{K0: k, Auto: auto, TempDir: dir, Cut: func(r io.ReaderAt, s []Sec) ([]int, error) { return econx.UnitCutAt(r, s, target) }, Emit: func(b []byte) error { comps = append(comps, append([]byte(nil), b...)); return nil }})
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(old.Meta, got.Meta) || !reflect.DeepEqual(old.Comps, comps) || !reflect.DeepEqual(old.Cuts, got.Cuts) || old.Stats != got.Stats {
						t.Fatalf("identity changed auto=%d k=%d target=%d stats %+v/%+v", auto, k, target, old.Stats, got.Stats)
					}
					out, err := Decode(got.Meta, comps)
					if err != nil || !bytes.Equal(out, data) {
						t.Fatal("full proof", err)
					}
					if len(got.Comps) != 0 || len(got.Refs) != 0 {
						t.Fatal("retained component/reference output")
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Fatal("reference spool leak", entries, err)
					}
				}
			}
		}
	}
}
func TestA4StreamSinkFailureCleansSpool(t *testing.T) {
	data := fixture(t, "fixture-api28.dex")
	f, _ := parse(data)
	dir := t.TempDir()
	sentinel := errors.New("sink failure")
	if _, err := EncodeAt(f, bytes.NewReader(data), StreamConfig{TempDir: dir, Emit: func([]byte) error { return sentinel }}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("spool leak", err)
	}
}

func TestA4DecodeGetterMayReuseBuffer(t *testing.T) {
	data := fixture(t, "fixture-api28.dex")
	f, e := parse(data)
	if e != nil {
		t.Fatal(e)
	}
	rep, e := Encode(f, data, Config{Cut: fixedCut(700)})
	if e != nil {
		t.Fatal(e)
	}
	var scratch []byte
	calls := 0
	got, e := DecodeFrom(rep.Meta, len(rep.Comps), func(i int) ([]byte, error) {
		calls++
		clear(scratch)
		scratch = append(scratch[:0], rep.Comps[i]...)
		return scratch, nil
	})
	if e != nil || !bytes.Equal(got, data) || calls != 2*len(rep.Comps) {
		t.Fatal("getter full proof", e, calls)
	}
	calls = 0
	_, e = DecodeFrom(rep.Meta, len(rep.Comps), func(i int) ([]byte, error) {
		calls++
		if calls > len(rep.Comps) {
			return nil, io.ErrClosedPipe
		}
		return rep.Comps[i], nil
	})
	if !errors.Is(e, io.ErrClosedPipe) {
		t.Fatal("second-pass error lost", e)
	}
}
