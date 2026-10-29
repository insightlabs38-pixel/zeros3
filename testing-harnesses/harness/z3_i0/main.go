// z3_i0 is the black-box validation of ZeroS3's optional structured
// representation layer (Z3-I0). It drives real zeros3 binaries over S3 and the
// CLI and inspects only documented store files:
//
//   - compat: a pre-I0 binary (ZEROS3_BASE_BIN, built from the commit before I0)
//     and the I0 binary run the same ordinary S3 workload; level 1..5 stores made
//     by the old binary open unchanged in every mode; the first representation
//     raises the store to format 6 and the old binary then refuses it cleanly;
//   - kinds: tar, SQLite, CAR, Merkle and tar->{SQLite,CAR,Merkle} objects in
//     shadow and prefer mode survive compact, tier moves, repack, gc, history
//     prune and restarts with exact GETs and `verify -deep`;
//   - crash: SIGKILL during representation-bearing PUTs leaves a valid store;
//   - large: a large structured object is analysed, stored and read with bounded
//     server memory;
//   - reads: CDC vs representation timings (whole, 4 KiB, 64 KiB, 1 MiB), storage
//     accounting, and restart cost with 0 / ~100 / ~1000 roots.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/i0 ZEROS3_BASE_BIN=/path/to/base \
//	  go run ./harness/z3_i0 [-phase all|compat|bundles|kinds|crash|large|reads] [-large-mib 256]
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha3"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
	bucket          = "z3"
)

var (
	failed          bool
	bin             string
	baseB           string
	codecFixtureDir string
	gitFixtureDir   string
)

func check(name string, ok bool, detail string, args ...any) {
	if ok {
		fmt.Printf("PASS: %s\n", name)
		return
	}
	failed = true
	fmt.Printf("FAIL: %s: %s\n", name, fmt.Sprintf(detail, args...))
}

func record(format string, args ...any) { fmt.Printf("RECORD "+format+"\n", args...) }

// ---- fixtures (self-contained; the harness links nothing from the product) -------------

func sqliteDB(pages, ps int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, pages*ps)
	r.Read(b)
	copy(b, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(b[16:], uint16(ps))
	b[18], b[19] = 1, 1
	for i := 0; i < pages; i++ {
		switch {
		case i == 0:
			b[100] = 0x0d
		case i%5 == 0:
			binary.BigEndian.PutUint32(b[i*ps:], uint32(i+2))
		default:
			b[i*ps] = 0x0d
		}
	}
	return b
}

func updatePages(db []byte, ps int, pages []int, seed int64) []byte {
	out := append([]byte(nil), db...)
	r := rand.New(rand.NewSource(seed))
	for _, p := range pages {
		pg := out[p*ps : (p+1)*ps]
		r.Read(pg[16:])
		pg[0] = 0x0d
	}
	return out
}

func uv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func carOf(blocks [][]byte) []byte {
	cid := func(blk []byte) []byte {
		d := sha256.Sum256(blk)
		return append([]byte{0x01, 0x55, 0x12, 0x20}, d[:]...)
	}
	root := cid(blocks[0])
	hdr := []byte{0xa2, 0x65, 'r', 'o', 'o', 't', 's', 0x81, 0xd8, 0x2a, 0x58, byte(len(root) + 1), 0x00}
	hdr = append(hdr, root...)
	hdr = append(hdr, 0x67, 'v', 'e', 'r', 's', 'i', 'o', 'n', 0x01)
	out := uv(nil, uint64(len(hdr)))
	out = append(out, hdr...)
	for _, b := range blocks {
		c := cid(b)
		out = uv(out, uint64(len(c)+len(b)))
		out = append(out, c...)
		out = append(out, b...)
	}
	return out
}

func blocks(n, size int, seed int64) [][]byte {
	r := rand.New(rand.NewSource(seed))
	bs := make([][]byte, n)
	for i := range bs {
		bs[i] = make([]byte, size/2+r.Intn(size))
		r.Read(bs[i])
	}
	return bs
}

type merkleBuilder struct {
	ids map[string][]byte
}

func (m *merkleBuilder) add(p []byte, h func([]byte) []byte) string {
	id := string(h(p))
	m.ids[id] = p
	return id
}

func (m *merkleBuilder) build(keys []uint64, shift uint, ver map[uint64]int, h func([]byte) []byte) string {
	if len(keys) <= 8 || shift == 0 {
		p := []byte{0x01, 0x00}
		p = uv(p, uint64(len(keys)))
		for _, k := range keys {
			v := []byte(fmt.Sprintf("value-%d-v%d-%s", k, ver[k], strings.Repeat("x", int(k%37))))
			p = binary.BigEndian.AppendUint64(p, k)
			p = uv(p, uint64(len(v)))
			p = append(p, v...)
		}
		return m.add(p, h)
	}
	shift -= 4
	var kids [16][]uint64
	for _, k := range keys {
		kids[(k>>shift)&0xf] = append(kids[(k>>shift)&0xf], k)
	}
	var bm uint16
	var body []byte
	for i := range kids {
		if len(kids[i]) == 0 {
			continue
		}
		bm |= 1 << uint(i)
		id := m.build(kids[i], shift, ver, h)
		body = uv(body, uint64(len(id)))
		body = append(body, id...)
	}
	p := binary.BigEndian.AppendUint16([]byte{0x02, 0x00}, bm)
	return m.add(append(p, body...), h)
}

