// z2_repack exercises pack-aware garbage collection and immutable
// repacking through a real zeros3 binary. For each utilization profile it
// ingests live objects over S3, holds a multipart upload open while
// `zeros3 compact` packs everything, aborts the upload (leaving dead
// packed chunks beside live ones), and then measures `gc` reporting and
// `zeros3 repack` under the default policy and under a full-reclaim
// threshold: bytes read, rewritten and reclaimed, write amplification,
// repack throughput and peak RSS, server open time, and full/range GET
// before and after. A separate store whose dead chunks occupy whole packs
// shows direct removal by `gc -apply`. Manifests and ETags must not change.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_repack [-size-mib 1024] [-pack-size-mib 32]
package main

import (
	"bytes"
	"context"
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
	partSize        = 16 << 20
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

type statsJSON struct {
	LooseChunkCount      int     `json:"loose_chunk_count"`
	PackCount            int     `json:"pack_count"`
	PackedChunkCount     int     `json:"packed_chunk_count"`
	PackFileBytes        int64   `json:"pack_file_bytes"`
	PackedLiveChunkCount int     `json:"packed_live_chunk_count"`
	PackedDeadChunkCount int     `json:"packed_dead_chunk_count"`
	PackedLiveBytes      int64   `json:"packed_live_bytes"`
	PackedDeadBytes      int64   `json:"packed_dead_bytes"`
	PackUtilization      float64 `json:"pack_utilization"`
	PacksFullyDead       int     `json:"packs_fully_dead"`
	PacksPartiallyDead   int     `json:"packs_partially_dead"`
	WholeReclaim         int64   `json:"pack_whole_reclaimable_bytes"`
	RepackReclaim        int64   `json:"pack_repack_reclaimable_bytes"`
}

type compactJSON struct {
	PacksWritten int `json:"packs_written"`
}

type repackJSON struct {
	PacksSelected  int   `json:"packs_selected"`
	PacksFullyDead int   `json:"packs_fully_dead"`
	PacksRewritten int   `json:"packs_rewritten"`
	RecordsCopied  int   `json:"records_copied"`
	BytesRead      int64 `json:"bytes_read"`
	PacksWritten   int   `json:"packs_written"`
	BytesWritten   int64 `json:"bytes_written"`
	PacksDeleted   int   `json:"packs_deleted"`
	BytesDeleted   int64 `json:"bytes_deleted"`
	BytesReclaimed int64 `json:"bytes_reclaimed"`
}

type gcJSON struct {
	PacksDeleted   int   `json:"packs_deleted"`
	BytesDeleted   int64 `json:"bytes_deleted"`
	PacksFullyDead int   `json:"packs_fully_dead"`
	WholeReclaim   int64 `json:"pack_whole_reclaimable_bytes"`
}

type env struct {
	bin      string
	packMiB  int64
	maxRSS   int64
	storeDir string
	live     []*object
	etags    map[string]string
}

func main() {
	sizeMiB := flag.Int64("size-mib", 1024, "payload per profile in MiB (live + dead)")
	packMiB := flag.Int64("pack-size-mib", 32, "target pack size passed to compact and repack")
	maxRSS := flag.Int64("max-rss-mib", 256, "fail if repack or the server peaks above this")
	flag.Parse()

	bin := os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	for _, p := range []struct {
		name string
		live int64
	}{{"live90", 90}, {"live50", 50}, {"live10", 10}} {
		e := &env{bin: bin, packMiB: *packMiB, maxRSS: *maxRSS}
		e.profile(p.name, *sizeMiB, p.live)
	}
	e := &env{bin: bin, packMiB: *packMiB, maxRSS: *maxRSS}
	e.deadPacks(*sizeMiB)
	if failed {
		os.Exit(1)
	}
}

// build ingests liveMiB of live objects, packs them with an open multipart
// upload of deadMiB, and aborts the upload. With separate, the live data is
// packed before the upload starts, so the dead chunks fill whole packs.
func (e *env) build(liveMiB, deadMiB int64, separate bool) {
	dir, err := os.MkdirTemp("", "zeros3-z2-repack-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	e.storeDir = dir
	ctx := context.Background()
	srv, addr, _ := startServer(e.bin, e.storeDir)
	cli := newClient(ctx, addr)
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err == nil, "%v", err)
	const nLive = 4
	e.etags = map[string]string{}
	for i := 0; i < nLive; i++ {
		o := &object{seed: uint64(i + 1), size: liveMiB << 20 / nLive}
		e.live = append(e.live, o)
		out, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(e.key(i)),
			Body: &object{seed: o.seed, size: o.size}, ContentLength: aws.Int64(o.size)})
		if err != nil {
			check("PutObject", false, "%v", err)
			os.Exit(1)
		}
		e.etags[e.key(i)] = aws.ToString(out.ETag)
	}
	if separate {
		stop(srv)
		e.run("compact", "-store", e.storeDir, "-pack-size-mib", fmt.Sprint(e.packMiB), "-json")
		srv, addr, _ = startServer(e.bin, e.storeDir)
		cli = newClient(ctx, addr)
	}
	up, err := cli.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("dead")})
	if err != nil {
		check("CreateMultipartUpload", false, "%v", err)
		os.Exit(1)
	}
	for n := int32(1); int64(n-1)*partSize < deadMiB<<20; n++ {
		size := min(partSize, deadMiB<<20-int64(n-1)*partSize)
		_, err := cli.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("dead"), UploadId: up.UploadId,
			PartNumber: aws.Int32(n), Body: &object{seed: 1000 + uint64(n), size: size}, ContentLength: aws.Int64(size)})
		if err != nil {
			check("UploadPart", false, "%v", err)
			os.Exit(1)
		}
	}
	stop(srv)
	e.run("compact", "-store", e.storeDir, "-pack-size-mib", fmt.Sprint(e.packMiB), "-json")
	srv, addr, _ = startServer(e.bin, e.storeDir)
	cli = newClient(ctx, addr)
	_, err = cli.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("dead"), UploadId: up.UploadId})
	check("AbortMultipartUpload", err == nil, "%v", err)
	stop(srv)
}

