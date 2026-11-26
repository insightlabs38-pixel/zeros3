package main

// FB-02 (M-A5) replication equivalence and idempotence. Equivalence is
// (object SHA-256, size, content type, user metadata), never the ETag; the
// explicit idempotent commit (if_unchanged, `replicate -if-unchanged`, the
// authority import) checks it before anything is written; an ordinary rerun
// still publishes a new version (ADR-POL-01 pending). Fixtures cover
// ETag-equal but content-different objects (a forged ETag), metadata-only,
// content-only and content-type-only differences, and multipart-origin
// sources whose ETag differs from the same bytes' single-part ETag.

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fb02Counter counts requests by method and path in front of a server.
type fb02Counter struct {
	h  http.Handler
	mu sync.Mutex
	n  map[string]int
}

func (c *fb02Counter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.n[r.Method+" "+r.URL.Path]++
	c.mu.Unlock()
	c.h.ServeHTTP(w, r)
}

func (c *fb02Counter) take() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.n
	c.n = map[string]int{}
	return out
}

type fb02Pair struct {
	src, dst       *Server
	dstDir         string
	srcTS, dstTS   *httptest.Server
	srcCnt, dstCnt *fb02Counter
	creds          Credentials
	region         string
}

func newFB02Pair(t *testing.T) *fb02Pair {
	t.Helper()
	_, src, dstDir, dst, creds, region := newReplicateTestServerPair(t)
	p := &fb02Pair{src: src, dst: dst, dstDir: dstDir, creds: creds, region: region,
		srcCnt: &fb02Counter{h: src, n: map[string]int{}}, dstCnt: &fb02Counter{h: dst, n: map[string]int{}}}
	p.srcTS, p.dstTS = httptest.NewServer(p.srcCnt), httptest.NewServer(p.dstCnt)
	t.Cleanup(p.srcTS.Close)
	t.Cleanup(p.dstTS.Close)
	mustCreateReplicateBucket(t, src, "src")
	mustCreateReplicateBucket(t, dst, "dst")
	return p
}

func (p *fb02Pair) cfg(srcKey, dstKey string) replicateConfig {
	return replicateConfig{
		Source: syncClientConfig{Endpoint: p.srcTS.URL, Bucket: "src", Key: srcKey, Creds: p.creds, Region: p.region, HTTPClient: p.srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: p.dstTS.URL, Bucket: "dst", Key: dstKey, Creds: p.creds, Region: p.region, HTTPClient: p.dstTS.Client()},
	}
}

func (p *fb02Pair) ns(ifUnchanged, mustBeAbsent bool) namespaceReplicateConfig {
	c := p.cfg("", "")
	return namespaceReplicateConfig{Source: c.Source, Dest: c.Dest, IfUnchanged: ifUnchanged, DestMustBeAbsent: mustBeAbsent}
}

// fb02Versions is the current manifest UUID and the retained history length.
func fb02Versions(t *testing.T, s *Store, bucket, key string) (string, int) {
	t.Helper()
	hist, cur, err := s.ListVersions(bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		return "", len(hist)
	}
	return cur.manifestUUID, len(hist)
}

