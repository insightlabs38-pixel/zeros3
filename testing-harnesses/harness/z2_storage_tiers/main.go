// z2_storage_tiers demonstrates hot/warm/cold physical pack tiers end to end
// through a real zeros3 binary: objects are compacted directly into each
// tier, packs are moved between tiers across restarts, history pruning makes
// cold records dead for gc and a tier-preserving repack, and a pack is moved
// back to hot -- while every object stays byte-exact (full and ranged GET),
// deep verify passes, and per-tier directory accounting matches `tier
// status`. It also checks the store-format contract: hot-only stores of the
// previous build open, the first non-hot pack raises FORMAT.json to 5, and
// the previous build refuses the result.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_storage_tiers [-seg-mib 4] [-baseline-bin /path/to/Z2-08/zeros3]
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
)

var (
	failed         bool
	bin, storeDir  string
	segMiB         int64
	baselineBinary string
)

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

// slice returns the SHA-256 of bytes [start, start+n) of the blob.
func (b *blob) slice(start, n int64) [32]byte {
	c := &blob{segs: b.segs, pos: start}
	return sha(io.LimitReader(c, n))
}

func sha(r io.Reader) [32]byte {
	h := sha256.New()
	_, _ = io.Copy(h, r)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

type tierJSON struct {
	Tier           string `json:"tier"`
	Marker         string `json:"marker"`
	PackCount      int    `json:"pack_count"`
	PackedChunks   int    `json:"packed_chunk_count"`
	PackFileBytes  int64  `json:"pack_file_bytes"`
	DuplicatePacks int    `json:"duplicate_packs"`
}

type statusJSON struct {
	StoreFormatVersion int        `json:"store_format_version"`
	LooseChunks        int        `json:"loose_chunks"`
	Tiers              []tierJSON `json:"tiers"`
}

type packSummaryJSON struct {
	LiveSetOK            bool  `json:"live_set_ok"`
	PackCount            int   `json:"pack_count"`
	PackFileBytes        int64 `json:"pack_file_bytes"`
	PackedDeadChunkCount int   `json:"packed_dead_chunk_count"`
	PacksDeleted         int   `json:"packs_deleted"`
	ChunksUnreachable    int   `json:"chunks_unreachable"`
}

type moveJSON struct {
	DryRun     bool  `json:"dry_run"`
	PacksMoved int   `json:"packs_moved"`
	BytesMoved int64 `json:"bytes_moved"`
	Packs      []struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	} `json:"packs"`
}

type repackJSON struct {
	PacksDeleted int `json:"packs_deleted"`
	PacksWritten int `json:"packs_written"`
	Selected     []struct {
		Tier string `json:"tier"`
	} `json:"selected"`
}

