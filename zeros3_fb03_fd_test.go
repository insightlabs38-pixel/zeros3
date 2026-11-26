package main

// FB-03 M-A8: the pack descriptor cache (section 4c, F3-FS-01, F3-CARRY-05).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fb03DeletedOpen lists this process's open descriptors on deleted files
// under dir.
func fb03DeletedOpen(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	var out []string
	for _, e := range ents {
		l, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(l, dir) && strings.HasSuffix(l, " (deleted)") {
			out = append(out, l)
		}
	}
	return out
}

func fb03FDFiles(t *testing.T, n int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		if err := os.WriteFile(p, []byte{byte(i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func fb03ReadByte(d *packFD) (byte, error) {
	var b [1]byte
	_, err := d.f.ReadAt(b[:], 0)
	return b[0], err
}

func TestFB03_PackFDCacheUnit(t *testing.T) {
	t.Setenv("ZEROS3_PACK_FDS", "3")
	dir, paths := fb03FDFiles(t, 6)
	c := &packFDCache{}
	get := func(i int) *packFD {
		t.Helper()
		d, err := c.get(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		if b, err := fb03ReadByte(d); err != nil || b != byte(i) {
			t.Fatalf("file %d read %d, %v", i, b, err)
		}
		return d
	}
	for i := range 5 {
		c.put(get(i))
	}
	if c.size() != 3 || c.opens.Load() != 5 {
		t.Fatalf("size %d opens %d after 5 files with room for 3", c.size(), c.opens.Load())
	}
	c.put(get(4)) // a hit opens nothing
	if c.opens.Load() != 5 {
		t.Fatal("a cached file was opened again")
	}

	// An eviction while a reader holds the file closes it when the reader lets go.
	d := get(4)
	c.evict(paths[4])
	if _, err := fb03ReadByte(d); err != nil {
		t.Fatal("evicting closed a file a reader still held")
	}
	c.put(d)
	if _, err := fb03ReadByte(d); err == nil {
		t.Fatal("an evicted file stayed open after its last reader")
	}

	// Busy entries are never dropped for room; the next get trims.
	var held []*packFD
	for i := range 5 {
		held = append(held, get(i))
	}
	if c.size() != 5 {
		t.Fatalf("size %d with 5 busy entries", c.size())
	}
	for _, d := range held {
		c.put(d)
	}
	c.put(get(5))
	if c.size() != 3 {
		t.Fatalf("size %d after trimming", c.size())
	}

	// unlinked: nothing stays open on a removed file, even one a reader cached
	// while the removal ran.
	c.put(get(5))
	if err := c.unlinked(func() error {
		c.put(get(5)) // a reader of an older snapshot caches it again
		return os.Remove(paths[5])
	}, paths[5]); err != nil {
		t.Fatal(err)
	}
	if l := fb03DeletedOpen(t, dir); len(l) != 0 {
		t.Fatalf("open on removed files: %v", l)
	}

	// After close nothing is cached and every descriptor closes on put.
	c.close()
	d = get(1)
	c.put(d)
	if c.size() != 0 {
		t.Fatal("a closed cache kept a descriptor")
	}
	if _, err := fb03ReadByte(d); err == nil {
		t.Fatal("a closed cache left a descriptor open")
	}

	// Disabled: every get opens and every put closes.
	t.Setenv("ZEROS3_PACK_FDS", "0")
	z := &packFDCache{}
	d, err := z.get(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	z.put(d)
	if z.size() != 0 {
		t.Fatal("a disabled cache kept a descriptor")
	}
}

func TestFB03_PackFDCacheConcurrent(t *testing.T) {
	t.Setenv("ZEROS3_PACK_FDS", "4")
	dir, paths := fb03FDFiles(t, 10)
	c := &packFDCache{}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(r *rand.Rand) {
			defer wg.Done()
			for range 4000 {
				i := r.IntN(len(paths))
				switch r.IntN(10) {
				case 0:
					c.evict(paths[i])
				case 1:
					c.purge()
				default:
					d, err := c.get(paths[i])
					if err != nil {
						t.Error(err)
						return
					}
					if b, err := fb03ReadByte(d); err != nil || b != byte(i) {
						t.Errorf("file %d read %d, %v", i, b, err)
					}
					c.put(d)
				}
			}
		}(rand.New(rand.NewPCG(uint64(w), 3)))
	}
	wg.Wait()
	if c.size() > 4 {
		t.Fatalf("size %d over the bound when idle", c.size())
	}
	c.close()
	if n := c.size(); n != 0 {
		t.Fatalf("size %d after close", n)
	}
	_ = dir
}

// fb03Get GETs an object through the server (the path the cache serves).
func fb03Get(t *testing.T, srv *Server, creds Credentials, bucket, key string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/"+bucket+"/"+key, nil)
	empty := sha256.Sum256(nil)
	if err := signSigV4Request(req, creds, "us-east-1", hex.EncodeToString(empty[:]), time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", key, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// TestFB03_PackFDCacheRetirement serves GETs through cached descriptors,
// retires packs online, and requires every retired pack's file closed once
// it is unlinked (its space reclaimed) while the remaining objects still read.
func TestFB03_PackFDCacheRetirement(t *testing.T) {
	dir, want := b4Fixture(t, 16, 90<<10)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	b4Garbage(t, s, want)
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	srv := NewServer(s, creds, "us-east-1")
	s.purgeReadCaches()
	for k, v := range want {
		if got := fb03Get(t, srv, creds, "b", k); !bytes.Equal(got, v) {
			t.Fatalf("%s differs", k)
		}
	}
	if s.fds.size() == 0 {
		t.Fatal("serving GETs cached no pack descriptor")
	}
	opens := s.fds.opens.Load()
	s.ccache.purge()
	for k := range want {
		fb03Get(t, srv, creds, "b", k)
	}
	if s.fds.opens.Load() != opens {
		t.Fatalf("a second pass over the same packs opened %d files", s.fds.opens.Load()-opens)
	}
	rep, err := s.maintainOnline(context.Background(), b4Opts())
	if err != nil {
		t.Fatal(err)
	}
	if rep.PacksRetired == 0 {
		t.Fatal("maintenance retired no pack")
	}
	if l := fb03DeletedOpen(t, dir); len(l) != 0 {
		t.Fatalf("retired packs still open after unlink: %v", l)
	}
	s.purgeReadCaches()
	for k, v := range want {
		if got := fb03Get(t, srv, creds, "b", k); !bytes.Equal(got, v) {
			t.Fatalf("%s differs after retirement", k)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := s.fds.size(); n != 0 {
		t.Fatalf("%d descriptors left after Close", n)
	}
}

// TestFB03_PackFDCacheSealAndMove covers the other two unlink paths: a
// segment's file is renamed or linked into a pack and its name removed at
// seal, and a tier move removes the source pack. Descriptors cached while
// serving must not outlive either name.
func TestFB03_PackFDCacheSealAndMove(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 64<<10, 16<<10)
	s, dir := p1cOpen(t)
	want := p1cPutN(t, s, "f", 40, 8000)
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	srv := NewServer(s, creds, "us-east-1")
	serve := func(stage string) {
		t.Helper()
		s.purgeReadCaches()
		for k, v := range want {
			if got := fb03Get(t, srv, creds, "b", k); !bytes.Equal(got, v) {
				t.Fatalf("%s: %s differs", stage, k)
			}
		}
		if s.fds.size() == 0 {
			t.Fatalf("%s: serving cached no descriptor", stage)
		}
	}
	serve("open segments")
	s.sealSegments()
	if l := fb03DeletedOpen(t, dir); len(l) != 0 {
		t.Fatalf("segment names still open after seal: %v", l)
	}
	serve("sealed")
	moved := 0
	for _, p := range s.packSnap().packs {
		if p.open || p.tier != tierHot {
			continue
		}
		if err := s.movePack(p.id, tierHot, tierCold); err != nil {
			t.Fatalf("move %s: %v", p.id, err)
		}
		moved++
	}
	if moved == 0 {
		t.Fatal("no sealed pack to move")
	}
	if l := fb03DeletedOpen(t, dir); len(l) != 0 {
		t.Fatalf("moved packs still open at their old names: %v", l)
	}
	if err := s.reloadPacks(); err != nil {
		t.Fatal(err)
	}
	serve("moved")
}
