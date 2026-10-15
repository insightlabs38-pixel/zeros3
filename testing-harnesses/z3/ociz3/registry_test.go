package ociz3

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRegistry is a TLS server speaking just enough of the distribution API:
// anonymous bearer-token auth, manifests and blobs by digest.
type fakeRegistry struct {
	srv      *httptest.Server
	blobs    map[string][]byte
	manifest map[string][]byte
	requests atomic.Int32
	limited  atomic.Int32 // number of 429s still to serve
	tokens   atomic.Int32
	evil     func(path string) []byte // overrides a response body
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{blobs: map[string][]byte{}, manifest: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokens.Add(1)
		if r.URL.Query().Get("scope") != "repository:lib/img:pull" {
			http.Error(w, "scope", 400)
			return
		}
		fmt.Fprint(w, `{"token":"t0k"}`)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("Www-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="fake"`)
			w.WriteHeader(401)
			return
		}
		if f.limited.Load() > 0 {
			f.limited.Add(-1)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v2/lib/img/")
		var body []byte
		switch {
		case strings.HasPrefix(path, "blobs/"):
			body = f.blobs[strings.TrimPrefix(path, "blobs/")]
		case strings.HasPrefix(path, "manifests/"):
			body = f.manifest[strings.TrimPrefix(path, "manifests/")]
		}
		if f.evil != nil {
			if b := f.evil(path); b != nil {
				body = b
			}
		}
		if body == nil {
			w.WriteHeader(404)
			return
		}
		w.Write(body)
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) client(t *testing.T, budget int64) *Client {
	c := NewClient(strings.TrimPrefix(f.srv.URL, "https://"), t.TempDir(), budget)
	c.HTTP = f.srv.Client()
	return c
}

func TestClientAuthVerificationCacheAndBudget(t *testing.T) {
	f := newFakeRegistry(t)
	blob := []byte(strings.Repeat("layer bytes ", 100))
	bd := Sum256(blob)
	f.blobs[bd] = blob
	man := []byte(`{"schemaVersion":2,"mediaType":"` + MTOCIIndex + `","manifests":[]}`)
	md := Sum256(man)
	f.manifest[md] = man

	c := f.client(t, 0)
	d, got, err := c.Manifest("lib/img", md)
	if err != nil || d != md || string(got) != string(man) {
		t.Fatalf("manifest: %v", err)
	}
	if f.tokens.Load() != 1 {
		t.Fatalf("token requests: %d", f.tokens.Load())
	}
	if b, err := c.Blob("lib/img", bd, int64(len(blob))); err != nil || string(b) != string(blob) {
		t.Fatalf("blob: %v", err)
	}
	if f.tokens.Load() != 1 {
		t.Fatalf("token was renegotiated: %d", f.tokens.Load())
	}
	// both are now served from the cache without a request
	before := f.requests.Load()
	if _, err := c.Blob("lib/img", bd, int64(len(blob))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Manifest("lib/img", md); err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != before {
		t.Fatal("cached content was requested again")
	}

	// a server that sends other bytes than the digest names is rejected and not cached
	bad := f.client(t, 0)
	f.evil = func(p string) []byte {
		if strings.HasPrefix(p, "blobs/") {
			return append([]byte(nil), blob[:len(blob)-1]...)
		}
		return nil
	}
	if _, err := bad.Blob("lib/img", bd, int64(len(blob))); !errors.Is(err, ErrDigest) {
		t.Fatalf("short blob: %v", err)
	}
	f.evil = func(p string) []byte {
		if strings.HasPrefix(p, "blobs/") {
			return append(append([]byte(nil), blob...), 'x')
		}
		return nil
	}
	if _, err := bad.Blob("lib/img", bd, int64(len(blob))); !errors.Is(err, ErrDigest) {
		t.Fatalf("long blob: %v", err)
	}
	f.evil = func(p string) []byte {
		if strings.HasPrefix(p, "manifests/") {
			return []byte(`{"schemaVersion":2}`)
		}
		return nil
	}
	if _, _, err := bad.Manifest("lib/img", md); !errors.Is(err, ErrDigest) {
		t.Fatalf("manifest swapped: %v", err)
	}
	f.evil = nil
	if b := bad.cached(strings.TrimPrefix(bd, "sha256:")); b != nil {
		t.Fatal("a rejected blob was cached")
	}

	// the byte budget and hostile sizes stop a fetch before any request
	small := f.client(t, 100)
	before = f.requests.Load()
	if _, err := small.Blob("lib/img", bd, int64(len(blob))); !errors.Is(err, ErrLimit) {
		t.Fatalf("budget: %v", err)
	}
	if _, err := f.client(t, 0).Blob("lib/img", bd, 1<<62); !errors.Is(err, ErrLimit) {
		t.Fatalf("hostile size: %v", err)
	}
	if _, err := f.client(t, 0).Blob("lib/img", bd, -1); !errors.Is(err, ErrLimit) {
		t.Fatalf("negative size: %v", err)
	}
	if f.requests.Load() != before {
		t.Fatal("a refused fetch reached the server")
	}
}

func TestClientRetriesRateLimit(t *testing.T) {
	f := newFakeRegistry(t)
	blob := []byte("x")
	f.blobs[Sum256(blob)] = blob
	f.limited.Store(1)
	c := f.client(t, 0)
	if _, err := c.Blob("lib/img", Sum256(blob), 1); err != nil {
		t.Fatalf("after a 429: %v", err)
	}
}

func TestClientRefusesInsecureOrMalformedChallenge(t *testing.T) {
	c := NewClient("example.invalid", "", 0)
	for _, ch := range []string{
		`Basic realm="x"`,
		`Bearer realm="http://auth.example/token",service="s"`,
		`Bearer realm="file:///etc/passwd"`,
		`Bearer service="s"`,
		``,
	} {
		if _, err := c.token(ch, "lib/img"); err == nil {
			t.Errorf("challenge %q accepted", ch)
		}
	}
	_ = tls.VersionTLS12
}

// FetchImage selects platforms and skips attestation manifests.
func TestFetchImageSelectsPlatforms(t *testing.T) {
	f := newFakeRegistry(t)
	mk := func(arch string) (Descriptor, []byte) {
		spec := ImageSpec{OS: "linux", Arch: arch, Layers: []LayerSpec{{Tar: []byte(strings.Repeat("t", 1024) + arch)}}}
		b, err := spec.Build()
		if err != nil {
			t.Fatal(err)
		}
		for d, v := range b.Blobs {
			f.blobs[d] = v
			f.manifest[d] = v
		}
		return b.Desc, b.Manifest
	}
	amd, _ := mk("amd64")
	arm, _ := mk("arm64")
	att := Descriptor{MediaType: MTOCIManifest, Digest: arm.Digest, Size: arm.Size, Platform: &Platform{OS: "unknown", Architecture: "unknown"}}
	root, _ := RootIndex([]Descriptor{amd, arm, att}, nil, MTOCIIndex)
	rd := Sum256(root)
	f.manifest[rd] = root
	c := f.client(t, 0)
	got, st, notes, err := c.FetchImage("lib/img", rd, []string{"linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(root) {
		t.Fatal("root bytes differ")
	}
	if _, ok := st[amd.Digest]; !ok {
		t.Error("amd64 manifest missing")
	}
	if _, ok := st[arm.Digest]; ok {
		t.Error("arm64 manifest fetched though not selected")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "non-image") {
		t.Errorf("notes: %v", notes)
	}
}
