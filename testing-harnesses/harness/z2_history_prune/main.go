// z2_history_prune demonstrates history retention end to end through a real
// zeros3 binary: overwritten and deleted versions pin packed storage; a
// dry-run plans a retention policy; `versions prune -apply` retires exact
// history roots durably; and the existing gc and repack then reclaim what
// those roots pinned -- while the current objects, the retained history and
// an independent snapshot all stay readable. It also checks that a build
// predating the history-prune journal record refuses the pruned store.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_history_prune [-seg-mib 4] [-baseline-bin /path/to/older/zeros3]
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	bucket          = "z2"
	restoredBucket  = "z2-restored"
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

// seg is a deterministic incompressible byte run; blob concatenates segs.
type seg struct {
	seed uint64
	size int64
}

type blob struct {
	segs []seg
	pos  int64
}

func (b *blob) total() (n int64) {
	for _, s := range b.segs {
		n += s.size
	}
	return n
}

// Read returns bytes from the segment containing the current position.
func (b *blob) Read(p []byte) (int, error) {
	if b.pos >= b.total() {
		return 0, io.EOF
	}
	base := int64(0)
	for _, s := range b.segs {
		if b.pos < base+s.size {
			p = p[:min(int64(len(p)), base+s.size-b.pos)]
			for i := 0; i < len(p); {
				at := b.pos - base + int64(i)
				var w [8]byte
				binary.LittleEndian.PutUint64(w[:], splitmix(s.seed<<40^uint64(at/8)))
				i += copy(p[i:], w[at%8:])
			}
			b.pos += int64(len(p))
			return len(p), nil
		}
		base += s.size
	}
	return 0, io.EOF
}

func (b *blob) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = off
	case io.SeekCurrent:
		b.pos += off
	case io.SeekEnd:
		b.pos = b.total() + off
	}
	return b.pos, nil
}

type gcJSON struct {
	LiveSetOK               bool  `json:"live_set_ok"`
	HistoricalRootCount     int   `json:"historical_root_count"`
	SnapshotRootCount       int   `json:"snapshot_root_count"`
	ChunksUnreachable       int   `json:"chunks_unreachable"`
	ManifestsUnreachable    int   `json:"manifests_unreachable"`
	ReclaimablePayloadBytes int64 `json:"reclaimable_payload_bytes"`
	PackedDeadChunkCount    int   `json:"packed_dead_chunk_count"`
	PackedDeadBytes         int64 `json:"packed_dead_bytes"`
	PackFileBytes           int64 `json:"pack_file_bytes"`
	ChunksDeleted           int   `json:"chunks_deleted"`
	ManifestsDeleted        int   `json:"manifests_deleted"`
	PacksDeleted            int   `json:"packs_deleted"`
	BytesDeleted            int64 `json:"bytes_deleted"`
}

type pruneJSON struct {
	Applied              bool  `json:"applied"`
	KeysMatched          int   `json:"keys_matched"`
	HistoricalExamined   int   `json:"historical_examined"`
	HistoricalRetained   int   `json:"historical_retained"`
	HistoricalSelected   int   `json:"historical_selected"`
	SelectedLogicalBytes int64 `json:"selected_logical_bytes"`
	JournalFrames        int   `json:"journal_frames"`
	VersionsPruned       int   `json:"versions_pruned"`
	Versions             []struct {
		Key       string `json:"key"`
		VersionID string `json:"version_id"`
	} `json:"versions"`
}

type versionRow struct {
	VersionID string `json:"version_id"`
	Status    string `json:"status"`
	Deleted   bool   `json:"deleted"`
}

type repackJSON struct {
	PacksDeleted int   `json:"packs_deleted"`
	PacksWritten int   `json:"packs_written"`
	BytesWritten int64 `json:"bytes_written"`
	BytesDeleted int64 `json:"bytes_deleted"`
}

var bin, storeDir string

func main() {
	segMiB := flag.Int64("seg-mib", 4, "size of each shared/unique half of an object generation, in MiB")
	baseline := flag.String("baseline-bin", "", "a zeros3 build that predates history pruning (must refuse the pruned store)")
	flag.Parse()
	bin = os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	for _, kv := range os.Environ() { // ambient AWS_* would override the fixed credentials
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			os.Unsetenv(k)
		}
	}
	var err error
	if storeDir, err = os.MkdirTemp("", "zeros3-z2-history-"); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)
	S := *segMiB << 20

	// generation i of "doc": a shared first half plus a unique second half.
	doc := func(i int) *blob { return &blob{segs: []seg{{1000, S}, {uint64(2000 + i), S}}} }
	tmp := func(i int) *blob { return &blob{segs: []seg{{uint64(3000 + i), S}}} }
	other := func() *blob { return &blob{segs: []seg{{4000, S}}} }

	ctx := context.Background()
	srv, addr, _ := startServer()
	cli := newClient(ctx, addr)
	put := func(k string, b *blob) {
		_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(k), Body: b, ContentLength: aws.Int64(b.total())})
		if err != nil {
			check("PutObject "+k, false, "%v", err)
			os.Exit(1)
		}
	}
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err == nil, "%v", err)
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(restoredBucket)})
	check("CreateBucket (restore target)", err == nil, "%v", err)
	var snapID string
	for i := 0; i < 6; i++ {
		put("doc", doc(i))
		if i == 2 { // snapshot while generation 2 is current
			out, err := exec.Command(bin, "snapshot", "create", "-endpoint", "http://"+addr, "-json", "s3://"+bucket+"/").Output()
			var s struct {
				SnapshotID string `json:"snapshot_id"`
			}
			check("snapshot create at generation 2", err == nil && json.Unmarshal(out, &s) == nil && s.SnapshotID != "", "%v %s", err, out)
			snapID = s.SnapshotID
		}
	}
	put("other", other())
	put("tmp", tmp(0))
	put("tmp", tmp(1))
	_, err = cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("tmp")})
	check("DeleteObject tmp", err == nil, "%v", err)
	stop(srv)

	// Restore the deleted tmp version, so a retired history row shares its manifest with a current root.
	var tmpRows []versionRow
	run(&tmpRows, "versions", "-store", storeDir, "-bucket", bucket, "-key", "tmp", "-json")
	restored := false
	for _, r := range tmpRows {
		if r.Deleted {
			out, err := exec.Command(bin, "restore", "-store", storeDir, "-bucket", bucket, "-key", "tmp", "-version", r.VersionID).CombinedOutput()
			check("restore deleted tmp", err == nil, "%v %s", err, out)
			restored = true
		}
	}
	check("tmp has a deleted historical version", restored, "%+v", tmpRows)

	out, err := exec.Command(bin, "compact", "-store", storeDir, "-pack-size-mib", fmt.Sprint(*segMiB)).CombinedOutput()
	check("compact", err == nil, "%v %s", err, out)
	var before gcJSON
	run(&before, "gc", "-store", storeDir, "-json")
	check("history pins packed storage before prune", before.LiveSetOK && before.HistoricalRootCount == 7 && before.ChunksUnreachable == 0 && before.PackedDeadChunkCount == 0, "%+v", before)
	readyBefore := readyTime()
	journalBefore, formatBefore := journalSize(), formatVersion()
	fmt.Printf("RECORD before historical_roots=%d pack_bytes=%d journal_bytes=%d format=%d open_ms=%d\n",
		before.HistoricalRootCount, before.PackFileBytes, journalBefore, formatBefore, readyBefore.Milliseconds())

	// Dry run: plans, mutates nothing.
	fingerprint := dirFingerprint()
	t := time.Now()
	var plan pruneJSON
	run(&plan, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-keep-last", "1", "-json")
	planTime := time.Since(t)
	check("dry-run plan: keep-last 1", !plan.Applied && plan.KeysMatched == 2 && plan.HistoricalExamined == 7 && plan.HistoricalSelected == 5 && plan.HistoricalRetained == 2, "%+v", plan)
	check("dry-run mutates nothing", dirFingerprint() == fingerprint && formatVersion() == formatBefore, "store changed")
	out, err = exec.Command(bin, "versions", "prune", "-store", storeDir, "-bucket", bucket).CombinedOutput()
	check("a retention criterion is required", err != nil, "%s", out)
	out, err = exec.Command(bin, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-key", "doc", "-prefix", "d", "-keep-last", "1").CombinedOutput()
	check("-key and -prefix are exclusive", err != nil, "%s", out)

	// Apply.
	t = time.Now()
	var applied pruneJSON
	run(&applied, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-keep-last", "1", "-apply", "-json")
	applyTime := time.Since(t)
	journalAfter := journalSize()
	check("apply retires exactly the planned versions", applied.Applied && applied.VersionsPruned == 5 && applied.JournalFrames >= 1 && sameIDs(plan, applied), "%+v", applied)
	check("FORMAT.json raised before the first prune record", formatVersion() == 4, "format %d", formatVersion())
	fmt.Printf("RECORD prune plan_ms=%.1f apply_ms=%.1f frames=%d journal_added_bytes=%d selected_logical_bytes=%d\n",
		float64(planTime.Microseconds())/1000, float64(applyTime.Microseconds())/1000, applied.JournalFrames, journalAfter-journalBefore, applied.SelectedLogicalBytes)
	var again pruneJSON
	run(&again, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-keep-last", "1", "-apply", "-json")
	check("re-running the same command is a no-op", again.HistoricalSelected == 0 && again.VersionsPruned == 0 && again.JournalFrames == 0, "%+v", again)
	check("no journal growth from the no-op", journalSize() == journalAfter, "journal grew")

	if *baseline != "" {
		out, err := exec.Command(*baseline, "stats", "-store", storeDir).CombinedOutput()
		check("a build predating pruning refuses the pruned store", err != nil && strings.Contains(string(out), "unsupported store format version 4"), "%v %s", err, out)
	}
	readyAfter := readyTime()
	fmt.Printf("RECORD after historical_roots=%d journal_bytes=%d format=%d open_ms=%d\n", before.HistoricalRootCount-applied.VersionsPruned, journalAfter, formatVersion(), readyAfter.Milliseconds())

	for _, k := range []string{"doc", "tmp"} {
		var rows []versionRow
		run(&rows, "versions", "-store", storeDir, "-bucket", bucket, "-key", k, "-json")
		hist := 0
		for _, r := range rows {
			if r.Status == "historical" {
				hist++
			}
		}
		check("versions after restart: "+k, hist == 1 && len(rows) == 2, "%+v", rows)
	}

	// Pruned roots become ordinary garbage; gc and repack reclaim it.
	var dry gcJSON
	run(&dry, "gc", "-store", storeDir, "-json")
	check("gc sees the newly unreachable roots", dry.LiveSetOK && dry.HistoricalRootCount == 2 && dry.SnapshotRootCount == 1 && dry.ManifestsUnreachable == 4 &&
		dry.PackedDeadChunkCount > 0, "%+v", dry)
	fmt.Printf("RECORD unreachable manifests=%d loose_chunks=%d loose_payload_bytes=%d packed_dead_chunks=%d packed_dead_bytes=%d\n",
		dry.ManifestsUnreachable, dry.ChunksUnreachable, dry.ReclaimablePayloadBytes, dry.PackedDeadChunkCount, dry.PackedDeadBytes)
	var gc gcJSON
	run(&gc, "gc", "-store", storeDir, "-apply", "-json")
	fmt.Printf("RECORD gc manifests_deleted=%d chunks_deleted=%d packs_deleted=%d bytes_deleted=%d\n", gc.ManifestsDeleted, gc.ChunksDeleted, gc.PacksDeleted, gc.BytesDeleted)
	var rp repackJSON
	run(&rp, "repack", "-store", storeDir, "-apply", "-max-live-percent", "100", "-pack-size-mib", fmt.Sprint(*segMiB), "-json")
	var final gcJSON
	run(&final, "gc", "-store", storeDir, "-json")
	check("repack reclaimed the dead packed records", final.PackedDeadChunkCount == 0 && final.ChunksUnreachable == 0 && final.ManifestsUnreachable == 0 &&
		final.PackFileBytes <= before.PackFileBytes-dry.PackedDeadBytes/2, "before %d after %+v", before.PackFileBytes, final)
	fmt.Printf("RECORD repack packs_deleted=%d packs_written=%d bytes_deleted=%d bytes_written=%d pack_bytes_before=%d pack_bytes_after=%d\n",
		rp.PacksDeleted, rp.PacksWritten, rp.BytesDeleted, rp.BytesWritten, before.PackFileBytes, final.PackFileBytes)
	out, err = exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("verify -deep after reclamation", err == nil, "%v\n%s", err, out)

	// Current, retained and snapshot data all remain readable.
	var rows []versionRow
	run(&rows, "versions", "-store", storeDir, "-bucket", bucket, "-key", "doc", "-json")
	retained := ""
	for _, r := range rows {
		if r.Status == "historical" {
			retained = r.VersionID
		}
	}
	srv, addr, _ = startServer()
	cli = newClient(ctx, addr)
	get := func(b, k string, want *blob, what string) {
		resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b), Key: aws.String(k)})
		ok := err == nil
		if ok {
			ok = sha(resp.Body) == sha(want)
			resp.Body.Close()
		}
		check(what, ok, "%v", err)
	}
	get(bucket, "doc", doc(5), "current doc (generation 5)")
	get(bucket, "other", other(), "current other")
	get(bucket, "tmp", tmp(1), "current tmp (restored from a deleted version)")
	snap := exec.Command(bin, "snapshot", "restore", "-endpoint", "http://"+addr, snapID, "s3://"+restoredBucket+"/")
	out, err = snap.CombinedOutput()
	check("snapshot restore after prune+gc+repack", err == nil, "%v\n%s", err, out)
	get(restoredBucket, "doc", doc(2), "snapshot-restored doc is generation 2")
	stop(srv)
	out, err = exec.Command(bin, "restore", "-store", storeDir, "-bucket", bucket, "-key", "doc", "-version", retained).CombinedOutput()
	check("restore the retained history version", err == nil, "%v %s", err, out)
	srv, addr, _ = startServer()
	cli = newClient(ctx, addr)
	get(bucket, "doc", doc(4), "restored retained version is generation 4")
	stop(srv)
	out, err = exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("final verify -deep", err == nil, "%v\n%s", err, out)

	if failed {
		os.Exit(1)
	}
}

