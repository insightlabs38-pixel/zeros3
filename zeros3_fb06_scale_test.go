package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFB06ScaleOpen(t *testing.T) {
	if dir := os.Getenv("ZEROS3_FB06_OPEN_CHILD"); dir != "" {
		fb06ScaleChild(t, dir)
		return
	}
	count := os.Getenv("ZEROS3_FB06_SCALE")
	if count == "" {
		t.Skip("set ZEROS3_FB06_SCALE to the generated record count")
	}
	n, err := strconv.Atoi(count)
	mustOK(t, err)
	if n < 1 || n > 25_000_000 {
		t.Fatal("scale must be between 1 and 25 million records")
	}
	dir := t.TempDir()
	s := b4OpenMaint(t, dir)
	const recordsPerPack = 128 << 10
	start := time.Now()
	for base := 0; base < n; base += recordsPerPack {
		k := min(n-base, recordsPerPack)
		b, err := s.newPackBuilder(tierHot, nil, k)
		mustOK(t, err)
		for i := range k {
			var data [8]byte
			binary.LittleEndian.PutUint64(data[:], uint64(base+i))
			b.Add(sha256.Sum256(data[:]), data[:])
		}
		tmp, _, err := b.Finish()
		mustOK(t, err)
		mustOK(t, os.MkdirAll(tierPackDir(dir, tierHot), 0o755))
		mustOK(t, os.Rename(tmp, filepath.Join(tierPackDir(dir, tierHot), hex.EncodeToString(b.sum[:])+packFileSuffix)))
	}
	mustOK(t, s.Close())
	prepared := time.Since(start)
	start = time.Now()
	packs, err := buildLocatorGeneration(dir, fb06Format(t, dir))
	mustOK(t, err)
	info, err := os.Stat(filepath.Join(dir, locatorGenerationName))
	mustOK(t, err)
	t.Logf("physical stage records=%d packs=%d prepare=%s generation=%s generation_bytes=%d",
		n, packs, prepared, time.Since(start), info.Size())
	debug.FreeOSMemory()

	for _, profile := range []string{"canonical", "generation", "tags", "tags-advised-cold"} {
		if profile == "tags-advised-cold" {
			cmd := exec.Command("python3", "-c", "import os,sys; fd=os.open(sys.argv[1],os.O_RDONLY); os.posix_fadvise(fd,0,0,os.POSIX_FADV_DONTNEED); os.close(fd)",
				filepath.Join(dir, locatorGenerationName))
			if result, err := cmd.CombinedOutput(); err != nil {
				t.Logf("cache eviction advice unavailable: %v %s", err, result)
				continue
			}
		}
		child := exec.Command(os.Args[0], "-test.run", "^TestFB06ScaleOpen$", "-test.v")
		child.Env = append(os.Environ(), "ZEROS3_FB06_OPEN_CHILD="+dir,
			"ZEROS3_FB06_OPEN_PROFILE="+profile, "ZEROS3_FB06_OPEN_COUNT="+count)
		result, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("profile %s: %v\n%s", profile, err, result)
		}
		for _, line := range strings.Split(string(result), "\n") {
			if strings.HasPrefix(line, "FB06 ") {
				fmt.Println(line)
			}
		}
	}
}

func fb06ScaleChild(t *testing.T, dir string) {
	profile := os.Getenv("ZEROS3_FB06_OPEN_PROFILE")
	count, err := strconv.Atoi(os.Getenv("ZEROS3_FB06_OPEN_COUNT"))
	mustOK(t, err)
	tags := strings.HasPrefix(profile, "tags")
	runtime.GC()
	var before, retained runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	s, err := openStoreWithLocator(dir, true, true, false, profile == "generation", tags)
	open := time.Since(start)
	mustOK(t, err)
	defer s.Close()
	if s.locatorFromGeneration.Load() != (profile != "canonical") || s.packSnap().distinct() != count {
		t.Fatal("profile did not index the complete generated inventory")
	}
	runtime.GC()
	runtime.ReadMemStats(&retained)
	status, err := os.ReadFile("/proc/self/status")
	mustOK(t, err)
	var rss, peak string
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			peak = strings.Join(strings.Fields(line)[1:], "")
		}
		if strings.HasPrefix(line, "VmRSS:") {
			rss = strings.Join(strings.Fields(line)[1:], "")
		}
	}
	for _, i := range []int{0, count / 2, count - 1} {
		var data [8]byte
		binary.LittleEndian.PutUint64(data[:], uint64(i))
		got, err := s.casRead(sha256.Sum256(data[:]))
		mustOK(t, err)
		if len(got) != len(data) || binary.LittleEndian.Uint64(got) != uint64(i) {
			t.Fatalf("different generated record %d", i)
		}
	}
	fmt.Printf("FB06 profile=%s records=%d open_ms=%.3f retained_bytes=%d bytes_per_record=%.3f rss=%s peak_rss=%s\n",
		profile, count, float64(open)/float64(time.Millisecond),
		int64(retained.HeapAlloc)-int64(before.HeapAlloc), float64(int64(retained.HeapAlloc)-int64(before.HeapAlloc))/float64(count),
		rss, peak)
}

func TestFB06DedupIngest(t *testing.T) {
	if os.Getenv("ZEROS3_FB06_INGEST") != "1" {
		t.Skip("set ZEROS3_FB06_INGEST=1 for paired native ingest timings")
	}
	dir, originals := b4Fixture(t, 192, 160<<10)
	format := fb06Format(t, dir)
	packs, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	keys := slices.Sorted(maps.Keys(originals))
	charged := len(keys) * len(originals[keys[0]])
	for pair := range 6 {
		profiles := []string{"canonical", "tags"}
		if pair%2 != 0 {
			slices.Reverse(profiles)
		}
		for _, profile := range profiles {
			tags := profile == "tags"
			s, err := openStoreWithLocator(dir, true, true, false, false, tags)
			mustOK(t, err)
			if s.locatorFromGeneration.Load() != tags {
				t.Fatal("wrong locator opened for dedup benchmark")
			}
			start := time.Now()
			for _, key := range keys {
				_, err := s.PutObject("b", "repeat/"+key, originals[key], "application/octet-stream", nil)
				mustOK(t, err)
			}
			elapsed := time.Since(start)
			_, checked, err := s.GetObject("b", "repeat/"+keys[len(keys)-1])
			mustOK(t, err)
			if sha256.Sum256(checked) != sha256.Sum256(originals[keys[len(keys)-1]]) {
				t.Fatal("deduplicated put returned different bytes")
			}
			mustOK(t, s.Close())
			files, err := listPackInventory(dir, format.StoreFormatVersion)
			mustOK(t, err)
			if len(files) != packs {
				t.Fatal("a deduplicated PUT changed the sealed packs")
			}
			fmt.Printf("FB06 pair=%d profile=%s objects=%d bytes=%d elapsed_ms=%.3f mib_s=%.3f\n", pair, profile,
				len(keys), charged, float64(elapsed)/float64(time.Millisecond), float64(charged)/(1<<20)/elapsed.Seconds())
		}
	}
}