// merkleSnapshot is the exact snapshot archive of a 16-ary trie (SHA3-256 native ids).
func merkleSnapshot(keys []uint64, ver map[uint64]int) []byte {
	h := func(b []byte) []byte { s := sha3.Sum256(b); return s[:] }
	m := &merkleBuilder{ids: map[string][]byte{}}
	ks := append([]uint64(nil), keys...)
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	root := m.build(ks, 64, ver, h)
	out := append([]byte("Z3SNAP1\x00"), uv(nil, uint64(len(root)))...)
	out = append(out, root...)
	seen := map[string]bool{}
	stack := []string{root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p := m.ids[id]
		out = uv(out, uint64(len(p)))
		out = append(out, p...)
		if p[0] == 0x02 {
			bm := binary.BigEndian.Uint16(p[2:])
			q := 4
			kids := make([]string, 16)
			for i := 0; i < 16; i++ {
				if bm&(1<<uint(i)) == 0 {
					continue
				}
				l, n := binary.Uvarint(p[q:])
				kids[i] = string(p[q+n : q+n+int(l)])
				q += n + int(l)
			}
			for i := 15; i >= 0; i-- {
				stack = append(stack, kids[i])
			}
		}
	}
	return out
}

func keysOf(n int, seed int64) []uint64 {
	r := rand.New(rand.NewSource(seed))
	seen := map[uint64]bool{}
	var ks []uint64
	for len(ks) < n {
		if k := r.Uint64(); !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	return ks
}

func tarOf(entries map[string][]byte, order []string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range order {
		d := entries[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(d)), Format: tar.FormatUSTAR}); err != nil {
			panic(err)
		}
		tw.Write(d)
	}
	tw.Close()
	return buf.Bytes()
}

type fixture struct {
	name string
	data []byte
}

func fixtures() []fixture {
	db := sqliteDB(1500, 4096, 1)
	bl := blocks(1200, 3000, 2)
	car := carOf(bl)
	mk := merkleSnapshot(keysOf(6000, 3), nil)
	rnd := make([]byte, 700<<10)
	rand.New(rand.NewSource(4)).Read(rnd)
	fx := []fixture{
		{"sqlite.db", db},
		{"sqlite-update.db", updatePages(db, 4096, []int{7, 99, 1200}, 5)},
		{"archive.car", car},
		{"snapshot.merkle", mk},
		{"plain.tar", tarOf(map[string][]byte{"a": rnd, "b": []byte("hello"), "c": rnd[:5000]}, []string{"a", "b", "c"})},
		{"tar-sqlite.tar", tarOf(map[string][]byte{"app.db": db, "README": []byte("readme")}, []string{"README", "app.db"})},
		{"tar-car.tar", tarOf(map[string][]byte{"data.car": car, "meta": []byte("m")}, []string{"meta", "data.car"})},
		{"tar-merkle.tar", tarOf(map[string][]byte{"state.snap": mk, "x": []byte("x")}, []string{"x", "state.snap"})},
		{"random.bin", rnd},
		{"fake-sqlite.db", append([]byte("SQLite format 3\x00"), rnd[:30000]...)},
	}
	if codecFixtureDir != "" {
		plain, err := os.ReadFile(filepath.Join(codecFixtureDir, "nested.tar"))
		if err != nil {
			panic(err)
		}
		zst, err := os.ReadFile(filepath.Join(codecFixtureDir, "nested.zst"))
		if err != nil {
			panic(err)
		}
		var gz bytes.Buffer
		w, err := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
		if err != nil {
			panic(err)
		}
		if _, err = w.Write(plain); err != nil {
			panic(err)
		}
		if err = w.Close(); err != nil {
			panic(err)
		}
		fx = append(fx, fixture{"nested.tar.gz", gz.Bytes()}, fixture{"nested.tar.zst", zst})
	}
	if gitFixtureDir != "" {
		for _, name := range []string{"sha1-ofs.pack", "sha256-ref.pack", "sha1-ofs.idx"} {
			data, err := os.ReadFile(filepath.Join(gitFixtureDir, name))
			if err != nil {
				panic(err)
			}
			fx = append(fx, fixture{name, data})
		}
	}
	return fx

}

// ---- process and client plumbing ------------------------------------------------------------

// cleanEnv drops ambient AWS credentials: the servers must use their built-in test pair.
func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "AWS_") {
			continue
		}
		env = append(env, e)
	}
	return env
}

func startServer(binary, storeDir string, extra ...string) (*exec.Cmd, string, time.Duration) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	args := append([]string{"serve", "-store", storeDir, "-addr", addr}, extra...)
	srv := exec.Command(binary, args...)
	srv.Env = cleanEnv()
	srv.Stdout, srv.Stderr = io.Discard, os.Stderr
	began := time.Now()
	if err := srv.Start(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return srv, addr, time.Since(began)
		} else if time.Now().After(deadline) {
			fmt.Println("FAIL: zeros3 did not start")
			os.Exit(2)
		}
	}
}

func stop(srv *exec.Cmd) int64 {
	_ = srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()
	return maxRSS(srv)
}

func maxRSS(c *exec.Cmd) int64 {
	if c.ProcessState == nil {
		return -1
	}
	if ru, ok := c.ProcessState.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss << 10
	}
	return -1
}

func newClient(addr string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")))
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + addr)
		o.UsePathStyle = true
	})
}

func cli(binary string, args ...string) (string, string, error) {
	var so, se bytes.Buffer
	cmd := exec.Command(binary, args...)
	cmd.Env = cleanEnv()
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

func mustCLI(name string, args ...string) string {
	so, se, err := cli(bin, args...)
	check(name, err == nil, "%v\n%s", err, se)
	return so
}

type stats map[string]any

func statsOf(binary, dir string) stats {
	out, _, err := cli(binary, "stats", "-json", "-store", dir)
	var st stats
	if err != nil || json.Unmarshal([]byte(out), &st) != nil {
		fmt.Println("FAIL: stats -json:", err)
		os.Exit(1)
	}
	return st
}

func num(st stats, k string) int64 {
	f, _ := st[k].(float64)
	return int64(f)
}

func formatLevel(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	if err != nil {
		return -1
	}
	var f struct {
		V int `json:"store_format_version"`
	}
	json.Unmarshal(b, &f)
	return f.V
}

func put(c *s3.Client, key string, data []byte) string {
	out, err := c.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data)))})
	if err != nil {
		check("PutObject "+key, false, "%v", err)
		return ""
	}
	return aws.ToString(out.ETag)
}

