package namespace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestLookupBoundariesAndDigestRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	basis := sha256.Sum256([]byte("immutable fixture"))
	if err := Build(f, basis, func() (Row, error) {
		if n == 800 {
			return Row{}, io.EOF
		}
		k := fmt.Sprintf("same-prefix/%04d", n*2)
		n++
		return Row{k, sha256.Sum256([]byte(k)), uint64(n * 8)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bytes.NewReader(raw), int64(len(raw)), [32]byte{}); err == nil {
		t.Fatal("opened with a different source identity")
	}
	table, err := Open(bytes.NewReader(raw), int64(len(raw)), basis)
	if err != nil {
		t.Fatal(err)
	}
	for i := -1; i <= 1600; i++ {
		k := fmt.Sprintf("same-prefix/%04d", i)
		r, found, err := table.Lookup(k)
		want := i >= 0 && i < 1600 && i%2 == 0
		if err != nil || found != want || (found && (r.Digest != sha256.Sum256([]byte(k)) || r.Size != uint64((i/2+1)*8))) {
			t.Fatalf("%q: %+v, %t, %v", k, r, found, err)
		}
	}
	raw[table.blocks[1].Offset+31] ^= 1
	if _, _, err := table.Lookup("same-prefix/0512"); err == nil {
		t.Fatal("lookup accepted a corrupted block")
	}
	raw[len(raw)-1] ^= 1
	if _, err := Open(bytes.NewReader(raw), int64(len(raw)), basis); err == nil {
		t.Fatal("open accepted a corrupted index")
	}
}

type fixtureRow struct {
	Bucket, Key, SHA string
	Deleted          bool
	C                struct{ Size uint64 }
}

func parseRow(scanner *bufio.Scanner) (Row, bool, error) {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Row{}, false, err
		}
		return Row{}, false, io.EOF
	}
	var f fixtureRow
	if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
		return Row{}, false, err
	}
	r := Row{Key: f.Bucket + "/" + f.Key, Size: f.C.Size}
	if n, err := hex.Decode(r.Digest[:], []byte(f.SHA)); err != nil || n != len(r.Digest) {
		return Row{}, false, fmt.Errorf("invalid fixture digest for %q", r.Key)
	}
	return r, !f.Deleted, nil
}

func visible(scanner *bufio.Scanner) (Row, error) {
	for {
		r, present, err := parseRow(scanner)
		if present || err != nil {
			return r, err
		}
	}
}

func TestMillionObjectNamespace(t *testing.T) {
	manifest, out := os.Getenv("ZEROS3_P9_NAMESPACE_FIXTURE"), os.Getenv("ZEROS3_P9_NAMESPACE_OUTPUT")
	if manifest == "" || out == "" {
		t.Skip("set ZEROS3_P9_NAMESPACE_FIXTURE and ZEROS3_P9_NAMESPACE_OUTPUT")
	}
	f, err := os.Open(manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	var stamp [32]byte
	h.Sum(stamp[:0])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(out+".table", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	buildInput := bufio.NewScanner(f)
	start := time.Now()
	if err := Build(dst, stamp, func() (Row, error) { return visible(buildInput) }); err != nil {
		t.Fatal(err)
	}
	if err := dst.Sync(); err != nil {
		t.Fatal(err)
	}
	build := time.Since(start)
	info, err := dst.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start = time.Now()
	table, err := Open(dst, info.Size(), stamp)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(f)
	count, lookupCount := 0, 0
	checkError := error(nil)
	if err := table.Scan(func(row Row) bool {
		want, err := visible(scan)
		if err != nil || row != want {
			checkError = fmt.Errorf("mismatched live row at %d: %q, %v", count, row.Key, err)
			return false
		}
		count++
		return true
	}); err != nil || checkError != nil {
		t.Fatal(err, checkError)
	}
	if _, err := visible(scan); err != io.EOF {
		t.Fatal("checkpoint omitted visible rows", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	sampleInput := bufio.NewScanner(f)
	rng := rand.New(rand.NewPCG(4407, 116))
	var latency []int64
	for {
		row, want, err := parseRow(sampleInput)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if rng.IntN(256) != 0 {
			continue
		}
		start := time.Now()
		got, ok, err := table.Lookup(row.Key)
		latency = append(latency, time.Since(start).Nanoseconds())
		if err != nil || ok != want || (ok && got != row) {
			t.Fatalf("lookup for %s differed: %+v, %t, %v", row.Key, got, ok, err)
		}
		lookupCount++
	}
	if count == 0 || len(latency) == 0 {
		t.Fatal("empty scale evidence")
	}
	slices.Sort(latency)
	report := map[string]any{
		"manifest_sha256": fmt.Sprintf("%x", stamp), "visible_rows_exact": count,
		"random_visible_or_deleted_lookups_exact": lookupCount, "blocks": len(table.blocks),
		"table_bytes": info.Size(), "build_ms": build.Milliseconds(), "open_us": elapsed.Microseconds(),
		"open_allocated_bytes": after.TotalAlloc - before.TotalAlloc,
		"lookup_p50_us":        float64(latency[len(latency)/2]) / 1000,
		"lookup_p95_us":        float64(latency[len(latency)*95/100]) / 1000,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if err := os.WriteFile(out+".json", append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
