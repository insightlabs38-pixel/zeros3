package main

// FB-02 (M-A3): every S3 flexible-checksum algorithm, both aws-chunked
// trailer modes, the CompleteMultipartUpload full-object claim, signed-only
// bulk transfers, and bounded peer response reads.
//
// The expected digests come from outside zeros3.go: the published CRC check
// values for "123456789", Go's standard library, a bitwise CRC-64/NVME
// written here, and AWS's own signed-trailer example request.

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // S3 checksum algorithm under test, not a security use.
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var fb02Algos = []string{"crc32", "crc32c", "crc64nvme", "sha1", "sha256"}

// fb02RefCRC64NVME is a bitwise CRC-64/NVME (reflected 0x9a6c9329ac4bc9b5,
// init and xorout all ones), deliberately independent of the table-driven
// production code.
func fb02RefCRC64NVME(b []byte) uint64 {
	crc := ^uint64(0)
	for _, c := range b {
		crc ^= uint64(c)
		for i := 0; i < 8; i++ {
			if crc&1 == 1 {
				crc = crc>>1 ^ 0x9a6c9329ac4bc9b5
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

// fb02Sum is the big-endian digest of b under the named algorithm, computed
// without zeros3.go.
func fb02Sum(name string, b []byte) []byte {
	switch name {
	case "crc32":
		return binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(b))
	case "crc32c":
		return binary.BigEndian.AppendUint32(nil, crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	case "crc64nvme":
		return binary.BigEndian.AppendUint64(nil, fb02RefCRC64NVME(b))
	case "sha1":
		s := sha1.Sum(b) //nolint:gosec // see import.
		return s[:]
	case "sha256":
		s := sha256.Sum256(b)
		return s[:]
	}
	panic(name)
}

func fb02B64(name string, b []byte) string {
	return base64.StdEncoding.EncodeToString(fb02Sum(name, b))
}

func TestFB02_ChecksumCheckValues(t *testing.T) {
	want := map[string]string{
		"crc32":     "cbf43926",
		"crc32c":    "e3069283",
		"crc64nvme": "ae8b14860a799888",
		"sha1":      "f7c3bc1d808e04732adf679965ccc34ca7ae3441",
		"sha256":    "15e2b0d3c33891ebb0f1ef609ec419420c20e320ce94c65fbc8c3312448eb225",
	}
	check := []byte("123456789")
	for _, name := range fb02Algos {
		if got := hex.EncodeToString(fb02Sum(name, check)); got != want[name] {
			t.Fatalf("reference %s check value %s, want %s", name, got, want[name])
		}
		algo, ok := checksumAlgoNamed(name)
		if !ok {
			t.Fatalf("%s is not a known algorithm", name)
		}
		h := algo.newHash()
		h.Write(check)
		if got := hex.EncodeToString(h.Sum(nil)); got != want[name] || h.Size() != algo.size() || len(h.Sum(nil)) != algo.size() {
			t.Fatalf("zeros3 %s check value %s (size %d), want %s", name, got, h.Size(), want[name])
		}
		if algo.header() != "x-amz-checksum-"+name {
			t.Fatalf("header for %s is %q", name, algo.header())
		}
	}
	for _, bad := range []string{"", "md5", "crc64", "CRC-32", "sha512", "xxhash64"} {
		if _, ok := checksumAlgoNamed(bad); ok {
			t.Fatalf("%q accepted as a checksum algorithm", bad)
		}
	}
	// Names are case-insensitive on the wire (x-amz-sdk-checksum-algorithm: CRC32C).
	if a, ok := checksumAlgoNamed("CRC64NVME"); !ok || a != checksumCRC64NVME {
		t.Fatal("upper-case algorithm name not recognised")
	}
}

func TestFB02_CRC64NVMEMatchesBitwiseReference(t *testing.T) {
	data := genRandomBytes(2026, 70000)
	for _, n := range []int{0, 1, 7, 8, 9, 15, 16, 17, 63, 64, 65, 1000, 4096, 65536, 70000} {
		want := fb02RefCRC64NVME(data[:n])
		h := checksumCRC64NVME.newHash()
		// Uneven writes exercise the slicing-by-8 tail handling.
		for off, step := 0, 1; off < n; step = step*3 + 1 {
			end := min(off+step, n)
			h.Write(data[off:end])
			off = end
		}
		if got := binary.BigEndian.Uint64(h.Sum(nil)); got != want {
			t.Fatalf("len %d: %016x, want %016x", n, got, want)
		}
		h.Reset()
		h.Write(data[:n])
		if got := binary.BigEndian.Uint64(h.Sum([]byte{})); got != want {
			t.Fatalf("len %d after Reset: %016x, want %016x", n, got, want)
		}
	}
}

// TestFB02_HeaderChecksumMatrix: a declared x-amz-checksum-<algo> is
// verified exactly under every payload mode, and nothing is published when
// it does not match.
func TestFB02_HeaderChecksumMatrix(t *testing.T) {
	f := newPutFixture(t)
	body := genRandomBytes(7, 300<<10)
	other := append([]byte{}, body...)
	other[1234] ^= 1
	for _, name := range fb02Algos {
		h := "x-amz-checksum-" + name
		short := base64.StdEncoding.EncodeToString(fb02Sum(name, body)[1:])
		for _, mode := range []string{"", "UNSIGNED-PAYLOAD"} {
			cases := []struct {
				label      string
				hdr        map[string]string
				wantStatus int
				wantCode   string
			}{
				{"match", map[string]string{h: fb02B64(name, body)}, 200, ""},
				{"match-sdk-algorithm", map[string]string{h: fb02B64(name, body), "x-amz-sdk-checksum-algorithm": strings.ToUpper(name)}, 200, ""},
				{"match-with-mode-and-type", map[string]string{h: fb02B64(name, body), "x-amz-checksum-mode": "ENABLED", "x-amz-checksum-type": "FULL_OBJECT"}, 200, ""},
				{"mismatch", map[string]string{h: fb02B64(name, other)}, 400, "BadDigest"},
				{"short-value", map[string]string{h: short}, 400, "InvalidRequest"},
				{"not-base64", map[string]string{h: "!!!!"}, 400, "InvalidRequest"},
				{"composite-on-put", map[string]string{h: fb02B64(name, body) + "-2"}, 400, "InvalidRequest"},
			}
			for _, c := range cases {
				key := fmt.Sprintf("hdr-%s-%s-%s", name, strings.ToLower(mode), c.label)
				t.Run(key, func(t *testing.T) {
					f.t = t
					status, code := f.put(key, body, mode, c.hdr, nil, false)
					if status != c.wantStatus || code != c.wantCode {
						t.Fatalf("got %d %q, want %d %q", status, code, c.wantStatus, c.wantCode)
					}
					got, data := f.get(key)
					if c.wantStatus == 200 && (got != 200 || !bytes.Equal(data, body)) {
						t.Fatalf("accepted object reads back %d (%d bytes)", got, len(data))
					}
					if c.wantStatus != 200 && got != 404 {
						t.Fatalf("rejected upload left an object behind: GET %d", got)
					}
				})
			}
		}
	}
	// Signed aws-chunked bodies carry header claims too.
	for _, name := range fb02Algos {
		for _, good := range []bool{true, false} {
			key := fmt.Sprintf("chunked-%s-%v", name, good)
			t.Run(key, func(t *testing.T) {
				f.t = t
				v := fb02B64(name, body)
				if !good {
					v = fb02B64(name, other)
				}
				status, code := f.putChunked(awsChunkedPut{path: "/b/" + key, payload: body, sizes: []int{65536, 1000}, hdr: map[string]string{"x-amz-checksum-" + name: v}})
				if good && status != 200 || !good && (status != 400 || code != "BadDigest") {
					t.Fatalf("got %d %q", status, code)
				}
				if got, _ := f.get(key); (got == 200) != good {
					t.Fatalf("GET %d after a %v claim", got, good)
				}
			})
		}
	}
}

func TestFB02_ChecksumHeaderRules(t *testing.T) {
	f := newPutFixture(t)
	body := []byte("checksum header rules")
	cases := []struct {
		label      string
		hdr        map[string]string
		wantStatus int
		wantCode   string
	}{
		{"two-algorithms", map[string]string{"x-amz-checksum-crc32": fb02B64("crc32", body), "x-amz-checksum-sha1": fb02B64("sha1", body)}, 400, "InvalidRequest"},
		{"unknown-algorithm", map[string]string{"x-amz-checksum-md5": base64.StdEncoding.EncodeToString(make([]byte, 16))}, 501, "NotImplemented"},
		{"unknown-plus-known", map[string]string{"x-amz-checksum-xxhash": "AAAA", "x-amz-checksum-crc32": fb02B64("crc32", body)}, 501, "NotImplemented"},
		{"sdk-algorithm-mismatch", map[string]string{"x-amz-checksum-crc32": fb02B64("crc32", body), "x-amz-sdk-checksum-algorithm": "CRC32C"}, 400, "InvalidRequest"},
		{"sdk-algorithm-alone", map[string]string{"x-amz-sdk-checksum-algorithm": "CRC32"}, 400, "InvalidRequest"},
		{"sdk-algorithm-unknown", map[string]string{"x-amz-checksum-crc32": fb02B64("crc32", body), "x-amz-sdk-checksum-algorithm": "MD5"}, 501, "NotImplemented"},
		{"trailer-without-chunked-body", map[string]string{"x-amz-trailer": "x-amz-checksum-crc32"}, 400, "InvalidRequest"},
		{"trailer-unknown", map[string]string{"x-amz-trailer": "x-amz-checksum-md5"}, 501, "NotImplemented"},
		{"trailer-two-names", map[string]string{"x-amz-trailer": "x-amz-checksum-crc32,x-amz-checksum-sha1"}, 400, "InvalidRequest"},
		{"header-and-trailer", map[string]string{"x-amz-trailer": "x-amz-checksum-crc32", "x-amz-checksum-crc32": fb02B64("crc32", body)}, 400, "InvalidRequest"},
		{"type-and-mode-only", map[string]string{"x-amz-checksum-type": "COMPOSITE", "x-amz-checksum-mode": "ENABLED", "x-amz-checksum-algorithm": "CRC32"}, 200, ""},
		{"md5-and-crc-both-good", map[string]string{"Content-MD5": md5Header(body), "x-amz-checksum-crc32c": fb02B64("crc32c", body)}, 200, ""},
		{"md5-bad-crc-good", map[string]string{"Content-MD5": md5Header([]byte("x")), "x-amz-checksum-crc32c": fb02B64("crc32c", body)}, 400, "BadDigest"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			f.t = t
			status, code := f.put("rules-"+c.label, body, "", c.hdr, nil, false)
			if status != c.wantStatus || code != c.wantCode {
				t.Fatalf("got %d %q, want %d %q", status, code, c.wantStatus, c.wantCode)
			}
			if got, _ := f.get("rules-" + c.label); (got == 200) != (c.wantStatus == 200) {
				t.Fatalf("GET %d", got)
			}
		})
	}
}

// fb02TrailerPut is one aws-chunked trailer upload; the zero value of each
// override field leaves the request valid.
type fb02TrailerPut struct {
	key      string
	payload  []byte
	sizes    []int
	unsigned bool
	algo     string
	xTrailer string                   // "" = x-amz-checksum-<algo>, "-" = omitted
	value    string                   // "" = the payload's real checksum
	lines    func([]string) []string  // the trailer lines before the blank one
	raw      func([]byte) []byte      // the encoded body
	hdr      map[string]string        // unsigned extra headers
	resign   func(prev string) string // override the trailer signature
}

func fb02TrailerSig(signer testSigner, when time.Time, prev, canon string) string {
	date := when.UTC().Format("20060102")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", date, signer.region)
	kDate := testHMAC([]byte("AWS4"+signer.secretKey), date)
	kSigning := testHMAC(testHMAC(testHMAC(kDate, signer.region), "s3"), "aws4_request")
	return hex.EncodeToString(testHMAC(kSigning, strings.Join([]string{
		"AWS4-HMAC-SHA256-TRAILER", when.UTC().Format("20060102T150405Z"), scope, prev, testHexSHA256([]byte(canon)),
	}, "\n")))
}

func (f *putFixture) putTrailer(c fb02TrailerPut) (int, string) {
	f.t.Helper()
	mode := "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	if c.unsigned {
		mode = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	}
	if c.algo == "" {
		c.algo = "crc32c"
	}
	req, err := http.NewRequest(http.MethodPut, f.ts.URL+"/b/"+c.key, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(c.payload)))
	signed := []string{"content-encoding", "x-amz-decoded-content-length"}
	switch c.xTrailer {
	case "":
		req.Header.Set("X-Amz-Trailer", "x-amz-checksum-"+c.algo)
		signed = append(signed, "x-amz-trailer")
	case "-":
	default:
		req.Header.Set("X-Amz-Trailer", c.xTrailer)
		signed = append(signed, "x-amz-trailer")
	}
	now := time.Now()
	signTestRequest(f.t, req, f.signer, req.URL.Path, req.URL.RawQuery, nil, now, &signOpts{payloadHash: mode, extraSignedHeaders: signed})
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	auth := req.Header.Get("Authorization")
	seed := auth[strings.LastIndex(auth, "Signature=")+len("Signature="):]
	frames := signedAWSFrames(f.signer, now, seed, c.payload, c.sizes)
	var b bytes.Buffer
	for i, fr := range frames {
		if c.unsigned {
			fmt.Fprintf(&b, "%x\r\n", fr.declared)
		} else {
			fmt.Fprintf(&b, "%x;chunk-signature=%s\r\n", fr.declared, fr.sig)
		}
		if i < len(frames)-1 {
			b.Write(fr.data)
			b.WriteString("\r\n")
		}
	}
	value := c.value
	if value == "" {
		value = fb02B64(c.algo, c.payload)
	}
	canon := "x-amz-checksum-" + c.algo + ":" + value
	lines := []string{canon}
	if !c.unsigned {
		sig := fb02TrailerSig(f.signer, now, frames[len(frames)-1].sig, canon+"\n")
		if c.resign != nil {
			sig = c.resign(frames[len(frames)-1].sig)
		}
		lines = append(lines, "x-amz-trailer-signature:"+sig)
	}
	if c.lines != nil {
		lines = c.lines(lines)
	}
	for _, l := range lines {
		b.WriteString(l + "\r\n")
	}
	b.WriteString("\r\n")
	body := b.Bytes()
	if c.raw != nil {
		body = c.raw(body)
	}
	req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var e s3ErrorBody
	_ = xml.Unmarshal(raw, &e)
	return resp.StatusCode, e.Code
}

func TestFB02_TrailerModes(t *testing.T) {
	f := newPutFixture(t)
	payload := genRandomBytes(11, 200<<10)
	other := append([]byte{}, payload...)
	other[99] ^= 0x40
	for _, unsigned := range []bool{true, false} {
		for _, algo := range fb02Algos {
			mode := "signed"
			if unsigned {
				mode = "unsigned"
			}
			cases := []struct {
				label      string
				put        fb02TrailerPut
				wantStatus int
				wantCode   string
			}{
				{"ok", fb02TrailerPut{}, 200, ""},
				{"ok-one-chunk", fb02TrailerPut{sizes: []int{1 << 20}}, 200, ""},
				{"ok-uneven-chunks", fb02TrailerPut{sizes: []int{1, 8192, 65536, 3}}, 200, ""},
				{"wrong-value", fb02TrailerPut{value: fb02B64(algo, other)}, 400, "BadDigest"},
				{"value-wrong-length", fb02TrailerPut{value: "AAAA"}, 400, "InvalidRequest"},
				{"value-not-base64", fb02TrailerPut{value: "%%%%"}, 400, "InvalidRequest"},
				{"missing-checksum-line", fb02TrailerPut{lines: func([]string) []string { return nil }}, 400, "InvalidRequest"},
				{"other-algorithm-line", fb02TrailerPut{lines: func(l []string) []string {
					l[0] = "x-amz-checksum-zz" + l[0][len("x-amz-checksum-"):]
					return l
				}}, 400, "InvalidRequest"},
				{"duplicate-checksum-line", fb02TrailerPut{lines: func(l []string) []string { return append([]string{l[0]}, l...) }}, 400, "InvalidRequest"},
				{"extra-line", fb02TrailerPut{lines: func(l []string) []string { return append(l, "x-amz-meta-sneaky:1") }}, 400, "InvalidRequest"},
				{"line-without-colon", fb02TrailerPut{lines: func(l []string) []string { return append(l[:1:1], "garbage") }}, 400, "InvalidRequest"},
				{"bare-lf-trailer", fb02TrailerPut{raw: func(b []byte) []byte { return append(bytes.TrimSuffix(b, []byte("\r\n\r\n")), '\n', '\n') }}, 400, "InvalidRequest"},
				{"cut-before-trailer", fb02TrailerPut{raw: func(b []byte) []byte { return b[:bytes.LastIndex(b, []byte("x-amz-checksum-"))] }}, 400, "IncompleteBody"},
				{"cut-inside-trailer", fb02TrailerPut{raw: func(b []byte) []byte { return b[:len(b)-3] }}, 400, "IncompleteBody"},
				{"no-blank-line", fb02TrailerPut{raw: func(b []byte) []byte { return b[:len(b)-2] }}, 400, "IncompleteBody"},
				{"garbage-after-trailer", fb02TrailerPut{raw: func(b []byte) []byte { return append(b, 'x') }}, 400, "InvalidRequest"},
				{"x-amz-trailer-omitted", fb02TrailerPut{xTrailer: "-"}, 400, "InvalidRequest"},
				{"x-amz-trailer-names-other", fb02TrailerPut{xTrailer: "x-amz-checksum-" + map[bool]string{true: "sha1", false: "crc32"}[algo == "crc32"]}, 400, "InvalidRequest"},
				{"sdk-algorithm-matches", fb02TrailerPut{hdr: map[string]string{"x-amz-sdk-checksum-algorithm": strings.ToUpper(algo)}}, 200, ""},
			}
			if unsigned {
				cases = append(cases,
					struct {
						label      string
						put        fb02TrailerPut
						wantStatus int
						wantCode   string
					}{"signature-line-in-unsigned-mode", fb02TrailerPut{lines: func(l []string) []string {
						return append(l, "x-amz-trailer-signature:"+strings.Repeat("0", 64))
					}}, 400, "InvalidRequest"},
					struct {
						label      string
						put        fb02TrailerPut
						wantStatus int
						wantCode   string
					}{"chunk-signature-in-unsigned-mode", fb02TrailerPut{raw: func(b []byte) []byte {
						i := bytes.Index(b, []byte("\r\n"))
						return append([]byte(string(b[:i])+";chunk-signature="+strings.Repeat("0", 64)), b[i:]...)
					}}, 400, "InvalidRequest"})
			} else {
				type tc = struct {
					label      string
					put        fb02TrailerPut
					wantStatus int
					wantCode   string
				}
				cases = append(cases,
					tc{"trailer-signature-wrong", fb02TrailerPut{resign: func(string) string { return strings.Repeat("ab", 32) }}, 403, "SignatureDoesNotMatch"},
					tc{"trailer-signature-over-other-value", fb02TrailerPut{resign: func(prev string) string {
						return fb02TrailerSig(f.signer, time.Now(), prev, "x-amz-checksum-"+algo+":"+fb02B64(algo, other)+"\n")
					}}, 403, "SignatureDoesNotMatch"},
					tc{"trailer-signature-missing", fb02TrailerPut{lines: func(l []string) []string { return l[:1] }}, 403, "SignatureDoesNotMatch"},
					tc{"trailer-signature-first", fb02TrailerPut{lines: func(l []string) []string { return []string{l[1], l[0]} }}, 400, "InvalidRequest"},
					tc{"chunk-signature-dropped", fb02TrailerPut{raw: func(b []byte) []byte {
						i := bytes.Index(b, []byte(";chunk-signature="))
						return append(append([]byte{}, b[:i]...), b[i+len(";chunk-signature=")+64:]...)
					}}, 400, "InvalidRequest"},
				)
			}
			for _, c := range cases {
				key := fmt.Sprintf("tr-%s-%s-%s", mode, algo, c.label)
				t.Run(key, func(t *testing.T) {
					f.t = t
					p := c.put
					p.key, p.payload, p.unsigned, p.algo = key, payload, unsigned, algo
					if p.sizes == nil {
						p.sizes = []int{65536}
					}
					status, code := f.putTrailer(p)
					if status != c.wantStatus || code != c.wantCode {
						t.Fatalf("got %d %q, want %d %q", status, code, c.wantStatus, c.wantCode)
					}
					got, data := f.get(key)
					if c.wantStatus == 200 && (got != 200 || !bytes.Equal(data, payload)) {
						t.Fatalf("accepted trailer upload reads back %d (%d bytes)", got, len(data))
					}
					if c.wantStatus != 200 && got != 404 {
						t.Fatalf("rejected trailer upload left an object: GET %d", got)
					}
				})
			}
		}
	}
	t.Run("empty-payload", func(t *testing.T) {
		f.t = t
		for _, unsigned := range []bool{true, false} {
			if st, code := f.putTrailer(fb02TrailerPut{key: "tr-empty", unsigned: unsigned, algo: "crc64nvme"}); st != 200 {
				t.Fatalf("unsigned=%v: %d %q", unsigned, st, code)
			}
		}
	})
}

// TestFB02_TrailerAWSDocumentationVector replays AWS's own
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER example (66560 bytes of 'a',
// crc32c trailer, example credentials, 2013-05-24): seed, chunk and trailer
// signatures are AWS's published values, not ones computed here.
func TestFB02_TrailerAWSDocumentationVector(t *testing.T) {
	store, _ := newBucketStore(t)
	if err := store.CreateBucket("examplebucket"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, "us-east-1")
	prev := sigv4Now
	sigv4Now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	defer func() { sigv4Now = prev }()

	const (
		seed     = "106e2a8a18243abcf37539882f36619c00e2dfc72633413f02d3b74544bfeb8e"
		sig1     = "b474d8862b1487a5145d686f57f013e54db672cee1c953b3010fb58501ef5aa2"
		sig2     = "1c1344b170168f8e65b41376b44b20fe354e373826ccbbe2c1d40a8cae51e5c7"
		sigFinal = "2ca2aba2005185cf7159c6277faf83795951dd77a3a99e6e65d5c9f85863f992"
		trailer  = "x-amz-checksum-crc32c:sOO8/Q=="
		trSig    = "d81f82fc3505edab99d459891051a732e8730629a2e4a59689829ca17fe2e435"
	)
	payload := bytes.Repeat([]byte("a"), 66560)
	if fb02B64("crc32c", payload) != "sOO8/Q==" {
		t.Fatal("the documented crc32c does not match the documented payload")
	}
	build := func(trailer, trSig, final string) []byte {
		var b bytes.Buffer
		fmt.Fprintf(&b, "10000;chunk-signature=%s\r\n%s\r\n", sig1, payload[:65536])
		fmt.Fprintf(&b, "400;chunk-signature=%s\r\n%s\r\n", sig2, payload[65536:])
		fmt.Fprintf(&b, "0;chunk-signature=%s\r\n%s\r\nx-amz-trailer-signature:%s\r\n\r\n", final, trailer, trSig)
		return b.Bytes()
	}
	send := func(key string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/examplebucket/"+key, bytes.NewReader(body))
		req.RequestURI = "/examplebucket/" + key
		req.Host = "s3.amazonaws.com"
		req.Header.Set("X-Amz-Date", "20130524T000000Z")
		req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
		req.Header.Set("X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER")
		req.Header.Set("Content-Encoding", "aws-chunked")
		req.Header.Set("X-Amz-Decoded-Content-Length", "66560")
		req.Header.Set("X-Amz-Trailer", "x-amz-checksum-crc32c")
		req.ContentLength = int64(len(body))
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,"+
			"SignedHeaders=content-encoding;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class;x-amz-trailer,Signature="+seed)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := send("chunkObject.txt", build(trailer, trSig, sigFinal)); rec.Code != 200 {
		t.Fatalf("AWS documentation request rejected: %d %s", rec.Code, rec.Body.String())
	}
	if _, got, err := store.GetObject("examplebucket", "chunkObject.txt"); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored object differs from the payload: %v", err)
	}
	for _, c := range []struct {
		name, trailer, sig, final string
		code                      int
	}{
		{"trailer-value-changed", "x-amz-checksum-crc32c:AAAAAA==", trSig, sigFinal, 403},
		{"trailer-signature-flipped", trailer, flipSig(trSig), sigFinal, 403},
		{"final-chunk-signature-flipped", trailer, trSig, flipSig(sigFinal), 403},
	} {
		if rec := send(c.name, build(c.trailer, c.sig, c.final)); rec.Code != c.code {
			t.Fatalf("%s: %d, want %d", c.name, rec.Code, c.code)
		}
		if _, _, err := store.GetObject("examplebucket", c.name); err == nil {
			t.Fatalf("%s: a tampered request published an object", c.name)
		}
	}
}

// TestFB02_MultipartChecksums: an UploadPart claim is verified per part, and
// CompleteMultipartUpload's x-amz-checksum-<algo> is a full-object claim
// verified over the assembled bytes before anything is published.
func TestFB02_MultipartChecksums(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	p1 := genRandomBytes(21, minMultipartPartSize)
	p2 := genRandomBytes(22, 123457)
	whole := append(append([]byte{}, p1...), p2...)
	upload := func(key string) (string, []completedPartXML) {
		id := doCreateMultipartUpload(t, client, ts.URL, signer, "b", key)
		var parts []completedPartXML
		for i, p := range [][]byte{p1, p2} {
			path := fmt.Sprintf("/b/%s?partNumber=%d&uploadId=%s", key, i+1, id)
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, path, p, map[string]string{"x-amz-checksum-crc64nvme": fb02B64("crc64nvme", p)})
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("part %d with a correct claim: %d", i+1, resp.StatusCode)
			}
			parts = append(parts, completedPartXML{PartNumber: i + 1, ETag: resp.Header.Get("ETag")})
		}
		return id, parts
	}
	complete := func(key, id string, parts []completedPartXML, hdr map[string]string) (int, string) {
		data, _ := xml.Marshal(completeMultipartUploadXML{Part: parts})
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPost, fmt.Sprintf("/b/%s?uploadId=%s", key, id), data, hdr)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var e s3ErrorBody
		_ = xml.Unmarshal(raw, &e)
		return resp.StatusCode, e.Code
	}
	getStatus := func(key string) int {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/"+key, nil, nil)
		resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("part-claim-mismatch", func(t *testing.T) {
		id := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "pm")
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/pm?partNumber=1&uploadId="+id, p2, map[string]string{"x-amz-checksum-sha1": fb02B64("sha1", p1)})
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("mismatched part claim: %d", resp.StatusCode)
		}
		if lp, _ := doListParts(t, client, ts.URL, signer, "b", "pm", id); len(lp.Part) != 0 {
			t.Fatalf("a rejected part was recorded: %+v", lp.Part)
		}
	})
	for _, algo := range fb02Algos {
		t.Run("full-object-"+algo, func(t *testing.T) {
			key := "full-" + algo
			id, parts := upload(key)
			if st, code := complete(key, id, parts, map[string]string{"x-amz-checksum-" + algo: fb02B64(algo, p1)}); st != 400 || code != "BadDigest" {
				t.Fatalf("wrong full-object claim: %d %q", st, code)
			}
			if st := getStatus(key); st != 404 {
				t.Fatalf("a rejected completion published the object: GET %d", st)
			}
			// The upload survives the failed completion and completes with
			// the right claim.
			if st, code := complete(key, id, parts, map[string]string{"x-amz-checksum-" + algo: fb02B64(algo, whole), "x-amz-checksum-type": "FULL_OBJECT"}); st != 200 {
				t.Fatalf("correct full-object claim: %d %q", st, code)
			}
			if st := getStatus(key); st != 200 {
				t.Fatalf("completed object: GET %d", st)
			}
		})
	}
	t.Run("composite-claim-not-implemented", func(t *testing.T) {
		id, parts := upload("comp")
		if st, code := complete("comp", id, parts, map[string]string{"x-amz-checksum-crc32": fb02B64("crc32", p1) + "-2"}); st != 501 || code != "NotImplemented" {
			t.Fatalf("composite claim: %d %q", st, code)
		}
		if st, code := complete("comp", id, parts, map[string]string{"x-amz-checksum-crc32": fb02B64("crc32", whole), "x-amz-checksum-type": "COMPOSITE"}); st != 501 || code != "NotImplemented" {
			t.Fatalf("COMPOSITE type: %d %q", st, code)
		}
		if st := getStatus("comp"); st != 404 {
			t.Fatalf("GET %d", st)
		}
		if st, code := complete("comp", id, parts, nil); st != 200 {
			t.Fatalf("plain completion after refusals: %d %q", st, code)
		}
	})
	t.Run("complete-body-md5-still-checks-xml", func(t *testing.T) {
		id, parts := upload("md5")
		if st, code := complete("md5", id, parts, map[string]string{"Content-MD5": md5Header([]byte("not the xml"))}); st != 400 || code != "BadDigest" {
			t.Fatalf("Content-MD5 over the XML: %d %q", st, code)
		}
	})
}