func get(c *s3.Client, key string, rng string) ([]byte, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := c.GetObject(context.Background(), in)
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func ranges(size int) []string {
	clamp := func(a, n int) string { return fmt.Sprintf("bytes=%d-%d", a, min(a+n-1, size-1)) }
	rs := []string{"bytes=0-99", fmt.Sprintf("bytes=%d-", max(0, size-700)), "bytes=-333"}
	if size > 20000 {
		rs = append(rs, "bytes=4096-12287", clamp(size/3, 65536))
	}
	if size > 2<<20 {
		rs = append(rs, clamp(size/5, 1<<20))
	}
	return rs
}

func rangeBytes(data []byte, spec string) []byte {
	var a, b int
	spec = strings.TrimPrefix(spec, "bytes=")
	switch {
	case strings.HasPrefix(spec, "-"):
		fmt.Sscanf(spec, "-%d", &a)
		return data[len(data)-a:]
	case strings.HasSuffix(spec, "-"):
		fmt.Sscanf(spec, "%d-", &a)
		return data[a:]
	}
	fmt.Sscanf(spec, "%d-%d", &a, &b)
	return data[a : b+1]
}

func readAllCheck(label string, c *s3.Client, fx []fixture) {
	ok := true
	var bad string
	for _, f := range fx {
		got, err := get(c, f.name, "")
		if err != nil || !bytes.Equal(got, f.data) {
			ok, bad = false, f.name
			break
		}
		if len(f.data) < 700 {
			badRange := fmt.Sprintf("bytes=%d-", len(f.data)-700)
			if got, err := get(c, f.name, badRange); err != nil || !bytes.Equal(got, f.data) {
				ok, bad = false, f.name+" malformed range did not serve full object "+badRange
				break
			}
		}
		for _, r := range ranges(len(f.data)) {
			got, err := get(c, f.name, r)
			if err != nil || !bytes.Equal(got, rangeBytes(f.data, r)) {
				ok, bad = false, f.name+" "+r
				break
			}
		}
	}
	check(label, ok, "mismatch in %s", bad)
}

// ---- phases --------------------------------------------------------------------------------------

func phaseCompat() {
	fmt.Println("== compat: pre-I0 binary vs I0 binary")
	fx := fixtures()
	mkStore := func(level int) string {
		dir, _ := os.MkdirTemp("", "z3i0-base-")
		srv, addr, _ := startServer(baseB, dir)
		c := newClient(addr)
		c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		for _, f := range fx {
			put(c, f.name, f.data)
		}
		stop(srv)
		if level >= 2 {
			out, se, err := cli(baseB, "compact", "-store", dir, "-pack-size-mib", "4", "-compression", "off")
			check(fmt.Sprintf("base compact (level %d)", level), err == nil, "%v %s %s", err, out, se)
		}
		if level >= 3 {
			// compressible object + compaction raises to level 3
			srv, addr, _ = startServer(baseB, dir)
			c = newClient(addr)
			put(c, "text.txt", bytes.Repeat([]byte("compressible object payload "), 60000))
			stop(srv)
			cli(baseB, "compact", "-store", dir, "-pack-size-mib", "4", "-compression", "auto")
		}
		if level >= 5 {
			cli(baseB, "tier", "move", "-store", dir, "-from", "hot", "-to", "warm", "-all", "-apply")
		}
		return dir
	}
	for _, level := range []int{1, 2, 3, 5} {
		dir := mkStore(level)
		got := formatLevel(dir)
		check(fmt.Sprintf("base store created at level %d", level), got == level, "level %d", got)
		baseStats := statsOf(baseB, dir)
		for _, mode := range []string{"off", "shadow", "prefer"} {
			srv, addr, _ := startServer(bin, dir, "-representations", mode)
			c := newClient(addr)
			readAllCheck(fmt.Sprintf("level %d opened by I0 (%s): GET and ranges exact", level, mode), c, fx)
			stop(srv)
			check(fmt.Sprintf("level %d: opening in %s mode does not move the format level", level, mode), formatLevel(dir) == level, "level %d", formatLevel(dir))
		}
		i0Stats := statsOf(bin, dir)
		same := true
		var diff string
		for k, v := range baseStats {
			if fmt.Sprint(v) != fmt.Sprint(i0Stats[k]) {
				same, diff = false, k
			}
		}
		check(fmt.Sprintf("level %d: every pre-existing stats field is unchanged (I0 adds fields only)", level), same, "field %s", diff)
		out, _, err := cli(bin, "verify", "-deep", "-store", dir)
		check(fmt.Sprintf("level %d: I0 verify -deep", level), err == nil, "%s", out)
		os.RemoveAll(dir)
	}

	// the first root raises the level; the old binary refuses and changes nothing
	dir := mkStore(1)
	defer os.RemoveAll(dir)
	srv, addr, _ := startServer(bin, dir, "-representations", "prefer")
	c := newClient(addr)
	put(c, "new.db", sqliteDB(300, 4096, 9))
	stop(srv)
	check("the first valid representation root raises the store to format 6", formatLevel(dir) == 6, "level %d", formatLevel(dir))
	snapshot := dirDigest(dir)
	for _, args := range [][]string{{"verify", "-store", dir}, {"stats", "-store", dir}, {"gc", "-store", dir, "-apply"}, {"compact", "-store", dir}} {
		_, se, err := cli(baseB, args...)
		check("pre-I0 binary refuses the format-6 store: "+args[0], err != nil && strings.Contains(se, "unsupported store format version 6"), "err=%v stderr=%q", err, se)
	}
	bs := exec.Command(baseB, "serve", "-store", dir, "-addr", "127.0.0.1:0")
	bs.Env = cleanEnv()
	var bse bytes.Buffer
	bs.Stderr = &bse
	err := bs.Run()
	check("pre-I0 serve refuses the format-6 store", err != nil && strings.Contains(bse.String(), "unsupported store format version 6"), "%v %s", err, bse.String())
	check("the refusal changed nothing on disk", dirDigest(dir) == snapshot, "store changed")
	// turning creation off does not downgrade, and the new binary still understands the roots
	srv, addr, _ = startServer(bin, dir, "-representations", "off")
	c = newClient(addr)
	got, err := get(c, "new.db", "")
	check("level-6 store in off mode still serves the object", err == nil && bytes.Equal(got, sqliteDB(300, 4096, 9)), "%v", err)
	stop(srv)
	check("off mode did not downgrade", formatLevel(dir) == 6, "level %d", formatLevel(dir))
	out, _, err := cli(bin, "gc", "-store", dir, "-apply", "-json")
	check("gc -apply on a level-6 store in off mode keeps the representation", err == nil, "%s", out)
	out, _, err = cli(bin, "verify", "-deep", "-store", dir, "-json")
	var vr struct {
		Roots    int `json:"representation_roots"`
		Verified int `json:"representations_verified"`
	}
	json.Unmarshal([]byte(out), &vr)
	check("the representation survived gc and is still exact", err == nil && vr.Roots == 1 && vr.Verified == 1, "%s", out)

	// differential: the same ordinary workload against the base binary and the I0 binary in off mode
	fmt.Println("-- differential workload")
	run := func(binary string, extra ...string) ([]string, string) {
		d, _ := os.MkdirTemp("", "z3i0-diff-")
		defer os.RemoveAll(d)
		srv, addr, _ := startServer(binary, d, extra...)
		c := newClient(addr)
		ctx := context.Background()
		var tr []string
		c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		for _, f := range fx {
			tr = append(tr, f.name+" put "+put(c, f.name, f.data))
		}
		for _, f := range fx {
			h, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(f.name)})
			if err != nil {
				tr = append(tr, f.name+" head error")
				continue
			}
			tr = append(tr, fmt.Sprintf("%s head %s %d %s", f.name, aws.ToString(h.ETag), aws.ToInt64(h.ContentLength), aws.ToString(h.ContentType)))
			got, _ := get(c, f.name, "")
			tr = append(tr, fmt.Sprintf("%s get %x", f.name, sha256.Sum256(got)))
			for _, r := range ranges(len(f.data)) {
				got, _ := get(c, f.name, r)
				tr = append(tr, fmt.Sprintf("%s %s %x", f.name, r, sha256.Sum256(got)))
			}
		}
		_, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("copy"), CopySource: aws.String(bucket + "/sqlite.db")})
		tr = append(tr, fmt.Sprintf("copy %v", err))
		put(c, "sqlite.db", fixtures()[1].data)
		lo, _ := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for _, o := range lo.Contents {
			tr = append(tr, fmt.Sprintf("list %s %d %s", aws.ToString(o.Key), aws.ToInt64(o.Size), aws.ToString(o.ETag)))
		}
		_, err = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("random.bin")})
		tr = append(tr, fmt.Sprintf("delete %v", err))
		stop(srv)
		// the set of chunk files is a pure function of the content
		var names []string
		filepath.WalkDir(filepath.Join(d, "chunks"), func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				names = append(names, e.Name())
			}
			return nil
		})
		sort.Strings(names)
		return tr, fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(names, ","))))
	}
	trBase, chBase := run(baseB)
	trOff, chOff := run(bin, "-representations", "off")
	check("S3 transcript: base vs I0 (off) identical", fmt.Sprint(trBase) == fmt.Sprint(trOff), "transcripts differ")
	check("chunk set: base vs I0 (off) identical", chBase == chOff, "chunk sets differ")
	for _, m := range []string{"shadow", "prefer"} {
		tr, _ := run(bin, "-representations", m)
		check("S3 transcript: base vs I0 ("+m+") identical", fmt.Sprint(trBase) == fmt.Sprint(tr), "transcripts differ")
	}
}

