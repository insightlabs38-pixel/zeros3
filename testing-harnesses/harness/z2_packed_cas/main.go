// z2_packed_cas exercises packed CAS storage through a real zeros3 binary:
// it ingests deterministic objects over S3 (every chunk lands loose),
// stops the server, runs `zeros3 compact`, and checks that the same
// objects read back byte-exact, with unchanged ETags and manifests, from
// immutable packs after a restart -- including a mixed store where new
// writes sit loose beside packs. It records the physical effect of
// compaction (file count, bytes), full/range GET throughput and latency
// before and after, server open time, and peak memory of compact and the
// server.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_packed_cas [-size-mib 256] [-objects 8] [-pack-size-mib 16]
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	bucket          = "z2"
	rangeLen        = 1 << 20
	rangeSamples    = 16
)

var failed bool

func check(name string, ok bool, detail string, args ...any) {
	if ok {
		fmt.Printf("PASS: %s\n", name)
		return
	}
	failed = true
	fmt.Printf("FAIL: %s: %s\n", name, fmt.Sprintf(detail, args...))
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

// object is a deterministic byte stream: incompressible and unique per
// seed, or (repeat) a short repeating pattern that deduplicates heavily.
type object struct {
	seed   uint64
	size   int64
	repeat bool
	pos    int64
}

func (o *object) fill(p []byte, off int64) {
	var w [8]byte
	for i := 0; i < len(p); {
		at := off + int64(i)
		if o.repeat {
			at %= 1 << 20
		}
		binary.LittleEndian.PutUint64(w[:], splitmix(o.seed<<40^uint64(at/8)))
		i += copy(p[i:], w[at%8:])
	}
}

func (o *object) Read(p []byte) (int, error) {
	if o.pos >= o.size {
		return 0, io.EOF
	}
	if rest := o.size - o.pos; int64(len(p)) > rest {
		p = p[:rest]
	}
	o.fill(p, o.pos)
	o.pos += int64(len(p))
	return len(p), nil
}

func (o *object) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		o.pos = off
	case io.SeekCurrent:
		o.pos += off
	case io.SeekEnd:
		o.pos = o.size + off
	}
	return o.pos, nil
}

func (o *object) window(off, n int64) []byte {
	out := make([]byte, n)
	o.fill(out, off)
	return out
}

type layout struct {
	Loose, Packs, PackedChunks, Files int
	LooseBytes, PackBytes             int64
}

type statsJSON struct {
	LooseChunkCount     int   `json:"loose_chunk_count"`
	LooseChunkFileBytes int64 `json:"loose_chunk_file_bytes"`
	PackCount           int   `json:"pack_count"`
	PackedChunkCount    int   `json:"packed_chunk_count"`
	PackFileBytes       int64 `json:"pack_file_bytes"`
}

type compactJSON struct {
	ChunksPacked int   `json:"chunks_packed"`
	PacksWritten int   `json:"packs_written"`
	LooseRemoved int   `json:"loose_removed"`
	PackBytes    int64 `json:"pack_bytes"`
}

