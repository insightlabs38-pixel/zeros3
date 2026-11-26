package main

// FB-01 checkpoint-witness reproductions (research/next only: they need the
// published namespace checkpoint).

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

// fb01SeedCheckpoint acknowledges before puts, publishes a checkpoint, then
// acknowledges after more puts past it.
func fb01SeedCheckpoint(t testing.TB, dir string, before, after int) *fb01Truth {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	tr := newFB01Truth()
	for i := 0; i < before; i++ {
		tr.put(t, s, fmt.Sprintf("k%03d", i), fmt.Sprintf("value %03d", i))
	}
	if err := s.nsConsolidate(true); err != nil {
		t.Fatal(err)
	}
	for i := before; i < before+after; i++ {
		tr.put(t, s, fmt.Sprintf("k%03d", i), fmt.Sprintf("value %03d", i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return tr
}

// TestFB01ReproF4JNL01 is F4-JNL-01: the journal cut below the published
// checkpoint. At 2337f48 the checkpoint is ignored and replay keeps the
// shorter journal (14 of 40 objects).
func TestFB01ReproF4JNL01(t *testing.T) {
	const before, after = 30, 10
	base := t.TempDir()
	tr := fb01SeedCheckpoint(t, base, before, after)
	d0, err := os.ReadFile(fb01Journal(base))
	if err != nil {
		t.Fatal(err)
	}
	fr := fb01Frames(d0)
	cuts := []struct {
		name string
		at   int64
	}{
		{"frame boundary below checkpoint (after seq 15)", fr[15].off},
		{"mid-frame below checkpoint", fr[15].off + 9},
		{"frame boundary above checkpoint (drops 5 acked frames)", fr[len(fr)-5].off},
		{"mid-frame above checkpoint (in the final frame)", fr[len(fr)-1].off + 9},
	}
	for _, c := range cuts {
		for _, useCk := range []bool{true, false} {
			for _, owner := range []bool{false, true} {
				dir := fb01Copy(t, base)
				if err := os.Truncate(fb01Journal(dir), c.at); err != nil {
					t.Fatal(err)
				}
				mode := fmt.Sprintf("shared checkpoint=%v", useCk)
				if owner {
					mode = fmt.Sprintf("owner checkpoint=%v", useCk)
				}
				r := fb01Open(t, dir, tr, "F4-JNL-01", c.name, mode, func(d string) (*Store, error) { return openStore(d, owner, useCk) })
				fb01MustOrResidual(t, r, c.at == fr[len(fr)-5].off)
			}
		}
	}
}

// fb01MustOrResidual is fb01Must, except that a frame-boundary cut above every
// checkpoint is the v1 format's known blind spot (X14 S6): no in-band or
// checkpoint evidence can see it, so its loss is recorded, not failed.
func fb01MustOrResidual(t testing.TB, r fb01Result, residual bool) {
	t.Helper()
	if residual && r.Outcome == "LOSS" {
		r.Case += " [S6 residual: undetectable in v1]"
		fb01Emit(t, r)
		return
	}
	fb01Must(t, r)
}

// TestFB01ReproFlipAfterCheckpoint flips a length bit in an acknowledged
// frame past the checkpoint (34 of 60 at 2337f48).
func TestFB01ReproFlipAfterCheckpoint(t *testing.T) {
	const before, after = 30, 30
	base := t.TempDir()
	tr := fb01SeedCheckpoint(t, base, before, after)
	d, err := os.ReadFile(fb01Journal(base))
	if err != nil {
		t.Fatal(err)
	}
	fr := fb01Frames(d)
	f := fr[before+5]
	binary.BigEndian.PutUint32(d[f.off+16:], f.plen^(1<<20))
	for _, owner := range []bool{false, true} {
		dir := fb01Copy(t, base)
		if err := os.WriteFile(fb01Journal(dir), d, 0o644); err != nil {
			t.Fatal(err)
		}
		mode := "shared"
		if owner {
			mode = "owner"
		}
		fb01Must(t, fb01Open(t, dir, tr, "F6-JNL-01-ckpt", fmt.Sprintf("length bit 20 in frame seq %d past the checkpoint", f.seq), mode,
			func(d string) (*Store, error) { return openStore(d, owner, true) }))
	}
}