// fb02CountManifests counts manifest publications by the sync commit path.
func fb02CountManifests(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	withTestHook(t, func(point string) {
		if point == hookAfterManifestPublished {
			mu.Lock()
			n++
			mu.Unlock()
		}
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// fb02ForgeETag commits, at bucket/key, an object holding body whose
// manifest carries etag instead of body's own: the tamper fixture for an
// ETag-equal but content-different destination.
func fb02ForgeETag(t *testing.T, s *Store, bucket, key string, body []byte, etag, contentType string, md map[string]string) {
	t.Helper()
	if _, err := s.PutObject(bucket, key, body, contentType, md); err != nil {
		t.Fatal(err)
	}
	_, man, err := s.HeadObject(bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	forged := buildManifestV1FromRefs(man.Chunks, man.TotalLength, sum, etag, contentType, md)
	id, msum, err := s.publishManifest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commitObjectRootChecked(bucket, key, id, msum, forged, func(*objectEntry, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e, _, err := s.HeadObject(bucket, key); err != nil || e.etag != etag {
		t.Fatalf("forged ETag not in place: %v %+v", err, e)
	}
}

// fb02PutMultipart writes body at bucket/key through a two-part upload, so
// its ETag is the multipart form ("...-2").
func fb02PutMultipart(t *testing.T, ts *httptest.Server, creds Credentials, region, bucket, key string, body []byte) string {
	t.Helper()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	up := doCreateMultipartUpload(t, ts.Client(), ts.URL, signer, bucket, key)
	cut := minMultipartPartSize
	var parts []completedPartXML
	for i, b := range [][]byte{body[:cut], body[cut:]} {
		etag, status, resp := doUploadPart(t, ts.Client(), ts.URL, signer, bucket, key, up, i+1, b)
		if status != http.StatusOK {
			t.Fatalf("UploadPart %d: %d %s", i+1, status, resp)
		}
		parts = append(parts, completedPartXML{PartNumber: i + 1, ETag: etag})
	}
	res, status, resp := doCompleteMultipartUpload(t, ts.Client(), ts.URL, signer, bucket, key, up, parts)
	if status != http.StatusOK || !strings.HasSuffix(strings.Trim(res.ETag, `"`), "-2") {
		t.Fatalf("CompleteMultipartUpload: %d %s", status, resp)
	}
	return strings.Trim(res.ETag, `"`)
}

func TestFB02_ReplicateEquivalenceCases(t *testing.T) {
	body := genRandomBytes(5101, 700_000)
	other := genRandomBytes(5102, 700_000)
	md := map[string]string{"origin": "fb02"}
	srcETag := fmt.Sprintf("%x", md5.Sum(body))
	cases := []struct {
		name  string
		setup func(t *testing.T, p *fb02Pair) // destination state before the run
		same  bool                            // the destination is equivalent
	}{
		{"absent", func(*testing.T, *fb02Pair) {}, false},
		{"equivalent", func(t *testing.T, p *fb02Pair) {
			if _, err := p.dst.store.PutObject("dst", "k", body, "application/x-fb02", md); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"metadata-only", func(t *testing.T, p *fb02Pair) {
			if _, err := p.dst.store.PutObject("dst", "k", body, "application/x-fb02", map[string]string{"origin": "other"}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"content-type-only", func(t *testing.T, p *fb02Pair) {
			if _, err := p.dst.store.PutObject("dst", "k", body, "text/plain", md); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"content-only", func(t *testing.T, p *fb02Pair) {
			if _, err := p.dst.store.PutObject("dst", "k", other, "application/x-fb02", md); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"etag-equal-content-different", func(t *testing.T, p *fb02Pair) {
			fb02ForgeETag(t, p.dst.store, "dst", "k", other, srcETag, "application/x-fb02", md)
		}, false},
	}
	for _, tc := range cases {
		for _, ifUnchanged := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/if-unchanged=%v", tc.name, ifUnchanged), func(t *testing.T) {
				p := newFB02Pair(t)
				if _, err := p.src.store.PutObject("src", "k", body, "application/x-fb02", md); err != nil {
					t.Fatal(err)
				}
				tc.setup(t, p)
				cfg := p.cfg("k", "k")
				cfg.IfUnchanged = ifUnchanged
				plan, err := planReplication(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if (plan.action == destActionEquivalent) != tc.same || plan.skip != (tc.same && ifUnchanged) {
					t.Fatalf("plan: action %q skip %v, want equivalent=%v", plan.action, plan.skip, tc.same)
				}
				beforeID, beforeHist := fb02Versions(t, p.dst.store, "dst", "k")
				fp := storeContentFingerprint(t, p.dstDir)
				manifests := fb02CountManifests(t)
				p.dstCnt.take()
				stats, err := replicateObject(cfg)
				if err != nil {
					t.Fatal(err)
				}
				afterID, afterHist := fb02Versions(t, p.dst.store, "dst", "k")
				reqs := p.dstCnt.take()
				if tc.same && ifUnchanged {
					// The skip: no commit, no negotiation, no write of any kind.
					if stats.Unchanged != 1 || afterID != beforeID || afterHist != beforeHist || manifests() != 0 ||
						reqs["POST "+zeros3SyncCommitPath] != 0 || storeContentFingerprint(t, p.dstDir) != fp {
						t.Fatalf("equivalent destination was not left alone: stats %+v, %s -> %s, history %d -> %d, %d manifests, requests %v",
							stats, beforeID, afterID, beforeHist, afterHist, manifests(), reqs)
					}
					return
				}
				// Everything else is replicated: a new current version holding
				// the source's bytes, content type and metadata.
				if stats.Unchanged != 0 || afterID == beforeID || manifests() != 1 {
					t.Fatalf("not replicated: stats %+v, version %s -> %s, %d manifests", stats, beforeID, afterID, manifests())
				}
				if beforeID != "" && afterHist != beforeHist+1 {
					t.Fatalf("history %d -> %d", beforeHist, afterHist)
				}
				entry, got, err := p.dst.store.GetObject("dst", "k")
				if err != nil || !bytes.Equal(got, body) || entry.contentType != "application/x-fb02" {
					t.Fatalf("destination does not hold the source object: %v", err)
				}
				_, man, _ := p.dst.store.HeadObject("dst", "k")
				if len(man.Metadata) != 1 || man.Metadata[0] != (metadataKV{Key: "origin", Value: "fb02"}) {
					t.Fatalf("metadata %+v", man.Metadata)
				}
			})
		}
	}
}

func TestFB02_MultipartOriginEquivalence(t *testing.T) {
	p := newFB02Pair(t)
	body := genRandomBytes(5103, minMultipartPartSize+300_000)
	srcETag := fb02PutMultipart(t, p.srcTS, p.creds, p.region, "src", "mp", body)
	if _, err := replicateObject(p.cfg("mp", "mp")); err != nil {
		t.Fatal(err)
	}
	dstEntry, _, err := p.dst.store.HeadObject("dst", "mp")
	if err != nil || dstEntry.etag == srcETag {
		t.Fatalf("fixture needs ETags that differ for the same bytes: %v %q", err, srcETag)
	}

	// The dry-run, the idempotent rerun and diff see one object.
	plan, err := planReplication(p.cfg("mp", "mp"))
	if err != nil || plan.action != destActionEquivalent {
		t.Fatalf("dry-run: %q %v", plan.action, err)
	}
	cfg := p.cfg("mp", "mp")
	cfg.IfUnchanged = true
	fp := storeContentFingerprint(t, p.dstDir)
	stats, err := replicateObject(cfg)
	if err != nil || stats.Unchanged != 1 || storeContentFingerprint(t, p.dstDir) != fp {
		t.Fatalf("idempotent rerun of a multipart-origin object wrote: %+v %v", stats, err)
	}
	d, err := diffObjects(cfg.Source, cfg.Dest)
	if err != nil || !d.ContentEqual {
		t.Fatalf("diff: ContentEqual %v %v", d.ContentEqual, err)
	}

	// A fork rerun over its own result is not a conflict. Without
	// -if-unchanged it still republishes, as before.
	before, hist := fb02Versions(t, p.dst.store, "dst", "mp")
	fork := p.cfg("mp", "mp")
	fork.DestMustBeAbsent = true
	if _, err := replicateObject(fork); err != nil {
		t.Fatalf("fork rerun over an equivalent multipart-origin object: %v", err)
	}
	if after, h := fb02Versions(t, p.dst.store, "dst", "mp"); after == before || h != hist+1 {
		t.Fatalf("fork rerun no longer republishes: %s -> %s, history %d -> %d", before, after, hist, h)
	}
	fork.IfUnchanged = true
	fp = storeContentFingerprint(t, p.dstDir)
	if stats, err := replicateObject(fork); err != nil || stats.Unchanged != 1 || storeContentFingerprint(t, p.dstDir) != fp {
		t.Fatalf("idempotent fork rerun: %+v %v", stats, err)
	}
}

// A create-only run never overwrites, and never skips, a destination whose
// ETag matches but whose content does not.
func TestFB02_CreateOnlyRefusesETagEqualContentDifferent(t *testing.T) {
	p := newFB02Pair(t)
	body, other := genRandomBytes(5104, 300_000), genRandomBytes(5105, 300_000)
	if _, err := p.src.store.PutObject("src", "k", body, "", nil); err != nil {
		t.Fatal(err)
	}
	fb02ForgeETag(t, p.dst.store, "dst", "k", other, fmt.Sprintf("%x", md5.Sum(body)), "application/octet-stream", nil)
	before, hist := fb02Versions(t, p.dst.store, "dst", "k")
	for _, ifUnchanged := range []bool{false, true} {
		cfg := p.cfg("k", "k")
		cfg.DestMustBeAbsent, cfg.IfUnchanged = true, ifUnchanged
		plan, err := planReplication(cfg)
		if err != nil || plan.action != destActionConflict || plan.skip {
			t.Fatalf("plan: %q skip %v %v", plan.action, plan.skip, err)
		}
		if _, err := replicateObject(cfg); !errors.Is(err, errSyncRemoteConflict) {
			t.Fatalf("create-only over different content: %v", err)
		}
	}
	if after, h := fb02Versions(t, p.dst.store, "dst", "k"); after != before || h != hist {
		t.Fatalf("destination changed: %s -> %s", before, after)
	}
}

func TestFB02_NamespaceRerunWritesNothing(t *testing.T) {
	p := newFB02Pair(t)
	const n = 24
	for i := 0; i < n; i++ {
		md := map[string]string{"i": fmt.Sprint(i)}
		if _, err := p.src.store.PutObject("src", fmt.Sprintf("o/%02d", i), genRandomBytes(int64(6000+i), 2000+i*3000), "", md); err != nil {
			t.Fatal(err)
		}
	}
	fb02PutMultipart(t, p.srcTS, p.creds, p.region, "src", "o/mp", genRandomBytes(5106, minMultipartPartSize+1000))
	first, err := replicateNamespace(p.ns(true, false))
	if err != nil || !first.OK() || first.Replicated != n+1 || first.Stats.Unchanged != 0 {
		t.Fatalf("first run: %+v %v", first, err)
	}

	fp := storeContentFingerprint(t, p.dstDir)
	manifests := fb02CountManifests(t)
	p.srcCnt.take()
	p.dstCnt.take()
	rerun, err := replicateNamespace(p.ns(true, false))
	srcReqs, dstReqs := p.srcCnt.take(), p.dstCnt.take()
	if err != nil || !rerun.OK() || rerun.Stats.Unchanged != n+1 || rerun.Stats.UploadedBytes != 0 {
		t.Fatalf("idempotent rerun: %+v %v", rerun, err)
	}
	if manifests() != 0 || storeContentFingerprint(t, p.dstDir) != fp {
		t.Fatalf("idempotent rerun wrote to the destination: %d manifests", manifests())
	}
	// One discovery per endpoint for the run; per object one source and
	// one destination describe, and no negotiate or commit.
	if srcReqs["GET "+zeros3SyncInfoPath] != 1 || dstReqs["GET "+zeros3SyncInfoPath] != 1 ||
		dstReqs["GET "+zeros3SyncObjectPath] != n+1 || dstReqs["POST "+zeros3SyncNegotiatePath] != 0 || dstReqs["POST "+zeros3SyncCommitPath] != 0 {
		t.Fatalf("requests: source %v destination %v", srcReqs, dstReqs)
	}
	t.Logf("idempotent rerun of %d objects: source %v destination %v", n+1, srcReqs, dstReqs)

	// The ordinary rerun keeps its behaviour: a new version per object.
	ordinary, err := replicateNamespace(p.ns(false, false))
	if err != nil || !ordinary.OK() || ordinary.Stats.Unchanged != 0 || manifests() != n+1 {
		t.Fatalf("ordinary rerun: %+v %v, %d manifests", ordinary, err, manifests())
	}
	t.Logf("ordinary rerun of %d objects: source %v destination %v", n+1, p.srcCnt.take(), p.dstCnt.take())
	if _, hist := fb02Versions(t, p.dst.store, "dst", "o/07"); hist != 1 {
		t.Fatalf("ordinary rerun history %d, want 1", hist)
	}
}

// fb02Commit sends one signed sync commit straight to srv.
func fb02Commit(t *testing.T, srv *Server, creds Credentials, region string, req syncCommitRequest) (int, syncCommitResponse, string) {
	t.Helper()
	req.Protocol, req.CDC, req.Hash = zeros3SyncProtocolVersion, zeros3SyncCDCFormat, zeros3SyncHashAlgorithm
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, zeros3SyncCommitPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	sum := sha256.Sum256(body)
	if err := signSigV4Request(r, creds, region, hex.EncodeToString(sum[:]), time.Now()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp syncCommitResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, resp, w.Body.String()
}

// fb02Chunks stores body under a scratch key and returns its chunk list.
func fb02Chunks(t *testing.T, s *Store, body []byte) []syncChunkDescriptor {
	t.Helper()
	key := fmt.Sprintf("scratch/%x", sha256.Sum256(body))
	if _, err := s.PutObject("b", key, body, "", nil); err != nil {
		t.Fatal(err)
	}
	_, man, err := s.HeadObject("b", key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]syncChunkDescriptor, len(man.Chunks))
	for i, c := range man.Chunks {
		out[i] = syncChunkDescriptor{SHA256: c.SHA256, Length: c.Length}
	}
	return out
}

func TestFB02_CommitIfUnchanged(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	mustCreateReplicateBucket(t, srv, "b")
	a, b := genRandomBytes(5107, 400_000), genRandomBytes(5108, 400_000)
	ca, cb := fb02Chunks(t, srv.store, a), fb02Chunks(t, srv.store, b)
	md := map[string]string{"x": "1"}
	commit := func(req syncCommitRequest) (int, syncCommitResponse, string) {
		req.Bucket, req.Key = "b", "k"
		return fb02Commit(t, srv, creds, region, req)
	}
	if code, first, body := commit(syncCommitRequest{Chunks: ca, Metadata: md, IfUnchanged: true}); code != 200 || first.Unchanged {
		t.Fatalf("first commit: %d %s", code, body)
	}
	cur, hist := fb02Versions(t, srv.store, "b", "k")
	fp := storeContentFingerprint(t, dir)
	manifests := fb02CountManifests(t)
	for _, req := range []syncCommitRequest{
		{Chunks: ca, Metadata: md, IfUnchanged: true},
		{Chunks: ca, Metadata: md, IfUnchanged: true, ExpectAbsent: true},
		{Chunks: ca, Metadata: md, IfUnchanged: true, ExpectedETag: "not-the-etag"},
		{Chunks: ca, Metadata: md, IfUnchanged: true, ContentType: "application/octet-stream"},
	} {
		code, resp, body := commit(req)
		if code != 200 || !resp.Unchanged || resp.VersionID != cur {
			t.Fatalf("equivalent commit %+v: %d %s", req, code, body)
		}
	}
	if manifests() != 0 || storeContentFingerprint(t, dir) != fp {
		t.Fatalf("an equivalent idempotent commit wrote: %d manifests", manifests())
	}
	// Not equivalent: the preconditions apply, then the commit publishes.
	if code, _, body := commit(syncCommitRequest{Chunks: cb, Metadata: md, IfUnchanged: true, ExpectAbsent: true}); code != http.StatusPreconditionFailed {
		t.Fatalf("different content with expect_absent: %d %s", code, body)
	}
	for _, req := range []syncCommitRequest{
		{Chunks: ca, Metadata: map[string]string{"x": "2"}, IfUnchanged: true},
		{Chunks: ca, Metadata: map[string]string{"x": "2"}, ContentType: "text/plain", IfUnchanged: true},
		{Chunks: cb, Metadata: map[string]string{"x": "2"}, ContentType: "text/plain", IfUnchanged: true},
		{Chunks: cb, Metadata: map[string]string{"x": "2"}, ContentType: "text/plain"}, // the ordinary rerun
	} {
		code, resp, body := commit(req)
		now, h := fb02Versions(t, srv.store, "b", "k")
		if code != 200 || resp.Unchanged || now == cur || h != hist+1 {
			t.Fatalf("commit %+v: %d %s, history %d", req, code, body, h)
		}
		cur, hist = now, h
	}

	// The capability is advertised.
	ts := httptest.NewServer(srv)
	defer ts.Close()
	d, err := discoverZeroS3Sync(syncClientConfig{Endpoint: ts.URL, Creds: creds, Region: region, HTTPClient: ts.Client()})
	if err != nil || !d.CommitIfUnchanged {
		t.Fatalf("discovery: %+v %v", d, err)
	}
}

// Idempotent commits racing each other and ordinary writers: commits of the
// same content to an absent key yield exactly one version, and no
// idempotent commit ever reports a version holding other content.
func TestFB02_ConcurrentIfUnchangedCommits(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	mustCreateReplicateBucket(t, srv, "b")
	a, b := genRandomBytes(5109, 200_000), genRandomBytes(5110, 200_000)
	ca, cb := fb02Chunks(t, srv.store, a), fb02Chunks(t, srv.store, b)
	shaA := fmt.Sprintf("%x", sha256.Sum256(a))

	const n = 8
	resps := make([]syncCommitResponse, n)
	codes := make([]int, n)
	runBarrieredConcurrentPuts(t, n, func(i int) {
		codes[i], resps[i], _ = fb02Commit(t, srv, creds, region, syncCommitRequest{Bucket: "b", Key: "same", Chunks: ca, IfUnchanged: true})
	})
	published := 0
	for i := range resps {
		if codes[i] != 200 || resps[i].VersionID != resps[0].VersionID {
			t.Fatalf("commit %d: %d %+v (first %+v)", i, codes[i], resps[i], resps[0])
		}
		if !resps[i].Unchanged {
			published++
		}
	}
	if cur, hist := fb02Versions(t, srv.store, "b", "same"); published != 1 || hist != 0 || cur != resps[0].VersionID {
		t.Fatalf("%d published, history %d", published, hist)
	}

	// Mixed: idempotent commits of a against ordinary commits of b.
	runBarrieredConcurrentPuts(t, n, func(i int) {
		req := syncCommitRequest{Bucket: "b", Key: "mixed", Chunks: ca, IfUnchanged: true}
		if i%2 == 1 {
			req = syncCommitRequest{Bucket: "b", Key: "mixed", Chunks: cb}
		}
		codes[i], resps[i], _ = fb02Commit(t, srv, creds, region, req)
	})
	hist, cur, err := srv.store.ListVersions("b", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{}
	for _, row := range hist {
		m, err := srv.store.readVerifiedManifest(row.manifestUUID, row.manifestSHA256)
		if err != nil {
			t.Fatal(err)
		}
		identity[row.manifestUUID] = m.ObjectSHA256
	}
	if m, err := srv.store.readVerifiedManifest(cur.manifestUUID, cur.manifestSHA256); err == nil {
		identity[cur.manifestUUID] = m.ObjectSHA256
	}
	versions := 0
	for i := range resps {
		if codes[i] != 200 {
			t.Fatalf("mixed commit %d: %d", i, codes[i])
		}
		if !resps[i].Unchanged {
			versions++
		} else if identity[resps[i].VersionID] != shaA {
			t.Fatalf("an idempotent commit of a reported version %s holding %s", resps[i].VersionID, identity[resps[i].VersionID])
		}
	}
	if versions != len(hist)+1 {
		t.Fatalf("%d published responses but %d versions", versions, len(hist)+1)
	}
}

// A crash after the manifest is written and before the namespace commit
// leaves the old version current and an unreferenced manifest for GC; a crash
// after the acknowledgement leaves the new one. Either way the idempotent
// rerun converges to one version of the new content, and the one after it
// writes nothing.
func TestFB02_IfUnchangedCrashCuts(t *testing.T) {
	useLooseLayout(t, "counts the orphan manifest left by the crash")
	for _, point := range []string{hookAfterManifestPublished, hookAfterAck} {
		t.Run(point, func(t *testing.T) {
			dir, srv, creds, region := newSyncTestServer(t)
			mustCreateReplicateBucket(t, srv, "b")
			a, b := genRandomBytes(5111, 300_000), genRandomBytes(5112, 300_000)
			ca, cb := fb02Chunks(t, srv.store, a), fb02Chunks(t, srv.store, b)
			if code, _, body := fb02Commit(t, srv, creds, region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: ca}); code != 200 {
				t.Fatalf("seed: %d %s", code, body)
			}
			old, _ := fb02Versions(t, srv.store, "b", "k")
			withTestHook(t, func(p string) {
				if p == point {
					panic(simulatedCrash{point: p})
				}
			})
			runExpectingSimulatedCrash(t, func() {
				fb02Commit(t, srv, creds, region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: cb, IfUnchanged: true})
			})
			testHook = nil
			srv.store.Close()

			s2, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			srv2 := NewServer(s2, creds, region)
			cur, hist := fb02Versions(t, s2, "b", "k")
			crashedEarly := point == hookAfterManifestPublished
			if (cur == old) != crashedEarly || hist != btoi(!crashedEarly) {
				t.Fatalf("after the crash: current %s (old %s), history %d", cur, old, hist)
			}
			code, resp, body := fb02Commit(t, srv2, creds, region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: cb, IfUnchanged: true})
			if code != 200 || resp.Unchanged == crashedEarly {
				t.Fatalf("rerun: %d %s", code, body)
			}
			fp := storeContentFingerprint(t, dir)
			if code, resp, _ := fb02Commit(t, srv2, creds, region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: cb, IfUnchanged: true}); code != 200 || !resp.Unchanged ||
				storeContentFingerprint(t, dir) != fp {
				t.Fatalf("second rerun wrote: %d %+v", code, resp)
			}
			if _, got, err := s2.GetObject("b", "k"); err != nil || !bytes.Equal(got, b) {
				t.Fatalf("content after rerun: %v", err)
			}
			if _, h := fb02Versions(t, s2, "b", "k"); h != 1 {
				t.Fatalf("history %d, want 1", h)
			}
			s2.Close()
			res, err := gcCollect(dir, true)
			if err != nil || !res.LiveSetOK || res.ManifestsDeleted != btoi(crashedEarly) {
				t.Fatalf("gc: %v %+v", err, res)
			}
		})
	}
}

// Snapshot describe returns the object SHA-256, and a restore rerun over a
// multipart-origin object's own result is not a conflict, while an
// ETag-equal but content-different destination is.
func TestFB02_SnapshotDescribeAndRestoreEquivalence(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := genRandomBytes(5113, minMultipartPartSize+200_000)
	fb02PutMultipart(t, ts, cfg.Creds, cfg.Region, "b", "src/mp", body)
	small := genRandomBytes(5114, 100_000)
	if _, err := srv.store.PutObject("b", "src/small", small, "", nil); err != nil {
		t.Fatal(err)
	}
	snap, err := createSnapshotRemote(cfg, "b", "src")
	if err != nil {
		t.Fatal(err)
	}
	d, err := fetchSnapshotObjectDescriptor(cfg, snap.SnapshotID, "src/mp")
	if err != nil || d.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatalf("snapshot describe object_sha256 %q: %v", d.ObjectSHA256, err)
	}
	dest := cfg
	dest.Bucket = "b"
	restore := restoreNamespaceConfig{Snapshot: cfg, SnapshotID: snap.SnapshotID, Dest: dest, DestPrefix: "out"}
	for run := 0; run < 2; run++ {
		res, err := restoreNamespace(restore)
		if err != nil || !res.OK() || res.Replicated != 2 {
			t.Fatalf("restore run %d: %+v %v", run, res, err)
		}
	}
	// A forged destination: the small object's ETag over other bytes.
	fb02ForgeETag(t, srv.store, "b", "out2/small", genRandomBytes(5115, 100_000), fmt.Sprintf("%x", md5.Sum(small)), "application/octet-stream", nil)
	before, _ := fb02Versions(t, srv.store, "b", "out2/small")
	restore.DestPrefix = "out2"
	res, err := restoreNamespace(restore)
	if err != nil || res.Failed != 1 || len(res.Failures) != 1 || res.Failures[0].Key != "src/small" {
		t.Fatalf("restore over a forged destination: %+v %v", res, err)
	}
	if after, _ := fb02Versions(t, srv.store, "b", "out2/small"); after != before {
		t.Fatalf("restore overwrote a destination with other content")
	}
}

// The authority import is the explicit idempotent path that already
// existed; its retry now writes no manifest (F6-REPL-03).
func TestFB02_AuthorityImportRetryWritesNoManifest(t *testing.T) {
	src, dst := authMigratedLab(t), newAuthLab(t)
	src.reclaim()
	man := src.man("k3")
	sum, err := decodeHexSHA256(man.ObjectSHA256)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	server := NewServer(dst.s, creds, defaultRegion)
	control := authImportRequest{Version: authXferVersion, SHA256: man.ObjectSHA256, syncCommitRequest: syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "b", Key: "replica", ContentType: man.ContentType, ExpectAbsent: true,
	}}
	for _, ref := range man.Chunks {
		control.Chunks = append(control.Chunks, syncChunkDescriptor{SHA256: ref.SHA256, Length: ref.Length})
	}
	var full bytes.Buffer
	if err := src.s.ExportAuthorityClosure(sum, &full); err != nil {
		t.Fatal(err)
	}
	send := func() (int, syncCommitResponse) {
		header, err := json.Marshal(control)
		if err != nil {
			t.Fatal(err)
		}
		wire := append(binaryBE32(len(header)), header...)
		wire = append(wire, full.Bytes()...)
		digest := sha256.Sum256(wire)
		r := httptest.NewRequest(http.MethodPost, zeros3AuthImportPath, bytes.NewReader(wire))
		if err := signSigV4Request(r, creds, defaultRegion, hex.EncodeToString(digest[:]), time.Now()); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		var resp syncCommitResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}
	code, first := send()
	if code != 200 || first.Unchanged {
		t.Fatalf("import: %d %+v", code, first)
	}
	manifests := fb02CountManifests(t)
	code, retry := send()
	if code != 200 || !retry.Unchanged || retry.VersionID != first.VersionID || manifests() != 0 {
		t.Fatalf("retry: %d %+v, %d manifests", code, retry, manifests())
	}
}

func binaryBE32(n int) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }
