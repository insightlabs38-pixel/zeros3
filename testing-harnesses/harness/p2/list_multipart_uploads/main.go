// Ephemeral, external interoperability harness for ZeroS3 P2:
// ListMultipartUploads prefix/delimiter/CommonPrefixes/pagination.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as a
// black-box client. It drives the real ListMultipartUploads API against a
// real zeros3 server binary over ordinary HTTP with path-style addressing,
// using only the AWS SDK's low-level S3 client calls (never any internal
// ZeroS3 API).
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sort"
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
)

var pass, fail int

func check(name string, err error) {
	if err != nil {
		fail++
		fmt.Printf("FAIL: %s: %v\n", name, err)
		return
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
}

func requireTrue(name string, cond bool, detail string) {
	if !cond {
		fail++
		fmt.Printf("FAIL: %s: %s\n", name, detail)
		return
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("zeros3 did not start listening on %s in time", addr)
}

type server struct {
	dir  string
	addr string
	cmd  *exec.Cmd
}

// startServerAt starts serve with explicit -access-key/-secret-key/-region
// flags matching this harness's client credentials -- required because P1
// added environment-variable credential fallback, and this sandbox's own
// outbound-proxy tooling sets AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY
// process-wide (inherited by the subprocess by default), which would
// otherwise silently override the intended static credentials exactly the
// way STATUS.md's P1-A section documents.
func startServerAt(binPath, dir string) *server {
	addr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(binPath, "serve", "-store", dir, "-addr", addr,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("starting zeros3 (store=%s): %v", dir, err)
	}
	if err := waitReady(addr, 5*time.Second); err != nil {
		cmd.Process.Kill()
		log.Fatalf("zeros3 (store=%s): %v", dir, err)
	}
	return &server{dir: dir, addr: addr, cmd: cmd}
}

func (s *server) endpoint() string { return "http://" + s.addr }

func (s *server) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.cmd.Process.Kill()
	s.cmd.Wait()
}

func newClient(endpoint string) *s3.Client {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		log.Fatalf("config.LoadDefaultConfig: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func mustCreateUpload(ctx context.Context, client *s3.Client, bucket, key string) string {
	out, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("CreateMultipartUpload key="+key, err)
	if out == nil {
		return ""
	}
	return aws.ToString(out.UploadId)
}

func sortedStrings(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()

	// =========================================================================
	// Phase 1: prefix filtering.
	// =========================================================================
	fmt.Println("\n=== Phase 1: prefix filtering ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-prefix-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "prefix-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 1)", err)

		for _, k := range []string{"alpha/a", "alpha/b", "alpha/sub/c", "beta/d"} {
			mustCreateUpload(ctx, client, bucket, k)
		}

		out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String("alpha/")})
		check("ListMultipartUploads prefix=alpha/", err)
		if out != nil {
			var got []string
			for _, u := range out.Uploads {
				got = append(got, aws.ToString(u.Key))
			}
			want := []string{"alpha/a", "alpha/b", "alpha/sub/c"}
			requireTrue("prefix=alpha/ returns exactly alpha/a, alpha/b, alpha/sub/c",
				stringSlicesEqual(sortedStrings(got), sortedStrings(want)), fmt.Sprintf("got %v", got))
			requireTrue("beta/d excluded by prefix=alpha/", !contains(got, "beta/d"), "beta/d leaked into prefix-filtered listing")
		}

		zero, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String("no-such-prefix/")})
		check("ListMultipartUploads prefix matching zero uploads", err)
		if zero != nil {
			requireTrue("prefix matching zero uploads returns an empty list", len(zero.Uploads) == 0, fmt.Sprintf("got %d uploads", len(zero.Uploads)))
		}
	}()

	// =========================================================================
	// Phase 2: delimiter + CommonPrefixes (the exact documented fixture).
	// =========================================================================
	fmt.Println("\n=== Phase 2: delimiter + CommonPrefixes ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-delim-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "delim-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 2)", err)

		for _, k := range []string{"a/file1", "a/file2", "a/sub/file3", "a/sub/file4", "a/sub2/file5", "b/file6"} {
			mustCreateUpload(ctx, client, bucket, k)
		}

		out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), Prefix: aws.String("a/"), Delimiter: aws.String("/"),
		})
		check("ListMultipartUploads prefix=a/&delimiter=/", err)
		if out != nil {
			var gotUploads, gotCP []string
			for _, u := range out.Uploads {
				gotUploads = append(gotUploads, aws.ToString(u.Key))
			}
			for _, cp := range out.CommonPrefixes {
				gotCP = append(gotCP, aws.ToString(cp.Prefix))
			}
			requireTrue("direct uploads are exactly a/file1, a/file2",
				stringSlicesEqual(sortedStrings(gotUploads), sortedStrings([]string{"a/file1", "a/file2"})),
				fmt.Sprintf("got %v", gotUploads))
			requireTrue("CommonPrefixes are exactly a/sub/, a/sub2/",
				stringSlicesEqual(sortedStrings(gotCP), sortedStrings([]string{"a/sub/", "a/sub2/"})),
				fmt.Sprintf("got %v", gotCP))
			requireTrue("underlying grouped uploads (a/sub/file3, a/sub/file4, a/sub2/file5) are not returned as direct Upload entries",
				!contains(gotUploads, "a/sub/file3") && !contains(gotUploads, "a/sub/file4") && !contains(gotUploads, "a/sub2/file5"),
				fmt.Sprintf("leaked into direct uploads: %v", gotUploads))

			// Ordering: verify the combined logical order (direct upload
			// keys and CommonPrefix keys merged) is ascending -- the
			// server-side traversal must respect a single ordered
			// namespace, not two independently-sorted lists concatenated.
			combined := append(append([]string{}, gotUploads...), gotCP...)
			sortedCombined := sortedStrings([]string{"a/file1", "a/file2", "a/sub/", "a/sub2/"})
			requireTrue("combined direct+CommonPrefix set matches the expected logical namespace",
				stringSlicesEqual(sortedStrings(combined), sortedCombined), fmt.Sprintf("got %v want %v", combined, sortedCombined))
		}
	}()

	// =========================================================================
	// Phase 3: pagination through a mixed Upload/CommonPrefix stream.
	// =========================================================================
	fmt.Println("\n=== Phase 3: pagination through mixed Upload/CommonPrefix stream ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-page-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "page-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 3)", err)

		// Logical stream (prefix=a/, delimiter=/): a/file1, a/sub/ (group
		// of 2), a/z -- 3 logical slots.
		mustCreateUpload(ctx, client, bucket, "a/file1")
		mustCreateUpload(ctx, client, bucket, "a/sub/x")
		mustCreateUpload(ctx, client, bucket, "a/sub/y")
		mustCreateUpload(ctx, client, bucket, "a/z")

		var seenUploads, seenCP []string
		var pages int
		keyMarker, uploadIDMarker := (*string)(nil), (*string)(nil)
		for {
			pages++
			if pages > 10 {
				fail++
				fmt.Println("FAIL: pagination did not terminate (possible infinite loop)")
				break
			}
			out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
				Bucket: aws.String(bucket), Prefix: aws.String("a/"), Delimiter: aws.String("/"),
				MaxUploads: aws.Int32(1), KeyMarker: keyMarker, UploadIdMarker: uploadIDMarker,
			})
			check(fmt.Sprintf("ListMultipartUploads page %d", pages), err)
			if out == nil {
				break
			}
			requireTrue(fmt.Sprintf("page %d has at most 1 logical result", pages), len(out.Uploads)+len(out.CommonPrefixes) <= 1,
				fmt.Sprintf("uploads=%d commonPrefixes=%d", len(out.Uploads), len(out.CommonPrefixes)))
			for _, u := range out.Uploads {
				seenUploads = append(seenUploads, aws.ToString(u.Key))
			}
			for _, cp := range out.CommonPrefixes {
				seenCP = append(seenCP, aws.ToString(cp.Prefix))
			}
			if !aws.ToBool(out.IsTruncated) {
				break
			}
			keyMarker, uploadIDMarker = out.NextKeyMarker, out.NextUploadIdMarker
		}
		requireTrue("pagination visited exactly 3 pages (3 logical slots, max-uploads=1)", pages == 3, fmt.Sprintf("got %d pages", pages))
		requireTrue("direct uploads across all pages are exactly a/file1, a/z",
			stringSlicesEqual(sortedStrings(seenUploads), sortedStrings([]string{"a/file1", "a/z"})), fmt.Sprintf("got %v", seenUploads))
		requireTrue("CommonPrefixes across all pages are exactly a/sub/ (once)",
			stringSlicesEqual(seenCP, []string{"a/sub/"}), fmt.Sprintf("got %v", seenCP))
	}()

	// =========================================================================
	// Phase 4: same-key multipart sessions (several upload IDs, one key).
	// =========================================================================
	fmt.Println("\n=== Phase 4: same-key multipart sessions ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-samekey-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "samekey-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 4)", err)

		var ids []string
		for i := 0; i < 4; i++ {
			ids = append(ids, mustCreateUpload(ctx, client, bucket, "dup"))
		}
		wantIDs := sortedStrings(ids)

		var seenIDs []string
		var pages int
		keyMarker, uploadIDMarker := (*string)(nil), (*string)(nil)
		for {
			pages++
			if pages > 10 {
				fail++
				fmt.Println("FAIL: same-key pagination did not terminate")
				break
			}
			out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
				Bucket: aws.String(bucket), MaxUploads: aws.Int32(1), KeyMarker: keyMarker, UploadIdMarker: uploadIDMarker,
			})
			check(fmt.Sprintf("ListMultipartUploads same-key page %d", pages), err)
			if out == nil {
				break
			}
			for _, u := range out.Uploads {
				seenIDs = append(seenIDs, aws.ToString(u.UploadId))
			}
			if !aws.ToBool(out.IsTruncated) {
				break
			}
			keyMarker, uploadIDMarker = out.NextKeyMarker, out.NextUploadIdMarker
		}
		requireTrue("all 4 upload IDs for the same key returned exactly once, in ascending order",
			stringSlicesEqual(seenIDs, wantIDs), fmt.Sprintf("got %v want %v", seenIDs, wantIDs))
	}()

	// =========================================================================
	// Phase 5: 1500+ uploads, full paginated traversal.
	// =========================================================================
	fmt.Println("\n=== Phase 5: 1500+ uploads, full paginated traversal ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-scale-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "scale-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 5)", err)

		const n = 1500
		want := map[string]bool{}
		createErrs := 0
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("scale/%05d", i)
			id := mustCreateUpload(ctx, client, bucket, key)
			if id == "" {
				createErrs++
				continue
			}
			want[key] = true
		}
		requireTrue("all 1500 CreateMultipartUpload calls succeeded", createErrs == 0, fmt.Sprintf("%d failures", createErrs))

		seen := map[string]bool{}
		var duplicates int
		var pages int
		keyMarker, uploadIDMarker := (*string)(nil), (*string)(nil)
		for {
			pages++
			if pages > n+5 {
				fail++
				fmt.Println("FAIL: scale traversal did not terminate")
				break
			}
			out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
				Bucket: aws.String(bucket), KeyMarker: keyMarker, UploadIdMarker: uploadIDMarker,
			})
			if err != nil {
				check(fmt.Sprintf("ListMultipartUploads scale page %d", pages), err)
				break
			}
			for _, u := range out.Uploads {
				k := aws.ToString(u.Key)
				if seen[k] {
					duplicates++
				}
				seen[k] = true
			}
			if !aws.ToBool(out.IsTruncated) {
				break
			}
			keyMarker, uploadIDMarker = out.NextKeyMarker, out.NextUploadIdMarker
		}
		requireTrue("scale traversal used more than one page (default max-uploads=1000 < 1500)", pages > 1, fmt.Sprintf("got %d pages", pages))
		requireTrue("scale traversal produced zero duplicate keys", duplicates == 0, fmt.Sprintf("%d duplicates", duplicates))
		requireTrue("scale traversal's exact final count is 1500, no omissions", len(seen) == len(want), fmt.Sprintf("got %d, want %d", len(seen), len(want)))
	}()

	// =========================================================================
	// Phase 6: weird keys (Unicode, spaces, %, #, ?, XML-sensitive chars).
	// =========================================================================
	fmt.Println("\n=== Phase 6: weird keys ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-weird-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		defer srv.stop()
		client := newClient(srv.endpoint())
		const bucket = "weird-bucket"
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 6)", err)

		weirdKeys := []string{
			"a b/space file",
			"100%/percent",
			"#tag/hash",
			"q?/question",
			"日本語/ファイル",
			"xml/<tag>&'quoted\"",
		}
		for _, k := range weirdKeys {
			mustCreateUpload(ctx, client, bucket, k)
		}

		out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
		check("ListMultipartUploads over weird keys", err)
		if out != nil {
			var got []string
			for _, u := range out.Uploads {
				got = append(got, aws.ToString(u.Key))
			}
			requireTrue("every weird key round-trips exactly",
				stringSlicesEqual(sortedStrings(got), sortedStrings(weirdKeys)), fmt.Sprintf("got %v want %v", got, weirdKeys))
		}

		unicodeOut, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), Prefix: aws.String("日本語/"), Delimiter: aws.String("/"),
		})
		check("ListMultipartUploads prefix over a Unicode segment", err)
		if unicodeOut != nil {
			requireTrue("Unicode-prefixed listing returns exactly one direct upload",
				len(unicodeOut.Uploads) == 1 && aws.ToString(unicodeOut.Uploads[0].Key) == "日本語/ファイル",
				fmt.Sprintf("got %+v", unicodeOut.Uploads))
		}
	}()

	// =========================================================================
	// Phase 7: restart -- listing must be identical, no persistent-format
	// change.
	// =========================================================================
	fmt.Println("\n=== Phase 7: restart -- listing identical ===")
	func() {
		dir, err := os.MkdirTemp("", "zeros3-p2-lmpu-restart-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		srv := startServerAt(binPath, dir)
		const bucket = "restart-bucket"
		client := newClient(srv.endpoint())
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket (phase 7)", err)
		for _, k := range []string{"a/file1", "a/sub/x", "a/sub/y", "b/file2"} {
			mustCreateUpload(ctx, client, bucket, k)
		}

		before, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), Delimiter: aws.String("/"),
		})
		check("ListMultipartUploads before restart", err)
		srv.stop()

		srv2 := startServerAt(binPath, dir)
		defer srv2.stop()
		client2 := newClient(srv2.endpoint())
		after, err := client2.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), Delimiter: aws.String("/"),
		})
		check("ListMultipartUploads after restart", err)

		if before != nil && after != nil {
			var beforeU, afterU, beforeCP, afterCP []string
			for _, u := range before.Uploads {
				beforeU = append(beforeU, aws.ToString(u.Key))
			}
			for _, u := range after.Uploads {
				afterU = append(afterU, aws.ToString(u.Key))
			}
			for _, cp := range before.CommonPrefixes {
				beforeCP = append(beforeCP, aws.ToString(cp.Prefix))
			}
			for _, cp := range after.CommonPrefixes {
				afterCP = append(afterCP, aws.ToString(cp.Prefix))
			}
			requireTrue("direct uploads identical before/after restart",
				stringSlicesEqual(sortedStrings(beforeU), sortedStrings(afterU)), fmt.Sprintf("before=%v after=%v", beforeU, afterU))
			requireTrue("CommonPrefixes identical before/after restart",
				stringSlicesEqual(sortedStrings(beforeCP), sortedStrings(afterCP)), fmt.Sprintf("before=%v after=%v", beforeCP, afterCP))
		}
	}()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