func main() {
	flag.Int64Var(&segMiB, "seg-mib", 4, "size of each object segment, in MiB")
	flag.StringVar(&baselineBinary, "baseline-bin", "", "the Z2-08 build (store formats 1-4 only): must open hot-only stores and refuse a v5 store")
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
	if storeDir, err = os.MkdirTemp("", "zeros3-z2-tiers-"); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)
	S := segMiB << 20
	ctx := context.Background()

	objs := map[string]*blob{
		"a": {segs: []seg{{1000, S}, {2001, S}}}, // a and b share their first half
		"b": {segs: []seg{{1000, S}, {2002, S}}},
		"c": {segs: []seg{{3003, S}}},
		"d": {segs: []seg{{3004, S}}},
	}
	mixed := &blob{segs: []seg{{2001, S}, {3003, S}, {3004, S}}} // chunks owned by three tiers
	pack := fmt.Sprint(segMiB)

	srv, addr, _ := startServer(bin)
	cli := newClient(ctx, addr)
	put := func(k string, b *blob) {
		b.pos = 0
		_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(k), Body: b, ContentLength: aws.Int64(b.total())})
		if err != nil {
			check("PutObject "+k, false, "%v", err)
			os.Exit(1)
		}
	}
	_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err == nil, "%v", err)
	stage := func(keys []string, tier string) time.Duration {
		put2 := func() {
			srv, addr, _ = startServer(bin)
			cli = newClient(ctx, addr)
			for _, k := range keys {
				put(k, objs[k])
			}
			stop(srv)
		}
		put2()
		t := time.Now()
		out, err := exec.Command(bin, "compact", "-store", storeDir, "-pack-size-mib", pack, "-tier", tier).CombinedOutput()
		check("compact -tier "+tier, err == nil, "%v %s", err, out)
		return time.Since(t)
	}
	stop(srv)
	tHot := stage([]string{"a", "b"}, "hot")
	check("a hot-only store stays below format 5", formatVersion(storeDir) < 5, "format %d", formatVersion(storeDir))
	tWarm := stage([]string{"c"}, "warm")
	check("first warm pack raised FORMAT.json to 5", formatVersion(storeDir) == 5, "format %d", formatVersion(storeDir))
	tCold := stage([]string{"d"}, "cold")
	srv, addr, _ = startServer(bin)
	cli = newClient(ctx, addr)
	put("e", mixed)
	stop(srv)
	objs["e"] = mixed
	fmt.Printf("RECORD compact hot_ms=%d warm_ms=%d cold_ms=%d\n", tHot.Milliseconds(), tWarm.Milliseconds(), tCold.Milliseconds())

	readAll := func(what string) {
		srv, addr, _ := startServer(bin)
		defer stop(srv)
		cli := newClient(ctx, addr)
		bad := 0
		for k, b := range objs {
			resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
			if err != nil {
				bad++
				continue
			}
			b.pos = 0
			if sha(resp.Body) != sha(b) {
				bad++
			}
			resp.Body.Close()
			total := b.total()
			for _, r := range [][2]int64{{0, 100}, {total/2 - 200_000, 400_000}, {total - 1000, 1000}} {
				resp, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k), Range: aws.String(fmt.Sprintf("bytes=%d-%d", r[0], r[0]+r[1]-1))})
				if err != nil || sha(resp.Body) != b.slice(r[0], r[1]) {
					bad++
				}
				if err == nil {
					resp.Body.Close()
				}
			}
		}
		check(what+": full and ranged GET are byte-exact", bad == 0, "%d mismatches", bad)
		out, err := exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
		check(what+": verify -deep", err == nil, "%v\n%s", err, out)
	}

	var st statusJSON
	run(&st, "tier", "status", "-store", storeDir, "-json")
	check("tier status: all three tiers populated, markers ok, loose chunks left in hot",
		st.StoreFormatVersion == 5 && len(st.Tiers) == 3 && st.Tiers[0].PackCount > 0 && st.Tiers[1].PackCount > 0 && st.Tiers[2].PackCount > 0 &&
			st.Tiers[1].Marker == "ok" && st.Tiers[2].Marker == "ok" && st.LooseChunks > 0, "%+v", st)
	accounting := func(what string) statusJSON {
		var s statusJSON
		run(&s, "tier", "status", "-store", storeDir, "-json")
		var agg packSummaryJSON
		run(&agg, "stats", "-store", storeDir, "-json")
		sumN, sumB := 0, int64(0)
		ok := true
		for i, name := range []string{"packs", "tiers/warm/packs", "tiers/cold/packs"} {
			n, bytes := dirPacks(filepath.Join(storeDir, name))
			ok = ok && n == s.Tiers[i].PackCount && bytes == s.Tiers[i].PackFileBytes
			sumN, sumB = sumN+n, sumB+bytes
		}
		check(what+": directory accounting matches tier status and aggregate stats", ok && sumN == agg.PackCount && sumB == agg.PackFileBytes, "%+v vs stats %+v", s, agg)
		return s
	}
	accounting("initial")
	readAll("mixed tiers")

	// Dry-run is the default and mutates nothing.
	fp := dirFingerprint()
	var dry moveJSON
	run(&dry, "tier", "move", "-store", storeDir, "-from", "hot", "-to", "warm", "-all", "-json")
	check("tier move defaults to dry-run", dry.DryRun && len(dry.Packs) > 0 && dirFingerprint() == fp, "%+v", dry)
	out, err := exec.Command(bin, "tier", "move", "-store", storeDir, "-from", "hot", "-to", "warm").CombinedOutput()
	check("tier move needs -pack or -all", err != nil, "%s", out)
	out, err = exec.Command(bin, "tier", "move", "-store", storeDir, "-from", "hot", "-to", "hot", "-all").CombinedOutput()
	check("tier move rejects equal tiers", err != nil, "%s", out)

	// hot -> warm, restart, warm -> cold, restart.
	var mv moveJSON
	rss1, d1 := runRSS(&mv, "tier", "move", "-store", storeDir, "-from", "hot", "-to", "warm", "-all", "-apply", "-json")
	check("hot -> warm", mv.PacksMoved > 0 && !mv.DryRun, "%+v", mv)
	hotMoved, hotBytes := mv.PacksMoved, mv.BytesMoved
	s := accounting("after hot->warm")
	check("no hot packs remain", s.Tiers[0].PackCount == 0, "%+v", s.Tiers[0])
	readAll("after hot->warm")
	rss2, d2 := runRSS(&mv, "tier", "move", "-store", storeDir, "-from", "warm", "-to", "cold", "-all", "-apply", "-json")
	s = accounting("after warm->cold")
	check("warm -> cold leaves everything cold", mv.PacksMoved > 0 && s.Tiers[0].PackCount == 0 && s.Tiers[1].PackCount == 0 && s.Tiers[2].PackCount > 0, "%+v", s)
	fmt.Printf("RECORD move hot_to_warm_bytes=%d ms=%d mib_s=%.1f peak_rss_kib=%d | warm_to_cold_bytes=%d ms=%d mib_s=%.1f peak_rss_kib=%d packs=%d\n",
		hotBytes, d1.Milliseconds(), mibs(hotBytes, d1), rss1, mv.BytesMoved, d2.Milliseconds(), mibs(mv.BytesMoved, d2), rss2, hotMoved)
	var again moveJSON
	run(&again, "tier", "move", "-store", storeDir, "-from", "hot", "-to", "warm", "-all", "-apply", "-json")
	check("re-running a finished move is a no-op", again.PacksMoved == 0, "%+v", again)
	readAll("all cold")
	coldOpen := readyTime(bin)

	// One cold pack back to hot: mixed physical placement again.
	var packID string
	if ents, _ := os.ReadDir(filepath.Join(storeDir, "tiers", "cold", "packs")); len(ents) > 0 {
		packID = strings.TrimSuffix(ents[0].Name(), ".pack")
	}
	run(&mv, "tier", "move", "-store", storeDir, "-from", "cold", "-to", "hot", "-pack", packID, "-apply", "-json")
	check("cold -> hot (one pack)", mv.PacksMoved == 1, "%+v", mv)
	readAll("one hot pack, rest cold")

	// Retention: delete d, overwrite c; prune; gc and a tier-preserving repack.
	srv, addr, _ = startServer(bin)
	cli = newClient(ctx, addr)
	_, err = cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("d")})
	check("DeleteObject d", err == nil, "%v", err)
	objs["c"] = &blob{segs: []seg{{3005, S}}}
	put("c", objs["c"])
	stop(srv)
	delete(objs, "d")
	objs["e"] = mixed // e still references d's chunks (d's history is the only other pin)
	out, err = exec.Command(bin, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-keep-last", "0", "-apply").CombinedOutput()
	check("versions prune", err == nil, "%v %s", err, out)
	var dead, gc, fin packSummaryJSON
	run(&dead, "gc", "-store", storeDir, "-json")
	check("history prune made packed records dead", dead.LiveSetOK && dead.PackedDeadChunkCount > 0, "%+v", dead)
	before := accounting("before gc/repack")
	run(&gc, "gc", "-store", storeDir, "-apply", "-json")
	var rp repackJSON
	run(&rp, "repack", "-store", storeDir, "-apply", "-max-live-percent", "100", "-pack-size-mib", pack, "-json")
	run(&fin, "gc", "-store", storeDir, "-json")
	after := accounting("after gc and repack")
	check("gc + repack reclaimed every dead packed record", fin.PackedDeadChunkCount == 0 && fin.ChunksUnreachable == 0, "%+v", fin)
	check("repack preserved tiers", (before.Tiers[2].PackCount == 0) == (after.Tiers[2].PackCount == 0) && (before.Tiers[0].PackCount == 0) == (after.Tiers[0].PackCount == 0) && after.Tiers[1].PackCount == 0,
		"before %+v after %+v", before.Tiers, after.Tiers)
	readAll("after prune, gc, repack")

	// Compact the remaining loose data into warm: all three tiers again.
	t0 := time.Now()
	out, err = exec.Command(bin, "compact", "-store", storeDir, "-pack-size-mib", pack, "-tier", "warm").CombinedOutput()
	check("compact remaining loose chunks -tier warm", err == nil, "%v %s", err, out)
	rpWarm := time.Now()
	final := accounting("final")
	check("hot, warm, and cold all hold packs", final.Tiers[0].PackCount > 0 && final.Tiers[1].PackCount > 0 && final.Tiers[2].PackCount > 0, "%+v", final.Tiers)
	readAll("final")
	var rpw repackJSON
	run(&rpw, "repack", "-store", storeDir, "-tier", "warm", "-apply", "-pack-size-mib", pack, "-json")
	check("tmp directories are empty", emptyDir(filepath.Join(storeDir, "tmp")) && emptyDir(filepath.Join(storeDir, "tiers", "warm", "tmp")) && emptyDir(filepath.Join(storeDir, "tiers", "cold", "tmp")), "leftover staging files")
	fmt.Printf("RECORD final compact_warm_ms=%d open_ms: cold_only=%d three_tiers=%d | pack_files hot=%d warm=%d cold=%d\n",
		rpWarm.Sub(t0).Milliseconds(), coldOpen.Milliseconds(), readyTime(bin).Milliseconds(), final.Tiers[0].PackCount, final.Tiers[1].PackCount, final.Tiers[2].PackCount)
	check("FORMAT.json is still 5 and never downgrades", formatVersion(storeDir) == 5, "format %d", formatVersion(storeDir))

	if baselineBinary != "" {
		out, err := exec.Command(baselineBinary, "stats", "-store", storeDir).CombinedOutput()
		check("the Z2-08 build refuses the v5 store", err != nil && strings.Contains(string(out), "unsupported store format version 5"), "%v %s", err, out)
		formatCompat(ctx, S)
	}
	if failed {
		os.Exit(1)
	}
}

