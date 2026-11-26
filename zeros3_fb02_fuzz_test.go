package main

// FB-02 fuzz targets, run locally for a bounded time, e.g.
//   go test -run '^$' -fuzz '^FuzzFB02AWSChunkTrailer$' -fuzztime 60s .
//
//   - FuzzFB02AWSChunkTrailer: the aws-chunked decoder in both trailer
//     modes on arbitrary bodies. It never returns more than the declared
//     length, and reaches EOF only with the declared length decoded and a
//     well-formed trailer value handed to the claim;
//   - FuzzFB02PayloadCheck: checksum header parsing on arbitrary names and
//     values. Whatever it accepts is internally consistent and verifiable;
//   - FuzzFB02SnapshotIndex (M-A6): the restore session index against the
//     fresh lookup, on arbitrary descriptor bytes;
//   - FuzzFB02SyncIdentity (M-A5): the equivalence rule's algebra.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func fb02FuzzStream(unsigned bool, algo checksumAlgo, decodedLen int64) (*awsChunkedStream, testSigner, time.Time) {
	signer := testSigner{accessKey: "AK", secretKey: "secret", region: "us-east-1"}
	when := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	kDate := testHMAC([]byte("AWS4secret"), when.Format("20060102"))
	return &awsChunkedStream{
		signingKey: testHMAC(testHMAC(testHMAC(kDate, "us-east-1"), "s3"), "aws4_request"),
		amzDate:    when.Format("20060102T150405Z"),
		scope:      when.Format("20060102") + "/us-east-1/s3/aws4_request",
		seedSig:    strings.Repeat("ab", 32),
		decodedLen: decodedLen,
		unsigned:   unsigned,
		trailer:    true,
		claim:      &checksumTrailer{algo: algo},
	}, signer, when
}

