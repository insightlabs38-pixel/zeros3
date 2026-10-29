package clrz3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
)

func TestA4StreamedCLRIdentity(t *testing.T) {
	for _, path := range []string{"testdata/fixture-r0.dll", "testdata/fixture-earlytype.dll", "testdata/fixture-body.dll"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f := parse(t, data)
		for _, auto := range []int{0, 12} {
			for k := 1; k <= 4; k++ {
				for _, target := range []int{1024, 2048, 4096} {
					old, err := Encode(f, data, Config{K0: k, Auto: auto, Cut: econx.UnitCutBytes(target)})
					if err != nil {
						t.Fatal(path, err)
					}
					var comps [][]byte
					dir := t.TempDir()
					got, err := EncodeTo(f, data, StreamConfig{K0: k, Auto: auto, TempDir: dir, Cut: func(r io.ReaderAt, s []idxref.Sec) ([]int, error) { return econx.UnitCutAt(r, s, target) }, Emit: func(b []byte) error { comps = append(comps, append([]byte(nil), b...)); return nil }})
					if err != nil {
						t.Fatal(path, err)
					}
					if !bytes.Equal(old.Meta, got.Meta) || !reflect.DeepEqual(old.Comps, comps) || !reflect.DeepEqual(old.Cuts, got.Cuts) || old.Stats != got.Stats {
						t.Fatalf("%s changed auto=%d k=%d target=%d stats %+v/%+v", path, auto, k, target, old.Stats, got.Stats)
					}
					out, err := Decode(got.Meta, comps)
					if err != nil || !bytes.Equal(out, data) {
						t.Fatal("proof", err)
					}
					if len(got.Comps) != 0 || len(got.Refs) != 0 {
						t.Fatal("retained output")
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Fatal("spool leak", err)
					}
				}
			}
		}
	}
}
func TestA4CLRSinkFailureCleansSpools(t *testing.T) {
	data, err := os.ReadFile("testdata/fixture-body.dll")
	if err != nil {
		t.Fatal(err)
	}
	f := parse(t, data)
	dir := t.TempDir()
	sentinel := errors.New("sink failure")
	if _, err = EncodeTo(f, data, StreamConfig{TempDir: dir, Emit: func([]byte) error { return sentinel }}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("spool leak", err)
	}
}
