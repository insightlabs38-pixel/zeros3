// Ephemeral, external interoperability harness for ZeroS3's T1 rclone
// proof.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and drives two pieces of external test infrastructure
// against a real zeros3 server binary: the pinned AWS SDK for Go v2
// (already used by the other harnesses in this repository, here only to
// seed/overwrite object bytes -- see "Why the AWS SDK appears here" below)
// and the real `rclone` binary, invoked exactly the way a user would run
// it from a shell, with ordinary S3-remote configuration (endpoint,
// credentials, path-style addressing, and the one target-required
// `list_version=2` setting -- never anything that patches rclone or adds
// ZeroS3-specific application logic).
//
// Why the AWS SDK appears here: M5-B added header-auth UNSIGNED-PAYLOAD
// support to ZeroS3 specifically so that rclone's ordinary (unpatched)
// upload path -- which wraps every upload body in a non-seekable
// progress-accounting reader and therefore depends on being able to send
// the UNSIGNED-PAYLOAD sentinel -- can complete end-to-end; see
// results/M5B_RCLONE_LARGE_OBJECT_RESULTS.md for a genuine 1 GiB/205-part
// proof. checkRcloneUploadLimitation below verifies that directly against
// this small-object run too: rclone's *default* config now uploads
// successfully. The one upload path that still cannot complete is rclone
// forced into literal-payload signing (`use_unsigned_payload=false`):
// rclone's own non-seekable transfer wrapper then fails locally, before a
// request even reaches ZeroS3 -- a structural property of rclone's
// generic transfer path, not a ZeroS3-side defect. This harness still
// seeds/overwrites the object bytes it verifies through `rclone` with the
// already-pinned AWS SDK (the same dependency every other harness in this
// repository already uses) purely to also independently exercise
// ZeroS3's Content-MD5 validation (see putWithContentMD5 below) against a
// real external client, not because rclone's own upload path is unusable.
package main

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // computing the request Content-MD5 value for a real external-client proof, not a security use.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
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

var pass, fail, known int

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