func main() {
	sizeMiB := flag.Int64("size-mib", 256, "total unique payload in MiB")
	objects := flag.Int("objects", 8, "number of unique objects the payload is split across")
	packMiB := flag.Int64("pack-size-mib", 16, "target pack size passed to compact")
	maxRSS := flag.Int64("max-rss-mib", 128, "fail if compact or the server peaks above this")
	flag.Parse()

	bin := os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	storeDir, err := os.MkdirTemp("", "zeros3-z2-packed-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)

	per := *sizeMiB << 20 / int64(*objects)
	objs := make([]*object, 0, *objects+1)
	for i := 0; i < *objects; i++ {
		objs = append(objs, &object{seed: uint64(i + 1), size: per})
	}
	objs = append(objs, &object{seed: 99, size: 8 << 20, repeat: true})
	key := func(i int) string { return fmt.Sprintf("obj-%02d", i) }

	ctx := context.Background()
	srv, addr, _ := startServer(bin, storeDir)
	cli := newClient(ctx, addr)
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err == nil, "%v", err)
	etags := map[string]string{}
	start := time.Now()
	for i, o := range objs {
		out, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key(i)),
			Body: &object{seed: o.seed, size: o.size, repeat: o.repeat}, ContentLength: aws.Int64(o.size)})
		if err != nil {
			check("PutObject "+key(i), false, "%v", err)
			os.Exit(1)
		}
		etags[key(i)] = aws.ToString(out.ETag)
	}
	var total int64
	for _, o := range objs {
		total += o.size
	}
	fmt.Printf("RECORD ingest objects=%d logical_mib=%d elapsed_ms=%d\n", len(objs), total>>20, time.Since(start).Milliseconds())
	stop(srv)

	manifestsBefore := hashDir(filepath.Join(storeDir, "manifests"))
	before := measure(bin, storeDir, objs, key, "loose")
	check("loose store: no packs, chunks are files", before.Packs == 0 && before.Loose > 0, "%+v", before)
	fmt.Printf("RECORD layout phase=loose loose_files=%d packs=%d store_files=%d loose_bytes=%d\n",
		before.Loose, before.Packs, before.Files, before.LooseBytes)

	// compact
	t := time.Now()
	cmd := exec.Command(bin, "compact", "-store", storeDir, "-pack-size-mib", fmt.Sprint(*packMiB), "-json")
	var cout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &cout, os.Stderr
	err = cmd.Run()
	compactElapsed := time.Since(t)
	check("zeros3 compact", err == nil, "%v", err)
	var cres compactJSON
	check("compact -json parses", json.Unmarshal(cout.Bytes(), &cres) == nil, "%s", cout.String())
	compactRSS := maxRSSBytes(cmd)
	fmt.Printf("RECORD compact elapsed_ms=%d mib_per_s=%.1f chunks_packed=%d packs=%d peak_rss_mib=%d\n",
		compactElapsed.Milliseconds(), float64(before.LooseBytes>>20)/compactElapsed.Seconds(), cres.ChunksPacked, cres.PacksWritten, compactRSS>>20)
	check("compact peak RSS is bounded", compactRSS > 0 && compactRSS <= *maxRSS<<20, "peak %d MiB, limit %d", compactRSS>>20, *maxRSS)
	check("compact wrote multiple packs", cres.PacksWritten >= 2, "%+v", cres)

	after := measure(bin, storeDir, objs, key, "packed")
	check("packed store: no loose chunk files remain", after.Loose == 0 && after.Packs == cres.PacksWritten, "%+v", after)
	check("every chunk file became a packed record", after.PackedChunks == before.Loose, "loose before %d, packed %d", before.Loose, after.PackedChunks)
	fmt.Printf("RECORD layout phase=packed loose_files=%d packs=%d store_files=%d pack_bytes=%d physical_ratio=%.4f\n",
		after.Loose, after.Packs, after.Files, after.PackBytes, float64(after.PackBytes)/float64(before.LooseBytes))
	check("manifests are byte-identical after compaction", hashDir(filepath.Join(storeDir, "manifests")) == manifestsBefore, "manifests changed")

	// Mixed: a restarted server serves packs and accepts new loose writes.
	srv, addr, _ = startServer(bin, storeDir)
	cli = newClient(ctx, addr)
	for i, o := range objs {
		head, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key(i))})
		check("ETag unchanged after compaction "+key(i), err == nil && aws.ToString(head.ETag) == etags[key(i)] && aws.ToInt64(head.ContentLength) == o.size, "%v", err)
	}
	fresh := &object{seed: 500, size: 8 << 20}
	_, err = cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("fresh"),
		Body: &object{seed: fresh.seed, size: fresh.size}, ContentLength: aws.Int64(fresh.size)})
	check("PutObject beside packs", err == nil, "%v", err)
	rc, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("fresh")})
	var h = md5.New()
	if err == nil {
		_, _ = io.Copy(h, rc.Body)
		rc.Body.Close()
	}
	wantFresh := md5.New()
	_, _ = io.Copy(wantFresh, &object{seed: fresh.seed, size: fresh.size})
	check("mixed store read-back of a new loose object", err == nil && bytes.Equal(h.Sum(nil), wantFresh.Sum(nil)), "%v", err)
	stop(srv)
	mixed := layoutOf(bin, storeDir)
	check("mixed store has loose chunks and packs", mixed.Loose > 0 && mixed.Packs == after.Packs, "%+v", mixed)

	out, err := exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("zeros3 verify -deep over the mixed store", err == nil, "%v\n%s", err, out)

	if failed {
		os.Exit(1)
	}
}

