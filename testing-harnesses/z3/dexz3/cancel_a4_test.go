package dexz3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

type a4CancelReader struct {
	r      io.ReaderAt
	cancel bool
}

func (r *a4CancelReader) ReadAt(p []byte, off int64) (int, error) {
	if r.cancel {
		return 0, context.Canceled
	}
	return r.r.ReadAt(p, off)
}
func TestA4StreamingCancellationCleanup(t *testing.T) {
	data := fixture(t, "fixture-api28.dex")
	f, e := parse(data)
	if e != nil {
		t.Fatal(e)
	}
	for _, point := range []string{"walk", "patches", "references"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			r := &a4CancelReader{r: bytes.NewReader(data)}
			fired := false
			_, e := EncodeAt(f, r, StreamConfig{TempDir: dir, Observe: func(p string) {
				if p == point {
					r.cancel = true
					fired = true
				}
			}, Emit: func([]byte) error { return nil }})
			if !fired || !errors.Is(e, context.Canceled) {
				t.Fatalf("cancellation %s: %v fired=%v", point, e, fired)
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) != 0 {
				t.Fatalf("spool leak: %v %v", entries, e)
			}
		})
	}
	dir := t.TempDir()
	if _, e := EncodeAt(f, bytes.NewReader(data), StreamConfig{TempDir: dir, Emit: func([]byte) error { return context.Canceled }}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal("sink cancellation leak", entries, e)
	}
}