// phaseBundles shows that portable bundles and native replication stay CDC-authoritative:
// representation state never travels, a pre-I0 binary can verify and import what an I0
// store exports, and destinations are not raised to format 6.
func phaseBundles() {
	fmt.Println("== bundles and native replication are unchanged")
	fx := fixtures()
	creds := []string{"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region}
	build := func(binary string, extra ...string) (dir, snap string) {
		dir, _ = os.MkdirTemp("", "z3i0-bnd-")
		srv, addr, _ := startServer(binary, dir, extra...)
		c := newClient(addr)
		c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		for _, f := range fx {
			put(c, f.name, f.data)
		}
		args := append([]string{"snapshot", "create", "-json", "-endpoint", "http://" + addr}, creds...)
		out, se, err := cli(binary, append(args, "s3://"+bucket+"/")...)
		check("snapshot create ("+filepath.Base(binary)+")", err == nil, "%v %s", err, se)
		var sm struct {
			ID string `json:"snapshot_id"`
		}
		json.Unmarshal([]byte(out), &sm)
		stop(srv)
		return dir, sm.ID
	}
	exportOf := func(binary, dir, snap, name string) (string, map[string]any) {
		out := filepath.Join(os.TempDir(), name)
		os.Remove(out)
		so, se, err := cli(binary, "bundle", "export", "-store", dir, "-snapshot", snap, "-out", out, "-json")
		check("bundle export from "+name, err == nil, "%v %s", err, se)
		var m map[string]any
		json.Unmarshal([]byte(so), &m)
		return out, m
	}
	baseDir, baseSnap := build(baseB)
	repDir, repSnap := build(bin, "-representations", "prefer")
	defer os.RemoveAll(baseDir)
	defer os.RemoveAll(repDir)
	check("prefer-mode source store holds representation roots", formatLevel(repDir) == 6, "level %d", formatLevel(repDir))
	bBase, mBase := exportOf(baseB, baseDir, baseSnap, "z3i0-base.zs3b")
	bRep, mRep := exportOf(bin, repDir, repSnap, "z3i0-rep.zs3b")
	defer os.Remove(bBase)
	defer os.Remove(bRep)
	same := true
	var diff string
	for k, v := range mBase {
		if _, isNum := v.(float64); isNum && !strings.HasSuffix(k, "_bytes") && fmt.Sprint(v) != fmt.Sprint(mRep[k]) {
			same, diff = false, k
		}
	}
	check("bundle export counts from a representation store equal the base store's", same && len(mBase) > 0, "field %s: base=%v rep=%v", diff, mBase[diff], mRep[diff])
	for _, b := range []string{bBase, bRep} {
		_, se, err := cli(baseB, "bundle", "inspect", "-verify", "-in", b)
		check("pre-I0 binary verifies "+filepath.Base(b), err == nil, "%s", se)
	}
	for _, imp := range []struct{ name, binary string }{{"I0", bin}, {"pre-I0", baseB}} {
		dst, _ := os.MkdirTemp("", "z3i0-imp-")
		_, se, err := cli(imp.binary, "bundle", "import", "-store", dst, "-in", bRep)
		check(imp.name+" imports the representation-store bundle", err == nil, "%s", se)
		_, statErr := os.Stat(filepath.Join(dst, "representations"))
		check(imp.name+" import creates no representation state", os.IsNotExist(statErr), "representations/ exists")
		check(imp.name+" import leaves the destination below format 6", formatLevel(dst) < 6, "level %d", formatLevel(dst))
		_, se, err = cli(imp.binary, "verify", "-deep", "-store", dst)
		check(imp.name+" verify -deep after import", err == nil, "%s", se)
		srv, addr, _ := startServer(bin, dst, "-representations", "prefer")
		rc := newClient(addr)
		rc.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		_, se, err = cli(bin, append([]string{"snapshot", "restore", "-endpoint", "http://" + addr}, append(creds, repSnap, "s3://"+bucket+"/")...)...)
		check(imp.name+" imported snapshot restores through an I0 server", err == nil, "%s", se)
		readAllCheck(imp.name+" imported store served by I0 (prefer): exact", rc, fx)
		stop(srv)
		os.RemoveAll(dst)
	}
	// native replication from a prefer-mode server into a pre-I0 server
	rsrv, raddr, _ := startServer(bin, repDir, "-representations", "prefer")
	ddir, _ := os.MkdirTemp("", "z3i0-repl-")
	defer os.RemoveAll(ddir)
	dsrv, daddr, _ := startServer(baseB, ddir)
	dc := newClient(daddr)
	dc.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	_, se, err := cli(bin, "replicate", "-recursive", "-from", "http://"+raddr, "-to", "http://"+daddr,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey, "-region", region,
		"s3://"+bucket+"/", "s3://"+bucket+"/")
	check("replicate prefer-mode I0 server -> pre-I0 server", err == nil, "%v %s", err, se)
	readAllCheck("replicated destination (pre-I0 binary): GET and ranges exact", dc, fx)
	stop(dsrv)
	stop(rsrv)
	check("replication left the pre-I0 destination at its own format", formatLevel(ddir) < 6, "level %d", formatLevel(ddir))
	_, se, err = cli(baseB, "verify", "-deep", "-store", ddir)
	check("pre-I0 verify -deep of the replicated destination", err == nil, "%s", se)
}

func dirDigest(dir string) string {
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || strings.HasSuffix(p, "LOCK") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s|%v\n", rel, d.IsDir())
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func verifyDeep(label, dir string, wantRoots int) {
	out, se, err := cli(bin, "verify", "-deep", "-store", dir, "-json")
	var vr struct {
		Roots    int `json:"representation_roots"`
		Verified int `json:"representations_verified"`
		Missing  int `json:"missing"`
		Corrupt  int `json:"corrupt"`
		Invalid  int `json:"invalid"`
	}
	json.Unmarshal([]byte(out), &vr)
	ok := err == nil && vr.Missing+vr.Corrupt+vr.Invalid == 0
	if wantRoots >= 0 {
		ok = ok && vr.Roots == wantRoots && vr.Verified == wantRoots
	}
	check(label, ok, "%v roots=%d verified=%d want=%d %s %s", err, vr.Roots, vr.Verified, wantRoots, out, se)
}

func phaseKinds() {
	fmt.Println("== kinds: structural representations through maintenance and restarts")
	fx := fixtures()
	wantRoots := len(fx) - 2 // everything but random.bin and fake-sqlite.db
	for _, mode := range []string{"shadow", "prefer"} {
		dir, _ := os.MkdirTemp("", "z3i0-kinds-")
		defer os.RemoveAll(dir)
		srv, addr, _ := startServer(bin, dir, "-representations", mode)
		c := newClient(addr)
		c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		for _, f := range fx {
			put(c, f.name, f.data)
		}
		readAllCheck(mode+": initial GETs and ranges exact", c, fx)
		stop(srv)
		st := statsOf(bin, dir)
		check(mode+": roots only for structural objects", num(st, "representation_root_count") == int64(wantRoots), "roots %d", num(st, "representation_root_count"))
		check(mode+": store format is 6", formatLevel(dir) == 6, "level %d", formatLevel(dir))
		verifyDeep(mode+": verify -deep (loose)", dir, wantRoots)
		record("kinds mode=%s roots=%d objects=%d components=%d component_bytes=%d chunks=%d chunk_bytes=%d root_bytes=%d loose_files=%d",
			mode, num(st, "representation_root_count"), num(st, "representation_object_count"), num(st, "representation_component_count"),
			num(st, "representation_component_logical_bytes"), num(st, "representation_reachable_chunk_count"), num(st, "representation_reachable_chunk_bytes"),
			num(st, "representation_metadata_file_bytes"), num(st, "loose_chunk_count"))

		mustCLI(mode+": compact", "compact", "-store", dir, "-pack-size-mib", "2")
		verifyDeep(mode+": verify -deep (compacted)", dir, wantRoots)
		st2 := statsOf(bin, dir)
		record("kinds mode=%s after_compact loose=%d packs=%d packed_chunks=%d pack_bytes=%d", mode, num(st2, "loose_chunk_count"), num(st2, "pack_count"), num(st2, "packed_chunk_count"), num(st2, "pack_file_bytes"))
		check(mode+": compaction packed the representation components", num(st2, "loose_chunk_count") == 0, "loose %d", num(st2, "loose_chunk_count"))

		// spread the packs over the tiers
		out := mustCLI(mode+": tier status", "tier", "status", "-store", dir, "-json")
		var ts struct {
			Tiers []struct {
				Tier  string `json:"tier"`
				Packs []struct {
					ID string `json:"id"`
				} `json:"packs"`
			} `json:"tiers"`
		}
		_ = json.Unmarshal([]byte(out), &ts)
		var ids []string
		for _, t := range ts.Tiers {
			if t.Tier == "hot" {
				for _, p := range t.Packs {
					ids = append(ids, p.ID)
				}
			}
		}
		for i, id := range ids {
			to := []string{"warm", "cold", "hot"}[i%3]
			if to != "hot" {
				cli(bin, "tier", "move", "-store", dir, "-from", "hot", "-to", to, "-pack", id, "-apply")
			}
		}
		verifyDeep(mode+": verify -deep (tiers spread)", dir, wantRoots)
		srv, addr, _ = startServer(bin, dir, "-representations", mode)
		c = newClient(addr)
		readAllCheck(mode+": GETs exact after restart on compacted, tiered packs", c, fx)
		// history: overwrite, prune the old version, gc
		put(c, "sqlite.db", fx[1].data)
		stop(srv)
		mustCLI(mode+": versions prune", "versions", "prune", "-store", dir, "-bucket", bucket, "-key", "sqlite.db", "-keep-last", "0", "-apply")
		out, _, err := cli(bin, "gc", "-store", dir, "-json")
		var gcr struct {
			Orphans int `json:"representation_orphan_roots"`
		}
		json.Unmarshal([]byte(out), &gcr)
		check(mode+": gc dry-run reports the pruned version's orphan root", err == nil && gcr.Orphans == 1, "%s", out)
		mustCLI(mode+": gc -apply", "gc", "-store", dir, "-apply")
		mustCLI(mode+": repack", "repack", "-store", dir, "-apply")
		verifyDeep(mode+": verify -deep after gc and repack", dir, wantRoots)
		srv, addr, _ = startServer(bin, dir, "-representations", mode)
		c = newClient(addr)
		fx2 := append([]fixture(nil), fx...)
		fx2[0] = fixture{"sqlite.db", fx[1].data}
		readAllCheck(mode+": GETs exact after history prune, gc, repack and restart", c, fx2)
		stop(srv)
	}
}

func phaseCrash(rounds int) {
	fmt.Println("== crash: SIGKILL during representation-bearing PUTs")
	db := sqliteDB(3000, 4096, 21)
	tarB := tarOf(map[string][]byte{"app.db": db, "x": []byte("x")}, []string{"x", "app.db"})
	data := [][]byte{db, tarB, carOf(blocks(2500, 3000, 22)), merkleSnapshot(keysOf(10000, 23), nil)}
	if codecFixtureDir != "" || gitFixtureDir != "" {
		fx := fixtures()
		data = append([][]byte{fx[len(fx)-2].data, fx[len(fx)-1].data}, data...)
	}
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < rounds; round++ {
		dir, _ := os.MkdirTemp("", "z3i0-crash-")
		srv, addr, _ := startServer(bin, dir, "-representations", "prefer")
		c := newClient(addr)
		c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		put(c, "committed", data[0])
		acked := map[string][]byte{"committed": data[0]}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 40; i++ {
				k := fmt.Sprintf("k%02d", i)
				d := data[i%len(data)]
				_, err := c.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(k), Body: bytes.NewReader(d), ContentLength: aws.Int64(int64(len(d)))})
				if err != nil {
					return
				}
				acked[k] = d
			}
		}()
		time.Sleep(time.Duration(50+rng.Intn(600)) * time.Millisecond)
		_ = srv.Process.Kill()
		_ = srv.Wait()
		<-done
		out, se, err := cli(bin, "verify", "-store", dir, "-json")
		check(fmt.Sprintf("crash round %d: verify after SIGKILL", round), err == nil, "%v %s %s", err, out, se)
		srv, addr, _ = startServer(bin, dir, "-representations", "prefer")
		c = newClient(addr)
		ok := true
		for k, d := range acked {
			got, err := get(c, k, "")
			ok = ok && err == nil && bytes.Equal(got, d)
		}
		check(fmt.Sprintf("crash round %d: every acknowledged object is exact (%d)", round, len(acked)), ok, "mismatch")
		stop(srv)
		verifyDeep(fmt.Sprintf("crash round %d: verify -deep", round), dir, -1)
		o1, _, err := cli(bin, "gc", "-store", dir, "-apply", "-json")
		check(fmt.Sprintf("crash round %d: gc -apply", round), err == nil, "%s", o1)
		verifyDeep(fmt.Sprintf("crash round %d: verify -deep after gc", round), dir, -1)
		srv, addr, _ = startServer(bin, dir, "-representations", "prefer")
		c = newClient(addr)
		ok = true
		for k, d := range acked {
			got, err := get(c, k, "")
			ok = ok && err == nil && bytes.Equal(got, d)
		}
		check(fmt.Sprintf("crash round %d: acknowledged objects exact after gc", round), ok, "mismatch")
		stop(srv)
		os.RemoveAll(dir)
	}
}