// measure starts a fresh server, times its readiness, then times a full
// GET and ranged GETs of the first object (checking every byte), and
// returns the store's physical layout.
func measure(bin, storeDir string, objs []*object, key func(int) string, phase string) layout {
	ctx := context.Background()
	cold := dropCaches()
	srv, addr, ready := startServer(bin, storeDir)
	cli := newClient(ctx, addr)
	big := objs[0]

	start := time.Now()
	rc, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key(0))})
	var n int64
	if err == nil {
		n, err = compare(rc.Body, &object{seed: big.seed, size: big.size})
		rc.Body.Close()
	}
	el := time.Since(start)
	check(phase+": full GET is byte-exact", err == nil && n == big.size, "%v (%d of %d)", err, n, big.size)
	fmt.Printf("RECORD get phase=%s cold_cache=%v size_mib=%d elapsed_ms=%d mib_per_s=%.1f\n", phase, cold, big.size>>20, el.Milliseconds(), float64(big.size>>20)/el.Seconds())

	var worst, sum time.Duration
	exact := true
	for i := 0; i < rangeSamples; i++ {
		off := splitmix(uint64(i)*7919) % uint64(big.size-rangeLen)
		s := time.Now()
		resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key(0)),
			Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+rangeLen-1))})
		var got []byte
		if err == nil {
			got, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		d := time.Since(s)
		sum += d
		worst = max(worst, d)
		exact = exact && err == nil && bytes.Equal(got, big.window(int64(off), rangeLen))
	}
	check(phase+": ranged GETs are byte-exact", exact, "mismatch")
	fmt.Printf("RECORD range phase=%s samples=%d len_kib=%d mean_ms=%.2f max_ms=%d\n", phase, rangeSamples, rangeLen>>10,
		float64(sum.Microseconds())/1000/rangeSamples, worst.Milliseconds())

	rss := stop(srv)
	fmt.Printf("RECORD open phase=%s cold_cache=%v server_ready_ms=%d server_peak_rss_mib=%d\n", phase, cold, ready.Milliseconds(), rss>>20)
	check(phase+": server peak RSS is bounded", rss > 0 && rss <= 128<<20, "peak %d MiB", rss>>20)
	return layoutOf(bin, storeDir)
}

// dropCaches evicts the page cache (best effort; needs root) so read
// timings are not just memory copies. It reports whether it succeeded.
func dropCaches() bool {
	_ = exec.Command("sync").Run()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200) == nil
}

func layoutOf(bin, storeDir string) layout {
	out, err := exec.Command(bin, "stats", "-json", "-store", storeDir).Output()
	var st statsJSON
	if err != nil || json.Unmarshal(out, &st) != nil {
		fmt.Println("FAIL: stats -json:", err)
		os.Exit(1)
	}
	l := layout{Loose: st.LooseChunkCount, Packs: st.PackCount, PackedChunks: st.PackedChunkCount, LooseBytes: st.LooseChunkFileBytes, PackBytes: st.PackFileBytes}
	_ = filepath.WalkDir(storeDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			l.Files++
		}
		return nil
	})
	return l
}

func hashDir(dir string) string {
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			h.Write([]byte(d.Name()))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func startServer(bin, storeDir string) (*exec.Cmd, string, time.Duration) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	began := time.Now()
	if err := srv.Start(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return srv, addr, time.Since(began)
		} else if time.Now().After(deadline) {
			fmt.Println("FAIL: zeros3 did not start")
			os.Exit(2)
		}
	}
}

// stop interrupts a server, waits, and returns its peak RSS.
func stop(srv *exec.Cmd) int64 {
	_ = srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()
	return maxRSSBytes(srv)
}

func maxRSSBytes(c *exec.Cmd) int64 {
	if c.ProcessState == nil {
		return -1
	}
	if ru, ok := c.ProcessState.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss << 10
	}
	return -1
}

func newClient(ctx context.Context, addr string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region),
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

func compare(got, want io.Reader) (int64, error) {
	a, b := make([]byte, 1<<20), make([]byte, 1<<20)
	var total int64
	for {
		n, gerr := io.ReadFull(got, a)
		if m, _ := io.ReadFull(want, b[:n]); m != n || !bytes.Equal(a[:n], b[:n]) {
			return total, fmt.Errorf("content differs near offset %d", total)
		}
		total += int64(n)
		switch gerr {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			if _, err := io.ReadFull(want, b[:1]); err != io.EOF {
				return total, fmt.Errorf("download ended early at %d", total)
			}
			return total, nil
		default:
			return total, gerr
		}
	}
}
