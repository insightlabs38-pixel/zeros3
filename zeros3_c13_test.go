package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestC13PresetPackAdmissionAndReopen(t *testing.T) {
	l := newAuthLab(t)
	prefix := genRandomBytes(13092, 8192)
	keys := make(map[[32]byte][]byte)
	var batch []compactCandidate
	for i := range 32 {
		pos := i * 177
		data := bytes.Clone(prefix[pos : pos+2048])
		data = append(data, byte(i), 123)
		sum := sha256.Sum256(data)
		keys[sum] = data
		batch = append(batch, compactCandidate{sum: sum, size: int64(len(data))})
	}
	read := func(sum [32]byte) ([]byte, error) { return keys[sum], nil }
	skip := func(_ compactCandidate, err error) error { return err }
	comp := newPackCompressor()
	comp.prefix = prefix
	staged, entries, err := l.s.stagePack(tierHot, batch, read, skip, comp)
	mustOK(t, err)
	if len(entries) != len(batch)+2 || entries[0].codec != packCodecPresetDict || entries[len(entries)-1].codec != packCodecPresetDict {
		t.Fatal("paid preset did not stage two recovery contexts")
	}
	for _, entry := range entries[1 : len(entries)-1] {
		if entry.codec != packCodecPreset {
			t.Fatal("fixture record escaped the matching preset")
		}
	}
	info, err := l.s.publishPack(tierHot, staged, entries)
	mustOK(t, err)
	if !slices.Contains(l.s.formatSnapshot().Features, packPresetFeature) {
		t.Fatal("preset pack became visible without a format gate")
	}
	verify := func() {
		for sum, want := range keys {
			got, err := l.s.casRead(sum)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("preset read differs: %v", err)
			}
		}
		if _, err := l.s.casStat(sha256.Sum256(prefix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a local dictionary became a logical CAS chunk: %v", err)
		}
	}
	verify()
	l.close()
	l.open()
	var readers sync.WaitGroup
	for sum, want := range keys {
		readers.Go(func() {
			if got, err := l.s.casRead(sum); err != nil || !bytes.Equal(got, want) {
				t.Errorf("concurrent preset read differs: %v", err)
			}
		})
	}
	readers.Wait()
	verify()
	// An equal user object still gets its own logical record.
	l.put("authorized-object", prefix)
	l.exact("object matching a hidden dictionary")
	path := filepath.Join(l.dir, "packs", info.id+packFileSuffix)
	evict := func() {
		packDicts.Lock()
		for key := range packDicts.m {
			if key.path == path {
				delete(packDicts.m, key)
			}
		}
		packDicts.order = slices.DeleteFunc(packDicts.order, func(key packDictKey) bool { return key.path == path })
		packDicts.Unlock()
	}
	b5Flip(t, path, int64(entries[0].off)+23)
	evict()
	member := entries[1].sha
	got, err := l.s.casRead(member)
	if err != nil || !bytes.Equal(got, keys[member]) {
		t.Fatalf("verified backup dictionary did not recover the member: %v", err)
	}
	b5Flip(t, path, int64(entries[0].off)+23)
	b5Flip(t, path, int64(entries[len(entries)-1].off)+23)
	evict()
	if got, err := l.s.casRead(member); err != nil || !bytes.Equal(got, keys[member]) {
		t.Fatalf("verified primary dictionary did not recover the member: %v", err)
	}
	b5Flip(t, path, int64(entries[0].off)+23)
	evict()
	if _, err := l.s.casRead(member); err == nil {
		t.Fatal("both pack-local dictionaries were corrupt")
	}
	l.exact("ordinary object remains separate from the damaged preset")
}

func TestC13PresetPackPublication(t *testing.T) {
	prefix := genRandomBytes(13092, 8192)
	opt := repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true, Recompress: true, Prefix: prefix}
	if cut := os.Getenv("ZEROS3_C13_TEST_KILL"); cut != "" {
		testHook = func(point string) {
			if point == cut {
				mustOK(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
			}
		}
		_, err := repackStore(os.Getenv("ZEROS3_C13_TEST_STORE"), opt)
		t.Fatalf("missed the requested publication cut: %v", err)
	}
	bodies := make(map[string][]byte)
	for i := range 32 {
		pos := i * 177
		bodies[fmt.Sprintf("fragment-%02d", i)] = bytes.Clone(prefix[pos : pos+2048])
	}
	for _, cut := range []string{hookPackBeforePublish, hookPackAfterRename} {
		t.Run(cut, func(t *testing.T) {
			dir, snap := bundleFixture(t, bodies)
			ref, _ := bundleExportT(t, dir, snap, false)
			want, err := os.ReadFile(ref)
			mustOK(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestC13PresetPackPublication$")
			child.Env = append(os.Environ(), "ZEROS3_C13_TEST_KILL="+cut, "ZEROS3_C13_TEST_STORE="+dir)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("expected preset publication SIGKILL: %v\n%s", err, output)
			}
			check := func() {
				s, err := OpenStore(dir)
				mustOK(t, err)
				defer s.Close()
				verify, err := s.Verify(true)
				if err != nil || !verify.OK() || !slices.Contains(s.formatSnapshot().Features, packPresetFeature) {
					t.Fatalf("preset publication left an invalid store: %v %+v", err, verify.Issues)
				}
			}
			check()
			_, err = repackStore(dir, opt)
			mustOK(t, err)
			_, err = gcCollect(dir, true)
			mustOK(t, err)
			check()
			out, _ := bundleExportT(t, dir, snap, false)
			got, err := os.ReadFile(out)
			mustOK(t, err)
			if !bytes.Equal(got, want) {
				t.Fatal("pack-local context changed the portable bundle")
			}
			dst := t.TempDir()
			bundleImportT(t, dst, out)
			bundleRestoreCheck(t, dst, snap, bodies)
		})
	}
}

func TestC13PresetPackRejectsUnpaidContext(t *testing.T) {
	l := newAuthLab(t)
	prefix := genRandomBytes(13101, 8192)
	data := genRandomBytes(13104, 4096)
	sum := sha256.Sum256(data)
	comp := newPackCompressor()
	comp.prefix = prefix
	read := func([32]byte) ([]byte, error) { return data, nil }
	staged, entries, err := l.s.stagePack(tierHot, []compactCandidate{{sum: sum, size: int64(len(data))}},
		read, func(_ compactCandidate, err error) error { return err }, comp)
	mustOK(t, err)
	defer os.Remove(staged)
	if len(entries) != 1 || entries[0].codec == packCodecPreset {
		t.Fatal("unpaid context was materialized")
	}
}