// TestFB02_DeleteObjectsBodyChecksum: a buffered control body is verified
// against its claim before it is acted on.
func TestFB02_DeleteObjectsBodyChecksum(t *testing.T) {
	f := newPutFixture(t)
	if st, _ := f.put("victim", []byte("keep me"), "", nil, nil, false); st != 200 {
		t.Fatal(st)
	}
	body := []byte(`<Delete><Object><Key>victim</Key></Object></Delete>`)
	for _, c := range []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"x-amz-checksum-crc32c": fb02B64("crc32c", []byte("other"))}, 400},
		{map[string]string{"x-amz-checksum-crc64nvme": fb02B64("crc64nvme", body)}, 200},
	} {
		resp := doSignedRequest(t, f.ts.Client(), f.ts.URL, f.signer, http.MethodPost, "/b?delete", body, c.hdr)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("DeleteObjects with %v: %d, want %d", c.hdr, resp.StatusCode, c.want)
		}
		if got, _ := f.get("victim"); (got == 200) != (c.want != 200) {
			t.Fatalf("after DeleteObjects %d the object reads %d", resp.StatusCode, got)
		}
	}
}

// TestFB02_BulkRequiresSignedPayload: bulk frames are trusted only as far
// as SigV4 binds them, so an unsigned payload mode is a downgrade the server
// refuses before reading the frame; a replay of a signed request is an
// idempotent no-op and a replayed signature over another body is refused.
func TestFB02_BulkRequiresSignedPayload(t *testing.T) {
	n := bulkTestUploadServer(t, nil)
	chunk := genRandomBytes(31, 9000)
	frame := bulkTestFrame(chunk)
	post := func(path string, payloadHash string, body []byte, when time.Time, creds Credentials, mutate func(*http.Request)) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, n.ts.URL+path, bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		if err := signSigV4Request(req, creds, n.cfg.Region, payloadHash, when); err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(req)
		}
		resp, err := n.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	sum := sha256.Sum256(frame)
	digest := hex.EncodeToString(sum[:])
	for _, path := range []string{zeros3BulkUploadPath, zeros3BulkFetchPath, zeros3BulkNegotiatePath} {
		st, body := post(path, "UNSIGNED-PAYLOAD", frame, time.Now(), n.creds, nil)
		if st != http.StatusForbidden || !strings.Contains(body, "SignedPayloadRequired") {
			t.Fatalf("%s UNSIGNED-PAYLOAD: %d %s", path, st, body)
		}
	}
	if n.has(bulkTestDesc(chunk)) {
		t.Fatal("a refused unsigned upload published a chunk")
	}
	// Presigned (query) authentication never binds the body either.
	host := strings.TrimPrefix(n.ts.URL, "http://")
	q := buildTestPresignedQuery(t, testSigner{accessKey: n.creds.AccessKeyID, secretKey: n.creds.SecretAccessKey, region: n.cfg.Region}, http.MethodPost, zeros3BulkUploadPath, host, presignOpts{})
	resp, err := n.ts.Client().Post(n.ts.URL+zeros3BulkUploadPath+"?"+q, "application/octet-stream", bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "SignedPayloadRequired") {
		t.Fatalf("presigned bulk upload: %d %s", resp.StatusCode, raw)
	}
	// Wrong secret, and a request dated outside the skew window.
	if st, _ := post(zeros3BulkUploadPath, digest, frame, time.Now(), Credentials{AccessKeyID: n.creds.AccessKeyID, SecretAccessKey: "wrong"}, nil); st != http.StatusForbidden {
		t.Fatalf("wrong secret: %d", st)
	}
	if st, _ := post(zeros3BulkUploadPath, digest, frame, time.Now().Add(-time.Hour), n.creds, nil); st != http.StatusForbidden {
		t.Fatalf("stale request: %d", st)
	}
	if n.has(bulkTestDesc(chunk)) {
		t.Fatal("a refused request published a chunk")
	}
	// A signed request, then its exact replay: the second is a no-op.
	signedAt := time.Now()
	var captured http.Header
	keep := func(r *http.Request) { captured = r.Header.Clone() }
	if st, body := post(zeros3BulkUploadPath, digest, frame, signedAt, n.creds, keep); st != 200 {
		t.Fatalf("signed upload: %d %s", st, body)
	}
	before, err := n.srv.store.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	replay := func(body []byte) int {
		req, _ := http.NewRequest(http.MethodPost, n.ts.URL+zeros3BulkUploadPath, bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header = captured.Clone()
		resp, err := n.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := replay(frame); st != 200 {
		t.Fatalf("replayed identical request: %d", st)
	}
	after, err := n.srv.store.Verify(true)
	if err != nil || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("a replay changed the store: %+v -> %+v (%v)", before, after, err)
	}
	other := genRandomBytes(32, 9000)
	if st := replay(bulkTestFrame(other)); st != http.StatusForbidden {
		t.Fatalf("replayed signature over another frame: %d", st)
	}
	if n.has(bulkTestDesc(other)) {
		t.Fatal("a replayed signature published another frame's chunk")
	}
	// Our own client always signs a fixed digest, so it keeps working.
	if err := uploadBulkFrame(context.Background(), n.cfg, bulkTestFrame(other)); err != nil {
		t.Fatalf("compliant client: %v", err)
	}
}