// streamDB is a deterministic SQLite-shaped stream that never holds the object.
type streamDB struct {
	pages, ps int
	seed      int64
	pos       int64
}

func (s *streamDB) page(i int) []byte {
	r := rand.New(rand.NewSource(s.seed + int64(i)*7919))
	b := make([]byte, s.ps)
	r.Read(b)
	switch {
	case i == 0:
		copy(b, "SQLite format 3\x00")
		binary.BigEndian.PutUint16(b[16:], uint16(s.ps))
		b[18], b[19], b[100] = 1, 1, 0x0d
	case i%5 == 0:
		binary.BigEndian.PutUint32(b, uint32(i+2))
	default:
		b[0] = 0x0d
	}
	return b
}

func (s *streamDB) size() int64 { return int64(s.pages) * int64(s.ps) }

func (s *streamDB) Read(p []byte) (int, error) {
	if s.pos >= s.size() {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && s.pos < s.size() {
		pg := int(s.pos / int64(s.ps))
		b := s.page(pg)[s.pos%int64(s.ps):]
		c := copy(p[n:], b)
		n += c
		s.pos += int64(c)
	}
	return n, nil
}

func (s *streamDB) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = s.size() + off
	}
	return s.pos, nil
}

func (s *streamDB) window(off, n int64) []byte {
	out := make([]byte, 0, n)
	for p := off; p < off+n; {
		pg := int(p / int64(s.ps))
		b := s.page(pg)[p%int64(s.ps):]
		take := min(int64(len(b)), off+n-p)
		out = append(out, b[:take]...)
		p += take
	}
	return out
}