func (e *env) key(i int) string { return fmt.Sprintf("live-%02d", i) }

// run executes a zeros3 subcommand, failing the harness if it errors, and
// returns its stdout, wall time, and peak RSS.
func (e *env) run(args ...string) ([]byte, time.Duration, int64) {
	cmd := exec.Command(e.bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	t := time.Now()
	err := cmd.Run()
	el := time.Since(t)
	check("zeros3 "+args[0], err == nil, "%v\n%s", err, out.String())
	return out.Bytes(), el, maxRSSBytes(cmd)
}

func (e *env) stats() statsJSON {
	out, err := exec.Command(e.bin, "stats", "-json", "-store", e.storeDir).Output()
	var st statsJSON
	if err != nil || json.Unmarshal(out, &st) != nil {
		fmt.Println("FAIL: stats -json:", err)
		os.Exit(1)
	}
	return st
}

func storeFiles(dir string) (n int) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func (e *env) profile(name string, totalMiB, livePct int64) {
	liveMiB := totalMiB * livePct / 100
	e.build(liveMiB, totalMiB-liveMiB, false)
	defer os.RemoveAll(e.storeDir)
	fmt.Printf("RECORD profile name=%s live_mib=%d dead_mib=%d\n", name, liveMiB, totalMiB-liveMiB)

	manifests := hashDir(filepath.Join(e.storeDir, "manifests"))
	before := e.stats()
	filesBefore := storeFiles(e.storeDir)
	check(name+": fixture has dead packed chunks", before.PackedDeadChunkCount > 0, "%+v", before)
	fmt.Printf("RECORD packs phase=before profile=%s packs=%d records=%d pack_bytes=%d live_records=%d dead_records=%d utilization=%.3f fully_dead=%d partly_dead=%d repack_reclaimable=%d files=%d\n",
		name, before.PackCount, before.PackedChunkCount, before.PackFileBytes, before.PackedLiveChunkCount, before.PackedDeadChunkCount,
		before.PackUtilization, before.PacksFullyDead, before.PacksPartiallyDead, before.RepackReclaim, filesBefore)
	e.read(name+"-before", true)

	outDry, _, _ := e.run("repack", "-store", e.storeDir, "-pack-size-mib", fmt.Sprint(e.packMiB), "-json")
	var dry repackJSON
	check(name+": repack dry-run parses", json.Unmarshal(outDry, &dry) == nil, "%s", outDry)
	check(name+": dry-run changed nothing", e.stats().PackFileBytes == before.PackFileBytes, "pack bytes changed")
	fmt.Printf("RECORD repack mode=dry-run profile=%s policy=default selected=%d copied=%d bytes_read=%d would_write=%d would_reclaim=%d\n",
		name, dry.PacksSelected, dry.RecordsCopied, dry.BytesRead, dry.BytesWritten, dry.BytesReclaimed)

	e.pass(name, "default", 50, before.PackFileBytes)
	mid := e.stats()
	e.pass(name, "full", 100, mid.PackFileBytes)

	after := e.stats()
	filesAfter := storeFiles(e.storeDir)
	fmt.Printf("RECORD packs phase=after profile=%s packs=%d records=%d pack_bytes=%d live_records=%d dead_records=%d utilization=%.3f files=%d\n",
		name, after.PackCount, after.PackedChunkCount, after.PackFileBytes, after.PackedLiveChunkCount, after.PackedDeadChunkCount, after.PackUtilization, filesAfter)
	fmt.Printf("RECORD reclaim profile=%s pack_bytes_before=%d pack_bytes_after=%d reclaimed=%d reclaimed_pct=%.1f\n",
		name, before.PackFileBytes, after.PackFileBytes, before.PackFileBytes-after.PackFileBytes,
		100*float64(before.PackFileBytes-after.PackFileBytes)/float64(before.PackFileBytes))
	check(name+": no dead packed chunks remain", after.PackedDeadChunkCount == 0 && after.PacksFullyDead == 0, "%+v", after)
	check(name+": live packed records are unchanged", after.PackedLiveChunkCount == before.PackedLiveChunkCount && after.PackedLiveBytes == before.PackedLiveBytes, "%+v vs %+v", before, after)
	check(name+": manifests are byte-identical", hashDir(filepath.Join(e.storeDir, "manifests")) == manifests, "manifests changed")
	e.read(name+"-after", true)
	out, err := exec.Command(e.bin, "verify", "-deep", "-store", e.storeDir).CombinedOutput()
	check(name+": verify -deep after repack", err == nil, "%v\n%s", err, out)
}

// pass applies one repack and records its cost.
func (e *env) pass(name, policy string, livePct int, packBytesBefore int64) {
	out, el, rss := e.run("repack", "-apply", "-store", e.storeDir, "-pack-size-mib", fmt.Sprint(e.packMiB), "-max-live-percent", fmt.Sprint(livePct), "-json")
	var r repackJSON
	check(name+": repack "+policy+" parses", json.Unmarshal(out, &r) == nil, "%s", out)
	wa := 0.0
	if r.BytesReclaimed > 0 {
		wa = float64(r.BytesWritten) / float64(r.BytesReclaimed)
	}
	mibps := 0.0
	if el.Seconds() > 0 {
		mibps = float64(r.BytesRead+r.BytesWritten) / (1 << 20) / el.Seconds()
	}
	fmt.Printf("RECORD repack mode=apply profile=%s policy=%s selected=%d fully_dead=%d rewritten=%d copied=%d bytes_read=%d packs_written=%d bytes_written=%d packs_deleted=%d bytes_deleted=%d reclaimed=%d write_amp=%.2f elapsed_ms=%d io_mib_per_s=%.1f peak_rss_mib=%d\n",
		name, policy, r.PacksSelected, r.PacksFullyDead, r.PacksRewritten, r.RecordsCopied, r.BytesRead, r.PacksWritten, r.BytesWritten, r.PacksDeleted, r.BytesDeleted, r.BytesReclaimed, wa, el.Milliseconds(), mibps, rss>>20)
	check(name+": repack peak RSS is bounded ("+policy+")", rss > 0 && rss <= e.maxRSS<<20, "peak %d MiB, limit %d", rss>>20, e.maxRSS)
	check(name+": repack accounting ("+policy+")", r.BytesReclaimed == packBytesBefore-e.stats().PackFileBytes, "reported %d, actual %d", r.BytesReclaimed, packBytesBefore-e.stats().PackFileBytes)
}

// deadPacks shows packs holding only dead chunks being removed whole.
func (e *env) deadPacks(totalMiB int64) {
	liveMiB := totalMiB / 2
	e.build(liveMiB, totalMiB-liveMiB, true)
	defer os.RemoveAll(e.storeDir)
	name := "dead-packs"
	before := e.stats()
	check(name+": fixture has fully dead packs", before.PacksFullyDead > 0 && before.WholeReclaim > 0, "%+v", before)
	fmt.Printf("RECORD packs phase=before profile=%s packs=%d pack_bytes=%d fully_dead=%d partly_dead=%d whole_reclaimable=%d\n",
		name, before.PackCount, before.PackFileBytes, before.PacksFullyDead, before.PacksPartiallyDead, before.WholeReclaim)
	manifests := hashDir(filepath.Join(e.storeDir, "manifests"))

	dryOut, _, _ := e.run("gc", "-store", e.storeDir, "-json")
	var dry gcJSON
	check(name+": gc dry-run parses", json.Unmarshal(dryOut, &dry) == nil, "%s", dryOut)
	check(name+": gc dry-run changed nothing", e.stats().PackFileBytes == before.PackFileBytes, "pack bytes changed")

	out, el, rss := e.run("gc", "-apply", "-store", e.storeDir, "-json")
	var g gcJSON
	check(name+": gc apply parses", json.Unmarshal(out, &g) == nil, "%s", out)
	after := e.stats()
	fmt.Printf("RECORD gc mode=apply profile=%s packs_deleted=%d bytes_deleted=%d elapsed_ms=%d peak_rss_mib=%d pack_bytes_after=%d\n",
		name, g.PacksDeleted, g.BytesDeleted, el.Milliseconds(), rss>>20, after.PackFileBytes)
	check(name+": gc removed every dead pack whole", g.PacksDeleted == before.PacksFullyDead && after.PackFileBytes == before.PackFileBytes-before.WholeReclaim && after.PacksFullyDead == 0,
		"deleted %d of %d, bytes %d -> %d", g.PacksDeleted, before.PacksFullyDead, before.PackFileBytes, after.PackFileBytes)
	check(name+": manifests are byte-identical", hashDir(filepath.Join(e.storeDir, "manifests")) == manifests, "manifests changed")
	e.read(name+"-after", false)
	vout, err := exec.Command(e.bin, "verify", "-deep", "-store", e.storeDir).CombinedOutput()
	check(name+": verify -deep after gc", err == nil, "%v\n%s", err, vout)
}

// read starts a fresh server, times readiness, then checks ETags, one full
// GET, and ranged GETs of the first live object byte for byte.
func (e *env) read(phase string, timed bool) {
	ctx := context.Background()
	cold := dropCaches()
	srv, addr, ready := startServer(e.bin, e.storeDir)
	cli := newClient(ctx, addr)
	for i, o := range e.live {
		head, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(e.key(i))})
		check(phase+": ETag unchanged "+e.key(i), err == nil && aws.ToString(head.ETag) == e.etags[e.key(i)] && aws.ToInt64(head.ContentLength) == o.size, "%v", err)
	}
	big := e.live[0]
	start := time.Now()
	rc, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(e.key(0))})
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
		resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(e.key(0)),
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
	fmt.Printf("RECORD range phase=%s samples=%d len_kib=%d mean_ms=%.2f max_ms=%d\n", phase, rangeSamples, rangeLen>>10, float64(sum.Microseconds())/1000/rangeSamples, worst.Milliseconds())
	rss := stop(srv)
	fmt.Printf("RECORD open phase=%s cold_cache=%v server_ready_ms=%d server_peak_rss_mib=%d\n", phase, cold, ready.Milliseconds(), rss>>20)
	check(phase+": server peak RSS is bounded", rss > 0 && rss <= 128<<20, "peak %d MiB", rss>>20)
}

// dropCaches evicts the page cache (best effort; needs root) so read
// timings are not just memory copies. It reports whether it succeeded.
func dropCaches() bool {
	_ = exec.Command("sync").Run()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200) == nil
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