// fb02HostilePeer answers every request with a body of n bytes (n < 0:
// endless, no Content-Length).
func fb02HostilePeer(t *testing.T, n int64) syncClientConfig {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		}
		w.WriteHeader(200)
		buf := bytes.Repeat([]byte("{"), 1<<16)
		for sent := int64(0); n < 0 || sent < n; {
			step := int64(len(buf))
			if n >= 0 && n-sent < step {
				step = n - sent
			}
			if _, err := w.Write(buf[:step]); err != nil {
				return
			}
			sent += step
		}
	}))
	t.Cleanup(ts.Close)
	return syncClientConfig{Endpoint: ts.URL, Creds: Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}, Region: "us-east-1", Bucket: "b", Key: "k"}
}

func TestFB02_PeerResponsesAreBounded(t *testing.T) {
	t.Run("declared-length-refused-before-reading", func(t *testing.T) {
		cfg := fb02HostilePeer(t, 10<<30)
		if _, err := discoverZeroS3Sync(cfg); !errors.Is(err, errPeerResponseTooLarge) {
			t.Fatalf("discover against a 10 GiB answer: %v", err)
		}
	})
	t.Run("endless-stream-stops-at-limit", func(t *testing.T) {
		cfg := fb02HostilePeer(t, -1)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, _, err := cfg.signAndDo(context.Background(), http.MethodGet, zeros3SyncInfoPath, nil, nil)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, errPeerResponseTooLarge) {
			t.Fatalf("endless answer: %v", err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 4*maxPeerResponseBytes {
			t.Fatalf("reading an endless answer allocated %d bytes", grew)
		}
	})
	t.Run("chunk-fetch-bound-is-one-chunk", func(t *testing.T) {
		cfg := fb02HostilePeer(t, maxSyncChunkBytes+1)
		if _, err := fetchSourceChunk(context.Background(), cfg, strings.Repeat("0", 64)); !errors.Is(err, errPeerResponseTooLarge) {
			t.Fatalf("oversized chunk answer: %v", err)
		}
		cfg = fb02HostilePeer(t, maxSyncChunkBytes)
		if _, err := fetchSourceChunk(context.Background(), cfg, strings.Repeat("0", 64)); !errors.Is(err, errReplicateChunkMismatch) {
			t.Fatalf("a full-size chunk answer must be read and then verified: %v", err)
		}
	})
	t.Run("limit-is-inclusive", func(t *testing.T) {
		cfg := fb02HostilePeer(t, 1000)
		if _, body, err := cfg.signAndDoLimit(context.Background(), http.MethodGet, "/x", nil, nil, 1000); err != nil || len(body) != 1000 {
			t.Fatalf("an answer exactly at the limit: %d bytes, %v", len(body), err)
		}
		if _, _, err := cfg.signAndDoLimit(context.Background(), http.MethodGet, "/x", nil, nil, 999); !errors.Is(err, errPeerResponseTooLarge) {
			t.Fatalf("one byte past the limit: %v", err)
		}
	})
}