func phaseLarge(mib int) {
	fmt.Printf("== large: %d MiB SQLite-shaped object, bounded memory\n", mib)
	ps := 4096
	db := &streamDB{pages: mib << 20 / ps, ps: ps, seed: 77}
	var peaks = map[string]int64{}
	for _, mode := range []string{"off", "prefer"} {
		dir, _ := os.MkdirTemp("", "z3i0-large-")
		srv, addr, _ := startServer(bin, dir, "-representations", mode)
		c := newClient(addr)
		ctx := context.Background()
		c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		t0 := time.Now()
		_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.db"), Body: &streamDB{pages: db.pages, ps: ps, seed: db.seed}, ContentLength: aws.Int64(db.size())})
		check(fmt.Sprintf("large %s: PUT", mode), err == nil, "%v", err)
		putDur := time.Since(t0)
		t0 = time.Now()
		out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.db")})
		h := sha256.New()
		var n int64
		if err == nil {
			n, _ = io.Copy(h, out.Body)
			out.Body.Close()
		}
		getDur := time.Since(t0)
		want := sha256.New()
		io.Copy(want, &streamDB{pages: db.pages, ps: ps, seed: db.seed})
		check(fmt.Sprintf("large %s: GET is byte-exact", mode), err == nil && n == db.size() && bytes.Equal(h.Sum(nil), want.Sum(nil)), "%v n=%d", err, n)
		got, err := get(c, "big.db", "bytes=1048576-1052671")
		check(fmt.Sprintf("large %s: 4 KiB range exact", mode), err == nil && bytes.Equal(got, db.window(1<<20, 4096)), "%v", err)
		rss := stop(srv)
		peaks[mode] = rss
		record("large mode=%s object_mib=%d put_s=%.1f get_s=%.1f server_peak_rss_mib=%d", mode, mib, putDur.Seconds(), getDur.Seconds(), rss>>20)
		if mode == "prefer" {
			st := statsOf(bin, dir)
			record("large stats roots=%d components=%d chunks=%d chunk_bytes=%d", num(st, "representation_root_count"), num(st, "representation_component_count"), num(st, "representation_reachable_chunk_count"), num(st, "representation_reachable_chunk_bytes"))
			check("large: a root was published", num(st, "representation_root_count") == 1, "roots %d", num(st, "representation_root_count"))
			// memory is O(component metadata) (about 0.5-0.8 KiB per 4 KiB page, dominated by the
			// store-wide hex-keyed reachability maps), never O(object bytes)
			bound := int64(64<<20) + int64(db.pages)*1024
			check("large: analysis + serving peak is bounded (peak RSS <= 64 MiB + 1 KiB per component, < object/2)", rss <= bound && rss < db.size()/2, "peak %d MiB", rss>>20)
			// an offline deep verify reconstructs the whole object: measure its own peak
			cmd := exec.Command(bin, "verify", "-deep", "-store", dir)
			cmd.Env = cleanEnv()
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			t0 := time.Now()
			err := cmd.Run()
			vrss := maxRSS(cmd)
			check("large: verify -deep ok", err == nil, "%v", err)
			record("large verify_deep_s=%.1f peak_rss_mib=%d", time.Since(t0).Seconds(), vrss>>20)
			check("large: verify -deep memory is bounded (peak RSS <= 64 MiB + 1 KiB per component, < object/2)", vrss <= bound && vrss < db.size()/2, "peak %d MiB", vrss>>20)
		}
		os.RemoveAll(dir)
	}
	if peaks["off"] > 0 && peaks["prefer"] > 0 {
		record("large rss_delta_mib=%d", (peaks["prefer"]-peaks["off"])>>20)
	}
}