func sameIDs(a, b pruneJSON) bool {
	if len(a.Versions) != len(b.Versions) {
		return false
	}
	for i := range a.Versions {
		if a.Versions[i] != b.Versions[i] {
			return false
		}
	}
	return true
}

func sha(r io.Reader) [32]byte {
	h := sha256.New()
	_, _ = io.Copy(h, r)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// run executes a zeros3 subcommand and decodes its JSON output into v.
func run(v any, args ...string) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("FAIL: zeros3 %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		os.Exit(1)
	}
	if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
		fmt.Printf("FAIL: zeros3 %s: bad JSON: %v\n%s\n", strings.Join(args, " "), err, stdout.String())
		os.Exit(1)
	}
}

func journalSize() int64 {
	st, err := os.Stat(filepath.Join(storeDir, "journal", "visibility.log"))
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	return st.Size()
}

func formatVersion() int {
	var f struct {
		V int `json:"store_format_version"`
	}
	b, _ := os.ReadFile(filepath.Join(storeDir, "FORMAT.json"))
	_ = json.Unmarshal(b, &f)
	return f.V
}

// dirFingerprint hashes every file in the store except the LOCK file.
func dirFingerprint() string {
	h := sha256.New()
	_ = filepath.WalkDir(storeDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() != "LOCK" {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(storeDir, p)
			fmt.Fprintf(h, "%s %d\n", rel, len(b))
			h.Write(b)
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil))
}

func readyTime() time.Duration {
	srv, _, ready := startServer()
	stop(srv)
	return ready
}

func startServer() (*exec.Cmd, string, time.Duration) {
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

func stop(srv *exec.Cmd) {
	_ = srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()
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
