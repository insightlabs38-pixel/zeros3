// z2_streaming_put drives one large ordinary PutObject and a full-object
// GET through a real zeros3 server with the AWS SDK for Go v2 and checks
// that the server's peak resident memory stays far below the object size
// in both directions, that the object reads back byte-for-byte (windowed
// range GETs plus one streamed full GET, so nothing is buffered whole on
// either side), and that the store verifies clean afterwards. The GET
// phase runs against a freshly restarted server so its peak RSS is its own.
//
// The object is generated on the fly and never held in memory. By default
// it is a repeating random block, so chunks deduplicate and the run
// exercises streaming rather than disk speed; -unique makes every byte
// distinct for a throughput measurement.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_streaming_put [-size-mib 288] [-unique] [-max-rss-mib 128]
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	repeatPeriod    = 3<<20 + 12345
	readbackWindow  = 8 << 20
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

// body is a deterministic, seekable object of a given size whose bytes are
// a pure function of their offset.
type body struct {
	size   int64
	unique bool
	pos    int64
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

func (b *body) fill(p []byte, off int64) {
	if !b.unique {
		off %= repeatPeriod
	}
	var w [8]byte
	for i := 0; i < len(p); {
		blk := (off + int64(i)) / 8
		binary.LittleEndian.PutUint64(w[:], splitmix(uint64(blk)))
		i += copy(p[i:], w[(off+int64(i))%8:])
	}
}

func (b *body) Read(p []byte) (int, error) {
	if b.pos >= b.size {
		return 0, io.EOF
	}
	if rest := b.size - b.pos; int64(len(p)) > rest {
		p = p[:rest]
	}
	if b.unique {
		b.fill(p, b.pos)
	} else {
		// The repeating block wraps mid-read; fill each segment.
		for done := 0; done < len(p); {
			seg := int(min(int64(len(p)-done), repeatPeriod-(b.pos+int64(done))%repeatPeriod))
			b.fill(p[done:done+seg], (b.pos+int64(done))%repeatPeriod)
			done += seg
		}
	}
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *body) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = off
	case io.SeekCurrent:
		b.pos += off
	case io.SeekEnd:
		b.pos = b.size + off
	}
	return b.pos, nil
}

func (b *body) window(off, n int64) []byte {
	out := make([]byte, n)
	_, _ = (&body{size: b.size, unique: b.unique, pos: off}).Read(out)
	return out
}

func procStatus(pid int, field string) int64 {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, field+":") {
			kb, _ := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
			return kb << 10
		}
	}
	return -1
}

