package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// ---- immutable fixture images -------------------------------------------------------------------------------
//
// A fixture that many tests build identically (a store holding a dataset) is built once per test binary and kept as an
// image under ZEROS3_FIXTURE_CACHE (set by the T1 runner to a directory named by the identity of the binary, so a code
// change starts from nothing). Every use gets a private copy, cloned with FICLONE where the filesystem offers it and
// copied otherwise, so tests never share a mutable file. An image carries the SHA-256 of its tree; it is checked the
// first time a process uses it and rebuilt when it does not match. Without the variable nothing is cached.

var fixtureChecked sync.Map // image directory -> true once this process hashed it

func fixtureRoot() string { return os.Getenv("ZEROS3_FIXTURE_CACHE") }

// fixtureTreeID is the content identity of a directory: every relative path, size and file digest.
func fixtureTreeID(dir string) (string, error) {
	var names []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fh := sha256.New()
		sz, err := io.Copy(fh, f)
		f.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %d %x\n", n, sz, fh.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fixtureClone copies src into the existing empty directory dst, cloning file extents when possible.
func fixtureClone(src, dst string) error {
	const ficlone = 0x40049409
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, out.Fd(), ficlone, in.Fd()); errno != 0 {
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
		}
		return out.Close()
	})
}

// fixtureImage fills the empty directory dir with the image named key; on a miss build populates it first.
func fixtureImage(t testing.TB, dir, key string, build func(dir string)) {
	t.Helper()
	root := fixtureRoot()
	if ents, err := os.ReadDir(dir); root == "" || err != nil || len(ents) > 0 {
		build(dir) // no cache, or the directory already holds something: build in place
		return
	}
	sum := sha256.Sum256([]byte(key + "|" + runtime.GOARCH))
	img := filepath.Join(root, hex.EncodeToString(sum[:16]))
	if err := os.MkdirAll(root, 0o755); err != nil {
		build(dir)
		return
	}
	lk, err := os.OpenFile(img+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		build(dir)
		return
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX); err != nil {
		build(dir)
		return
	}
	defer syscall.Flock(int(lk.Fd()), syscall.LOCK_UN)
	idFile := img + ".id"
	valid := func() bool {
		want, err := os.ReadFile(idFile)
		if err != nil {
			return false
		}
		if _, ok := fixtureChecked.Load(img); ok {
			return true
		}
		got, err := fixtureTreeID(img)
		if err != nil || got != strings.TrimSpace(string(want)) {
			return false
		}
		fixtureChecked.Store(img, true)
		return true
	}
	if !valid() {
		os.RemoveAll(img)
		os.Remove(idFile)
		tmp := fmt.Sprintf("%s.tmp.%d", img, os.Getpid())
		os.RemoveAll(tmp)
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			build(dir)
			return
		}
		build(tmp)
		id, err := fixtureTreeID(tmp)
		if err != nil || os.Rename(tmp, img) != nil || os.WriteFile(idFile, []byte(id+"\n"), 0o644) != nil {
			os.RemoveAll(tmp)
			build(dir)
			return
		}
		fixtureChecked.Store(img, true)
	}
	if err := fixtureClone(img, dir); err != nil {
		t.Fatalf("fixture image %s: %v", key, err)
	}
}

// packFixtureKey identifies the dataset of putPackTestObjects together with everything about the writer that decides
// the physical layout.
func packFixtureKey(data map[string][]byte, keys []string) string {
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s %d ", k, len(data[k]))
		h.Write(data[k])
	}
	env := os.Environ()
	sort.Strings(env)
	var z []string
	for _, e := range env {
		if strings.HasPrefix(e, "ZEROS3_") && !strings.HasPrefix(e, "ZEROS3_FIXTURE_CACHE=") && !strings.HasPrefix(e, "ZEROS3_TEST_BIN=") && !strings.HasPrefix(e, "ZEROS3_BINCACHE=") {
			z = append(z, e)
		}
	}
	return fmt.Sprintf("pack-objects %x layout direct=%d segments=%v env=%s", h.Sum(nil), directPackMinBytes, smallObjectSegments, strings.Join(z, ","))
}

// The image of a dataset is a store: cloning gives the same bytes and the same answers as building it again.
func TestFixtureImageEqualsBuildingInPlace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ZEROS3_FIXTURE_CACHE", root)
	data := map[string][]byte{"a": genRandomBytes(1, 300_000), "b": genRandomBytes(2, 50_000)}
	keys := []string{"a", "b"}
	plain := t.TempDir()
	t.Setenv("ZEROS3_FIXTURE_CACHE", "")
	putPackTestObjects(t, plain, data, keys...)
	t.Setenv("ZEROS3_FIXTURE_CACHE", root)
	first, second := t.TempDir(), t.TempDir()
	putPackTestObjects(t, first, data, keys...)  // miss: built, stored as an image, cloned
	putPackTestObjects(t, second, data, keys...) // hit: cloned
	for _, d := range []string{first, second} {
		s, err := OpenStore(d)
		if err != nil {
			t.Fatal(err)
		}
		for k, body := range data {
			if _, got, err := s.GetObject("b", k); err != nil || string(got) != string(body) {
				t.Fatalf("%s: %v", k, err)
			}
		}
		if vr, err := s.Verify(true); err != nil || !vr.OK() {
			t.Fatalf("verify: %v", err)
		}
		s.Close()
	}
	// private copies: damaging one leaves the image and the next clone intact
	largest := func(dir string) (best string) {
		var size int64
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if info, e := d.Info(); err == nil && e == nil && !d.IsDir() && info.Size() > size {
				best, size = p, info.Size()
			}
			return nil
		})
		return best
	}
	if victim := largest(first); victim == "" || os.WriteFile(victim, []byte("damaged"), 0o644) != nil {
		t.Fatal("no file to damage")
	}
	third := t.TempDir()
	putPackTestObjects(t, third, data, keys...)
	a, _ := fixtureTreeID(second)
	b, _ := fixtureTreeID(third)
	if a != b {
		t.Fatal("a mutated clone leaked into the image")
	}
	// an image that no longer matches its identity is rebuilt, not trusted
	fixtureChecked = sync.Map{}
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if e.IsDir() {
			os.WriteFile(largest(filepath.Join(root, e.Name())), []byte("rot"), 0o644)
		}
	}
	fourth := t.TempDir()
	putPackTestObjects(t, fourth, data, keys...)
	s4, err := OpenStore(fourth)
	if err != nil {
		t.Fatal(err)
	}
	defer s4.Close()
	for k, body := range data {
		if _, got, err := s4.GetObject("b", k); err != nil || string(got) != string(body) {
			t.Fatalf("a rotted image was served: %s: %v", k, err)
		}
	}
	if vr, err := s4.Verify(true); err != nil || !vr.OK() {
		t.Fatalf("a rotted image was served: verify %v", err)
	}
}
