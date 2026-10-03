// z2_pack_compression exercises adaptive packed-record compression through a
// real zeros3 binary.
//
// Phase 1 ingests one deterministic object per data family into its own
// store over S3, compacts a copy raw (-compression off) and a copy adaptive,
// and compares physical size, record mix, compact throughput and peak RSS,
// then restarts a server on each and compares full-GET throughput, ranged-GET
// latency and open time with every byte checked.
//
// Phase 2 builds one store through the whole lifecycle: old raw packs
// (served by the current binary), new loose writes, an adaptive compact that
// yields mixed raw/deflate packs, restart, byte-exact full and ranged reads,
// stats accounting, an adaptive repack that reclaims dead records and
// recompresses the old raw ones, and deep verify.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_pack_compression [-class-mib 16] [-combined-mib 64]
package main

import (
	"bytes"
	"compress/flate"
	"context"
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
	blockSize       = 64 << 10
	rangeLen        = 256 << 10
	rangeSamples    = 16
	partSize        = 5 << 20
	// Small enough that even a heavily deduplicated family fills a pack.
	classPackMiB = 2
)

var (
	failed bool
	maxRSS int64
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

var words = strings.Fields("the of and to in is that for it as was with be by on not he this are or his from at which but have an had they you were their one all we can her has there been if more when will would who so no")

// object is a deterministic byte stream of one data family, built from
// 64 KiB blocks so any window can be regenerated for range checks.
type object struct {
	kind  string
	seed  uint64
	size  int64
	pos   int64
	cache map[int64][]byte
}

func newObject(kind string, seed uint64, size int64) *object {
	return &object{kind: kind, seed: seed, size: size, cache: map[int64][]byte{}}
}

func (o *object) block(idx int64) []byte {
	if b, ok := o.cache[idx]; ok {
		return b
	}
	st := splitmix(o.seed<<32 ^ uint64(idx))
	next := func() uint64 { st = splitmix(st); return st }
	kind := o.kind
	switch kind {
	case "mixed": // 512 KiB segments alternating text and random
		kind = "english"
		if idx/8%2 == 1 {
			kind = "random"
		}
	case "duplicate": // a 1 MiB random pattern repeated
		idx %= 16
		st = splitmix(o.seed<<32 ^ uint64(idx))
		kind = "random"
	}
	var b bytes.Buffer
	text := func(gen func()) {
		for b.Len() < blockSize {
			gen()
		}
	}
	switch kind {
	case "english":
		text(func() {
			n := uint64(len(words))
			b.WriteString(words[next()%n*(next()%n)/n])
			switch next() % 12 {
			case 0:
				b.WriteString(".\n")
			case 1:
				b.WriteString(", ")
			default:
				b.WriteByte(' ')
			}
		})
	case "json":
		text(func() {
			fmt.Fprintf(&b, `{"id":%d,"name":"user-%d","email":"user%d@example.com","active":%t,"score":%d.%02d,"tags":["alpha","beta"],"created":"2026-%02d-%02dT%02d:00:00Z"}`+"\n",
				next()%1000000, next()%5000, next()%5000, next()%2 == 0, next()%100, next()%100, 1+next()%12, 1+next()%28, next()%24)
		})
	case "web":
		i := 0
		text(func() {
			switch i++; i % 3 {
			case 0:
				fmt.Fprintf(&b, `<div class="card card--%d"><a href="/p/%d" class="link">%s</a><span data-id="%d"></span></div>`+"\n", next()%9, next()%9999, words[next()%uint64(len(words))], next()%99999)
			case 1:
				fmt.Fprintf(&b, `.c%d{margin:0;padding:%dpx;color:#%06x;display:flex}`+"\n", next()%999, next()%40, next()%(1<<24))
			default:
				fmt.Fprintf(&b, "function f%d(a,b){var x=a+b*%d;return document.getElementById('n%d').textContent=x;}\n", next()%999, next()%99, next()%999)
			}
		})
	case "gzip": // deflated text: high-entropy, already-compressed content
		var raw bytes.Buffer
		fw, _ := flate.NewWriter(&raw, 6)
		o2 := newObject("english", o.seed, 0)
		for i := int64(0); raw.Len() < blockSize; i++ {
			fw.Write(o2.block(idx*4096 + i))
			fw.Flush()
		}
		b.Write(raw.Bytes())
	default: // random
		var w [8]byte
		for b.Len() < blockSize {
			binary.LittleEndian.PutUint64(w[:], next())
			b.Write(w[:])
		}
	}
	out := b.Bytes()[:blockSize]
	if len(o.cache) > 4 {
		o.cache = map[int64][]byte{}
	}
	o.cache[idx] = out
	return out
}

func (o *object) fill(p []byte, off int64) {
	for i := 0; i < len(p); {
		at := off + int64(i)
		i += copy(p[i:], o.block(at / blockSize)[at%blockSize:])
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
	LooseChunkFileBytes  int64   `json:"loose_chunk_file_bytes"`
	PackCount            int     `json:"pack_count"`
	PackedChunkCount     int     `json:"packed_chunk_count"`
	PackFileBytes        int64   `json:"pack_file_bytes"`
	PackedRawRecords     int     `json:"packed_raw_records"`
	PackedCompressedRecs int     `json:"packed_compressed_records"`
	PackedLogicalBytes   int64   `json:"packed_logical_bytes"`
	PackedStoredBytes    int64   `json:"packed_stored_bytes"`
	PackCompressionSaved int64   `json:"pack_compression_saved_bytes"`
	PackCompressionRatio float64 `json:"pack_compression_ratio"`
	PackedDeadChunkCount int     `json:"packed_dead_chunk_count"`
}

type compactJSON struct {
	ChunksPacked      int   `json:"chunks_packed"`
	PacksWritten      int   `json:"packs_written"`
	PackBytes         int64 `json:"pack_bytes"`
	RawRecords        int   `json:"raw_records"`
	CompressedRecords int   `json:"compressed_records"`
	LogicalBytes      int64 `json:"logical_bytes"`
	StoredBytes       int64 `json:"stored_bytes"`
}

type repackJSON struct {
	PacksSelected     int   `json:"packs_selected"`
	RecordsCopied     int   `json:"records_copied"`
	BytesRead         int64 `json:"bytes_read"`
	BytesWritten      int64 `json:"bytes_written"`
	BytesDeleted      int64 `json:"bytes_deleted"`
	BytesReclaimed    int64 `json:"bytes_reclaimed"`
	RawRecords        int   `json:"raw_records"`
	CompressedRecords int   `json:"compressed_records"`
	LogicalBytes      int64 `json:"logical_bytes"`
	StoredBytes       int64 `json:"stored_bytes"`
}

func main() {
	classMiB := flag.Int64("class-mib", 16, "payload per data family in phase 1")
	combinedMiB := flag.Int64("combined-mib", 64, "total payload of the phase 2 lifecycle store")
	packMiB := flag.Int64("pack-size-mib", 8, "target pack size passed to compact and repack")
	rssLimit := flag.Int64("max-rss-mib", 192, "fail if any maintenance command or server peaks above this")
	flag.Parse()
	maxRSS = *rssLimit << 20

	bin := os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	work, err := os.MkdirTemp("", "zeros3-z2-compress-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(work)

	h := &harness{bin: bin, work: work, packMiB: *packMiB, ctx: context.Background()}
	h.phaseClasses(*classMiB << 20)
	h.phaseLifecycle(*combinedMiB << 20)
	if failed {
		os.Exit(1)
	}
}

type harness struct {
	bin, work string
	packMiB   int64
	ctx       context.Context
}

// ---- phase 1: one store per data family -----------------------------------

func (h *harness) phaseClasses(size int64) {
	type want struct {
		name     string
		minSaved float64 // percent of logical bytes; 0 = must stay raw
	}
	classes := []want{{"english", 30}, {"json", 60}, {"web", 50}, {"random", 0}, {"gzip", 0}, {"duplicate", 0}, {"mixed", 15}}
	fmt.Printf("RECORD header phase=class %-9s %9s %9s %9s %7s %7s %6s %9s %9s %8s %8s\n",
		"class", "logical", "raw_pack", "auto_pack", "saved%", "deflate", "raw", "cmp_raw", "cmp_auto", "get_raw", "get_auto")
	for i, c := range classes {
		obj := func() *object { return newObject(c.name, uint64(i+1), size) }
		base := filepath.Join(h.work, "class-"+c.name)
		h.ingest(base, map[string]*object{"obj": obj()})
		rawDir, autoDir := base+"-raw", base+"-auto"
		must(exec.Command("cp", "-a", base, rawDir).Run())
		must(os.Rename(base, autoDir))

		rawC, rawElapsed, rawRSS := h.compact(rawDir, "off", classPackMiB)
		autoC, autoElapsed, autoRSS := h.compact(autoDir, "auto", classPackMiB)
		rawS, autoS := h.stats(rawDir), h.stats(autoDir)
		saved := 100 * float64(autoS.PackedLogicalBytes-autoS.PackedStoredBytes) / float64(autoS.PackedLogicalBytes)
		check(c.name+": every family is packed identically in both modes", rawC.ChunksPacked > 0 && rawC.ChunksPacked == autoC.ChunksPacked && rawS.LooseChunkCount == 0 && autoS.LooseChunkCount == 0, "%+v vs %+v", rawC, autoC)
		check(c.name+": raw-mode compact stores every record raw", rawC.CompressedRecords == 0 && rawC.StoredBytes == rawC.LogicalBytes, "%+v", rawC)
		check(c.name+": stats agree with compact accounting", autoS.PackedCompressedRecs == autoC.CompressedRecords && autoS.PackedRawRecords == autoC.RawRecords &&
			autoS.PackedStoredBytes == autoC.StoredBytes && autoS.PackedLogicalBytes == autoC.LogicalBytes && autoS.PackFileBytes == autoC.PackBytes, "%+v vs %+v", autoS, autoC)
		if c.minSaved == 0 {
			check(c.name+": incompressible data is not expanded", autoS.PackedCompressedRecs == 0 && autoS.PackFileBytes == rawS.PackFileBytes, "auto %d raw %d deflate=%d", autoS.PackFileBytes, rawS.PackFileBytes, autoS.PackedCompressedRecs)
		} else {
			check(c.name+": physical size drops meaningfully", saved >= c.minSaved && autoS.PackFileBytes < rawS.PackFileBytes, "saved %.1f%% (want >= %.0f%%)", saved, c.minSaved)
		}
		check(c.name+": maintenance RSS is bounded", rawRSS <= maxRSS && autoRSS <= maxRSS, "raw %d auto %d MiB", rawRSS>>20, autoRSS>>20)

		rawGet := h.readBack(rawDir, map[string]*object{"obj": obj()}, c.name+"/raw")
		autoGet := h.readBack(autoDir, map[string]*object{"obj": obj()}, c.name+"/auto")
		mib := float64(size >> 20)
		fmt.Printf("RECORD class=%s logical_mib=%d raw_pack_bytes=%d auto_pack_bytes=%d saved_pct=%.1f ratio=%.2f deflate_records=%d raw_records=%d compact_mib_s_raw=%.0f compact_mib_s_auto=%.0f compact_rss_mib_auto=%d get_mib_s_raw=%.0f get_mib_s_auto=%.0f range_ms_raw=%.2f range_ms_auto=%.2f open_ms_raw=%d open_ms_auto=%d\n",
			c.name, size>>20, rawS.PackFileBytes, autoS.PackFileBytes, saved, ratioOf(autoS), autoS.PackedCompressedRecs, autoS.PackedRawRecords,
			mib/rawElapsed.Seconds(), mib/autoElapsed.Seconds(), autoRSS>>20, rawGet.mibps, autoGet.mibps, rawGet.rangeMS, autoGet.rangeMS, rawGet.openMS, autoGet.openMS)
		os.RemoveAll(rawDir)
		os.RemoveAll(autoDir)
	}
}

func ratioOf(s statsJSON) float64 {
	if s.PackedStoredBytes == 0 {
		return 0
	}
	return float64(s.PackedLogicalBytes) / float64(s.PackedStoredBytes)
}

// ---- phase 2: raw packs -> mixed packs -> repack ---------------------------

func (h *harness) phaseLifecycle(total int64) {
	dir := filepath.Join(h.work, "lifecycle")
	kinds := []string{"english", "json", "web", "random", "gzip", "mixed"}
	per := total / int64(len(kinds))
	objs := map[string]*object{}
	for i, k := range kinds {
		objs["old-"+k] = newObject(k, uint64(100+i), per)
	}

	// Old raw packs, including parts of a multipart upload that is later
	// aborted, so their records become dead beside live ones.
	srv, addr := h.serve(dir)
	cli := newClient(h.ctx, addr)
	_, err := cli.CreateBucket(h.ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("lifecycle: CreateBucket", err == nil, "%v", err)
	etags := map[string]string{}
	for k, o := range objs {
		etags[k] = h.put(cli, k, newObject(o.kind, o.seed, o.size))
	}
	mp, err := cli.CreateMultipartUpload(h.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("aborted")})
	must(err)
	dead := newObject("english", 900, 3*partSize)
	for p := int32(1); p <= 3; p++ {
		_, err := cli.UploadPart(h.ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("aborted"), UploadId: mp.UploadId, PartNumber: aws.Int32(p),
			Body: bytes.NewReader(dead.window(int64(p-1)*partSize, partSize))})
		must(err)
	}
	stop(srv)
	_, _, _ = h.compact(dir, "off", h.packMiB)
	check("old raw packs keep the packed store version", h.formatVersion(dir) == 2, "FORMAT.json version %d", h.formatVersion(dir))
	st := h.stats(dir)
	check("old raw packs hold only raw records", st.PackedCompressedRecs == 0 && st.PackedRawRecords > 0, "%+v", st)
	rawPackBytes := st.PackFileBytes
	h.readBack2(dir, objs, etags, "old raw packs")

	srv, addr = h.serve(dir)
	cli = newClient(h.ctx, addr)
	_, err = cli.AbortMultipartUpload(h.ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("aborted"), UploadId: mp.UploadId})
	check("lifecycle: AbortMultipartUpload", err == nil, "%v", err)
	for i, k := range []string{"json", "english", "random"} {
		o := newObject(k, uint64(200+i), per/2)
		objs["new-"+k] = o
		etags["new-"+k] = h.put(cli, "new-"+k, newObject(o.kind, o.seed, o.size))
	}
	stop(srv)

	cres, elapsed, rss := h.compact(dir, "auto", h.packMiB)
	st = h.stats(dir)
	fmt.Printf("RECORD lifecycle step=adaptive-compact chunks=%d raw=%d deflate=%d pack_bytes=%d mib_s=%.0f rss_mib=%d\n",
		cres.ChunksPacked, cres.RawRecords, cres.CompressedRecords, cres.PackBytes, float64(per*3/2>>20)/elapsed.Seconds(), rss>>20)
	check("adaptive compact adds compressed records beside old raw ones", cres.CompressedRecords > 0 && cres.RawRecords > 0 && st.PackedRawRecords > cres.RawRecords, "%+v %+v", cres, st)
	check("compressed packs raise the store format version", h.formatVersion(dir) == 3, "FORMAT.json version %d", h.formatVersion(dir))
	check("lifecycle: compact RSS is bounded", rss <= maxRSS, "%d MiB", rss>>20)
	check("dead records exist before repack", st.PackedDeadChunkCount > 0, "%+v", st)
	h.readBack2(dir, objs, etags, "mixed raw+deflate packs")
	check("deep verify over mixed packs", h.deepVerify(dir), "verify failed")

	before := h.stats(dir)
	out, rrss, relapsed := h.runJSON("repack", "-store", dir, "-apply", "-max-live-percent", "100", "-pack-size-mib", fmt.Sprint(h.packMiB), "-compression", "auto", "-json")
	var rres repackJSON
	check("repack -json parses", json.Unmarshal(out, &rres) == nil, "%s", out)
	after := h.stats(dir)
	fmt.Printf("RECORD lifecycle step=repack selected=%d copied=%d bytes_read=%d bytes_written=%d bytes_reclaimed=%d raw=%d deflate=%d mib_s=%.0f rss_mib=%d old_raw_pack_bytes=%d pack_bytes_before=%d pack_bytes_after=%d\n",
		rres.PacksSelected, rres.RecordsCopied, rres.BytesRead, rres.BytesWritten, rres.BytesReclaimed, rres.RawRecords, rres.CompressedRecords,
		float64(rres.BytesRead>>20)/relapsed.Seconds(), rrss>>20, rawPackBytes, before.PackFileBytes, after.PackFileBytes)
	check("repack leaves no dead packed records", after.PackedDeadChunkCount == 0, "%+v", after)
	check("repack accounting matches physical pack bytes", rres.BytesReclaimed == before.PackFileBytes-after.PackFileBytes && rres.BytesReclaimed > 0, "reported %d actual %d", rres.BytesReclaimed, before.PackFileBytes-after.PackFileBytes)
	check("repack recompresses live records that were raw", rres.CompressedRecords > 0 && rres.StoredBytes < rres.LogicalBytes && rres.BytesRead == rres.LogicalBytes, "%+v", rres)
	check("repack RSS is bounded", rrss <= maxRSS, "%d MiB", rrss>>20)
	h.readBack2(dir, objs, etags, "after repack")
	check("deep verify after repack", h.deepVerify(dir), "verify failed")
	st = h.stats(dir)
	fmt.Printf("RECORD lifecycle step=final packs=%d raw=%d deflate=%d logical=%d stored=%d pack_bytes=%d ratio=%.2f\n",
		st.PackCount, st.PackedRawRecords, st.PackedCompressedRecs, st.PackedLogicalBytes, st.PackedStoredBytes, st.PackFileBytes, ratioOf(st))
}

// ---- helpers ----------------------------------------------------------------

func (h *harness) put(cli *s3.Client, key string, o *object) string {
	out, err := cli.PutObject(h.ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: o, ContentLength: aws.Int64(o.size)})
	if err != nil {
		check("PutObject "+key, false, "%v", err)
		os.Exit(1)
	}
	return aws.ToString(out.ETag)
}

