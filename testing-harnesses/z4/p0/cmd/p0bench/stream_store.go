package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const streamFixtureBucket = "scale-fixture"

type streamFixtureCursor struct {
	Version int    `json:"version"`
	StoreID string `json:"store_id"`
	Binary  string `json:"binary_sha256"`
	Size    int    `json:"object_bytes"`
	Seed    uint64 `json:"seed"`
	Next    int    `json:"next_object"`
}

func saveStreamCursor(path string, state streamFixtureCursor) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mkstream-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(state); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Small repeatable payloads keep the scale experiment within a physical
// storage budget. The embedded index makes every actual body distinct.
func streamFixtureBody(index, size int, seed uint64) []byte {
	data := make([]byte, size)
	binary.LittleEndian.PutUint64(data[0:], uint64(index))
	binary.LittleEndian.PutUint64(data[8:], seed)
	sum := sha256.Sum256(data[:16])
	for off := 16; off < size; off += len(sum) {
		copy(data[off:], sum[:])
	}
	return data
}

func verifyStreamObject(client *S3, url string, want []byte) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	sign(req, "UNSIGNED-PAYLOAD")
	response, err := client.do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	got, err := io.ReadAll(io.LimitReader(response.Body, int64(len(want))+1))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || sha256.Sum256(got) != sha256.Sum256(want) {
		return fmt.Errorf("%s: independent content check failed, status %d, %d bytes", req.URL.Path, response.StatusCode, len(got))
	}
	return nil
}

func putStreamObject(client *S3, url string, body []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("If-None-Match", "*")
	digest := sha256.Sum256(body)
	sign(req, fmt.Sprintf("%x", digest))
	response, err := client.do(req)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return verifyStreamObject(client, url, body)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: PUT status %d", req.URL.Path, response.StatusCode)
	}
	return nil
}

func runStreamStore(c *common, dir string, objects, size, batch, verifyEvery int, seed uint64) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(abs+".mkstream.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("fixture writer in use: %w", err)
	}

	f, err := os.Open(c.binary())
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil {
		return err
	}
	want := streamFixtureCursor{Version: 1, Binary: fmt.Sprintf("%x", hash.Sum(nil)), Size: size, Seed: seed}
	cursorPath := abs + ".mkstream.json"
	cursor, err := os.ReadFile(cursorPath)
	switch {
	case err == nil:
		var last streamFixtureCursor
		if err := json.Unmarshal(cursor, &last); err != nil {
			return err
		}
		if last.Version != want.Version || last.Binary != want.Binary || last.Size != want.Size ||
			last.Seed != want.Seed || last.Next < 0 || last.Next > objects || last.Next > 0 && last.StoreID == "" {
			return errors.New("fixture parameters or requested object count conflict with the cursor")
		}
		want = last
	case errors.Is(err, os.ErrNotExist):
		if _, err := os.Stat(abs); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refusing to initialize fixture in an existing directory: %s", abs)
		}
		if err := saveStreamCursor(cursorPath, want); err != nil {
			return err
		}
	default:
		return err
	}

	readID := func() (string, error) {
		encoded, err := os.ReadFile(filepath.Join(abs, "FORMAT.json"))
		if err != nil {
			return "", err
		}
		var format struct {
			StoreID string `json:"store_id"`
		}
		if err := json.Unmarshal(encoded, &format); err != nil || format.StoreID == "" {
			return "", errors.New("fixture store identity is invalid")
		}
		return format.StoreID, nil
	}
	if want.StoreID != "" {
		if id, err := readID(); err != nil || id != want.StoreID {
			return fmt.Errorf("fixture store does not match cursor identity: %v", err)
		}
	}
	newServer := func() (*Server, *S3, error) {
		srv, err := StartServer(c.binary(), abs, c.profDir(0), c.srvArgs(), c.envList())
		if err != nil {
			return nil, nil, err
		}
		api := &S3{c: newClient(c.conc + 8), base: srv.URL}
		if err := waitReady(api.c, srv.URL); err != nil {
			srv.Stop()
			return nil, nil, err
		}
		return srv, api, nil
	}
	srv, api, err := newServer()
	if err != nil {
		return err
	}
	defer func() {
		if srv != nil {
			srv.Stop()
		}
	}()
	if want.StoreID == "" {
		want.StoreID, err = readID()
		if err != nil {
			return err
		}
		if err := saveStreamCursor(cursorPath, want); err != nil {
			return err
		}
	}
	if err := api.CreateBucket(streamFixtureBucket); err != nil {
		return err
	}
	key := func(api *S3, i int) string { return fmt.Sprintf("%s/%s/o-%012d", api.base, streamFixtureBucket, i) }
	start := time.Now()
	resumed := want.Next
	for want.Next < objects {
		n := min(batch, objects-want.Next)
		begin := want.Next
		if err := pool(n, c.conc, func(i int) error {
			index := begin + i
			return putStreamObject(api, key(api, index), streamFixtureBody(index, size, seed))
		}); err != nil {
			return err
		}
		want.Next += n
		if err := saveStreamCursor(cursorPath, want); err != nil {
			return err
		}
		if want.Next/100000 != begin/100000 {
			fmt.Fprintf(os.Stderr, "%d/%d acknowledged fixture objects\n", want.Next, objects)
		}
	}
	ingestSeconds := time.Since(start).Seconds()
	if err := srv.Stop(); err != nil {
		return err
	}
	srv = nil
	reader, api, err := newServer()
	if err != nil {
		return err
	}
	defer func() {
		if reader != nil {
			reader.Stop()
		}
	}()
	picks := (objects-1)/verifyEvery + 1
	if err := pool(picks, c.conc, func(i int) error {
		index := i * verifyEvery
		return verifyStreamObject(api, key(api, index), streamFixtureBody(index, size, seed))
	}); err != nil {
		return err
	}
	if last := objects - 1; last%verifyEvery != 0 {
		if err := verifyStreamObject(api, key(api, last), streamFixtureBody(last, size, seed)); err != nil {
			return err
		}
		picks++
	}
	if err := reader.Stop(); err != nil {
		return err
	}
	reader = nil
	emit(c, map[string]any{"scenario": "mkstream", "objects": objects, "store_id": want.StoreID, "store": DuStore(abs),
		"resumed_objects": resumed, "logical_bytes": int64(objects) * int64(size), "sampled_exact_reads": picks,
		"ingest_s": ingestSeconds, "object_bytes": size, "seed": seed, "binary_sha256": want.Binary})
	return nil
}

func cmdMkstream(args []string) {
	fs := flag.NewFlagSet("mkstream", flag.ExitOnError)
	c := addCommon(fs)
	dir := fs.String("dir", "", "new or cursor-owned fixture directory")
	objects := fs.Int("objects", 0, "final number of visible objects")
	size := fs.Int("object-bytes", 1024, "exact size per deterministic object, 16 to 16384 bytes")
	batch := fs.Int("batch", 1024, "maximum object count between durable fixture cursor updates")
	verifyEvery := fs.Int("verify-every", 4096, "read and hash every nth key in a fresh process")
	seed := fs.Uint64("seed", 910035, "fixture identity seed")
	fs.Parse(args)
	if *dir == "" || *objects < 1 || *size < 16 || *size > 16384 || *batch < 1 || *batch > 65536 ||
		*verifyEvery < 1 || c.conc < 1 || c.noVerify || fs.NArg() != 0 {
		die("mkstream requires a fixture directory, positive counts and bounded object size and batch")
	}
	if err := runStreamStore(c, *dir, *objects, *size, *batch, *verifyEvery, *seed); err != nil {
		die("mkstream: %v", err)
	}
}