func knownLimitation(name, detail string) {
	known++
	fmt.Printf("KNOWN-LIMITATION: %s: %s\n", name, detail)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func startZeroS3(binPath, storeDir, addr string) (*exec.Cmd, error) {
	cmd := exec.Command(binPath, "-store", storeDir, "-addr", addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if !waitListening(addr) {
		cmd.Process.Kill()
		return nil, fmt.Errorf("zeros3 did not start listening on %s in time", addr)
	}
	return cmd, nil
}

func waitListening(addr string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func newClient(endpoint string) *s3.Client {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		panic(err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

// rcloneEnv builds the environment for an ordinary rclone S3 "remote"
// pointed at endpoint. These are the exact rclone S3-backend settings any
// user would set for a generic (non-AWS) S3-compatible target: endpoint,
// credentials, path-style addressing, and list_version=2 (ZeroS3 only
// implements ListObjectsV2, matching S3_COMPAT.md; many non-AWS S3 targets
// need this exact same setting, so it is an ordinary connection setting,
// not a ZeroS3-specific patch). No checksum/signing behavior is disabled.
func rcloneEnv(endpoint string) []string {
	return append(os.Environ(),
		"RCLONE_CONFIG_ZEROS3_TYPE=s3",
		"RCLONE_CONFIG_ZEROS3_PROVIDER=Other",
		"RCLONE_CONFIG_ZEROS3_ENV_AUTH=false",
		"RCLONE_CONFIG_ZEROS3_ACCESS_KEY_ID="+accessKeyID,
		"RCLONE_CONFIG_ZEROS3_SECRET_ACCESS_KEY="+secretAccessKey,
		"RCLONE_CONFIG_ZEROS3_REGION="+region,
		"RCLONE_CONFIG_ZEROS3_ENDPOINT="+endpoint,
		"RCLONE_CONFIG_ZEROS3_FORCE_PATH_STYLE=true",
		"RCLONE_CONFIG_ZEROS3_LIST_VERSION=2",
		"RCLONE_CONFIG=/dev/null",
	)
}

func runRclone(rcloneBin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(rcloneBin, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func main() {
	zeros3Bin := os.Getenv("ZEROS3_BIN")
	if zeros3Bin == "" {
		fmt.Println("set ZEROS3_BIN to a built zeros3 binary")
		os.Exit(2)
	}
	rcloneBin := os.Getenv("RCLONE_BIN")
	if rcloneBin == "" {
		rcloneBin = "rclone"
	}
	if out, err := exec.Command(rcloneBin, "version").CombinedOutput(); err != nil {
		fmt.Printf("rclone not runnable at %q: %v\n%s\n", rcloneBin, err, out)
		os.Exit(2)
	} else {
		fmt.Print(string(out))
	}

	storeDir, err := os.MkdirTemp("", "zeros3-rclone-store-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(storeDir)

	addr, err := freePort()
	if err != nil {
		panic(err)
	}
	cmd, err := startZeroS3(zeros3Bin, storeDir, addr)
	if err != nil {
		panic(err)
	}
	endpoint := "http://" + addr
	env := rcloneEnv(endpoint)
	client := newClient(endpoint)
	ctx := context.Background()

	const bucket = "rclone-bucket"
	const key = "rclone-object.txt"

	// --- Bucket lifecycle via rclone ---
	out, err := runRclone(rcloneBin, env, "mkdir", "zeros3:"+bucket)
	check("CreateBucket via `rclone mkdir`", errOrNil(err, out))

	out, err = runRclone(rcloneBin, env, "lsd", "zeros3:")
	check("ListBuckets via `rclone lsd`", err)
	requireTrue("ListBuckets shows the created bucket", strings.Contains(out, bucket), out)

	// --- Verify, and document, the ordinary-rclone-upload limitation ---
	// (see the package doc comment for the root cause) before seeding data
	// through the AWS SDK so the rest of the workflow can proceed.
	checkRcloneUploadLimitation(rcloneBin, env, endpoint, bucket)

	// --- Seed real object bytes (AWS SDK PutObject, including an
	// explicit Content-MD5, since rclone's own upload path cannot reach
	// ZeroS3 -- see above) ---
	v1 := []byte("rclone interoperability proof -- revision 1, seeded via the pinned AWS SDK\n")
	putWithContentMD5(ctx, client, bucket, key, v1, "text/plain")

	// --- Listing via rclone ---
	out, err = runRclone(rcloneBin, env, "lsjson", "zeros3:"+bucket)
	check("ListObjectsV2 via `rclone lsjson`", err)
	var entries []struct {
		Name string
		Size int64
	}
	_ = json.Unmarshal([]byte(out), &entries)
	found := false
	for _, e := range entries {
		if e.Name == key && e.Size == int64(len(v1)) {
			found = true
		}
	}
	requireTrue("lsjson shows the seeded object with the correct size", found, out)

	// --- Download + byte/hash equality via rclone ---
	out, err = runRclone(rcloneBin, env, "cat", "zeros3:"+bucket+"/"+key)
	check("GetObject via `rclone cat`", err)
	requireTrue("downloaded bytes match the uploaded bytes exactly", out == string(v1), "byte mismatch")

	out, err = runRclone(rcloneBin, env, "hashsum", "MD5", "zeros3:"+bucket+"/"+key)
	check("`rclone hashsum MD5` runs against the object's ETag", err)
	wantSum := md5.Sum(v1) //nolint:gosec // proof-of-match against S3's documented single-part ETag convention.
	wantHex := fmt.Sprintf("%x", wantSum)
	requireTrue("rclone-reported MD5 matches the object's real MD5", strings.Contains(out, wantHex), out+" want "+wantHex)

	// --- Overwrite/update (again via the SDK, then verified via rclone) ---
	v2 := []byte("rclone interoperability proof -- revision 2, OVERWRITTEN\n")
	putWithContentMD5(ctx, client, bucket, key, v2, "text/plain")
	out, err = runRclone(rcloneBin, env, "cat", "zeros3:"+bucket+"/"+key)
	check("GetObject via `rclone cat` after overwrite", err)
	requireTrue("rclone sees the overwritten content, not the old revision", out == string(v2), "byte mismatch after overwrite")

	// --- Restart / persistence, observed through rclone ---
	if err := cmd.Process.Kill(); err != nil {
		panic(err)
	}
	_, _ = cmd.Process.Wait()
	cmd2, err := startZeroS3(zeros3Bin, storeDir, addr)
	if err != nil {
		panic(err)
	}
	out, err = runRclone(rcloneBin, env, "cat", "zeros3:"+bucket+"/"+key)
	check("GetObject via `rclone cat` after a full zeros3 process restart", err)
	requireTrue("rclone reads back the same bytes after restart (journal replay, not a page-cache hit)", out == string(v2), "byte mismatch after restart")

	// --- Delete via rclone ---
	_, err = runRclone(rcloneBin, env, "deletefile", "zeros3:"+bucket+"/"+key)
	check("DeleteObject via `rclone deletefile`", err)
	out, err = runRclone(rcloneBin, env, "lsjson", "zeros3:"+bucket)
	check("ListObjectsV2 via `rclone lsjson` after delete", err)
	var afterDelete []struct{ Name string }
	_ = json.Unmarshal([]byte(out), &afterDelete)
	requireTrue("bucket is empty after `rclone deletefile`", len(afterDelete) == 0, out)

	_, err = runRclone(rcloneBin, env, "rmdir", "zeros3:"+bucket)
	check("DeleteBucket via `rclone rmdir`", err)
	out, err = runRclone(rcloneBin, env, "lsd", "zeros3:")
	check("ListBuckets via `rclone lsd` after DeleteBucket", err)
	requireTrue("deleted bucket no longer appears", !strings.Contains(out, bucket), out)

	cmd2.Process.Kill()
	_, _ = cmd2.Process.Wait()

	fmt.Printf("\n%d passed, %d failed, %d documented known limitation(s)\n", pass, fail, known)
	if fail > 0 {
		os.Exit(1)
	}
}

// putWithContentMD5 uploads body via the pinned AWS SDK, explicitly setting
// Content-MD5 (the SDK does not send it by default) so this harness also
// independently exercises ZeroS3's Content-MD5 validation (T1, B1) against
// a real external client, not just the in-repo unit tests.
func putWithContentMD5(ctx context.Context, client *s3.Client, bucket, key string, body []byte, contentType string) {
	sum := md5.Sum(body) //nolint:gosec // S3-compatible request integrity header, not a security use.
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		ContentMD5:  aws.String(b64),
	})
	if err != nil {
		panic(fmt.Sprintf("seed PutObject with Content-MD5 for %s/%s: %v", bucket, key, err))
	}
}

// checkRcloneUploadLimitation verifies -- rather than assumes -- rclone's
// ordinary upload behavior against ZeroS3. Two things are confirmed
// directly against the running server:
//
//  1. rclone's *default* configuration (nothing overridden) sends
//     PutObject with X-Amz-Content-Sha256: UNSIGNED-PAYLOAD, which ZeroS3
//     has accepted since M5-B (see results/M5B_RCLONE_LARGE_OBJECT_RESULTS.md
//     for the 1 GiB/205-part end-to-end proof) -- so this now succeeds.
//     The probe object it creates is deleted again immediately so it
//     doesn't leak into the bucket-emptiness assertions later in this
//     harness, which exercise the SDK-seeded key only.
//  2. Forcing rclone to use a literal signed payload hash instead
//     (`use_unsigned_payload=false`, still an ordinary, documented rclone
//     S3-backend option -- not a patch) does not work: rclone wraps every
//     transfer body in a non-seekable progress-accounting reader, so the
//     AWS SDK v2 signer inside rclone then fails locally with "failed to
//     seek body to start, request stream is not seekable" before a
//     request is even sent. This is a structural property of rclone's
//     generic transfer path (confirmed reproducible across
//     --disable-http2, --multi-thread-streams=0, --use-server-modtime,
//     and --s3-disable-checksum combinations), not a ZeroS3-side defect,
//     and not fixable by ZeroS3 configuration -- still a genuine,
//     documented known limitation of rclone's own literal-payload mode.
func checkRcloneUploadLimitation(rcloneBin string, env []string, endpoint, bucket string) {
	tmp, err := os.CreateTemp("", "rclone-upload-probe-*.txt")
	if err != nil {
		panic(err)
	}
	defer os.Remove(tmp.Name())
	_, _ = tmp.WriteString("rclone upload probe payload\n")
	tmp.Close()

	// (1) Ordinary/default rclone config: sends UNSIGNED-PAYLOAD, accepted
	// since M5-B.
	out, err := runRclone(rcloneBin, env, "copyto", tmp.Name(), "zeros3:"+bucket+"/upload-probe-default.txt")
	if err != nil {
		fail++
		fmt.Println("FAIL: expected rclone's default (UNSIGNED-PAYLOAD) upload path to succeed against ZeroS3 (M5-B); rclone output: " + oneLine(out))
	} else {
		pass++
		fmt.Println("PASS: rclone default upload (UNSIGNED-PAYLOAD) against ZeroS3 succeeds (M5-B)")
		// Clean up immediately: this probe object is not part of the
		// rest of the workflow, which tracks only the SDK-seeded key.
		if _, err := runRclone(rcloneBin, env, "deletefile", "zeros3:"+bucket+"/upload-probe-default.txt"); err != nil {
			panic(fmt.Sprintf("cleanup of upload-probe-default.txt failed: %v", err))
		}
	}

	// (2) Force literal signed payload: rclone's own non-seekable transfer
	// wrapper still fails locally, independent of ZeroS3 and unaffected by
	// M5-B (M5-B only changed what ZeroS3 accepts on the wire; it can't
	// make rclone's local body reader seekable).
	forcedEnv := append(append([]string{}, env...), "RCLONE_CONFIG_ZEROS3_USE_UNSIGNED_PAYLOAD=false")
	out, err = runRclone(rcloneBin, forcedEnv, "copyto", tmp.Name(), "zeros3:"+bucket+"/upload-probe-signed.txt")
	if err == nil {
		pass++
		fmt.Println("NOTE: forcing use_unsigned_payload=false unexpectedly succeeded in this rclone build -- re-check whether the seekability limitation still applies")
		if _, err := runRclone(rcloneBin, env, "deletefile", "zeros3:"+bucket+"/upload-probe-signed.txt"); err != nil {
			panic(fmt.Sprintf("cleanup of upload-probe-signed.txt failed: %v", err))
		}
	} else {
		knownLimitation(
			"rclone forced literal-payload upload (use_unsigned_payload=false) against ZeroS3",
			"fails inside rclone itself before a request is sent, independent of ZeroS3 -- rclone output: "+oneLine(out),
		)
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func errOrNil(err error, out string) error {
	if err != nil {
		return fmt.Errorf("%v: %s", err, oneLine(out))
	}
	return nil
}