func (h *harness) ingest(dir string, objs map[string]*object) map[string]string {
	srv, addr := h.serve(dir)
	cli := newClient(h.ctx, addr)
	_, err := cli.CreateBucket(h.ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	must(err)
	etags := map[string]string{}
	for k, o := range objs {
		etags[k] = h.put(cli, k, newObject(o.kind, o.seed, o.size))
	}
	stop(srv)
	return etags
}

func (h *harness) compact(dir, mode string, packMiB int64) (compactJSON, time.Duration, int64) {
	out, rss, el := h.runJSON("compact", "-store", dir, "-pack-size-mib", fmt.Sprint(packMiB), "-compression", mode, "-json")
	var res compactJSON
	check("compact -compression "+mode+" -json parses", json.Unmarshal(out, &res) == nil, "%s", out)
	return res, el, rss
}

func (h *harness) runJSON(name string, args ...string) ([]byte, int64, time.Duration) {
	cmd := exec.Command(h.bin, append([]string{name}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	start := time.Now()
	err := cmd.Run()
	el := time.Since(start)
	check("zeros3 "+name, err == nil, "%v", err)
	return out.Bytes(), rssOf(cmd), el
}

func (h *harness) stats(dir string) statsJSON {
	out, err := exec.Command(h.bin, "stats", "-json", "-store", dir).Output()
	var st statsJSON
	if err != nil || json.Unmarshal(out, &st) != nil {
		fmt.Println("FAIL: stats -json:", err)
		os.Exit(1)
	}
	return st
}

func (h *harness) formatVersion(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	var f struct {
		V int `json:"store_format_version"`
	}
	if err != nil || json.Unmarshal(b, &f) != nil {
		return -1
	}
	return f.V
}

func (h *harness) deepVerify(dir string) bool {
	out, err := exec.Command(h.bin, "verify", "-deep", "-store", dir).CombinedOutput()
	if err != nil {
		fmt.Printf("%s\n", out)
	}
	return err == nil
}

type readStats struct {
	mibps, rangeMS float64
	openMS         int64
}

// readBack starts a server on dir (cold cache when possible), times a full
// GET and ranged GETs of every object, and verifies every byte.
func (h *harness) readBack(dir string, objs map[string]*object, label string) readStats {
	return h.readBackImpl(dir, objs, nil, label)
}

func (h *harness) readBack2(dir string, objs map[string]*object, etags map[string]string, label string) {
	h.readBackImpl(dir, objs, etags, label)
}

func (h *harness) readBackImpl(dir string, objs map[string]*object, etags map[string]string, label string) readStats {
	dropCaches()
	srv, addr, ready := h.serveTimed(dir)
	cli := newClient(h.ctx, addr)
	var rs readStats
	rs.openMS = ready.Milliseconds()
	var totalBytes int64
	var fullTime, rangeSum time.Duration
	var ranges int
	exact, rangesExact, etagsOK := true, true, true
	for k, o := range objs {
		start := time.Now()
		rc, err := cli.GetObject(h.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		if err == nil {
			_, err = io.Copy(io.Discard, rc.Body)
			rc.Body.Close()
		}
		fullTime += time.Since(start)
		totalBytes += o.size
		if err == nil {
			if rc, err = cli.GetObject(h.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)}); err == nil {
				_, err = compare(rc.Body, newObject(o.kind, o.seed, o.size))
				rc.Body.Close()
			}
		}
		exact = exact && err == nil
		if err != nil {
			fmt.Printf("  %s %s: %v\n", label, k, err)
		}
		if etags != nil {
			head, err := cli.HeadObject(h.ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
			etagsOK = etagsOK && err == nil && aws.ToString(head.ETag) == etags[k] && aws.ToInt64(head.ContentLength) == o.size
		}
		for i := 0; i < rangeSamples/4; i++ {
			off := int64(splitmix(uint64(i)*7919+uint64(len(k)))) & (1<<62 - 1) % (o.size - rangeLen)
			s := time.Now()
			resp, err := cli.GetObject(h.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k), Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+rangeLen-1))})
			var got []byte
			if err == nil {
				got, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			rangeSum += time.Since(s)
			ranges++
			rangesExact = rangesExact && err == nil && bytes.Equal(got, o.window(off, rangeLen))
		}
	}
	rss := stop(srv)
	check(label+": full GETs are byte-exact", exact, "mismatch")
	check(label+": ranged GETs are byte-exact", rangesExact, "mismatch")
	if etags != nil {
		check(label+": ETags and sizes unchanged", etagsOK, "changed")
	}
	check(label+": server RSS is bounded", rss <= maxRSS, "%d MiB", rss>>20)
	rs.mibps = float64(totalBytes>>20) / fullTime.Seconds()
	rs.rangeMS = float64(rangeSum.Microseconds()) / 1000 / float64(ranges)
	if etags != nil {
		fmt.Printf("RECORD read label=%q full_mib_s=%.0f range_ms=%.2f open_ms=%d server_rss_mib=%d\n", label, rs.mibps, rs.rangeMS, rs.openMS, rss>>20)
	}
	return rs
}

func dropCaches() {
	_ = exec.Command("sync").Run()
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
}

func (h *harness) serve(dir string) (*exec.Cmd, string) {
	srv, addr, _ := h.serveTimed(dir)
	return srv, addr
}

func (h *harness) serveTimed(dir string) (*exec.Cmd, string, time.Duration) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(h.bin, "serve", "-store", dir, "-addr", addr)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	began := time.Now()
	must(srv.Start())
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(2 * time.Millisecond) {
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
	return rssOf(srv)
}

func rssOf(c *exec.Cmd) int64 {
	if c.ProcessState == nil {
		return -1
	}
	if ru, ok := c.ProcessState.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss << 10
	}
	return -1
}

func must(err error) {
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
}

func newClient(ctx context.Context, addr string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")))
	must(err)
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