// fb02FuzzBody is a valid trailer body for payload under the stream's key.
func fb02FuzzBody(signer testSigner, when time.Time, unsigned bool, algo checksumAlgo, payload []byte, size int) []byte {
	frames := signedAWSFrames(signer, when, strings.Repeat("ab", 32), payload, []int{size})
	var b bytes.Buffer
	for i, fr := range frames {
		if unsigned {
			fmt.Fprintf(&b, "%x\r\n", fr.declared)
		} else {
			fmt.Fprintf(&b, "%x;chunk-signature=%s\r\n", fr.declared, fr.sig)
		}
		if i < len(frames)-1 {
			b.Write(fr.data)
			b.WriteString("\r\n")
		}
	}
	h := algo.newHash()
	h.Write(payload)
	canon := algo.header() + ":" + base64.StdEncoding.EncodeToString(h.Sum(nil))
	b.WriteString(canon + "\r\n")
	if !unsigned {
		b.WriteString("x-amz-trailer-signature:" + fb02TrailerSig(signer, when, frames[len(frames)-1].sig, canon+"\n") + "\r\n")
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

func FuzzFB02AWSChunkTrailer(f *testing.F) {
	for _, unsigned := range []bool{true, false} {
		for a := checksumCRC32; a <= checksumSHA256; a++ {
			st, signer, when := fb02FuzzStream(unsigned, a, 0)
			for _, n := range []int{0, 1, 300} {
				payload := bytes.Repeat([]byte{byte('a' + n%26)}, n)
				st.decodedLen, st.claim = int64(n), &checksumTrailer{algo: a}
				body := fb02FuzzBody(signer, when, unsigned, a, payload, 128)
				if got, err := io.ReadAll(st.newReader(bytes.NewReader(body))); err != nil || !bytes.Equal(got, payload) {
					f.Fatalf("seed unsigned=%v %s len %d does not decode: %v", unsigned, a.header(), n, err)
				}
				f.Add(body, unsigned, uint8(a-1), uint16(n))
			}
		}
	}
	f.Add([]byte("0\r\n\r\n"), true, uint8(checksumCRC32), uint16(0))
	f.Add([]byte("0\r\nx-amz-checksum-crc32:AAAAAA==\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n"), true, uint8(checksumCRC32), uint16(0))
	f.Fuzz(func(t *testing.T, body []byte, unsigned bool, a uint8, n uint16) {
		algo := checksumAlgo(1 + int(a)%int(checksumSHA256))
		st, _, _ := fb02FuzzStream(unsigned, algo, int64(n%2048))
		r := st.newReader(bytes.NewReader(body))
		out, err := io.ReadAll(io.LimitReader(r, 1<<20))
		if int64(len(out)) > st.decodedLen {
			t.Fatalf("decoded %d bytes, more than the declared %d", len(out), st.decodedLen)
		}
		if err != nil {
			if st.claim.sum != nil {
				t.Fatalf("a failed body handed the claim a value: %v", err)
			}
			return
		}
		if int64(len(out)) != st.decodedLen {
			t.Fatalf("EOF after %d of %d declared bytes", len(out), st.decodedLen)
		}
		if len(st.claim.sum) != algo.size() {
			t.Fatalf("EOF with a %d-byte trailer value for %s", len(st.claim.sum), algo.header())
		}
	})
}

func FuzzFB02PayloadCheck(f *testing.F) {
	f.Add("crc32", "AAAAAA==", "", "", "")
	f.Add("crc64nvme", "AAAAAAAAAAA=", "", "CRC64NVME", "")
	f.Add("sha256", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=-3", "", "", "")
	f.Add("", "", "x-amz-checksum-sha1", "sha1", "")
	f.Add("crc32c", "AAAAAA==", "x-amz-checksum-crc32c", "", "1B2M2Y8AsgTpgAmY7PhCfg==")
	f.Add("mode", "ENABLED", "x-amz-checksum-crc32, x-amz-checksum-crc32", "", "")
	f.Fuzz(func(t *testing.T, name, value, trailer, sdk, md5 string) {
		r, _ := http.NewRequest(http.MethodPut, "http://h/b/k", nil)
		if name != "" {
			r.Header.Set("x-amz-checksum-"+name, value)
		}
		if trailer != "" {
			r.Header.Set("x-amz-trailer", trailer)
		}
		if sdk != "" {
			r.Header.Set("x-amz-sdk-checksum-algorithm", sdk)
		}
		if md5 != "" {
			r.Header.Set("Content-MD5", md5)
		}
		c, err := parsePayloadCheck(r, "")
		if err != nil {
			return
		}
		claims := 0
		if c.sum != nil {
			claims++
		}
		if c.composite {
			claims++
		}
		if c.trailer != nil {
			claims++
			if c.trailer.algo != c.algo || c.trailer.sum != nil {
				t.Fatalf("trailer claim %+v under algo %v", c.trailer, c.algo)
			}
		}
		if (c.algo == checksumNone) != (claims == 0) || claims > 1 {
			t.Fatalf("algo %v with %d claims: %+v", c.algo, claims, c)
		}
		if c.sum != nil && len(c.sum) != c.algo.size() {
			t.Fatalf("a %d-byte value accepted for %s", len(c.sum), c.algo.header())
		}
		// Whatever was accepted can be verified without panicking, and a
		// header claim over the empty body passes only when it is that
		// body's digest.
		if err := c.verifyBytes(nil); err == nil && c.sum != nil {
			h := c.algo.newHash()
			if !bytes.Equal(h.Sum(nil), c.sum) {
				t.Fatalf("a claim that is not the empty body's digest verified")
			}
		}
		body, object := c.objectClaim()
		if object.algo != checksumNone && (body.algo != checksumNone || object.algo != c.algo || object.trailer != nil) {
			t.Fatalf("objectClaim split %+v into %+v and %+v", c, body, object)
		}
	})
}

// FuzzFB02SnapshotIndex (M-A6): the session index agrees with the fresh
// path's sort.Search over the decoded entries, for every entry key and an
// arbitrary probe, holds each entry's manifest UUID and SHA-256, and stays
// within twice the descriptor's size. It runs on the raw descriptor bytes
// and, since the CRC rejects nearly every mutated frame, also on a
// well-formed descriptor built from an arbitrary newline-separated key set.
func FuzzFB02SnapshotIndex(f *testing.F) {
	d := sampleSnapshotDescriptor()
	if b, err := encodeSnapshotDescriptor(d); err == nil {
		f.Add(b, "a.txt\nb.txt", "a.txt")
		f.Add(b, "", "a")
		f.Add(b, "x\ny\x00\nz/\u00e9", "c")
	}
	check := func(t *testing.T, data []byte, probe string) {
		d, err := decodeSnapshotDescriptor(data)
		if err != nil {
			return
		}
		ix, err := newSnapshotIndex(d.SnapshotID, d, nil)
		if err != nil {
			return
		}
		if ix.cost > 2*int64(len(data)) {
			t.Fatalf("index cost %d for a %d-byte descriptor", ix.cost, len(data))
		}
		keys := []string{probe}
		for _, e := range d.Entries {
			keys = append(keys, e.Key, e.Key+"\x00", e.Key[:len(e.Key)/2])
		}
		for _, k := range keys {
			i, found := ix.find(k)
			j := sort.Search(len(d.Entries), func(j int) bool { return d.Entries[j].Key >= k })
			want := j < len(d.Entries) && d.Entries[j].Key == k
			if found != want || found && i != j {
				t.Fatalf("key %q: index (%d, %v), fresh (%d, %v)", k, i, found, j, want)
			}
			if found {
				sum, _ := decodeHexSHA256(d.Entries[j].ManifestSHA256)
				if ix.mans[i] != d.Entries[j].ManifestUUID || ix.sums[i] != sum {
					t.Fatalf("key %q: index holds another entry's manifest", k)
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, keyBlob, probe string) {
		check(t, data, probe)
		keys := strings.Split(keyBlob, "\n")
		sort.Strings(keys)
		keys = slices.Compact(keys)
		d := sampleSnapshotDescriptor()
		d.Entries = d.Entries[:0]
		for i, k := range keys {
			sum := sha256.Sum256([]byte(k))
			d.Entries = append(d.Entries, snapshotEntryV1{Key: k, ManifestUUID: newUUIDv7(), ManifestSHA256: hex.EncodeToString(sum[:]),
				Size: int64(i), ETag: "e", ContentType: "text/plain"})
		}
		if b, err := encodeSnapshotDescriptor(d); err == nil {
			check(t, b, probe)
		}
	})
}

// FuzzFB02SyncIdentity (M-A5): equivalence is an equivalence relation that
// never holds across a different object digest, size, content type or
// metadata, and never holds when either identity is unknown.
func FuzzFB02SyncIdentity(f *testing.F) {
	f.Add("aa", int64(5), "text/plain", "k", "v", "aa", int64(5), "", "k", "v")
	f.Add("", int64(0), "", "", "", "", int64(0), "", "", "")
	f.Fuzz(func(t *testing.T, sa string, za int64, ca, ka, va, sb string, zb int64, cb, kb, vb string) {
		hexOf := func(s string) string {
			if s == "" {
				return ""
			}
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		}
		a := newSyncIdentity(hexOf(sa), za, ca, []metadataKV{{Key: ka, Value: va}})
		b := newSyncIdentity(hexOf(sb), zb, cb, []metadataKV{{Key: kb, Value: vb}})
		eqAB, knownAB := a.match(b)
		eqBA, knownBA := b.match(a)
		if eqAB != eqBA || knownAB != knownBA {
			t.Fatalf("match is not symmetric")
		}
		if eqAB && !knownAB {
			t.Fatalf("equivalent with an identity unknown")
		}
		if knownAB != (sa != "" && sb != "") {
			t.Fatalf("known %v for digests %q %q", knownAB, sa, sb)
		}
		if eqAB && (sa != sb || za != zb || a.contentType != b.contentType || ka != kb || va != vb) {
			t.Fatalf("equivalent across different identities")
		}
		if self, known := a.match(a); known && !self {
			t.Fatalf("an identity is not equivalent to itself")
		}
	})
}
