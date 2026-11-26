package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func fb06Format(t *testing.T, root string) storeFormat {
	t.Helper()
	f, err := loadOrInitFormat(root, filepath.Join(root, "FORMAT.json"))
	mustOK(t, err)
	return f
}

func TestFB06GenerationRoundTripAndFallback(t *testing.T) {
	dir, want := b4Fixture(t, 16, 72<<10)
	format := fb06Format(t, dir)
	packs, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	if packs == 0 {
		t.Fatal("fixture has no sealed packs")
	}
	open := func(hit, maintain bool) *Store {
		t.Helper()
		s, err := openStoreWithLocator(dir, true, true, maintain, true)
		mustOK(t, err)
		if s.locatorFromGeneration.Load() != hit {
			t.Fatalf("locator generation hit=%v, want %v", s.locatorFromGeneration.Load(), hit)
		}
		b4Check(t, s, want)
		return s
	}
	s := open(true, true)
	mustOK(t, s.CreateBucket("new-journal-root"))
	_, err = s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if s.locatorFromGeneration.Load() {
		t.Fatal("maintenance retained generation-derived inventory")
	}
	mustOK(t, s.Close())
	s = open(true, false)
	mustOK(t, s.HeadBucket("new-journal-root"))
	mustOK(t, s.Close())

	files, err := listPackInventory(dir, format.StoreFormatVersion)
	mustOK(t, err)
	if _, err := loadLocatorGeneration(dir, "wrong-store", files); err == nil {
		t.Fatal("foreign store accepted")
	}
	path := filepath.Join(dir, locatorGenerationName)
	good, err := os.ReadFile(path)
	mustOK(t, err)
	damaged := bytes.Clone(good)
	damaged[len(damaged)-6] ^= 1
	mustOK(t, os.WriteFile(path, damaged, 0o600))
	s = open(false, false)
	mustOK(t, s.Close())
	mustOK(t, os.WriteFile(path, good[:len(good)-1], 0o600))
	s = open(false, false)
	mustOK(t, s.Close())

	mustOK(t, os.WriteFile(path, good, 0o600))
	s = open(true, false)
	large := b4Body(420, 2<<20)
	_, err = s.PutObject("b", "changes-pack-set", large, "application/octet-stream", nil)
	mustOK(t, err)
	want["changes-pack-set"] = large
	mustOK(t, s.Close())
	s = open(false, false)
	mustOK(t, s.Close())
	mustOK(t, os.Remove(path))
	s = open(false, false)
	mustOK(t, s.Close())
	_, err = buildLocatorGeneration(dir, format)
	mustOK(t, err)
	s = open(true, false)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}

func TestFB06GenerationPublishCut(t *testing.T) {
	if root := os.Getenv("ZEROS3_FB06_CUT_ROOT"); root != "" {
		testHook = func(point string) {
			if point == hookLocatorBeforePublish {
				os.Exit(73)
			}
		}
		_, err := buildLocatorGeneration(root, fb06Format(t, root))
		t.Fatalf("did not hit generation publication cut: %v", err)
	}
	dir, want := b4Fixture(t, 8, 80<<10)
	format := fb06Format(t, dir)
	_, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	path := filepath.Join(dir, locatorGenerationName)
	prior, err := os.ReadFile(path)
	mustOK(t, err)
	child := exec.Command(os.Args[0], "-test.run", "^TestFB06GenerationPublishCut$")
	child.Env = append(os.Environ(), "ZEROS3_FB06_CUT_ROOT="+dir)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("publication cut: %v, %s", err, out)
	}
	current, err := os.ReadFile(path)
	mustOK(t, err)
	if !bytes.Equal(prior, current) {
		t.Fatal("process interruption altered the existing generation")
	}
	s, err := openStoreWithLocator(dir, true, true, false, true)
	mustOK(t, err)
	if !s.locatorFromGeneration.Load() {
		t.Fatal("predecessor did not open after publication cut")
	}
	b4Check(t, s, want)
	mustOK(t, s.Close())
}
