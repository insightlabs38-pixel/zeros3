package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

// runnerVersion is part of every identity: a change of the result semantics invalidates older caches.
const runnerVersion = "t1run-v2"

// identityEnv lists the environment variables that can change what a test does without changing the binary.
var identityEnv = regexp.MustCompile(`^(ZEROS3_|(GOARCH|GOOS|GOMAXPROCS|GODEBUG|GOGC|GOMEMLIMIT|GOFLAGS|TZ|LANG|LC_[A-Z]+)=)`)

func isRuntimeOnlyEnv(e string) bool {
	return strings.HasPrefix(e, "ZEROS3_TEST_BIN=") || strings.HasPrefix(e, "ZEROS3_BINCACHE=") || strings.HasPrefix(e, "ZEROS3_FIXTURE_CACHE=")
}

// identityOf hashes everything a cached pass must have in common with the run that reuses it: the test
// binary, the mode, testing flags, the environment that matters, the machine class and the fixture files.
func identityOf(binHash, mode, extra, goVer, fixtures string, env []string, timeout string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%s\n%s\n%s/%s cpus=%d\n%s\n", runnerVersion, binHash, mode, extra, goVer, fixtures, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), timeout)
	var keep []string
	for _, e := range env {
		if identityEnv.MatchString(e) && !isRuntimeOnlyEnv(e) {
			keep = append(keep, e)
		}
	}
	sort.Strings(keep)
	for _, e := range keep {
		fmt.Fprintln(h, e)
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

var fixtureLit = regexp.MustCompile(`"(testing-harnesses/[^"\s]*)"`)

// fixtureHash covers every file the root tests read from testing-harnesses: the directory of each literal
// path the tests mention, plus the root testdata tree.
func fixtureHash(root string) string {
	dirs := map[string]bool{"testdata": true}
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "_test.go") {
			b, _ := os.ReadFile(filepath.Join(root, e.Name()))
			for _, m := range fixtureLit.FindAllSubmatch(b, -1) {
				p := string(m[1])
				if st, err := os.Stat(filepath.Join(root, p)); err == nil && st.IsDir() {
					dirs[p] = true
				} else {
					dirs[filepath.Dir(p)] = true
				}
			}
		}
	}
	var names []string
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, d := range names {
		filepath.WalkDir(filepath.Join(root, d), func(p string, de os.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			fmt.Fprintf(h, "%s ", rel)
			if f, err := os.Open(p); err == nil {
				io.Copy(h, f)
				f.Close()
			}
			return nil
		})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func sourceHash(root, goVer string) string {
	h := sha256.New()
	fmt.Fprintln(h, goVer)
	var files []string
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".go") || n == "go.mod" || n == "go.sum" {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(root, f))
		fmt.Fprintf(h, "%s %d\n", f, len(b))
		h.Write(b)
	}
	fmt.Fprintln(h, fixtureHash(root))
	return hex.EncodeToString(h.Sum(nil))
}

func fileHash(p string) string {
	f, err := os.Open(p)
	must(err)
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// writeAtomic publishes a file with a rename, so a concurrent reader or a crash never sees a partial entry.
func writeAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
	return err
}

// withLock serializes builders of the same artifact across concurrent invocations.
func withLock(path string, fn func() error) error {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// buildOnce runs build(tmpPath) under a lock and publishes the result atomically unless it already exists.
func buildOnce(path string, build func(tmp string) error) error {
	return withLock(path, func() error {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		tmp := fmt.Sprintf("%s.build.%d", path, os.Getpid())
		defer os.Remove(tmp)
		if err := build(tmp); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}

// loadCached returns a stored result only if it is a complete pass for exactly this identity. Skips,
// failures, timeouts and cancellations are never reusable.
func loadCached(dir, test, id string) (*result, bool) {
	b, err := os.ReadFile(filepath.Join(dir, test+".json"))
	if err != nil {
		return nil, false
	}
	var r result
	if json.Unmarshal(b, &r) != nil || r.Status != "pass" || r.Test != test || r.Identity != id || !r.Complete {
		return nil, false
	}
	r.Cached = true
	return &r, true
}

func freeMiB(dir string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 1 << 40
	}
	return int64(st.Bavail) * int64(st.Bsize) >> 20
}

// cleanStale removes per-process scratch of runners that no longer exist.
func cleanStale(tmpRoot string) {
	ents, _ := os.ReadDir(tmpRoot)
	for _, e := range ents {
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err == nil && pid != os.Getpid() && syscall.Kill(pid, 0) == syscall.ESRCH {
			os.RemoveAll(filepath.Join(tmpRoot, e.Name()))
		}
	}
}