func timeIt(n int, fn func()) time.Duration {
	var ds []time.Duration
	for i := 0; i < n; i++ {
		t := time.Now()
		fn()
		ds = append(ds, time.Since(t))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

func phaseReads() {
	fmt.Println("== reads: CDC vs representation, storage accounting, restart scaling")
	db := sqliteDB(6000, 4096, 31)
	revs := [][]byte{db}
	for r := 0; r < 4; r++ {
		var pages []int
		for i := 0; i < 40; i++ {
			pages = append(pages, (r*1543+i*149+7)%6000)
		}
		revs = append(revs, updatePages(revs[len(revs)-1], 4096, pages, int64(40+r)))
	}
	car := carOf(blocks(2000, 3000, 32))
	dir, _ := os.MkdirTemp("", "z3i0-reads-")
	defer os.RemoveAll(dir)
	srv, addr, _ := startServer(bin, dir, "-representations", "shadow")
	c := newClient(addr)
	c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	for i, r := range revs {
		put(c, fmt.Sprintf("db%d", i), r)
	}
	put(c, "archive.car", car)
	stop(srv)
	cdcOnly, _ := os.MkdirTemp("", "z3i0-cdc-")
	defer os.RemoveAll(cdcOnly)
	srv, addr, _ = startServer(bin, cdcOnly, "-representations", "off")
	c = newClient(addr)
	c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	for i, r := range revs {
		put(c, fmt.Sprintf("db%d", i), r)
	}
	put(c, "archive.car", car)
	stop(srv)
	a, b := statsOf(bin, cdcOnly), statsOf(bin, dir)
	record("storage loose cdc_only=%d cdc_plus_rep=%d rep_marginal=%d root_bytes=%d", num(a, "chunk_store_file_bytes"), num(b, "chunk_store_file_bytes"), num(b, "chunk_store_file_bytes")-num(a, "chunk_store_file_bytes"), num(b, "representation_metadata_file_bytes"))
	for _, d := range []string{cdcOnly, dir} {
		mustCLI("compact "+filepath.Base(d), "compact", "-store", d, "-pack-size-mib", "4")
	}
	a, b = statsOf(bin, cdcOnly), statsOf(bin, dir)
	record("storage compacted cdc_only=%d cdc_plus_rep=%d rep_marginal=%d", num(a, "chunk_store_file_bytes"), num(b, "chunk_store_file_bytes"), num(b, "chunk_store_file_bytes")-num(a, "chunk_store_file_bytes"))

	lastDB := len(revs) - 1
	cases := []struct {
		key string
		len int
	}{{fmt.Sprintf("db%d", lastDB), len(revs[lastDB])}, {"archive.car", len(car)}}
	for _, mode := range []string{"shadow", "prefer"} {
		srv, addr, ready := startServer(bin, dir, "-representations", mode)
		c := newClient(addr)
		record("open mode=%s ready_ms=%d", mode, ready.Milliseconds())
		for _, cs := range cases {
			for _, r := range []struct {
				name string
				spec string
			}{{"whole", ""}, {"4KiB", fmt.Sprintf("bytes=%d-%d", cs.len/2, cs.len/2+4095)}, {"64KiB", fmt.Sprintf("bytes=%d-%d", cs.len/3, cs.len/3+65535)}, {"1MiB", fmt.Sprintf("bytes=%d-%d", cs.len/5, cs.len/5+(1<<20)-1)}} {
				d := timeIt(5, func() { get(c, cs.key, r.spec) })
				record("read mode=%s object=%s read=%s median_ms=%.2f", mode, cs.key, r.name, float64(d.Microseconds())/1000)
			}
		}
		stop(srv)
	}

	// restart cost with many small roots
	for _, n := range []int{0, 100, 1000} {
		d, _ := os.MkdirTemp("", "z3i0-open-")
		srv, addr, _ := startServer(bin, d, "-representations", "prefer")
		c := newClient(addr)
		c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		for i := 0; i < n; i++ {
			put(c, fmt.Sprintf("s%04d", i), sqliteDB(12, 4096, int64(1000+i)))
		}
		stop(srv)
		st := statsOf(bin, d)
		var ready time.Duration
		for _, mode := range []string{"off", "prefer"} {
			srv, _, ready = startServer(bin, d, "-representations", mode)
			stop(srv)
			record("restart roots=%d mode=%s ready_ms=%d", num(st, "representation_root_count"), mode, ready.Milliseconds())
		}
		t := time.Now()
		cli(bin, "verify", "-store", d)
		record("restart roots=%d verify_ms=%d", num(st, "representation_root_count"), time.Since(t).Milliseconds())
		os.RemoveAll(d)
	}
}

func main() {
	phase := flag.String("phase", "all", "all|compat|bundles|kinds|crash|large|reads")
	largeMiB := flag.Int("large-mib", 256, "size of the large-object phase in MiB")
	crashRounds := flag.Int("crash-rounds", 4, "SIGKILL rounds")
	flag.StringVar(&gitFixtureDir, "git-fixtures", "", "optional I1C Git pack/idx fixture directory")
	flag.StringVar(&codecFixtureDir, "codec-fixtures", "", "optional I1B fixture directory: add gzip/zstd nested objects to every phase")
	flag.Parse()
	bin = os.Getenv("ZEROS3_BIN")
	baseB = os.Getenv("ZEROS3_BASE_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	if baseB == "" && (*phase == "all" || *phase == "compat" || *phase == "bundles") {
		fmt.Println("FAIL: ZEROS3_BASE_BIN (binary built from the commit before I0) is required for the compat phase")
		os.Exit(2)
	}
	_ = md5.New
	_ = hex.EncodeToString
	run := func(name string, f func()) {
		if *phase == "all" || *phase == name {
			f()
		}
	}
	run("compat", phaseCompat)
	run("bundles", phaseBundles)
	run("kinds", phaseKinds)
	run("crash", func() { phaseCrash(*crashRounds) })
	run("large", func() { phaseLarge(*largeMiB) })
	run("reads", phaseReads)
	if failed {
		os.Exit(1)
	}
}