func main() {
	sizeMiB := flag.Int64("size-mib", 288, "object size in MiB (must exceed 256 to cross the former buffering ceiling)")
	unique := flag.Bool("unique", false, "make every byte distinct (disk-bound) instead of a repeating block")
	maxRSS := flag.Int64("max-rss-mib", 128, "fail if the server's peak RSS after the upload exceeds this")
	flag.Parse()

	bin := os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	storeDir, err := os.MkdirTemp("", "zeros3-z2-streaming-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)

	srv, addr := startServer(bin, storeDir)
	defer func() { srv.Process.Kill() }()

	ctx := context.Background()
	cli := newClient(ctx, addr)
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("z2")})
	check("CreateBucket", err == nil, "%v", err)

	obj := &body{size: *sizeMiB << 20, unique: *unique}
	etag := md5.New()
	_, _ = io.Copy(etag, &body{size: obj.size, unique: obj.unique})
	wantETag := hex.EncodeToString(etag.Sum(nil))

	start := time.Now()
	_, err = cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("z2"), Key: aws.String("large"), Body: obj, ContentLength: aws.Int64(obj.size),
	})
	elapsed := time.Since(start)
	check("PutObject (streamed, beyond the former 256 MiB ceiling)", err == nil, "%v", err)
	peakRSS := procStatus(srv.Process.Pid, "VmHWM")
	fmt.Printf("RECORD put size_mib=%d unique=%v elapsed_ms=%d mib_per_s=%.1f server_peak_rss_mib=%d\n",
		*sizeMiB, *unique, elapsed.Milliseconds(), float64(*sizeMiB)/elapsed.Seconds(), peakRSS>>20)
	check("server peak RSS stays far below the object size", peakRSS > 0 && peakRSS <= *maxRSS<<20,
		"peak RSS %d MiB, limit %d MiB, object %d MiB", peakRSS>>20, *maxRSS, *sizeMiB)

	head, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("z2"), Key: aws.String("large")})
	check("HeadObject size and single-part ETag", err == nil && aws.ToInt64(head.ContentLength) == obj.size &&
		strings.Trim(aws.ToString(head.ETag), `"`) == wantETag, "head=%+v err=%v", head, err)

	exact := true
	for off := int64(0); off < obj.size && exact; off += readbackWindow {
		n := min(int64(readbackWindow), obj.size-off)
		resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("z2"), Key: aws.String("large"),
			Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+n-1))})
		if err != nil {
			check("windowed range readback", false, "offset %d: %v", off, err)
			exact = false
			break
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(got, obj.window(off, n)) {
			check("windowed range readback", false, "mismatch at offset %d", off)
			exact = false
		}
	}
	if exact {
		check("windowed range readback is byte-exact", true, "")
	}

	srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()

	srv, addr = startServer(bin, storeDir)
	cli = newClient(ctx, addr)
	get := func(rng string) (io.ReadCloser, int64, error) {
		in := &s3.GetObjectInput{Bucket: aws.String("z2"), Key: aws.String("large")}
		if rng != "" {
			in.Range = aws.String(rng)
		}
		resp, err := cli.GetObject(ctx, in)
		if err != nil {
			return nil, 0, err
		}
		return resp.Body, aws.ToInt64(resp.ContentLength), nil
	}

	start = time.Now()
	rc, length, err := get("")
	var matched int64
	if err == nil {
		matched, err = compareStream(rc, &body{size: obj.size, unique: obj.unique})
		rc.Close()
	}
	elapsed = time.Since(start)
	check("full GET is byte-exact", err == nil && matched == obj.size && length == obj.size,
		"matched %d of %d bytes, Content-Length %d, err %v", matched, obj.size, length, err)
	getRSS := procStatus(srv.Process.Pid, "VmHWM")
	fmt.Printf("RECORD get size_mib=%d unique=%v elapsed_ms=%d mib_per_s=%.1f server_peak_rss_mib=%d\n",
		*sizeMiB, *unique, elapsed.Milliseconds(), float64(*sizeMiB)/elapsed.Seconds(), getRSS>>20)
	check("server peak RSS during the full GET stays far below the object size", getRSS > 0 && getRSS <= *maxRSS<<20,
		"peak RSS %d MiB, limit %d MiB, object %d MiB", getRSS>>20, *maxRSS, *sizeMiB)

	const rangeLen = 1 << 20
	for _, rg := range []struct {
		name       string
		header     string
		start, end int64
	}{
		{"prefix", fmt.Sprintf("bytes=0-%d", rangeLen-1), 0, rangeLen - 1},
		{"interior", fmt.Sprintf("bytes=%d-%d", obj.size/2, obj.size/2+rangeLen-1), obj.size / 2, obj.size/2 + rangeLen - 1},
		{"suffix", fmt.Sprintf("bytes=-%d", rangeLen), obj.size - rangeLen, obj.size - 1},
	} {
		start := time.Now()
		rc, _, err := get(rg.header)
		var got []byte
		if err == nil {
			got, err = io.ReadAll(rc)
			rc.Close()
		}
		check("range GET "+rg.name, err == nil && bytes.Equal(got, obj.window(rg.start, rg.end-rg.start+1)), "%v (%d bytes)", err, len(got))
		fmt.Printf("RECORD range %s elapsed_ms=%d\n", rg.name, time.Since(start).Milliseconds())
	}
	check("server peak RSS after range GETs stays bounded", procStatus(srv.Process.Pid, "VmHWM") <= *maxRSS<<20,
		"peak RSS %d MiB", procStatus(srv.Process.Pid, "VmHWM")>>20)

	srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()
	out, err := exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("zeros3 verify -deep after the upload", err == nil, "%v\n%s", err, out)

	if failed {
		os.Exit(1)
	}
}

func startServer(bin, storeDir string) (*exec.Cmd, string) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	if err := srv.Start(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return srv, addr
		} else if time.Now().After(deadline) {
			fmt.Println("FAIL: zeros3 did not start")
			os.Exit(2)
		}
	}
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

// compareStream returns how many bytes of got matched want; the streams
// must be equal in content and length.
func compareStream(got, want io.Reader) (int64, error) {
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