// formatCompat: a store built entirely by the previous build (formats 1-4)
// opens under this build, reaches 5 only at the first non-hot pack, and stays
// at 5 once everything is hot again.
func formatCompat(ctx context.Context, S int64) {
	old := storeDir
	defer func() { storeDir = old }()
	var err error
	if storeDir, err = os.MkdirTemp("", "zeros3-z2-tiers-compat-"); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)
	srv, addr, _ := startServer(baselineBinary)
	cli := newClient(ctx, addr)
	cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	for _, o := range []struct {
		key  string
		seed uint64
	}{{"k", 7000}, {"k", 7002}, {"k2", 7001}} { // two generations of k, so prune has history to retire
		b := &blob{segs: []seg{{o.seed, S}}}
		cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(o.key), Body: b, ContentLength: aws.Int64(b.total())})
	}
	stop(srv)
	out, err := exec.Command(baselineBinary, "compact", "-store", storeDir, "-pack-size-mib", fmt.Sprint(segMiB)).CombinedOutput()
	check("previous build: compact", err == nil, "%v %s", err, out)
	out, err = exec.Command(baselineBinary, "versions", "prune", "-store", storeDir, "-bucket", bucket, "-keep-last", "0", "-apply").CombinedOutput()
	check("previous build: prune (format 4)", err == nil && formatVersion(storeDir) == 4, "%v %s format %d", err, out, formatVersion(storeDir))
	out, err = exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("this build opens the formats 1-4 store", err == nil, "%v %s", err, out)
	var st statusJSON
	run(&st, "tier", "status", "-store", storeDir, "-json")
	check("hot-only store: tier status ok, markers absent", st.StoreFormatVersion == 4 && st.Tiers[0].PackCount > 0 && st.Tiers[1].Marker == "absent", "%+v", st)
	srv, addr, _ = startServer(bin)
	cli = newClient(ctx, addr)
	b := &blob{segs: []seg{{7010, S}}}
	cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("new"), Body: b, ContentLength: aws.Int64(b.total())})
	stop(srv)
	out, err = exec.Command(bin, "compact", "-store", storeDir, "-pack-size-mib", fmt.Sprint(segMiB), "-tier", "cold").CombinedOutput()
	check("first cold publication raises format 4 -> 5", err == nil && formatVersion(storeDir) == 5, "%v %s format %d", err, out, formatVersion(storeDir))
	out, err = exec.Command(baselineBinary, "stats", "-store", storeDir).CombinedOutput()
	check("previous build refuses the upgraded store", err != nil && strings.Contains(string(out), "unsupported store format version 5"), "%v %s", err, out)
	run(&moveJSON{}, "tier", "move", "-store", storeDir, "-from", "cold", "-to", "hot", "-all", "-apply", "-json")
	run(&st, "tier", "status", "-store", storeDir, "-json")
	check("everything hot again: format stays 5", st.StoreFormatVersion == 5 && st.Tiers[2].PackCount == 0 && formatVersion(storeDir) == 5, "%+v", st)
	out, err = exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("verify -deep after round trip", err == nil, "%v %s", err, out)
}

func mibs(n int64, d time.Duration) float64 { return float64(n) / (1 << 20) / d.Seconds() }

func dirPacks(dir string) (int, int64) {
	n, total := 0, int64(0)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".pack") {
			info, _ := e.Info()
			n++
			total += info.Size()
		}
	}
	return n, total
}

func emptyDir(dir string) bool {
	ents, err := os.ReadDir(dir)
	return err == nil && len(ents) == 0
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

// runRSS is run, additionally reporting the child's wall time and peak RSS
// (VmHWM sampled from /proc while it runs; getrusage would inherit this
// process's own high-water mark across fork).
func runRSS(v any, args ...string) (int64, time.Duration) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	t := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL: zeros3 %s: %v\n", strings.Join(args, " "), err)
		os.Exit(1)
	}
	var peak int64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Millisecond):
			}
			b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
					var kib int64
					fmt.Sscan(rest, &kib)
					peak = max(peak, kib)
				}
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	d := time.Since(t)
	if err != nil {
		fmt.Printf("FAIL: zeros3 %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		os.Exit(1)
	}
	if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
		fmt.Printf("FAIL: zeros3 %s: bad JSON: %v\n", strings.Join(args, " "), err)
		os.Exit(1)
	}
	return peak, d
}

func formatVersion(dir string) int {
	var f struct {
		V int `json:"store_format_version"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
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

func readyTime(binary string) time.Duration {
	srv, _, ready := startServer(binary)
	stop(srv)
	return ready
}

func startServer(binary string) (*exec.Cmd, string, time.Duration) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(binary, "serve", "-store", storeDir, "-addr", addr)
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
